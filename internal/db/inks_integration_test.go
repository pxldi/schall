package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
)

func inkText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }

func TestACoverIsSavedWithItsInks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)

	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: []byte("jpeg"), ContentType: "image/jpeg", Source: "caa",
		InkDark: inkText("#101820"), InkLight: inkText("#f0a070"),
		Palette: []string{"#203040", "#f0a070"},
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}

	rows, err := queries.ReleaseInks(ctx, []uuid.UUID{albumID, uuid.New()})
	if err != nil {
		t.Fatalf("ReleaseInks() error = %v", err)
	}
	if len(rows) != 1 || rows[0].InkDark != "#101820" || rows[0].InkLight != "#f0a070" ||
		!slices.Equal(rows[0].Palette, []string{"#203040", "#f0a070"}) {
		t.Fatalf("ReleaseInks() = %+v, want the saved inks for the one pictured release", rows)
	}
}

func TestANewPictureReplacesTheOldPicturesInks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)

	for _, params := range []SaveReleaseCoverArtParams{
		{AlbumID: albumID, Image: []byte("old"), ContentType: "image/jpeg", Source: "caa",
			InkDark: inkText("#101820"), InkLight: inkText("#f0a070"), Palette: []string{"#f0a070"}},
		{AlbumID: albumID, Image: []byte("new"), ContentType: "image/svg+xml", Source: "user",
			Palette: []string{}},
	} {
		if err := queries.SaveReleaseCoverArt(ctx, params); err != nil {
			t.Fatalf("SaveReleaseCoverArt() error = %v", err)
		}
	}

	rows, err := queries.ReleaseInks(ctx, []uuid.UUID{albumID})
	if err != nil {
		t.Fatalf("ReleaseInks() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ReleaseInks() = %+v, want none: the new picture had none", rows)
	}
	waiting, err := queries.ReleaseCoversWithoutInks(ctx, 10)
	if err != nil {
		t.Fatalf("ReleaseCoversWithoutInks() error = %v", err)
	}
	if len(waiting) != 0 {
		t.Fatalf("ReleaseCoversWithoutInks() = %d rows, want the read picture left alone", len(waiting))
	}
}

func TestACoverCachedBeforeInksIsReadOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	// What every row looked like before 00106.
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		VALUES ($1, 'jpeg', 'image/jpeg', 'caa')
	`, albumID); err != nil {
		t.Fatal(err)
	}

	waiting, err := queries.ReleaseCoversWithoutInks(ctx, 10)
	if err != nil {
		t.Fatalf("ReleaseCoversWithoutInks() error = %v", err)
	}
	if len(waiting) != 1 || waiting[0].AlbumID != albumID {
		t.Fatalf("ReleaseCoversWithoutInks() = %+v, want the old cover", waiting)
	}
	written, err := queries.SaveReleaseInks(ctx, SaveReleaseInksParams{
		AlbumID: albumID, FetchedAt: waiting[0].FetchedAt,
		InkDark: inkText("#101820"), InkLight: inkText("#f0a070"), Palette: []string{"#f0a070"},
	})
	if err != nil || written != 1 {
		t.Fatalf("SaveReleaseInks() = %d, %v, want one row written", written, err)
	}
	again, err := queries.ReleaseCoversWithoutInks(ctx, 10)
	if err != nil {
		t.Fatalf("ReleaseCoversWithoutInks() error = %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("ReleaseCoversWithoutInks() = %+v after reading, want none", again)
	}
}

func TestInksReadFromAReplacedPictureAreNotWritten(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source, fetched_at)
		VALUES ($1, 'jpeg', 'image/jpeg', 'caa', now())
	`, albumID); err != nil {
		t.Fatal(err)
	}

	written, err := queries.SaveReleaseInks(ctx, SaveReleaseInksParams{
		AlbumID:   albumID,
		FetchedAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		InkDark:   inkText("#101820"), InkLight: inkText("#f0a070"), Palette: []string{"#f0a070"},
	})
	if err != nil || written != 0 {
		t.Fatalf("SaveReleaseInks() = %d, %v, want nothing written over a newer picture", written, err)
	}
}

func TestAPlaylistIsPicturedByItsFirstEntryWithARelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	_, albumID, trackID, _ := seedFollowedArtist(t, ctx, pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-cover", "musik")
	if err != nil {
		t.Fatal(err)
	}
	// The first entry leads nowhere; the second is a file mapped onto the
	// release.
	if _, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "BONES", EntryTitle: "HDMI",
	}); err != nil {
		t.Fatal(err)
	}
	owned, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 2, SourceTrackID: "t2",
		EntryArtist: "Boards of Canada", EntryTitle: "Roygbiv",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/roygbiv.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (library_file_id, track_id, method, confidence)
		VALUES ($1, $2, 'musicbrainz_recording_id', 1)
	`, fileID, trackID); err != nil {
		t.Fatal(err)
	}
	if err := queries.MarkPlaylistEntryOwned(ctx, owned.ID, uuid.NullUUID{UUID: fileID, Valid: true}); err != nil {
		t.Fatal(err)
	}

	read, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Playlist() error = %v", err)
	}
	if read.CoverAlbumID.UUID != albumID {
		t.Fatalf("cover = %v, want the second entry's release %s", read.CoverAlbumID, albumID)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("PlaylistEntries() error = %v", err)
	}
	if entries[0].ReleaseID.Valid || entries[1].ReleaseID.UUID != albumID {
		t.Fatalf("entries = %v, %v, want only the owned entry pictured by %s",
			entries[0].ReleaseID, entries[1].ReleaseID, albumID)
	}
}

func TestAnEntryWhoseWantResolvedIsPicturedByTheRecordingsRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	_, albumID, _, recordingID := seedFollowedArtist(t, ctx, pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-want-cover", "musik")
	if err != nil {
		t.Fatal(err)
	}
	_, targetID := entryWithWant(ctx, t, queries, playlist.ID, 1, "t1", "Roygbiv")
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_recording_id = $2, status = 'pending' WHERE id = $1
	`, targetID, recordingID); err != nil {
		t.Fatal(err)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("PlaylistEntries() error = %v", err)
	}
	if len(entries) != 1 || entries[0].ReleaseID.UUID != albumID {
		t.Fatalf("entries = %+v, want the recording's release %s", entries, albumID)
	}
}

func TestAPlaylistWithNoReleaseHasNoCover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-bare", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entryWithWant(ctx, t, queries, playlist.ID, 1, "t1", "HDMI")

	read, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Playlist() error = %v", err)
	}
	if read.CoverAlbumID.Valid {
		t.Fatalf("cover = %v, want none", read.CoverAlbumID)
	}
}
