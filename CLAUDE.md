# Schall

Self-hosted music collection manager. Go 1.25 API (chi, pgx/v5, sqlc, goose,
zerolog) + PostgreSQL + SvelteKit frontend (Svelte 5, TypeScript, Tailwind 4,
TanStack Query), shipped as one Docker image.

The spec lives outside this repository: the product end state, the roadmap,
the ADRs and the design rules. `CLAUDE.local.md` says where. Read it before
changing behaviour, and keep it truthful in the same session as the change.
`ADR NNNN` in a comment names a decision record there.
`web/src/design-tokens.yaml` lists the interface tokens, and
`web/src/lib/design-system.test.ts` fails when it disagrees with `styles.css`.

## The one rule that matters

Matching never guesses. A file is a recording only when something proves it:
MusicBrainz recording ID or ISRC agreement, exact tag agreement (the
enumerated combinations in `internal/identity/grade.go`), AcoustID read at the
leading cluster (ADR 0011, 0018), or the distributor's preview
sample (`internal/anchor`, ADR 0024, thresholds in
`internal/chromaprint`). A contradiction voids a pair whatever else agrees. An
absent tag is silence, not agreement. Anything unproven is surfaced to the
person with its evidence — never resolved by similarity, string distance, a
score, or a threshold. Manual decisions win and are permanent.

You may change matching, identity, import, acquisition and schema code freely.
When you do, the PR says what the change can admit that it could not before
(usually: nothing), and adds the test that would have failed before.

## How to work

- Make reasonable calls, fix what you find, open a PR. Ask only when a choice
  changes what the product is, or before deleting music.
- Mention a new dependency in the PR; no need to ask first.
- Match the surrounding code's style. Don't add summary `.md` files.
- `make check` before calling anything done.

## Commands

| Task | Command |
| --- | --- |
| Install frontend deps | `npm --prefix web install` |
| Dev (Postgres + API :8080 + Vite :5173) | `make dev` |
| All tests | `make test` |
| One Go test | `go test ./internal/<pkg>/ -run <TestName>` |
| Integration tests | set `SCHALL_TEST_DATABASE_URL` to a **scratch** DB (the suite truncates every table); a throwaway `docker run -d -p 55432:5432 -e POSTGRES_PASSWORD=x postgres:17-alpine` works |
| Browser tests | `make e2e` (seeds a scratch DB, API :8181 + Vite :5174) |
| Seeded collection to click through | `make seed`, then point `SCHALL_DATABASE_URL` at :5433 |
| Env docs + format + vet + svelte-check | `make check` |
| Regenerate sqlc | `make sqlc` |
| Apply migrations manually | `make migrate` (they also run embedded at startup) |
| Build image | `make build` |

Live AcoustID tests are opt-in: `SCHALL_ACOUSTID_API_KEY` set and `fpcalc` installed.

## Layout

- `cmd/schall/` — entry point; `cmd/schall-seed/` + `internal/seed/` — the fixed collection browser tests use
- `internal/server/` — HTTP handlers; serves the frontend from `dist/`
- `internal/db/` — sqlc output (`*.sql.go`, generated) + handwritten pgx repositories (everything else)
- `internal/migrations/` — goose SQL, embedded; `queries/` — sqlc sources
- `internal/matching/`, `internal/identity/`, `internal/tagmatch/` — library↔catalogue matching and the one grader
- `internal/downloads/` — transfers, source searches, import (`match.go`, `importer.go`, `single.go`)
- `internal/acquisition/` — wants: resolution, the sweeper, the retry-until-verified loop (`acquire.go`)
- `internal/sources/`, `internal/slskd/`, `internal/musicbrainz/`, `internal/acoustid/`, `internal/anchor/` — providers and clients
- `internal/library/` — scanner, tagging pass, layout moves; `internal/jobs/` — PostgreSQL job worker; `internal/events/` — SSE (invalidations only)
- `web/src/routes/` — pages; `web/src/lib/api.ts` — the API client; `web/e2e/` — Playwright

## Conventions

- Generated, never hand-edited: `internal/db/*.sql.go` (`make sqlc`), `internal/server/dist/`, `web/build/`.
- Migrations: `internal/migrations/NNNNN_name.sql`, next number, `-- +goose Up` / `Down`. Never edit an applied one — add another.
- Go: wrap errors `fmt.Errorf("verb noun %s: %w", id, err)`; expected failures are package sentinels that read as sentences, mapped in handlers via `api.problem`; inject `zerolog.Logger`, snake_case fields, lowercase messages; `ctx` first on anything doing I/O; unit tests beside the code, integration tests `*_integration_test.go` gated on `SCHALL_TEST_DATABASE_URL`.
- Frontend: Svelte 5 runes only; server state through TanStack Query with kebab-case keys; SSE drives updates, `refetchInterval` (15s, only while work is active) is the safety net; all HTTP through `api.ts`; Tailwind; the accent is chrome, never state. Component tests assert text/href/aria/disabled/what was sent — never class names.
- Escape `%` and `_` in every `ILIKE` (the shape in `queries/artists.sql`).

## Things that bite

- The seeded fixture must leave the job queue nothing to do, now and a year from now (`internal/seed`'s integration test asserts both). A fixture row startup would act on breaks the browser tests as flakiness.
- Soulseek bans an account ~30 min for fast or repeated searches, and a banned search reports "nothing on offer". Speed up by waiting less, never by asking more. slskd's `Queued` state means the search never reached Soulseek.
- AcoustID: validating a folder somebody chose it can only object; acquiring one file for a want it is the only thing that admits, read at the leading cluster alone. Tags alone never admit a downloaded file (ADR 0002).
- One open download request per want (`download_requests_open_target_idx`).
- Identities are MusicBrainz-keyed end to end; a second key (e.g. SoundCloud) is a schema decision — raise it before building.
- Deleting music needs a licence: a lease or an explicit person's decision, recorded before the unlink (ADR 0021).
- Navidrome sync calls as the client `schall-sync`. Subsonic `DefaultReportRealPath` has to be on for that client name, or path pairing returns nothing.

## How to write

Plain English. Say what changed and what it does. Do not give a thing a will
("the config decides"), do not use "not X, but Y" for effect, do not finish on
an aphorism, do not use an em dash as the default joint, do not say the same
thing twice in a different shape.

Short declarative sentences, active voice, ordinary words, numbers and file
paths instead of adjectives. Commit subjects are imperative and name the
change. PR bodies say what changed, why, what it can admit that it could not
before, and what test would have failed. Code comments say why the code is the
way it is, in one or two sentences.

Interface text is shorter again, and read by somebody mid-task who wants to
act. Name the thing, what happened, what to do, and the control they can see.
Explanations go behind a disclosure. A control label is one to three words,
verb first; a chip is a fragment; an error is at most two sentences.
