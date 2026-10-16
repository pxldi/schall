package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// placedByAFailedImport records a file the way a failed import left one on
// 2026-10-15: re-encoded and placed for a want, noted as transcoded on the
// request, with no copy row linking it because the import then failed.
func placedByAFailedImport(
	t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string,
	recordingID uuid.UUID, placedAt time.Time,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	fileID := libraryFile(t, pool, rootID, path, "Hum", "Electra 2000")
	identifiedAsRecording(t, pool, fileID, recordingID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET created_at = $2, transcoded_from_format = 'flac', bit_rate_kbps = 320
		WHERE id = $1
	`, fileID, placedAt); err != nil {
		t.Fatal(err)
	}
	// One want per recording, as acquisition_targets_recording_idx asks; every
	// failed request of it is one more file.
	var targetID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM acquisition_targets WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&targetID); errors.Is(err, pgx.ErrNoRows) {
		targetID = uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO acquisition_targets (
				id, origin, entry_artist, entry_title, musicbrainz_recording_id,
				status, resolution_method, resolved_at, next_attempt_at, attempts, summary
			)
			VALUES ($1, 'manual', 'Hum', 'Scraper', $2, 'pending', 'recording_id',
			        now(), now(), 1, 'Looking for a copy.')
		`, targetID, recordingID); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_error
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Hum', 1, 1, 25000000, 'flac', 0.8,
		        '[]'::jsonb, 'completed', 'needs_review',
		        'duplicate key value violates unique constraint "downloads_local_path_idx"')
	`, requestID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO import_transcodes (local_path, source_format, source_size_bytes, target_format)
		VALUES ($1, 'flac', 25000000, 'mp3')
	`, path); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_import_reviews (download_request_id, kind, detail, recorded_at)
		VALUES ($1, 'transcoded', $2, $3)
	`, requestID, "Scraper.flac was written as "+filepath.Base(path)+": 25 MB became 8 MB.",
		placedAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// Hum "Scraper" as the failed imports left it: several MP3s nobody recorded
// and the damaged FLAC they were fetched to replace. The newest MP3 stays and
// is matched again, the others go under a licence, and the FLAC stays.
func TestClearingLeftBehindCopiesKeepsOneCopyBesideTheDamagedFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	album := filepath.Join(rootPath, "Hum", "Electra 2000 [4e1612a2]")
	day := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

	damagedID := libraryFile(t, pool, rootID, filepath.Join(album, "05 - Scraper.flac"), "Hum", "Electra 2000")
	identifiedAsRecording(t, pool, damagedID, recordingID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET decode_error = 'invalid coding type', decode_checked_at = now()
		WHERE id = $1
	`, damagedID); err != nil {
		t.Fatal(err)
	}
	olderPath := filepath.Join(album, "05 Scraper.mp3")
	olderID := placedByAFailedImport(t, pool, rootID, olderPath, recordingID, day)
	keeperPath := filepath.Join(album, "5 - Scraper.mp3")
	keeperID := placedByAFailedImport(t, pool, rootID, keeperPath, recordingID, day.Add(2*time.Hour))

	matcher := &countingMatcher{}
	swept, err := NewRemover(pool, zerolog.Nop()).WithMatcher(matcher).ClearLeftBehindCopies(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(swept.RemovedPaths) != 1 || swept.RemovedPaths[0] != olderPath {
		t.Fatalf("removed = %v, want [%s]", swept.RemovedPaths, olderPath)
	}
	if _, err := os.Stat(olderPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the left-behind copy is still on disc: %v", err)
	}
	if filePresent(t, pool, olderID) {
		t.Fatal("the left-behind copy still has a library row")
	}
	if !filePresent(t, pool, keeperID) || !filePresent(t, pool, damagedID) {
		t.Fatal("the kept copy or the damaged file was deleted")
	}
	licences := removals(t, pool)
	if len(licences) != 1 || licences[0].removedBy != LeftBehindRemovalActor ||
		!licences[0].keptFileID.Valid || licences[0].keptFileID.UUID != keeperID {
		t.Fatalf("licences = %+v, want one by %q keeping %v", licences, LeftBehindRemovalActor, keeperID)
	}
	if len(matcher.reconciled) == 0 || matcher.reconciled[0] != keeperID {
		t.Fatalf("reconciled = %v, want the kept copy %v first", matcher.reconciled, keeperID)
	}
}

// A file that came any other way is never an extra. It is kept, and the copy
// the failed import left beside it goes.
func TestClearingLeftBehindCopiesKeepsAFileNoFailedImportPlaced(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	album := filepath.Join(rootPath, "Hum", "Electra 2000 [4e1612a2]")

	ownedPath := filepath.Join(album, "05 - Scraper.mp3")
	ownedID := libraryFile(t, pool, rootID, ownedPath, "Hum", "Electra 2000")
	identifiedAsRecording(t, pool, ownedID, recordingID)
	extraPath := filepath.Join(album, "01-05 Scraper.mp3")
	extraID := placedByAFailedImport(t, pool, rootID, extraPath, recordingID,
		time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))

	if _, err := NewRemover(pool, zerolog.Nop()).ClearLeftBehindCopies(ctx); err != nil {
		t.Fatal(err)
	}

	if !filePresent(t, pool, ownedID) {
		t.Fatal("a file no failed import placed was deleted")
	}
	if filePresent(t, pool, extraID) {
		t.Fatal("the left-behind copy beside it is still in the library")
	}
}

// A recording with no left-behind copy is not touched, however many files it has.
func TestClearingLeftBehindCopiesLeavesOrdinaryDuplicatesAlone(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	firstID := libraryFile(t, pool, rootID, filepath.Join(rootPath, "a", "roads.mp3"), "Portishead", "Dummy")
	secondID := libraryFile(t, pool, rootID, filepath.Join(rootPath, "b", "roads.mp3"), "Portishead", "Roads")
	identifiedAsRecording(t, pool, firstID, recordingID)
	identifiedAsRecording(t, pool, secondID, recordingID)

	swept, err := NewRemover(pool, zerolog.Nop()).ClearLeftBehindCopies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(swept.RemovedPaths) != 0 || !filePresent(t, pool, firstID) || !filePresent(t, pool, secondID) {
		t.Fatalf("removed = %v from a recording no failed import touched", swept.RemovedPaths)
	}
}
