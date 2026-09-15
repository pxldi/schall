package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/dbtest"
)

// Resolution costs a rate-limited provider request per file, so the queue must
// ask each question once: a scan that rediscovers a library must not queue the
// same file again, and the summary must report what is still unanswered.
func TestFileResolutionQueueAsksEachQuestionOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queries := New(pool)
	askedID, answeredID, missingID, failedID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, resolution_status)
		VALUES ($1, '/music/unasked.flac', 10, now(), 'pending'),
		       ($2, '/music/local-only.flac', 10, now(), 'local_only'),
		       ($3, '/music/gone.flac', 10, now(), 'pending'),
		       -- A provider outage says nothing about the music, so the file is
		       -- asked about again rather than left in that state forever.
		       ($4, '/music/provider-was-down.flac', 10, now(), 'failed')
	`, askedID, answeredID, missingID, failedID); err != nil {
		t.Fatal(err)
	}
	// A file that is no longer on disk is not worth a provider request.
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, missingID); err != nil {
		t.Fatal(err)
	}

	queued, err := queries.QueueFileResolutions(ctx, 100)
	if err != nil {
		t.Fatalf("QueueFileResolutions() error = %v", err)
	}
	if queued != 2 {
		t.Fatalf("queued = %d, want the unasked and the failed present files", queued)
	}
	again, err := queries.QueueFileResolutions(ctx, 100)
	if err != nil {
		t.Fatalf("QueueFileResolutions() error = %v", err)
	}
	if again != 0 {
		t.Errorf("queued again = %d, want the question asked only once", again)
	}

	job, err := queries.QueueFileResolution(ctx, askedID)
	if err != nil {
		t.Fatalf("QueueFileResolution() error = %v", err)
	}
	if job.Status != "queued" {
		t.Errorf("job status = %q", job.Status)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'resolve_library_file'
	`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 {
		t.Errorf("jobs = %d, want the queued job returned rather than a third one", jobs)
	}
	if _, err := queries.QueueFileResolution(ctx, missingID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("QueueFileResolution(missing) error = %v, want no rows", err)
	}

	summary, err := queries.LibrarySummary(ctx)
	if err != nil {
		t.Fatalf("LibrarySummary() error = %v", err)
	}
	if summary.PendingCount != 1 || summary.LocalOnlyCount != 1 ||
		summary.FailedCount != 1 || summary.ResolvedCount != 0 {
		t.Errorf("summary = %+v", summary)
	}

	page, err := queries.ListLibraryFiles(ctx, ListLibraryFilesParams{
		Limit: 10, Resolution: "local_only",
	})
	if err != nil {
		t.Fatalf("ListLibraryFiles() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != answeredID {
		t.Fatalf("page = %+v", page)
	}
	if page.Items[0].ResolutionStatus != "local_only" {
		t.Errorf("resolution status = %q", page.Items[0].ResolutionStatus)
	}
}

// An identity is one of two claims about a file: it is a MusicBrainz recording,
// or it is music MusicBrainz does not hold. A third kind would be a claim nothing
// in the project knows how to read.
func TestAnIdentityOfAnUnknownKindIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/unknown.flac', 10, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, method, summary)
		VALUES ($1, 'probably_something', 'guess', 'it felt right')
	`, fileID); err == nil {
		t.Fatal("an identity of a kind nothing reads was recorded")
	}
}

// Saying a file is a MusicBrainz recording without naming one is not an identity.
// It would read as resolved everywhere and answer nothing.
func TestAnExternalIdentityMustNameARecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/nameless.flac', 10, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, method, summary)
		VALUES ($1, 'external', 'recording-id', 'proven by identifier')
	`, fileID); err == nil {
		t.Fatal("a file was recorded as a MusicBrainz recording without naming one")
	}
}

// And the opposite: deciding a file is music MusicBrainz does not hold, while
// naming the recording it is, is a row that contradicts itself.
func TestALocalOnlyIdentityMayNotNameARecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/contradiction.flac', 10, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'local_only', $2, 'recording-id', 'MusicBrainz does not hold this')
	`, fileID, uuid.New()); err == nil {
		t.Fatal("a file was recorded as unknown to MusicBrainz and as one of its recordings")
	}
}
