package removals

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
	"github.com/rs/zerolog"
)

// fixture is one recording held twice: once on a file Schall downloaded for
// the want, once on a file somebody put in the library by hand.
type fixture struct {
	pool       *pgxpool.Pool
	service    *Service
	now        time.Time
	clock      *time.Time
	recording  uuid.UUID
	targetID   uuid.UUID
	downloaded uuid.UUID
	ownPath    string
	ownFile    uuid.UUID
	fetched    string
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := t.TempDir()
	rootID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, rootID, rootPath); err != nil {
		t.Fatal(err)
	}
	file := func(name string) (uuid.UUID, string) {
		t.Helper()
		path := filepath.Join(rootPath, name)
		if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		id := uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO library_files (id, path, size_bytes, modified_at, library_root_id)
			VALUES ($1, $2, 5, now(), $3)
		`, id, path, rootID); err != nil {
			t.Fatal(err)
		}
		return id, path
	}
	recording := uuid.New()
	downloaded, fetched := file("fetched.flac")
	ownFile, ownPath := file("ripped.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, provider, musicbrainz_recording_id, method, confidence, summary
		)
		VALUES ($1, 'external', 'musicbrainz', $2, 'recording-id', 1, 'tagged')
	`, ownFile, recording); err != nil {
		t.Fatal(err)
	}

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_title, musicbrainz_recording_id, status,
			acquired_at, acquired_library_file_id, summary
		)
		VALUES ($1, 'manual', 'Glory Box', $2, 'acquired', now(), $3, 'In your library.')
	`, targetID, recording, downloaded); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path, verdict, library_file_id
		)
		VALUES ($1, 'slskd', 'peer', 'Music\Glory Box.flac', 'accepted', $2)
	`, targetID, downloaded); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	clock := now
	service := NewService(pool, library.NewRemover(pool, zerolog.Nop()), zerolog.Nop()).
		WithClock(func() time.Time { return clock })
	return fixture{
		pool: pool, service: service, now: now, clock: &clock, recording: recording,
		targetID: targetID, downloaded: downloaded, fetched: fetched,
		ownFile: ownFile, ownPath: ownPath,
	}
}

func (f fixture) status(t *testing.T) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM acquisition_targets WHERE id = $1`, f.targetID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func onDisc(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func TestRemovingStopsTheWantAtOnce(t *testing.T) {
	f := setup(t)
	if _, err := f.service.Remove(context.Background(), f.targetID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if got := f.status(t); got != "not_wanted" {
		t.Errorf("want status = %s, want not_wanted", got)
	}
	if !onDisc(f.fetched) {
		t.Error("the downloaded file was deleted at once")
	}
}

func TestRemovingSchedulesOnlyTheDownloadedFile(t *testing.T) {
	f := setup(t)
	outcome, err := f.service.Remove(context.Background(), f.targetID)
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(outcome.Scheduled) != 1 || outcome.Scheduled[0].FileID != f.downloaded {
		t.Fatalf("scheduled = %+v, want only the downloaded file", outcome.Scheduled)
	}
	if !outcome.Scheduled[0].RemovesAt.Equal(f.now.Add(Grace)) {
		t.Errorf("removes at %s, want %s", outcome.Scheduled[0].RemovesAt, f.now.Add(Grace))
	}
	if outcome.Kept != 1 {
		t.Errorf("kept = %d, want the file added by hand", outcome.Kept)
	}
}

func TestRemovingAgainKeepsTheFirstDate(t *testing.T) {
	f := setup(t)
	if _, err := f.service.Remove(context.Background(), f.targetID); err != nil {
		t.Fatal(err)
	}
	*f.clock = f.now.Add(5 * 24 * time.Hour)
	again, err := f.service.Remove(context.Background(), f.targetID)
	if err != nil {
		t.Fatalf("Remove() again error = %v", err)
	}
	if len(again.Scheduled) != 1 || !again.Scheduled[0].RemovesAt.Equal(f.now.Add(Grace)) {
		t.Errorf("second press scheduled %+v, want the first date", again.Scheduled)
	}
}

func TestTheSweepLeavesAFileBeforeItsDay(t *testing.T) {
	f := setup(t)
	if _, err := f.service.Remove(context.Background(), f.targetID); err != nil {
		t.Fatal(err)
	}
	*f.clock = f.now.Add(Grace - time.Minute)
	swept, err := f.service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if swept.Removed != 0 || !onDisc(f.fetched) {
		t.Fatalf("swept %+v a minute early", swept)
	}
}

func TestTheSweepDeletesTheDownloadedFileOnItsDay(t *testing.T) {
	f := setup(t)
	if _, err := f.service.Remove(context.Background(), f.targetID); err != nil {
		t.Fatal(err)
	}
	*f.clock = f.now.Add(Grace)
	swept, err := f.service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if swept.Removed != 1 || onDisc(f.fetched) {
		t.Fatalf("swept %+v, want the downloaded file deleted", swept)
	}
	if !onDisc(f.ownPath) {
		t.Error("the file added by hand was deleted")
	}
	var state string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT state FROM scheduled_removals`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "removed" {
		t.Errorf("removal state = %s, want removed", state)
	}
}

// Deleting the file must not set the removed want looking for it again.
func TestTheDeleteLeavesTheWantRemoved(t *testing.T) {
	f := setup(t)
	if _, err := f.service.Remove(context.Background(), f.targetID); err != nil {
		t.Fatal(err)
	}
	*f.clock = f.now.Add(Grace)
	if _, err := f.service.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != "not_wanted" {
		t.Errorf("want status = %s after the delete, want not_wanted", got)
	}
}

func TestWantingAgainKeepsTheFile(t *testing.T) {
	f := setup(t)
	if _, err := f.service.Remove(context.Background(), f.targetID); err != nil {
		t.Fatal(err)
	}
	targets, err := f.service.KeepRecording(context.Background(), f.recording)
	if err != nil {
		t.Fatalf("KeepRecording() error = %v", err)
	}
	if len(targets) != 1 || targets[0] != f.targetID {
		t.Errorf("kept wants = %v, want the removed one", targets)
	}

	*f.clock = f.now.Add(Grace + time.Hour)
	swept, err := f.service.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if swept.Removed != 0 || !onDisc(f.fetched) {
		t.Fatalf("swept %+v, want the kept file left alone", swept)
	}
}

func TestAFileStillNeededIsSpared(t *testing.T) {
	for name, hold := range map[string]string{
		"playlist": `
			WITH list AS (
			    INSERT INTO playlists (source, name) VALUES ('manual', 'Mine') RETURNING id
			)
			INSERT INTO playlist_entries (playlist_id, position, entry_title, owned_library_file_id)
			SELECT id, 1, 'Glory Box', $1 FROM list`,
		"want": `
			INSERT INTO acquisition_targets (
				origin, entry_title, status, acquired_at, acquired_library_file_id,
				anchor_source, summary
			)
			VALUES ('playlist', 'Glory Box', 'acquired', now(), $1, 'deezer', 'In your library.')`,
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			if _, err := f.service.Remove(context.Background(), f.targetID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(context.Background(), hold, f.downloaded); err != nil {
				t.Fatal(err)
			}
			*f.clock = f.now.Add(Grace)
			swept, err := f.service.Sweep(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if swept.Spared != 1 || !onDisc(f.fetched) {
				t.Fatalf("swept %+v, want the file spared", swept)
			}
		})
	}
}

// A release page draws the day a removed track's file goes.
func TestAReleaseTrackSaysWhenItsFileIsDeleted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var albumID uuid.UUID
	if err := f.pool.QueryRow(ctx, `
		WITH artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES (gen_random_uuid(), 'Portishead', 'Portishead') RETURNING id
		),
		album AS (
		    INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		    SELECT id, gen_random_uuid(), 'Dummy' FROM artist RETURNING id
		)
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		SELECT id, $1, 'Glory Box', 1, 11 FROM album RETURNING album_id
	`, f.recording).Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Remove(ctx, f.targetID); err != nil {
		t.Fatal(err)
	}

	tracks, err := db.New(f.pool).ListAlbumTracks(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || !tracks[0].RemovesAt.Valid ||
		!tracks[0].RemovesAt.Time.Equal(f.now.Add(Grace)) {
		t.Fatalf("tracks = %+v, want the track to say its file goes in 30 days", tracks)
	}
}
