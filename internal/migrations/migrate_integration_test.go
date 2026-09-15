// This file is the external test package on purpose: internal/dbtest applies
// migrations itself, so an in-package test importing it would be an import
// cycle. The advisory lock has to be the same one every other integration test
// takes, which is what dbtest holds.
package migrations_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

// checkSchema is where a migration run of its own is applied, so that proving
// the set applies from nothing never touches the schema the rest of the suite
// is using. It is dropped again whatever the test concludes.
const checkSchema = "schall_migration_check"

// diskVersions is the version of every migration file, ascending.
func diskVersions(t *testing.T) []int64 {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	versions := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("migration %q does not begin with a version: %v", entry.Name(), err)
		}
		versions = append(versions, version)
	}
	return versions
}

// The whole set has to apply to a database that has never seen any of it. Every
// deployment does exactly this once, and an earlier migration that only works
// because a later one has already run is invisible to a suite that migrates a
// database it migrated yesterday.
func TestEveryMigrationAppliesToAnEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+checkSchema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+checkSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+checkSchema+" CASCADE")
	})

	// public stays on the path behind it so extensions installed there resolve;
	// everything the migrations create lands in the empty schema in front.
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	if err := migrations.Up(ctx, databaseURL+separator+"search_path="+checkSchema+",public"); err != nil {
		t.Fatalf("apply migrations to an empty schema: %v", err)
	}

	rows, err := pool.Query(ctx,
		"SELECT version_id FROM "+checkSchema+".goose_db_version WHERE is_applied ORDER BY version_id")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}

	// goose records version 0 for the table it creates for itself.
	want := diskVersions(t)
	if len(applied) != len(want)+1 {
		t.Fatalf("applied %v, want the %d migrations on disk", applied, len(want))
	}
	for index, version := range want {
		if applied[index+1] != version {
			t.Fatalf("applied[%d] = %d, want %d", index+1, applied[index+1], version)
		}
	}
}

// A second Up over a database that is already at the latest version applies
// nothing and reports success. Every start of the server does this.
func TestUpIsSilentOnADatabaseThatIsAlreadyMigrated(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")

	var before int64
	if err := pool.QueryRow(ctx,
		"SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, databaseURL); err != nil {
		t.Fatalf("Up() on an already-migrated database: %v", err)
	}
	var after int64
	if err := pool.QueryRow(ctx,
		"SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("version moved from %d to %d without a new migration", before, after)
	}
}

// The retry loop exists for a database that is not up yet; against one that is,
// it has to apply on the first attempt rather than wait for anything.
func TestUpWithRetrySucceedsAtOnceAgainstAReachableDatabase(t *testing.T) {
	dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")

	retries := 0
	err := migrations.UpWithRetry(context.Background(), databaseURL, migrations.RetryOptions{
		Window:   time.Minute,
		Interval: time.Second,
		OnRetry:  func(int, error) { retries++ },
	})
	if err != nil {
		t.Fatalf("UpWithRetry() error = %v", err)
	}
	if retries != 0 {
		t.Fatalf("waited %d times for a database that was already accepting connections", retries)
	}
}
