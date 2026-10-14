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

const filesGoneSchema = "schall_files_gone_check"

// filesGoneFixture is a schema migrated to just before 00130, with helpers for
// the rows a deletion used to leave pointing at nothing.
type filesGoneFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	scopedURL string
}

func newFilesGoneFixture(t *testing.T) filesGoneFixture {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+filesGoneSchema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+filesGoneSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+filesGoneSchema+" CASCADE")
	})
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + filesGoneSchema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 129); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}
	return filesGoneFixture{t: t, pool: pool, scopedURL: scopedURL}
}

func (fixture filesGoneFixture) exec(sql string, args ...any) {
	fixture.t.Helper()
	if _, err := fixture.pool.Exec(context.Background(), sql, args...); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture filesGoneFixture) migrate() {
	fixture.t.Helper()
	if err := migrations.UpTo(context.Background(), fixture.scopedURL, 130); err != nil {
		fixture.t.Fatalf("apply the migration under test: %v", err)
	}
}

func (fixture filesGoneFixture) file(path string) uuid.UUID {
	fixture.t.Helper()
	id := uuid.New()
	fixture.exec(`
		INSERT INTO `+filesGoneSchema+`.library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1, now())
	`, id, path)
	return id
}

func (fixture filesGoneFixture) want(status string, recordingID uuid.UUID, weeklyOnly bool) uuid.UUID {
	fixture.t.Helper()
	id := uuid.New()
	fixture.exec(`
		INSERT INTO `+filesGoneSchema+`.acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id, status,
			acquired_at, weekly_only, summary
		)
		VALUES ($1, 'manual', 'RIN', 'Dirty South', $2, $3,
		        CASE WHEN $3 = 'acquired' THEN now() END, $4, 'In your library.')
	`, id, recordingID, status, weeklyOnly)
	return id
}

// copyOf records an accepted copy that never recorded its file, and the
// transfer it came through. libraryFileID is what the scan linked the
// transfer to, if anything.
func (fixture filesGoneFixture) copyOf(targetID uuid.UUID, libraryFileID uuid.NullUUID) uuid.UUID {
	fixture.t.Helper()
	requestID, copyID := uuid.New(), uuid.New()
	fixture.exec(`
		INSERT INTO `+filesGoneSchema+`.download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_path, imported_at
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/RIN', 1, 1, 1, 'flac', 0.8, '[]'::jsonb,
		        'completed', 'imported', '/music', now())
	`, requestID, targetID)
	fixture.exec(`
		INSERT INTO `+filesGoneSchema+`.downloads (
			download_request_id, provider, remote_path, source_username, status,
			local_path, library_file_id
		)
		VALUES ($1, 'slskd', 'Music\RIN\14.flac', 'peer', 'completed', '/music/RIN/14.flac', $2)
	`, requestID, libraryFileID)
	fixture.exec(`
		INSERT INTO `+filesGoneSchema+`.acquisition_target_files (
			id, acquisition_target_id, provider, source_username, remote_path,
			verdict, download_request_id, decided_at
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music\RIN\14.flac', 'accepted', $3,
		        now() - interval '2 days')
	`, copyID, targetID, requestID)
	return copyID
}

func (fixture filesGoneFixture) copyRemoved(copyID uuid.UUID) bool {
	fixture.t.Helper()
	var removed bool
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT file_removed_at IS NOT NULL FROM `+filesGoneSchema+`.acquisition_target_files
		WHERE id = $1
	`, copyID).Scan(&removed); err != nil {
		fixture.t.Fatal(err)
	}
	return removed
}

type wantState struct {
	status   string
	fileID   uuid.NullUUID
	lookDue  bool
	acquired bool
}

func (fixture filesGoneFixture) wantState(id uuid.UUID) wantState {
	fixture.t.Helper()
	var state wantState
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT status, acquired_library_file_id, next_attempt_at IS NOT NULL, acquired_at IS NOT NULL
		FROM `+filesGoneSchema+`.acquisition_targets WHERE id = $1
	`, id).Scan(&state.status, &state.fileID, &state.lookDue, &state.acquired); err != nil {
		fixture.t.Fatal(err)
	}
	return state
}

// 00130 points an acquired want whose file was deleted at the present file
// proven to be its recording, the keeper of a duplicate pair.
func TestTheMigrationPointsAWantWhoseFileWentAtTheFileThatHoldsItsRecording(t *testing.T) {
	fixture := newFilesGoneFixture(t)
	recordingID := uuid.New()
	keptID := fixture.file("/music/RIN/Kleinstadt/14.flac")
	fixture.exec(`
		INSERT INTO `+filesGoneSchema+`.library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'acoustid', 'the audio is this recording')
	`, keptID, recordingID)
	wantID := fixture.want("acquired", recordingID, false)

	fixture.migrate()

	state := fixture.wantState(wantID)
	if state.status != "acquired" || !state.fileID.Valid || state.fileID.UUID != keptID {
		t.Fatalf("want = %s on %v, want acquired on %v", state.status, state.fileID, keptID)
	}
}

// 00130 sends an acquired want whose music is gone from the library back to
// looking for it.
func TestTheMigrationLooksAgainForAWantWhoseMusicIsGone(t *testing.T) {
	fixture := newFilesGoneFixture(t)
	wantID := fixture.want("acquired", uuid.New(), false)

	fixture.migrate()

	state := fixture.wantState(wantID)
	if state.status != "pending" || !state.lookDue || state.acquired {
		t.Fatalf("want = %+v, want pending with a look due", state)
	}
}

// The weekly run settles the wants it made, so 00130 leaves one alone.
func TestTheMigrationLeavesAWeeklyTrialWantAlone(t *testing.T) {
	fixture := newFilesGoneFixture(t)
	wantID := fixture.want("acquired", uuid.New(), true)

	fixture.migrate()

	if state := fixture.wantState(wantID); state.status != "acquired" {
		t.Fatalf("weekly want = %s, want acquired", state.status)
	}
}

// 00130 marks an accepted copy whose transfer leads to no library file as
// deleted, so a want stops reading it as a copy on its way in.
func TestTheMigrationMarksACopyWhoseFileWentAsRemoved(t *testing.T) {
	fixture := newFilesGoneFixture(t)
	wantID := fixture.want("pending", uuid.New(), false)
	copyID := fixture.copyOf(wantID, uuid.NullUUID{})

	fixture.migrate()

	if !fixture.copyRemoved(copyID) {
		t.Fatal("the copy whose file is gone is not marked")
	}
	if !fixture.wantState(wantID).lookDue {
		t.Fatal("the pending want holding it has no next look")
	}
}

// A copy that never recorded its file but whose file is still in the library
// is left as it is.
func TestTheMigrationLeavesACopyWhoseFileIsPresent(t *testing.T) {
	fixture := newFilesGoneFixture(t)
	wantID := fixture.want("pending", uuid.New(), false)
	fileID := fixture.file("/music/RIN/14.flac")
	copyID := fixture.copyOf(wantID, uuid.NullUUID{UUID: fileID, Valid: true})

	fixture.migrate()

	if fixture.copyRemoved(copyID) {
		t.Fatal("a copy whose file is present was marked as deleted")
	}
}
