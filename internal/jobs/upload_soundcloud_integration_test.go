package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/soundcloud"
	"github.com/pxldi/schall/internal/uploads"
	"github.com/rs/zerolog"
)

// fakeSoundCloudTracks answers the way SoundCloud does, without SoundCloud.
type fakeSoundCloudTracks struct {
	track   soundcloud.Track
	lookErr error
}

func (fake *fakeSoundCloudTracks) Lookup(context.Context, string) (soundcloud.Track, error) {
	if fake.lookErr != nil {
		return soundcloud.Track{}, fake.lookErr
	}
	return fake.track, nil
}

func (fake *fakeSoundCloudTracks) Artwork(context.Context, string) ([]byte, string, error) {
	return nil, "", nil
}

func anEditTrack() soundcloud.Track {
	return soundcloud.Track{
		ID:          "293",
		ExternalID:  "soundcloud:293",
		Title:       "PinkPantheress - Illegal (Abo Edit) by Abo",
		Uploader:    "Abo",
		UploaderURL: "https://soundcloud.com/abo",
		Permalink:   "https://soundcloud.com/abo/illegal",
	}
}

// stageUpload puts real, decodable audio in the staging folder under each
// name given, the way an accepted upload leaves it, so import can be asked
// about a real upload id.
func stageUpload(t *testing.T, staging *uploads.Staging, names ...string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := os.MkdirAll(staging.Path(id), 0o755); err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(filepath.Join("..", "tagging", "testdata", "silence.flac"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(staging.Path(id), name), audio, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// queueRealUploadImport inserts the jobs row Import's own CompleteUploadImport
// expects to find and update, and hands back the payload it was actually
// written with. Driving the worker straight off a Job{} in memory is enough
// for the rest of this package's tests, but here the importer talks to a real
// database — CompleteUploadImport refuses to commit an import with no queued
// job behind it — and the payload has to be the one QueueUploadImport's own
// SQL wrote, not a second encoding of it built by hand, or a naming key that
// drifted between the two would pass every test and name nothing in
// production.
func queueRealUploadImport(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	uploadID uuid.UUID, names []string, naming *db.UploadNaming,
) (uuid.UUID, []byte) {
	t.Helper()
	row, err := db.New(pool).QueueUploadImport(ctx, uploadID, names, int64(len(names))*8521, naming)
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE id = $1`, row.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return row.ID, payload
}

// newUploadWorker wires a real staging folder, a real importer, a real
// scanner and a real SoundCloud service against one scratch database — the
// same components cmd/schall/main.go wires together — behind a fake queue, so
// the job can be driven directly the way the rest of this package's worker
// tests do.
func newUploadWorker(
	t *testing.T, tracks soundcloud.Tracks,
) (*Worker, *fakeQueue, *uploads.Staging, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.Setup(t)
	base := t.TempDir()
	staging, err := uploads.NewStaging(filepath.Join(base, "staging"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	queries := db.New(pool)
	importer := uploads.NewImporter(staging, filepath.Join(base, "music"), queries, zerolog.Nop())
	scanner := library.NewScanner(pool)
	soundCloudService := soundcloud.NewService(pool, tracks, zerolog.Nop())
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop(), scanner).
		WithUploadImporter(importer).
		WithSoundCloud(soundCloudService)
	return worker, queue, staging, pool
}

// This is the one the issue is about: the address is known before the upload
// starts, and the file comes out of the import named under what the person
// confirmed rather than under the uploader and the raw SoundCloud title.
func TestAnUploadWithOneFileIsNamedFromTheConfirmedSoundCloudNaming(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	worker, queue, staging, pool := newUploadWorker(t, &fakeSoundCloudTracks{track: anEditTrack()})

	uploadID := stageUpload(t, staging, "illegal.flac")
	naming := &db.UploadNaming{
		URL: "https://soundcloud.com/abo/illegal", Artist: "PinkPantheress", Title: "Illegal", Remixer: "Abo",
	}
	jobID, payload := queueRealUploadImport(t, ctx, pool, uploadID, []string{"illegal.flac"}, naming)
	worker.process(ctx, Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != uuid.Nil {
		t.Fatalf("the job failed: %s", queue.message)
	}

	var kind, source, artist, title string
	var remixer *string
	var manual bool
	if err := pool.QueryRow(ctx, `
		SELECT kind, source, artist_name, track_title, remixer_name, is_manual
		FROM library_file_identities
	`).Scan(&kind, &source, &artist, &title, &remixer, &manual); err != nil {
		t.Fatalf("read the identity written for the upload: %v", err)
	}
	if kind != "source" || source != "soundcloud" {
		t.Fatalf("identity = %q/%q, want a manual SoundCloud source identity", kind, source)
	}
	if artist != "PinkPantheress" || title != "Illegal" || remixer == nil || *remixer != "Abo" {
		t.Fatalf("naming = %q by %q (remixer %v), want what was confirmed, not the uploader or the raw title",
			title, artist, remixer)
	}
	if !manual {
		t.Fatal("a SoundCloud naming is a decision somebody made; it must be manual")
	}
}

// One address names one track. A selection of more than one file has no
// single file for it to be about, so the naming is dropped and neither file
// is named from it — but both still land, because the address is not a
// reason to refuse the files themselves.
func TestAnUploadWithTwoFilesImportsBothAndNamesNeitherFromASoundCloudAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	worker, queue, staging, pool := newUploadWorker(t, &fakeSoundCloudTracks{track: anEditTrack()})

	uploadID := stageUpload(t, staging, "one.flac", "two.flac")
	naming := &db.UploadNaming{URL: "https://soundcloud.com/abo/illegal", Artist: "PinkPantheress", Title: "Illegal"}
	jobID, payload := queueRealUploadImport(t, ctx, pool, uploadID, []string{"one.flac", "two.flac"}, naming)
	worker.process(ctx, Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != uuid.Nil {
		t.Fatalf("the job failed: %s", queue.message)
	}

	// A naming for more than one file names none of them, but it is still not
	// a reason to refuse the files: both landed, on disk, exactly as an
	// upload with no address at all would have.
	var importPath string
	if err := pool.QueryRow(ctx,
		`SELECT payload->>'importPath' FROM jobs WHERE id = $1`, jobID,
	).Scan(&importPath); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.flac", "two.flac"} {
		if _, err := os.Stat(filepath.Join(importPath, name)); err != nil {
			t.Fatalf("%q did not land: %v", name, err)
		}
	}
	// No scan ran — nothing here needed one, since there was no single file
	// left to name — so there is no library_files row to check against; the
	// naming itself is what must be absent.
	var identities int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM library_file_identities`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("%d files were named from an address that named no single file", identities)
	}
}

// The file already landed by the time SoundCloud is asked again to confirm
// what it is. A track SoundCloud can no longer find is a reason to leave the
// file unnamed, never a reason to undo an import that already succeeded.
func TestASoundCloudFailureAfterAFileLandsStillCompletesTheImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	worker, queue, staging, pool := newUploadWorker(t, &fakeSoundCloudTracks{lookErr: errors.New("soundcloud is down")})

	uploadID := stageUpload(t, staging, "illegal.flac")
	naming := &db.UploadNaming{URL: "https://soundcloud.com/abo/illegal", Artist: "PinkPantheress", Title: "Illegal"}
	jobID, payload := queueRealUploadImport(t, ctx, pool, uploadID, []string{"illegal.flac"}, naming)
	worker.process(ctx, Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed == uuid.Nil || queue.failed != uuid.Nil {
		t.Fatalf("completed = %s, failed = %s (%s), want the import to succeed regardless",
			queue.completed, queue.failed, queue.message)
	}
	var files, identities int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM library_files`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("library_files = %d, want the file imported despite the naming failure", files)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM library_file_identities`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("%d identities were written for a naming that failed", identities)
	}
}
