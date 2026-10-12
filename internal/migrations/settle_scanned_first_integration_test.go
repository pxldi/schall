package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

const settleScannedFirstSchema = "schall_settle_scanned_first_check"

// 00109 settles the wants with no recording that stopped because the scan
// identified their proven copy first. A want whose file carries a person's
// answer stays stopped.
func TestTheMigrationSettlesWantsTheScanIdentifiedFirst(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")
	schema := settleScannedFirstSchema

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + schema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 108); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}

	stopped := func(title string, manual bool) uuid.UUID {
		t.Helper()
		targetID, fileID := uuid.New(), uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO `+schema+`.acquisition_targets (
				id, origin, entry_title, status, anchor_fingerprint, anchor_source,
				anchor_reference
			)
			VALUES ($1, 'playlist', $2, 'unresolved', 'AQAA', 'deezer', '3459592211')
		`, targetID, title); err != nil {
			t.Fatalf("insert want %q: %v", title, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO `+schema+`.library_files (id, path, size_bytes, modified_at)
			VALUES ($1, $2, 1, now())
		`, fileID, "/music/"+title+".flac"); err != nil {
			t.Fatalf("insert file %q: %v", title, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO `+schema+`.acquisition_target_files (
				acquisition_target_id, provider, source_username, remote_path,
				verdict, evidence, library_file_id
			)
			VALUES ($1, 'slskd', 'peer', $2, 'accepted',
			        '{"method":"published-sample","source":{"source":"deezer","externalId":"deezer:3459592211"}}',
			        $3)
		`, targetID, title, fileID); err != nil {
			t.Fatalf("insert copy %q: %v", title, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO `+schema+`.library_file_identities (
				library_file_id, kind, musicbrainz_recording_id, method, is_manual, summary
			)
			VALUES ($1, 'external', gen_random_uuid(), 'title-artist-duration', $2, 'scanned')
		`, fileID, manual); err != nil {
			t.Fatalf("insert identity %q: %v", title, err)
		}
		return targetID
	}
	scanned := stopped("lumora - slowed", false)
	decided := stopped("starly - slowed", true)

	if err := migrations.UpTo(ctx, scopedURL, 109); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	status := func(id uuid.UUID) string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `
			SELECT status FROM `+schema+`.acquisition_targets WHERE id = $1
		`, id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := status(scanned); got != "acquired" {
		t.Errorf("scanned-first want = %s, want acquired", got)
	}
	if got := status(decided); got != "unresolved" {
		t.Errorf("want over a person's answer = %s, want it left stopped", got)
	}
}
