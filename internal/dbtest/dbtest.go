// Package dbtest hands an integration test a PostgreSQL database that is its
// own for the length of the test.
//
// Every integration test in every package points at the one database in
// SCHALL_TEST_DATABASE_URL and empties it before seeding its own fixtures, so
// two tests running at the same time destroy each other's rows. `go test ./...`
// runs package binaries in parallel, which makes that collision the normal case
// rather than the unlucky one. Setup serialises the whole suite behind a single
// advisory lock. The lock only excludes tests that agree on its ID, so the ID
// lives here instead of in each test file, where copies of it drifted apart and
// stopped excluding anything.
package dbtest

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/migrations"
)

// lockID is the one advisory lock the integration suite serialises on. Never
// add a second one: two IDs are two tests running at once.
const lockID = 20260727

// setupTimeout bounds waiting for the lock. It has to cover every other
// integration test in the suite running to completion first, so it is far
// longer than any single test needs.
const setupTimeout = 2 * time.Minute

// Setup skips the test unless a test database is configured, then returns a
// pool onto an empty, fully migrated database that no other test can touch
// until this one returns.
func Setup(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SCHALL_TEST_DATABASE_URL is not set")
	}

	// Setup runs on its own deadline rather than the test's: a test that
	// allows itself 20 seconds of work should not fail because the suite
	// ahead of it took longer than that to finish.
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	lock, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire integration lock connection: %v", err)
	}
	t.Cleanup(lock.Release)
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		t.Fatalf("lock integration database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = lock.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
	})

	// Migrating under the lock as well, because goose applying the same
	// migration from two packages at once is its own race.
	if err := migrations.Up(ctx, databaseURL); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	truncate(ctx, t, pool)

	return pool
}

// truncate empties every application table rather than a list the test names.
// A test that forgets a table would otherwise inherit whatever the previous
// test left in it, and read that as its own fixture.
func truncate(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT quote_ident(tablename)
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'
	`)
	if err != nil {
		t.Fatalf("list test database tables: %v", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list test database tables: %v", err)
	}
	if len(tables) == 0 {
		return
	}
	if _, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" CASCADE"); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
}
