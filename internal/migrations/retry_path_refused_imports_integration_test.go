package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

const retryPathSchema = "schall_retry_path_check"

const refusedByAnOldPath = `import download: ERROR: duplicate key value violates unique ` +
	`constraint "downloads_local_path_idx" (SQLSTATE 23505)`

// retryPathFixture is a schema migrated to just before 00135.
type retryPathFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	scopedURL string
}

func newRetryPathFixture(t *testing.T) retryPathFixture {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+retryPathSchema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+retryPathSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+retryPathSchema+" CASCADE")
	})
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + retryPathSchema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 134); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}
	return retryPathFixture{t: t, pool: pool, scopedURL: scopedURL}
}

func (fixture retryPathFixture) exec(sql string, args ...any) {
	fixture.t.Helper()
	if _, err := fixture.pool.Exec(context.Background(), sql, args...); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture retryPathFixture) migrate() {
	fixture.t.Helper()
	if err := migrations.UpTo(context.Background(), fixture.scopedURL, 135); err != nil {
		fixture.t.Fatalf("apply the migration under test: %v", err)
	}
}

func (fixture retryPathFixture) want() uuid.UUID {
	fixture.t.Helper()
	id := uuid.New()
	fixture.exec(`
		INSERT INTO `+retryPathSchema+`.acquisition_targets
		    (id, origin, entry_title, status, musicbrainz_recording_id)
		VALUES ($1, 'manual', 'who do you love', 'pending', $2)
	`, id, uuid.New())
	return id
}

// setAside records a copy whose import ran out of attempts for reason, and
// which the stale-fetch pass then read as a download that ended without the
// file. It returns the request.
func (fixture retryPathFixture) setAside(targetID uuid.UUID, remotePath, reason string) uuid.UUID {
	fixture.t.Helper()
	id := uuid.New()
	fixture.exec(`
		INSERT INTO `+retryPathSchema+`.download_requests (
			id, acquisition_target_id, provider, source_username, source_directory, file_count,
			expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_error
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Pashanim', 1, 1, 1, 'flac', 0.8, '[]'::jsonb,
		        'completed', 'needs_review', $3)
	`, id, targetID, reason)
	fixture.exec(`
		INSERT INTO `+retryPathSchema+`.acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			verdict, summary, download_request_id
		)
		VALUES ($1, 'slskd', 'peer', $2, 'undelivered', 'Download ended without the file.', $3)
	`, targetID, remotePath, id)
	return id
}

// state is the import status, how many retries the history records, and the
// verdict of the copy.
func (fixture retryPathFixture) state(requestID uuid.UUID) (string, int, string) {
	fixture.t.Helper()
	var (
		status, verdict string
		retries         int
	)
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT requests.import_status,
		       (SELECT count(*) FROM `+retryPathSchema+`.download_import_reviews
		        WHERE download_request_id = $1 AND kind = 'revalidated'),
		       (SELECT verdict FROM `+retryPathSchema+`.acquisition_target_files
		        WHERE download_request_id = $1)
		FROM `+retryPathSchema+`.download_requests requests WHERE requests.id = $1
	`, requestID).Scan(&status, &retries, &verdict); err != nil {
		fixture.t.Fatal(err)
	}
	return status, retries, verdict
}

// 00135 sends an import the old path refused back through validation, and its
// copy back to being fetched.
func TestTheMigrationRetriesAnImportAnOldPathRefused(t *testing.T) {
	fixture := newRetryPathFixture(t)
	requestID := fixture.setAside(fixture.want(), `Music\Pashanim\01.flac`, refusedByAnOldPath)

	fixture.migrate()

	status, retries, verdict := fixture.state(requestID)
	if status != "pending" || retries != 1 || verdict != "fetching" {
		t.Fatalf("import = %s with %d retries, copy %s; want pending, 1, fetching",
			status, retries, verdict)
	}
}

// An import set aside for any other reason is somebody's to retry.
func TestTheMigrationLeavesAnImportSetAsideForAnotherReason(t *testing.T) {
	fixture := newRetryPathFixture(t)
	requestID := fixture.setAside(fixture.want(), `Music\Pashanim\01.flac`, "The tags name another release.")

	fixture.migrate()

	if status, retries, verdict := fixture.state(requestID); status != "needs_review" ||
		retries != 0 || verdict != "undelivered" {
		t.Fatalf("import = %s with %d retries, copy %s; want it untouched", status, retries, verdict)
	}
}

// A want that let in another copy since keeps that answer, so the refused copy
// is not retried into a second file of the same song.
func TestTheMigrationLeavesAnImportWhoseWantHasACopy(t *testing.T) {
	fixture := newRetryPathFixture(t)
	targetID := fixture.want()
	requestID := fixture.setAside(targetID, `Music\Pashanim\01.flac`, refusedByAnOldPath)
	fixture.exec(`
		INSERT INTO `+retryPathSchema+`.acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path, verdict
		)
		VALUES ($1, 'slskd', 'other', 'Music\Pashanim\02.flac', 'accepted')
	`, targetID)

	fixture.migrate()

	if status, _, _ := fixture.state(requestID); status != "needs_review" {
		t.Fatalf("import = %s, want needs_review", status)
	}
}

// A want somebody removed since stays removed.
func TestTheMigrationLeavesAnImportWhoseWantWasRemoved(t *testing.T) {
	fixture := newRetryPathFixture(t)
	targetID := fixture.want()
	requestID := fixture.setAside(targetID, `Music\Pashanim\01.flac`, refusedByAnOldPath)
	fixture.exec(`UPDATE `+retryPathSchema+`.acquisition_targets SET status = 'not_wanted' WHERE id = $1`, targetID)

	fixture.migrate()

	if status, _, _ := fixture.state(requestID); status != "needs_review" {
		t.Fatalf("import = %s, want needs_review", status)
	}
}
