<img src="web/static/logo.svg" alt="" width="56" />

# Schall

A lightweight, self-hosted music collection manager. It finds what your library
is missing, helps you acquire it, and remembers every manual decision
permanently.

**Schall never guesses.** When identifiers or positions are ambiguous it stops
and asks, and the answer is kept for good. Zero false positives beats
automation.

Status: active **v0.1 development**.

## Quick start

Prerequisites: Docker with Compose.

```sh
cp .env.example .env
docker compose up --build
```

Open <http://localhost:8080>. Compose publishes the API on loopback only, because
**Schall has no authentication**: everything the browser can do — settings,
uploads, deletion, every job control — is available to anyone who can reach the
port. Set `SCHALL_BIND_ADDRESS=0.0.0.0` to serve other machines, and put a
reverse proxy that asks who is calling in front of it before you do.

Migrations apply on first start. Compose mounts `./music` at `/music` and the
completed-download inbox `./downloads` read-only at `/downloads`; point them
elsewhere in `.env`:

```sh
SCHALL_MUSIC_PATH=/srv/music
SCHALL_DOWNLOADS_PATH=/srv/slskd/downloads
```

Schall runs as UID/GID `1000:1000` so it can write the music bind mount without
root — set `SCHALL_UID` and `SCHALL_GID` if that folder has a different owner.

Browser uploads wait in `SCHALL_UPLOAD_STAGING_PATH` — `./uploads` mounted at
`/uploads`, moved with `SCHALL_UPLOADS_PATH` — before they are validated and
copied into `SCHALL_IMPORT_LIBRARY_PATH`. It must be outside every music folder,
and Schall refuses to start if it is not: a scan that caught a half-written
upload would index a truncated file as music the library holds.

Then add `/music` as a music folder in the Library view and start a scan. Only
absolute paths inside `SCHALL_LIBRARY_ALLOWED_ROOTS` are accepted; the Compose
default permits `/music`.

## Local development

Go 1.24+, Node.js 22+, Docker.

```sh
npm --prefix web install
docker compose up -d postgres
export SCHALL_DATABASE_URL='postgres://schall:schall@localhost:5432/schall?sslmode=disable'
go run ./cmd/schall
```

In another shell:

```sh
npm --prefix web run dev
```

The Svelte dev server runs at <http://localhost:5173> and proxies `/api` to the
Go server on 8080. `make dev` starts all three at once; `make help` lists the
rest.

Integration tests skip themselves unless `SCHALL_TEST_DATABASE_URL` is set, and
the suite truncates every table it finds — point it at a scratch database, never
your development one.

### The seeded collection

Developing against an empty Schall means every page is an empty state. `make
seed` fills a second, persistent PostgreSQL with a fixed collection — five
artists, six releases, seventeen files, a transfer under way, and one of each
question the review queue can ask — so the UI can be worked on without owning
any music or waiting on MusicBrainz:

```sh
make seed
SCHALL_DATABASE_URL='postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable' \
  go run ./cmd/schall
npm --prefix web run dev
```

The collection is invented and its identifiers resolve nowhere, which is what
keeps it still: nothing in it is due to be refreshed, swept or asked about, so
what you are looking at is what the last seed wrote.

**That does not run out.** Stillness here is not a recent date waiting to go
stale — no date can stay recent against a window measured from now() — but the
absence of anything to ask about at all. Nobody in the collection is both
followed and known to MusicBrainz, which is the one pair that stands for "keep
asking", so the follow feed reaches nobody however long the database has been
up. `internal/seed`'s integration test asserts every one of these properties
twice: as seeded, and again against a clock a year on.

Its database is
`postgres-test` in `compose.yaml`, on port 5433, with its own volume. **Seeding
empties every table in it**, so `cmd/schall-seed` reads
`SCHALL_SEED_DATABASE_URL` rather than `SCHALL_DATABASE_URL`, and seeds nothing
it was not told to seed. Confirm the target one of two ways:

- `--expect <url>` names the database this run is meant to empty, and the target
  has to be that same database. `make seed` and the browser tests use it, so a
  `SCHALL_E2E_DATABASE_URL` pointed somewhere else is refused rather than
  emptied on a script's word.
- `--i-know-this-is-a-scratch-database` says the same thing without naming
  anything, for a database you have in front of you.

With neither, it refuses. It also refuses, whatever you passed, when the target
is the database `SCHALL_DATABASE_URL` names — unless that is the database you
named with `--expect`, which is how you refill one you have Schall pointed at.

Databases are compared as endpoints, never as text: `localhost` and `127.0.0.1`
are one database, an `sslmode` or an omitted port on one side changes nothing,
and two names for one server are resolved before they are called different.

### Browser tests

`make e2e` runs the Playwright suite in `web/e2e/` against the whole application:
the real Svelte build, the real Go API, and a freshly seeded database. Nothing is
mocked. It seeds, starts the API on 8181 and a Svelte dev server on 5174, and
leaves the ports development uses alone.

```sh
npx playwright install chromium   # once
make e2e
```

The tests never write. One fixture is applied per run and every test reads it, so
a test that followed an artist would change what the tests beside it are looking
at; anything that has to change data needs its own seeded database.

## Stack

Go 1.24 (chi, pgx/v5, sqlc, goose, zerolog) · PostgreSQL · SvelteKit with
Svelte 5, TypeScript and Tailwind CSS 4 · a single Docker image.

Database access goes through sqlc: edit SQL in `queries/`, run `make sqlc`, and
never hand-edit the generated files in `internal/db/`. Schema changes are new
goose migrations in `internal/migrations/`.

## Documentation

- [docs/api.md](docs/api.md) — HTTP surface, import and duplicate handling,
  identity resolution, followed vs held artists
- [docs/PRODUCT.md](docs/PRODUCT.md) — intended behaviour, and the source of
  truth for it: target state rather than current state
- [docs/ROADMAP.md](docs/ROADMAP.md) — what is next, and what each item was
  signed off against
- [docs/decisions/](docs/decisions/) — the ADRs behind the spec, including the
  rules that decide what may enter the library
- [DESIGN.md](DESIGN.md) — how the interface looks, and the rules it obeys

## Licence

MIT. See [LICENSE](LICENSE).
