package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
)

// newReleaseFixture is one release a followed artist published, and the one
// track on it the library ended up holding.
type newReleaseFixture struct {
	artistID uuid.UUID
	albumID  uuid.UUID
	trackID  uuid.UUID
	targetID uuid.UUID
	fileID   uuid.UUID
	path     string
}

// seedFollowedRelease writes what the follow feed leaves behind for one
// release: a followed artist, the release with its date, a track, and a want
// the feed made against that release. Whether the library owns the track is the
// caller's business — that is the thing under test.
func seedFollowedRelease(
	ctx context.Context, t *testing.T, queries *Queries, title string, releasedOn time.Time,
) newReleaseFixture {
	t.Helper()

	fixture := newReleaseFixture{
		artistID: uuid.New(),
		albumID:  uuid.New(),
		trackID:  uuid.New(),
		fileID:   uuid.New(),
		path:     "/music/Followed/" + title + ".flac",
	}

	if _, err := queries.db.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name, followed_at) VALUES ($1, $2, $2, now())
	`, fixture.artistID, "Followed "+title); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.db.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, release_date) VALUES ($1, $2, $3, $4)
	`, fixture.albumID, fixture.artistID, title, releasedOn); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.db.Exec(ctx, `
		INSERT INTO tracks (id, album_id, title, track_number) VALUES ($1, $2, $3, 1)
	`, fixture.trackID, fixture.albumID, title); err != nil {
		t.Fatal(err)
	}

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin:                 "follow_feed",
		OriginAlbumID:          uuid.NullUUID{UUID: fixture.albumID, Valid: true},
		EntryArtist:            "Followed " + title,
		EntryTitle:             title,
		MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.targetID = targetID
	return fixture
}

// ownIt puts the file in the library and records it as the copy the want was
// filled with, which is how the acquisition loop leaves an acquired want.
func (fixture newReleaseFixture) ownIt(ctx context.Context, t *testing.T, queries *Queries) {
	t.Helper()
	if _, err := queries.db.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, title_tag)
		VALUES ($1, $2, 1024, now(), $3)
	`, fixture.fileID, fixture.path, "Track on "+fixture.path); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'acquired', acquired_at = now(), acquired_library_file_id = $2,
		    next_attempt_at = NULL
		WHERE id = $1
	`, fixture.targetID, fixture.fileID); err != nil {
		t.Fatal(err)
	}
}

func TestTheNewReleasesPlaylistIsOneListForTheLifeOfTheInstallation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.NewReleasesPlaylist(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a new-releases playlist existed before any refresh: %v", err)
	}
	first, err := queries.EnsureNewReleasesPlaylist(ctx, "New releases")
	if err != nil {
		t.Fatal(err)
	}
	second, err := queries.EnsureNewReleasesPlaylist(ctx, "New releases")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("a second refresh made a second list: %s and %s", first.ID, second.ID)
	}
	if first.SourceID.Valid {
		t.Fatal("the new-releases playlist names a remote object it does not have")
	}
}

func TestOnlyOwnedTracksFromRecentlyFoundReleasesAreCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	today := time.Now().UTC()

	owned := seedFollowedRelease(ctx, t, queries, "Owned", today.AddDate(0, 0, -3))
	owned.ownIt(ctx, t, queries)

	// Wanted and not yet found. The feed made the want; nothing has arrived.
	seedFollowedRelease(ctx, t, queries, "StillWanted", today.AddDate(0, 0, -3))

	// Owned, but published long before the window opens.
	old := seedFollowedRelease(ctx, t, queries, "Old", today.AddDate(0, 0, -400))
	old.ownIt(ctx, t, queries)

	// Owned, from a release nobody followed anybody for: a want a person made
	// by hand, against the same kind of release.
	unfollowed := seedFollowedRelease(ctx, t, queries, "ByHand", today.AddDate(0, 0, -3))
	unfollowed.ownIt(ctx, t, queries)
	if _, err := queries.db.Exec(ctx, `
		UPDATE acquisition_targets SET origin = 'manual' WHERE id = $1
	`, unfollowed.targetID); err != nil {
		t.Fatal(err)
	}

	candidates, err := queries.NewReleaseCandidates(ctx, today.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].LibraryFileID != owned.fileID {
		t.Fatalf("the wrong file was a candidate: %s", candidates[0].LibraryFileID)
	}
}

func TestAReleaseALabelFeedFoundIsACandidateToo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	today := time.Now().UTC()

	// Nobody follows this artist; the label the release is on is what was
	// followed, so the want the feed made carries origin = 'label_feed'.
	fromLabel := seedFollowedRelease(ctx, t, queries, "FromLabel", today.AddDate(0, 0, -3))
	if _, err := queries.db.Exec(ctx, `
		UPDATE acquisition_targets SET origin = 'label_feed' WHERE id = $1
	`, fromLabel.targetID); err != nil {
		t.Fatal(err)
	}
	fromLabel.ownIt(ctx, t, queries)

	candidates, err := queries.NewReleaseCandidates(ctx, today.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].LibraryFileID != fromLabel.fileID {
		t.Fatalf("a release a followed label published was not a candidate: %+v", candidates)
	}
}

func TestATrackTheLibraryAlreadyHadCountsAsOwnedOnAFoundRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	today := time.Now().UTC()

	// The feed wanted one track on this release; a second track on the same
	// release was already in the library and mapped to it, so the feed never
	// made a want for it and its want carries no file.
	release := seedFollowedRelease(ctx, t, queries, "HalfOwned", today.AddDate(0, 0, -5))
	alreadyHad := uuid.New()
	if _, err := queries.db.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, title_tag)
		VALUES ($1, '/music/Followed/HalfOwned-2.flac', 1024, now(), 'Second track')
	`, alreadyHad); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.db.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method)
		VALUES ($1, $2, 'manual')
	`, release.trackID, alreadyHad); err != nil {
		t.Fatal(err)
	}

	candidates, err := queries.NewReleaseCandidates(ctx, today.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].LibraryFileID != alreadyHad {
		t.Fatalf("the track the library already had was not a candidate: %+v", candidates)
	}
}

func TestAFileTheLibraryHasLostIsNotOnTheList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	today := time.Now().UTC()

	gone := seedFollowedRelease(ctx, t, queries, "Gone", today.AddDate(0, 0, -2))
	gone.ownIt(ctx, t, queries)
	if _, err := queries.db.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, gone.fileID); err != nil {
		t.Fatal(err)
	}

	candidates, err := queries.NewReleaseCandidates(ctx, today.AddDate(0, 0, -60))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("a file the library has lost was a candidate: %+v", candidates)
	}
}

func TestTheNewReleasesPlaylistReadsNewestReleaseFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	today := time.Now().UTC()

	playlist, err := queries.EnsureNewReleasesPlaylist(ctx, "New releases")
	if err != nil {
		t.Fatal(err)
	}

	older := seedFollowedRelease(ctx, t, queries, "Older", today.AddDate(0, 0, -30))
	older.ownIt(ctx, t, queries)
	newer := seedFollowedRelease(ctx, t, queries, "Newer", today.AddDate(0, 0, -1))
	newer.ownIt(ctx, t, queries)

	// Added oldest first, so that the order on the list can only come from the
	// release dates and never from the order they were written in.
	for _, fixture := range []newReleaseFixture{older, newer} {
		if err := queries.AddNewReleasePlaylistMember(ctx, AddNewReleasePlaylistMemberParams{
			PlaylistID:    playlist.ID,
			LibraryFileID: fixture.fileID,
			AlbumID:       uuid.NullUUID{UUID: fixture.albumID, Valid: true},
			ReleasedOn:    releasedOn(ctx, t, queries, fixture.albumID),
			Title:         fixture.path,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := queries.OrderNewReleasePlaylistEntries(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %d", len(entries))
	}
	if !entries[0].OwnedFileID.Valid || entries[0].OwnedFileID.UUID != newer.fileID {
		t.Fatal("the newest release is not first on the list")
	}
}

func TestATrackTakenOutInThePlayerStaysOutAndOneRotatedOutDoesNot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	today := time.Now().UTC()

	playlist, err := queries.EnsureNewReleasesPlaylist(ctx, "New releases")
	if err != nil {
		t.Fatal(err)
	}

	removed := seedFollowedRelease(ctx, t, queries, "Removed", today.AddDate(0, 0, -4))
	removed.ownIt(ctx, t, queries)
	rotated := seedFollowedRelease(ctx, t, queries, "Rotated", today.AddDate(0, 0, -4))
	rotated.ownIt(ctx, t, queries)

	for _, fixture := range []newReleaseFixture{removed, rotated} {
		if err := queries.AddNewReleasePlaylistMember(ctx, AddNewReleasePlaylistMemberParams{
			PlaylistID:    playlist.ID,
			LibraryFileID: fixture.fileID,
			AlbumID:       uuid.NullUUID{UUID: fixture.albumID, Valid: true},
			ReleasedOn:    releasedOn(ctx, t, queries, fixture.albumID),
			Title:         fixture.path,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The sync confirms a removal by deleting the entry (ADR 0013). That is the
	// only thing it does, and it is what a person taking the track out of the
	// playlist in their player looks like from here.
	members, err := queries.NewReleasePlaylistMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.LibraryFileID == removed.fileID {
			if err := queries.DeletePlaylistEntry(ctx, member.EntryID.UUID); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The other one leaves because its release aged out, which the refresh does
	// through the member row and never by deleting an entry on its own.
	if err := queries.DropNewReleasePlaylistMember(ctx, rotated.fileID); err != nil {
		t.Fatal(err)
	}

	withdrawn, err := queries.WithdrawRemovedNewReleaseMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn != 1 {
		t.Fatalf("expected one withdrawal, got %d", withdrawn)
	}

	after, err := queries.NewReleasePlaylistMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("expected the withdrawn member alone to survive, got %d rows", len(after))
	}
	if after[0].LibraryFileID != removed.fileID || !after[0].WithdrawnAt.Valid {
		t.Fatalf("the wrong member was recorded as taken out: %+v", after[0])
	}
}

func TestOneNewReleasesRefreshIsQueuedAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if err := queries.QueueNewReleasesPlaylistRefresh(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := queries.QueueNewReleasesPlaylistRefresh(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := queries.db.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'refresh_new_releases_playlist'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("expected one queued refresh, got %d", queued)
	}
}

func TestTheNewReleasesWindowIsSavedAndBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.NewReleasesPlaylistSettings(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("settings existed before anybody saved any: %v", err)
	}
	saved, err := queries.SaveNewReleasesPlaylistSettings(ctx, SaveNewReleasesPlaylistSettingsParams{
		Enabled: true, WindowDays: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.WindowDays != 90 {
		t.Fatalf("the window was saved as %d days", saved.WindowDays)
	}
	if _, err := queries.SaveNewReleasesPlaylistSettings(ctx, SaveNewReleasesPlaylistSettingsParams{
		Enabled: true, WindowDays: 0,
	}); err == nil {
		t.Fatal("a window of no days was accepted")
	}
}

// releasedOn reads back the day a release was published, in the type the member
// row carries it as.
func releasedOn(ctx context.Context, t *testing.T, queries *Queries, albumID uuid.UUID) pgtype.Date {
	t.Helper()
	var day pgtype.Date
	if err := queries.db.QueryRow(ctx, `
		SELECT release_date FROM albums WHERE id = $1
	`, albumID).Scan(&day); err != nil {
		t.Fatal(err)
	}
	return day
}

// A file the person deletes takes its member row with it — the row references
// library_files ON DELETE CASCADE — but its playlist entry stays, because
// playlist_entries references the file ON DELETE SET NULL (00026). The refresh
// only ever deletes an entry it can reach through a member row, so the entry it
// can no longer reach is left on the list for good, indistinguishable from a
// track somebody added in their player.
func TestDeletingAFileLeavesItsEntryOnTheNewReleasesPlaylist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	today := time.Now().UTC()

	playlist, err := queries.EnsureNewReleasesPlaylist(ctx, "New releases")
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedFollowedRelease(ctx, t, queries, "Deleted", today.AddDate(0, 0, -4))
	fixture.ownIt(ctx, t, queries)
	if err := queries.AddNewReleasePlaylistMember(ctx, AddNewReleasePlaylistMemberParams{
		PlaylistID:    playlist.ID,
		LibraryFileID: fixture.fileID,
		AlbumID:       uuid.NullUUID{UUID: fixture.albumID, Valid: true},
		ReleasedOn:    releasedOn(ctx, t, queries, fixture.albumID),
		Title:         fixture.path,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.db.Exec(ctx, `DELETE FROM library_files WHERE id = $1`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	members, err := queries.NewReleasePlaylistMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("the member row should have gone with the file, got %d", len(members))
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the entry outlived the file and nothing can reach it now: %d left", len(entries))
	}
}
