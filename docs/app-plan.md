# Schall on a phone

Owner decisions, 2026-09-06: the app is Expo (React Native); the first
version decides and watches; the browser signs in through Authentik as today,
and the phone uses an app token minted in Settings, the shape Karakeep uses.
Steps 1 to 4 of the backend work are built; nothing else here is.
[ADR 0033](decisions/0033-an-app-token-is-minted-behind-the-forward-auth.md)
records the authentication decision.

## What exists today

- Schall reads a credential since 2026-09-06 (`internal/server/auth.go`). The
  deployment runs `SCHALL_AUTH=proxy` since 2026-09-07: a Bearer request on
  `/api/` bypasses forward-auth and is a token or a 401; everything else
  carries the proxy key Traefik adds after Authentik.
- The app exists under `app/` since 2026-09-07 (Expo SDK 57, expo-router,
  TanStack Query). It signs in by QR or typed token and covers Review with the
  preview player, Downloads, Wants, Search with Follow, and Settings. A folder
  question offers Check again only; resolving one file to a track stays on the
  web. Colours come from `app/src/design/tokens.ts`, generated from
  `web/src/styles.css` by `npm run tokens`, and `npm test` in `app/` fails
  when the two disagree. `make check` runs the app's typecheck and tests.
- `schall.example.com` is a Traefik `IngressRoute` in the `media` namespace with
  the `authentik-forward-auth` middleware from the `traefik` namespace. A
  second rule sends `/outpost.goauthentik.io/` to the Authentik server. The
  pod is a ClusterIP service, so every request that reaches it has passed
  Authentik. Authentik's outpost adds `X-authentik-username`,
  `X-authentik-uid`, `X-authentik-email` and `X-authentik-groups` to the
  request it forwards.
- The web app calls `/api/v1` on its own origin with `fetch`, so the
  forward-auth session cookie covers it. `GET /api/v1/events` is server-sent
  events carrying invalidations only.
- Notifications go to ntfy or a webhook (`internal/server/notifications.go`).
- Review audio comes from `/api/v1/review-queue/copies/{id}/audio` and the
  waveform from `/waveform` beside it.
- `make dev`, the browser tests and the seed run with no proxy in front.

## Authentication

### The shape

1. **Browser: unchanged.** Authentik forward-auth stays. Schall reads the
   `X-authentik-*` headers and records the username as the actor of the
   request, but only when the request carries the proxy key (below).
2. **Minting a token.** Settings gets a section named Phone. "Add a phone"
   asks for a name ("Pixel"), mints a token and shows it once: as text, and as
   a QR code that carries `{"server":"https://schall.example.com","token":"..."}`.
   The list under it shows every phone with its name, when it was added, when
   it was last used and a "Remove" button. Minting needs the browser identity;
   a request authenticated by a token cannot mint or remove tokens. A stolen
   phone therefore cannot add phones.
3. **Using a token.** The app sends `Authorization: Bearer <token>`. Traefik
   lets those requests past forward-auth with a third rule at priority 20 and
   no middleware: `Host(...) && PathPrefix(/api/) && HeaderRegexp(Authorization, ^Bearer )`
   (Traefik 2 spells it `HeadersRegexp`). Schall validates the token. A
   request with neither a valid token nor the proxy key gets 401.
   `/api/v1/health` is exempt: the kubelet's probes carry no credential and
   the route says only whether the database answers.
4. **Token format.** `schall_` followed by 32 random bytes in base64url. The
   prefix lets the middleware pick the token path without a lookup and lets
   secret scanners recognise a leaked one. Only the SHA-256 of the token is
   stored; the lookup is by hash, so no comparison of the plain text happens.
   `last_used_at` is written at most once a minute per token.

### The trust boundary

The `X-authentik-*` headers are plain request headers. Schall must know the
request came through Traefik before it believes them. Two ways:

- **A proxy key (recommended).** A Traefik `headers` middleware chained after
  `authentik-forward-auth` sets `X-Schall-Proxy-Key` to a value from a
  Kubernetes secret. Schall gets the same value as `SCHALL_AUTH_PROXY_KEY`
  and trusts the identity headers only when the key matches. Independent of
  network layout.
- **Trusted CIDRs.** `SCHALL_AUTH_TRUSTED_PROXIES` names the pod network.
  Weaker: every pod in the cluster is then a trusted proxy. Read the TCP peer
  address, not the one `middleware.RealIP` rewrote from `X-Forwarded-For`, so
  the auth middleware must run before `RealIP`. A request with any Bearer
  header takes the token path only: Traefik lets it past forward-auth, so its
  identity headers were written by the client.

`SCHALL_AUTH` selects the mode: `open` (today's behaviour, the default, one
warning at startup) or `proxy` (identity headers with the proxy key, or a
token, or 401). Production sets `proxy`. `make dev`, the browser tests and the
seed keep `open`.

### Why a token and not OIDC in the app

Authentik can issue OAuth tokens to the app with PKCE, and Schall could
validate them against Authentik's JWKS. That needs an OAuth application in
Authentik, refresh handling in the app, and a second validation path beside
the forward-auth one. The token route is one table, one middleware and one
settings section, and the owner already uses this shape in Karakeep. OIDC in
the app can be added later as a second accepted credential in the same
middleware; the app's request shape does not change.

### What it records

The middleware puts the actor (an Authentik username, or the phone's name)
on the request context and the request log gets an `actor` field. Decisions
in the database do not get an actor column in this step. If the owner wants
"who decided", that is a later migration.

### Schema

Migration `00095_app_tokens.sql`:

```sql
create table app_tokens (
  id           uuid primary key default gen_random_uuid(),
  name         text not null,
  token_hash   bytea not null unique,
  created_by   text not null,
  created_at   timestamptz not null default now(),
  last_used_at timestamptz,
  revoked_at   timestamptz
);
```

Routes:

- `GET /api/v1/me` — actor and how the request was authenticated. The app
  calls it after a scan to confirm the token works and to show the name.
- `GET /api/v1/settings/phones` — the list.
- `POST /api/v1/settings/phones` — mints one; the plain token is in this
  response and nowhere else. Browser identity required.
- `DELETE /api/v1/settings/phones/{id}` — revokes. Browser identity required.

Done when: in `proxy` mode `curl` with no credential gets 401, a fresh token
gets 200, a revoked token gets 401, a token cannot call `POST /settings/phones`,
`make dev` and `make e2e` run unchanged in `open` mode, and an integration
test covers the four.

## Backend work before the app, in order

1. **Authentication** as above. **Built 2026-09-06:** `SCHALL_AUTH`,
   `SCHALL_AUTH_PROXY_KEY` and `SCHALL_AUTH_TRUSTED_PROXIES` in
   `internal/config`, migration `00095_app_tokens.sql`, the middleware and the
   four routes in `internal/server/auth.go`, Settings → Phone at
   `/settings/phone`, and ADR 0033 accepted. Only `/api/` is covered; the
   frontend files are served without a credential.

   **Still to apply, in the flux repository:** the third IngressRoute rule at
   priority 20 with no middleware
   (`Host(...) && PathPrefix(/api/) && HeaderRegexp(Authorization, ^Bearer )`),
   and the `headers` middleware chained after `authentik-forward-auth` that
   sets `X-Schall-Proxy-Key` from a Kubernetes secret. Until both are applied
   the deployment keeps `SCHALL_AUTH=open`: turning proxy mode on without the
   key refuses every browser request.
2. **Types out of the client.** Built 2026-09-06: all 187 exported types,
   interfaces and pure-data consts moved from `web/src/lib/api.ts` to
   `web/src/lib/api-types.ts`, which has no `fetch` and no SvelteKit import.
   `api.ts` re-exports everything with `export * from './api-types'`, so
   every existing import site is unchanged. `api-types.test.ts` fails if the
   new file ever imports anything but a type-only `fetch`, `$app` or
   `@sveltejs` binding.
3. **Review payload check.** Built 2026-09-06: audited what the web's three
   review row kinds (Downloaded, Version, Folder) render from
   `GET /review-queue` and `GET /downloads?view=review`, and where each field
   comes from. Nothing needed adding — the copy audio and waveform routes are
   built from a copy's own `id`, already in `copies`, and the album cover is
   built from `target.originAlbumId` or the download's own `albumId`, both
   already sent (the first was undeclared in the web's TS type, since nothing
   there reads it, but it was already on the wire). `docs/api.md`'s "What a
   review row carries" has the field list and the three answers' routes per
   kind.
4. **Cover and audio caching.** Built 2026-09-06: `GET /albums/{id}/cover`,
   `GET /artists/{id}/image`, `GET /library/files/{id}/cover`, `GET
   /review-queue/copies/{id}/audio`, its `GET .../waveform`, and `GET
   /library/files/{id}/audio` all answer `Range` and `If-None-Match` through
   `http.ServeContent`, with a strong `ETag` and `Cache-Control: private`
   ahead of it. The two audio routes already went through `ServeContent` for
   the whole transcoded file, so they had `Range` and `Accept-Ranges`
   already; what they were missing was the `ETag`. The covers and the
   waveform wrote the body themselves and got neither before this. Nothing
   was left out: no route here serves a preview it cannot seek, because the
   transcoder always writes the whole file to disk first rather than
   streaming ffmpeg's output.
5. **Deep links in notifications.** Done. The ntfy message for "needs
   review" carries a `Click` header of `schall://review`, which opens the
   app's Review tab. The message names counts, never one question, so it
   links to the list. The phone already receives the owner's ntfy topic; no
   push service is needed in Schall. A webhook gets no link: nothing says
   what the far end would do with one.

## The app

- **Where.** `app/` in this repository. Expo with `expo-router`, TypeScript,
  TanStack Query, `expo-secure-store` for the token, `expo-camera` for the
  QR scan, `expo-audio` for previews. Metro `watchFolders` includes
  `../web/src/lib` so `import type` from `api-types.ts` resolves; the request
  layer is the app's own (base URL plus Bearer header).
- **Screens, first version.**
  - *Sign in*: server URL and token, typed or scanned. Calls `/me`.
  - *Review*: the queue as a list, one question per screen, the preview
    player with volume, the same three answers the web has (accept a copy,
    none of these, take it back) in the same words.
  - *Downloads*: open, needs review, failed; Cancel, Start, Decide.
  - *Wants*: the list with Stop looking and Look again.
  - *Search*: artist search and Follow.
  - *Settings*: server, phone name, sign out.
- **Updates.** The server's event stream over `react-native-sse`, which
  sends the Bearer header `EventSource` cannot, while the app is in the
  foreground; the 15 s `refetchInterval` stays as the safety net, the same
  as on the web.
- **Design.** The colours and type of `web/src/styles.css` exported to
  `app/tokens.ts` by a small script, with a test like
  `web/src/lib/design-system.test.ts` that fails when the two disagree. Row
  heights and the state tag follow the 2026-09-06 boards.
- **Building.** The owner's phone is Android (Symfonium). `expo run:android`
  or an EAS internal build produces an APK; no store listing. iOS later if
  wanted.
- **Checks.** `make check` runs `npm --prefix app run typecheck` and the app
  unit tests.
- **The web on a phone.** PRODUCT.md still says the web is fully usable in a
  mobile browser. Once the app covers Decide and watch, the owner decides
  whether the web's phone layouts stay maintained (leaning to drop them,
  2026-09-06, undecided).

## Order

Authentication PR, then `/me`, then the types split, then the app scaffold
with sign in, then Review, then Downloads and Wants, then Search, then the
ntfy deep link. Each is one PR.
