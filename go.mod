module github.com/pxldi/schall

go 1.25.0

require (
	github.com/dhowden/tag v0.0.0-20240417053706-3d75831295e8
	github.com/go-chi/chi/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/pressly/goose/v3 v3.24.1
	github.com/rs/zerolog v1.34.0
	go.senan.xyz/taglib v0.11.1
	golang.org/x/image v0.45.0
	golang.org/x/text v0.41.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/tetratelabs/wazero v1.10.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	// Not imported by any package we build (pgx moved SCRAM to crypto/pbkdf2 in
	// the standard library); pinned only to clear GHSA advisories against the
	// v0.31.0 that goose still declares. `go mod tidy` removes this line since
	// nothing in the build graph needs it — re-add it if that happens.
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
