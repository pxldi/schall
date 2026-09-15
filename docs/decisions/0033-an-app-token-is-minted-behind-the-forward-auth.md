# 0033 — An app token is minted behind the forward-auth

**Status:** accepted, 2026-09-06. The owner's decision, in their words: "sign
in with authentik and then api token for the app? Similar to karakeep". Built
the same day: `SCHALL_AUTH`, migration 00095, `internal/server/auth.go`, the
four routes and Settings → Phone. The Traefik rules live in the flux
repository and are not applied yet.

## Context

Schall has no authentication. `schall.example.com` sits behind Traefik with
Authentik's forward-auth middleware, so every request that reaches the pod has
a browser session with Authentik. That works for a web app on the same origin
and does not work for a phone app: a native app has no Authentik session
cookie, and forward-auth answers an API call without one with a redirect to a
login page.

Two ways to give the phone a credential were considered.

- **OIDC in the app.** Authentik issues an access token to the app with PKCE;
  Schall validates it against Authentik's JWKS. One identity everywhere, but
  it needs an OAuth application in Authentik, refresh handling in the app,
  and a second validation path in Schall beside the forward-auth headers.
- **An app token minted by Schall.** The owner signs in to the web app as
  today and mints a token in Settings. The phone sends it as a Bearer header.
  One table and one middleware. Karakeep does this and the owner uses it.

## Decision

The browser keeps forward-auth. Schall trusts the `X-authentik-*` identity
headers only when the request also carries a proxy key that Traefik adds
(`X-Schall-Proxy-Key`, matched against `SCHALL_AUTH_PROXY_KEY`). A phone
uses a token minted in Settings → Phone, shown once as text and a QR code,
stored as a SHA-256 hash with a name, the minting user, creation time, last
use and revocation. Traefik lets requests with a Bearer header on `/api/`
past forward-auth; Schall validates them. A token cannot mint or revoke
tokens.

`SCHALL_AUTH=open` keeps today's behaviour for development, the browser tests
and the seed. `SCHALL_AUTH=proxy` is production.

## Consequences

- One new table (`app_tokens`), three routes, one middleware, one settings
  section. The Traefik rules change in the flux repository.
- The request log gains an `actor` field. Decisions in the database do not
  record who made them; that would be a later migration.
- OIDC in the app can be added later as a second accepted credential in the
  same middleware. The app's request shape would not change.
- Matching is untouched. This decision admits nothing about a file that could
  not be admitted before.

The plan around it is [docs/app-plan.md](../app-plan.md).
