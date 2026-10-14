package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// countsByName runs every invariant and keys the counts by name.
func countsByName(ctx context.Context, t *testing.T, store *Queries) map[string]int64 {
	t.Helper()
	counts, err := store.CountInvariantViolations(ctx)
	if err != nil {
		t.Fatalf("count invariant violations: %v", err)
	}
	if len(counts) != len(Invariants) {
		t.Fatalf("got %d counts for %d invariants", len(counts), len(Invariants))
	}
	byName := make(map[string]int64, len(counts))
	for _, count := range counts {
		byName[count.Name] = count.Violations
	}
	return byName
}

func assertCounts(t *testing.T, got map[string]int64, want map[string]int64) {
	t.Helper()
	for name, violations := range got {
		if violations != want[name] {
			t.Errorf("%s = %d; want %d", name, violations, want[name])
		}
	}
}

func invariantStore(t *testing.T) (context.Context, *pgxpool.Pool, *Queries) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool := dbtest.Setup(t)
	return ctx, pool, New(pool)
}

// plantFileGoneWant writes the shape the live read of 2026-10-14 found 17
// times: an acquired want whose file row was deleted, shown on the release
// page as "Fetched once, file gone", and the accepted copy still pointing at
// that deleted file. It returns the want's recording.
func plantFileGoneWant(ctx context.Context, t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	recording, want, request := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
		    id, origin, entry_artist, entry_title, musicbrainz_recording_id,
		    status, acquired_at, acquired_library_file_id)
		VALUES ($1, 'manual', 'Souly', 'Nichts hittet mehr', $2, 'acquired', now(), NULL)
	`, want, recording); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
		    id, provider, source_username, source_directory, status, file_count,
		    expected_track_count, total_size_bytes, format, score, acquisition_target_id)
		VALUES ($1, 'slskd', 'peer', 'music', 'completed', 1, 1, 1, 'mp3', 1, $2)
	`, request, want); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
		    acquisition_target_id, provider, source_username, remote_path, verdict,
		    download_request_id, library_file_id)
		VALUES ($1, 'slskd', 'peer', 'music/07 Nichts Hittet Mehr.mp3', 'accepted', $2, NULL)
	`, want, request); err != nil {
		t.Fatal(err)
	}
	return recording
}

func TestEveryInvariantReadsZeroOnAnEmptyDatabase(t *testing.T) {
	ctx, _, store := invariantStore(t)
	assertCounts(t, countsByName(ctx, t, store), map[string]int64{})
}

func TestAnAcquiredWantWithItsFileDeletedBreaksTwoInvariants(t *testing.T) {
	ctx, pool, store := invariantStore(t)
	plantFileGoneWant(ctx, t, pool)

	assertCounts(t, countsByName(ctx, t, store), map[string]int64{
		"acquired_recording_not_held": 1,
		"accepted_copy_file_gone":     1,
	})
}

// The release page calls a recording held when any present file is proven to
// be it, so such a file settles the want even though the copy still points at
// nothing.
func TestAProvenFileElsewhereHoldsAnAcquiredWantsRecording(t *testing.T) {
	ctx, pool, store := invariantStore(t)
	recording := plantFileGoneWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		WITH root AS (
		    INSERT INTO library_roots (path) VALUES ('/music') RETURNING id),
		file AS (
		    INSERT INTO library_files (path, size_bytes, modified_at, library_root_id,
		                               resolution_status, match_status)
		    SELECT '/music/Souly/07 Nichts Hittet Mehr.mp3', 1, now(), id, 'resolved', 'unmatched'
		    FROM root RETURNING id)
		INSERT INTO library_file_identities (library_file_id, kind, musicbrainz_recording_id, method)
		SELECT id, 'external', $1, 'acoustid' FROM file
	`, recording); err != nil {
		t.Fatal(err)
	}

	assertCounts(t, countsByName(ctx, t, store), map[string]int64{
		"accepted_copy_file_gone": 1,
	})
}

// The live read found 16 albums with no tracks, most never refreshed.
func TestAnAlbumADayOldWithNoTracksBreaksAnInvariant(t *testing.T) {
	ctx, pool, store := invariantStore(t)
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (name, sort_name) VALUES ('Ye', 'Ye') RETURNING id)
		INSERT INTO albums (artist_id, title, created_at)
		SELECT id, 'YANDHI', now() - interval '2 days' FROM artist
	`); err != nil {
		t.Fatal(err)
	}

	assertCounts(t, countsByName(ctx, t, store), map[string]int64{
		"album_without_tracks": 1,
	})
}

// A second accepted copy records its file only on its transfer, and that file
// counts as the copy's.
func TestAnAcceptedCopyWhoseTransferHoldsItsFileKeepsTheInvariant(t *testing.T) {
	ctx, pool, store := invariantStore(t)
	want, request := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (id, origin, entry_artist, entry_title, status)
		VALUES ($1, 'manual', 'Souly', 'Nichts hittet mehr', 'unresolved')
	`, want); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
		    id, provider, source_username, source_directory, status, file_count,
		    expected_track_count, total_size_bytes, format, score, acquisition_target_id)
		VALUES ($1, 'slskd', 'peer', 'music', 'completed', 1, 1, 1, 'mp3', 1, $2)
	`, request, want); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		WITH root AS (
		    INSERT INTO library_roots (path) VALUES ('/music') RETURNING id),
		file AS (
		    INSERT INTO library_files (path, size_bytes, modified_at, library_root_id)
		    SELECT '/music/Souly/07 Nichts Hittet Mehr.mp3', 1, now(), id FROM root RETURNING id)
		INSERT INTO downloads (download_request_id, remote_path, status, library_file_id)
		SELECT $1, 'music/07 Nichts Hittet Mehr.mp3', 'completed', id FROM file
	`, request); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
		    acquisition_target_id, provider, source_username, remote_path, verdict,
		    download_request_id, library_file_id)
		VALUES ($1, 'slskd', 'peer', 'music/07 Nichts Hittet Mehr.mp3', 'accepted', $2, NULL)
	`, want, request); err != nil {
		t.Fatal(err)
	}

	assertCounts(t, countsByName(ctx, t, store), map[string]int64{})
}
