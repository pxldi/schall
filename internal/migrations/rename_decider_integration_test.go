// External test package for the same reason the rest of the migration tests
// are: internal/dbtest applies migrations itself, and an in-package test
// importing it would be an import cycle.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

// The schema 00101 is applied over.
const renameSchema = "schall_rename_check"

// 00101 renames the two values the project's old name was stored in. A database
// created after the rename never held them, so the only place the rewriting
// branch runs is a database that was migrated before it — and there a failure is
// a container that will not start. This test builds that database: apply up to
// 100, put the pre-rename constraints and default back, write rows that say
// "cantus", and then apply the migration under test.
func TestTheRenameMigrationRewritesTheStoredDecider(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+renameSchema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+renameSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+renameSchema+" CASCADE")
	})

	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + renameSchema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 100); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}

	// 00028 and 00052 are written with the new spelling, so a fresh database
	// cannot hold the old one. These statements put back exactly what a database
	// migrated before the rename has.
	if _, err := pool.Exec(ctx, `
		ALTER TABLE `+renameSchema+`.acquisition_target_files
		    DROP CONSTRAINT acquisition_target_files_decided_by_valid,
		    ALTER COLUMN decided_by SET DEFAULT 'cantus',
		    ADD CONSTRAINT acquisition_target_files_decided_by_valid
		        CHECK (decided_by IN ('cantus', 'user'));
		ALTER TABLE `+renameSchema+`.library_file_keep_reads
		    DROP CONSTRAINT library_file_keep_reads_signal_valid,
		    DROP CONSTRAINT library_file_keep_reads_pressed_keep_is_not_a_read,
		    DROP CONSTRAINT library_file_keep_reads_read_names_its_run,
		    ADD CONSTRAINT library_file_keep_reads_signal_valid
		        CHECK (signal IN ('navidrome_star', 'cantus_keep')),
		    ADD CONSTRAINT library_file_keep_reads_pressed_keep_is_not_a_read
		        CHECK ((signal = 'cantus_keep')
		               = (run_id IS NULL AND outcome = 'kept')),
		    ADD CONSTRAINT library_file_keep_reads_read_names_its_run
		        CHECK (signal = 'cantus_keep' OR run_id IS NOT NULL)
	`); err != nil {
		t.Fatalf("put the pre-rename schema back: %v", err)
	}

	var targetID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO `+renameSchema+`.acquisition_targets
		    (origin, entry_title, status, musicbrainz_recording_id, next_attempt_at)
		VALUES ('manual', 'held copy', 'pending', $1, now())
		RETURNING id
	`, uuid.New()).Scan(&targetID); err != nil {
		t.Fatalf("insert the want: %v", err)
	}
	// The copy the app itself set aside, and one a person decided. Only the first
	// is renamed; 'user' has to come through untouched.
	for _, decider := range []string{"cantus", "user"} {
		verdict := "held"
		if decider == "user" {
			verdict = "accepted"
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO `+renameSchema+`.acquisition_target_files
			    (acquisition_target_id, provider, source_username, remote_path,
			     file_name, size_bytes, verdict, decided_by, summary)
			VALUES ($1, 'slskd', 'peer', 'Music/x/'||$2||'.flac', $2||'.flac',
			        9000000, $3, $2, 'a copy')
		`, targetID, decider, verdict); err != nil {
			t.Fatalf("insert the copy decided by %q: %v", decider, err)
		}
	}

	var runID, fileID, leaseID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO `+renameSchema+`.weekly_playlist_runs (mode) VALUES ('report')
		RETURNING id
	`).Scan(&runID); err != nil {
		t.Fatalf("insert the run: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO `+renameSchema+`.library_files (path, size_bytes, modified_at)
		VALUES ('/music/a/one.flac', 9000000, now())
		RETURNING id
	`).Scan(&fileID); err != nil {
		t.Fatalf("insert the library file: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO `+renameSchema+`.library_file_leases
		    (library_file_id, library_path, musicbrainz_recording_id, run_id,
		     acquisition_target_id, expires_at)
		VALUES ($1, '/music/a/one.flac', $2, $3, $4, $5)
		RETURNING id
	`, fileID, uuid.New(), runID, targetID,
		time.Now().Add(7*24*time.Hour)).Scan(&leaseID); err != nil {
		t.Fatalf("insert the lease: %v", err)
	}
	// The pressed Keep, which names no run, and the star a run read. The star is
	// there because the migration rewrites the constraint that governs both.
	if _, err := pool.Exec(ctx, `
		INSERT INTO `+renameSchema+`.library_file_keep_reads
		    (lease_id, run_id, signal, outcome)
		VALUES ($1, NULL, 'cantus_keep', 'kept'),
		       ($1, $2, 'navidrome_star', 'kept')
	`, leaseID, runID); err != nil {
		t.Fatalf("insert the keep reads: %v", err)
	}

	if err := migrations.UpTo(ctx, scopedURL, 101); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	var renamed, byHand int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE decided_by = 'schall'),
		       count(*) FILTER (WHERE decided_by = 'user')
		FROM `+renameSchema+`.acquisition_target_files
	`).Scan(&renamed, &byHand); err != nil {
		t.Fatalf("read the copies back: %v", err)
	}
	if renamed != 1 || byHand != 1 {
		t.Fatalf("copies = %d by schall and %d by hand, want one of each", renamed, byHand)
	}

	var columnDefault string
	if err := pool.QueryRow(ctx, `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'acquisition_target_files'
		  AND column_name = 'decided_by'
	`, renameSchema).Scan(&columnDefault); err != nil {
		t.Fatalf("read the default back: %v", err)
	}
	if !strings.Contains(columnDefault, "'schall'") {
		t.Fatalf("default = %q, want it to name schall", columnDefault)
	}

	var keeps, stars int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE signal = 'schall_keep'),
		       count(*) FILTER (WHERE signal = 'navidrome_star')
		FROM `+renameSchema+`.library_file_keep_reads
	`).Scan(&keeps, &stars); err != nil {
		t.Fatalf("read the keep reads back: %v", err)
	}
	if keeps != 1 || stars != 1 {
		t.Fatalf("reads = %d keeps and %d stars, want one of each", keeps, stars)
	}

	// The old spelling is not merely unused afterwards, it is refused.
	if _, err := pool.Exec(ctx, `
		INSERT INTO `+renameSchema+`.acquisition_target_files
		    (acquisition_target_id, provider, source_username, remote_path,
		     file_name, size_bytes, verdict, decided_by, summary)
		VALUES ($1, 'slskd', 'peer', 'Music/x/late.flac', 'late.flac', 9000000,
		        'held', 'cantus', 'a copy')
	`, targetID); err == nil {
		t.Fatal("a copy decided by cantus was accepted, want the constraint to refuse it")
	}
}
