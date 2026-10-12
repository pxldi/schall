package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

// A popular list is recordings by MusicBrainz ID, and the catalogue answers
// for each by that ID alone: the track that carries it, whether a file holds
// it, and the want on it. An album row says its secondary types and how many of
// its tracks are wanted, so the artist page can file a mixtape under Other and
// draw the release's own toggle. Both say when a removed recording's file is
// deleted.
func TestPopularTracksAndTheReleaseRowsBesideThem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, mixtapeID, singleID := uuid.New(), uuid.New(), uuid.New()
	held, wanted, dismissed, absent := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	fileID := uuid.New()
	heldTrack, removedWant := uuid.New(), uuid.New()
	removesAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO artists (id, musicbrainz_id, name, sort_name) VALUES ($1, $2, 'Kendrick Lamar', 'Lamar, Kendrick')`,
			[]any{artistID, uuid.New()}},
		{`INSERT INTO albums (id, artist_id, title, album_type, secondary_types)
		  VALUES ($1, $2, 'Overly Dedicated', 'album', '{mixtape/street}')`, []any{mixtapeID, artistID}},
		{`INSERT INTO albums (id, artist_id, title, album_type) VALUES ($1, $2, 'luther', 'single')`,
			[]any{singleID, artistID}},
		{`INSERT INTO tracks (id, album_id, title, disc_number, track_number, musicbrainz_recording_id)
		  VALUES ($1, $2, 'Held', 1, 1, $3)`, []any{heldTrack, mixtapeID, held}},
		{`INSERT INTO tracks (album_id, title, disc_number, track_number, musicbrainz_recording_id)
		  VALUES ($1, 'Wanted', 1, 2, $2), ($1, 'Dismissed', 1, 3, $3)`, []any{mixtapeID, wanted, dismissed}},
		{`INSERT INTO tracks (album_id, title, disc_number, track_number, musicbrainz_recording_id)
		  VALUES ($1, 'luther', 1, 1, $2)`, []any{singleID, wanted}},
		{`INSERT INTO library_files (id, path, size_bytes, modified_at) VALUES ($1, '/music/held.flac', 4096, now())`,
			[]any{fileID}},
		{`INSERT INTO track_mappings (track_id, library_file_id, method) VALUES ($1, $2, 'exact')`,
			[]any{heldTrack, fileID}},
		{`INSERT INTO acquisition_targets (origin, entry_artist, entry_title, musicbrainz_recording_id,
		      status, resolution_method, resolved_at, next_attempt_at)
		  VALUES ('manual', 'Kendrick Lamar', 'Wanted', $1, 'pending', 'isrc', now(), now())`, []any{wanted}},
		{`INSERT INTO acquisition_targets (origin, entry_artist, entry_title, musicbrainz_recording_id,
		      status, resolution_method, resolved_at, not_wanted_at)
		  VALUES ('manual', 'Kendrick Lamar', 'Dismissed', $1, 'not_wanted', 'isrc', now(), now())`, []any{dismissed}},
		// Somebody removed the held track: its file goes in 30 days.
		{`INSERT INTO acquisition_targets (id, origin, entry_artist, entry_title, musicbrainz_recording_id,
		      status, resolution_method, resolved_at, not_wanted_at)
		  VALUES ($1, 'manual', 'Kendrick Lamar', 'Held', $2, 'not_wanted', 'isrc', now(), now())`,
			[]any{removedWant, held}},
		{`INSERT INTO scheduled_removals (acquisition_target_id, musicbrainz_recording_id, library_file_id,
		      library_path, removes_at)
		  VALUES ($1, $2, $3, '/music/held.flac', $4)`, []any{removedWant, held, fileID, removesAt}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed %q: %v", statement.sql, err)
		}
	}

	queries := New(pool)
	rows, err := queries.PopularTracks(ctx, artistID, []uuid.UUID{wanted, absent, held, dismissed})
	if err != nil {
		t.Fatalf("PopularTracks() error = %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}
	order := []uuid.UUID{rows[0].RecordingID, rows[1].RecordingID, rows[2].RecordingID, rows[3].RecordingID}
	if !slices.Equal(order, []uuid.UUID{wanted, absent, held, dismissed}) {
		t.Fatalf("order = %v, want the order asked in", order)
	}
	if rows[0].WantStatus.String != "pending" || rows[0].Held || !rows[0].TrackID.Valid {
		t.Errorf("wanted = %+v", rows[0])
	}
	if rows[1].TrackID.Valid || rows[1].Held || rows[1].WantID.Valid {
		t.Errorf("a recording the catalogue lacks came back with %+v", rows[1])
	}
	if !rows[2].Held || rows[2].AlbumID.UUID != mixtapeID || !rows[2].RemovesAt.Time.Equal(removesAt) {
		t.Errorf("held = %+v, want it leaving on %s", rows[2], removesAt)
	}
	if rows[0].RemovesAt.Valid {
		t.Errorf("a recording nobody removed is leaving: %+v", rows[0])
	}
	if rows[3].WantStatus.String != "not_wanted" {
		t.Errorf("dismissed = %+v", rows[3])
	}

	albums, err := queries.ListAlbums(ctx, ListAlbumsParams{ArtistID: uuid.NullUUID{UUID: artistID, Valid: true}})
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}
	byID := map[uuid.UUID]AlbumRow{}
	for _, album := range albums.Items {
		byID[album.ID] = album
	}
	mixtape := byID[mixtapeID]
	if !slices.Equal(mixtape.SecondaryTypes, []string{"mixtape/street"}) {
		t.Errorf("secondary types = %v", mixtape.SecondaryTypes)
	}
	// One pending want counts; the dismissal and the held track do not.
	if mixtape.WantedTrackCount != 1 || byID[singleID].WantedTrackCount != 1 {
		t.Errorf("wanted counts = %d, %d; want 1, 1", mixtape.WantedTrackCount, byID[singleID].WantedTrackCount)
	}
	// The removed track's file is leaving, so the release says when.
	if mixtape.LeavingTrackCount != 1 || !mixtape.RemovesAt.Time.Equal(removesAt) {
		t.Errorf("mixtape leaving = %d on %v; want 1 on %s", mixtape.LeavingTrackCount, mixtape.RemovesAt, removesAt)
	}
	if single := byID[singleID]; single.LeavingTrackCount != 0 || single.RemovesAt.Valid {
		t.Errorf("single leaving = %d on %v; want none", single.LeavingTrackCount, single.RemovesAt)
	}
}
