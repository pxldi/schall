#!/usr/bin/env bash
#
# Starts the API the browser tests drive, against the seeded test database.
#
# Playwright launches this as one of its `webServer` entries. The seeding is in
# here rather than in a Playwright global setup because the order matters and
# only a single process can guarantee it: Schall reads the database at startup —
# queueing resolutions for unasked files, sweeps for wants that have fallen due —
# so a run that seeded after the API came up would be testing against whatever
# the previous run left behind, plus the jobs this one queued for it.
#
# The binaries are built rather than `go run`, because `go run` starts the
# program as a child of itself and Playwright's shutdown then leaves the real
# server running on the port the next run wants.

set -euo pipefail

cd "$(dirname "$0")/.."

# The one database this script is entitled to empty, kept apart from the
# variable that can point the suite elsewhere.
DEFAULT_DATABASE_URL='postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable'
DATABASE_URL="${SCHALL_E2E_DATABASE_URL:-$DEFAULT_DATABASE_URL}"
ADDRESS="${SCHALL_E2E_API_ADDRESS:-127.0.0.1:8181}"
# One build directory per checkout rather than one for the machine, so two
# checkouts running the suite at once do not write each other's binaries
# mid-run. It is derived rather than random because the last line of this script
# execs the server out of it: a trap would never fire to clean a mktemp -d up,
# and every run would leave one behind. Same checkout, same directory, reused.
BUILD_DIR="${TMPDIR:-/tmp}/schall-e2e-$(printf '%s' "$PWD" | cksum | cut -d' ' -f1)"

mkdir -p "$BUILD_DIR"
go build -o "$BUILD_DIR/schall-seed" ./cmd/schall-seed
go build -o "$BUILD_DIR/schall" ./cmd/schall

# --expect is this script naming the database it means to empty, and the seeder
# checks the claim rather than taking it: the target has to be the database
# written above, in whatever spelling it arrives, so a SCHALL_E2E_DATABASE_URL
# pointed at somebody's development library is refused here instead of being
# seeded on this script's word. Naming it is also what makes an exported
# SCHALL_DATABASE_URL harmless — the seeder is being told this database is the
# one to empty, which is more specific than anything it could infer.
SCHALL_SEED_DATABASE_URL="$DATABASE_URL" \
	"$BUILD_DIR/schall-seed" --expect "$DEFAULT_DATABASE_URL"

# MusicBrainz is pointed at a closed port rather than left at its default. The
# fixture is arranged so that nothing is due to be asked about, but "arranged so
# that nothing asks" and "cannot ask" are different guarantees, and only the
# second one survives somebody adding a sweep.
exec env \
	SCHALL_DATABASE_URL="$DATABASE_URL" \
	SCHALL_HTTP_ADDRESS="$ADDRESS" \
	SCHALL_LIBRARY_ALLOWED_ROOTS=/music \
	SCHALL_MUSICBRAINZ_BASE_URL="http://127.0.0.1:9/ws/2" \
	SCHALL_LOG_LEVEL="${SCHALL_E2E_LOG_LEVEL:-warn}" \
	"$BUILD_DIR/schall"
