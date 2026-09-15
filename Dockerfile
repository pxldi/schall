FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ ./
# The build the interface names at the foot of the rail; CI passes the tag.
ARG SCHALL_VERSION=dev
ENV SCHALL_VERSION=$SCHALL_VERSION
RUN npm run build

FROM golang:1.25-alpine AS api
WORKDIR /src
RUN apk add --no-cache ca-certificates
# yt-dlp is fetched here rather than in the runtime image so that image keeps
# no downloader. The binary is glibc-linked and only copied, never run, here.
ARG YTDLP_VERSION=2026.08.19
ARG YTDLP_SHA256=58162f9bfdc27458ea47bfcb311cf47028f17d8154a8bf7d689861d46399230a
RUN wget -q -O /yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp_linux" \
    && echo "${YTDLP_SHA256}  /yt-dlp" | sha256sum -c \
    && chmod 755 /yt-dlp
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/build ./internal/server/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /schall ./cmd/schall

# Debian rather than distroless because acoustic verification needs fpcalc, and
# fpcalc needs a real audio decoder. Nothing else in Schall wants a userland:
# the binary is still CGO-free and would run on an empty image.
#
# What this costs is worth naming. Distroless had no shell, so a code-execution
# bug had nothing to pivot into; this image does. And fpcalc carries ffmpeg's
# decoders, which are pointed at audio downloaded from strangers — historically
# ffmpeg's most exploited surface. The decoder runs as a separate short-lived
# process under a timeout, never in-process, which is the reason to keep it that
# way even though calling a library would be faster.
#
# ffmpeg itself is here for the review queue's preview. Deciding whether a file
# is the wanted recording means hearing it, and the library holds formats no
# browser will play. It runs on the same terms as fpcalc — a short-lived process
# under a timeout, reading one file, writing to a pipe.
FROM debian:13-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg libchromaprint-tools ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=api /yt-dlp /usr/local/bin/yt-dlp

# The distroless image ran as nonroot and this one must too, so the switch does
# not quietly hand root to a process that never had it.
# 65532:65532 are the ids distroless used and the ids the deployment pins with
# runAsUser and runAsGroup. They are set explicitly rather than left to the next
# free number, so the image agrees with the pod instead of relying on it.
RUN groupadd --system --gid 65532 nonroot \
    && useradd --system --uid 65532 --gid 65532 --no-create-home nonroot

# The default upload staging folder, created here so that it belongs to the user
# the process runs as. A mount point the container runtime has to invent is
# invented owned by root, and a non-root Schall then refuses to start rather
# than accept a staging folder it cannot write to.
RUN install -d -o nonroot -g nonroot /uploads

COPY --from=api /schall /schall
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/schall"]
