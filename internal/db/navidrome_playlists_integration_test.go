package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// pushedList is a want list with a snapshot already recorded against it: the
// state every sync after the first one starts from.
func pushedList(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, sourceID, navidromeID string,
) (*Queries, uuid.UUID) {
	t.Helper()
	queries := New(pool)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, sourceID, "musik")
	if err != nil {
		t.Fatalf("seed the list: %v", err)
	}
	if err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{
			PlaylistID: playlist.ID, NavidromePlaylistID: navidromeID,
		}, nil,
	); err != nil {
		t.Fatalf("record the first push: %v", err)
	}
	return queries, playlist.ID
}

// seedTargetFile records what one offer for one want came to. Written directly
// rather than through the acquisition loop, because what this store reads is
// the verdict and the file, not how they were arrived at.
func seedTargetFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	targetID uuid.UUID, remotePath, verdict string, fileID uuid.NullUUID, decidedAt time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			verdict, library_file_id, decided_at
		)
		VALUES ($1, 'slskd', 'peer', $2, $3, $4, $5)
	`, targetID, remotePath, verdict, fileID, decidedAt); err != nil {
		t.Fatalf("seed the %s copy: %v", verdict, err)
	}
}

// refusedConstraint reports which constraint turned a write away.
func refusedConstraint(t *testing.T, err error) string {
	t.Helper()
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		t.Fatalf("error = %v, want the database to refuse the row", err)
	}
	return pgError.ConstraintName
}

// The snapshot is the only witness to what Navidrome was actually told, so what
// was written is what comes back: the remote list, and every track under the id
// and the path it was pushed at.
func TestASnapshotIsReadBackAsItWasPushed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "$NOT", EntryTitle: "Motorola",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/motorola.flac")

	if err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{
			PlaylistID: playlist.ID, NavidromePlaylistID: "nd-1", Adopted: true,
		},
		[]NavidromePlaylistSnapshotTrackParams{{
			Position: 1, NavidromeSongID: "song-1", LibraryPath: "/music/motorola.flac",
			LibraryFileID:   uuid.NullUUID{UUID: fileID, Valid: true},
			PlaylistEntryID: uuid.NullUUID{UUID: entry.ID, Valid: true},
		}},
	); err != nil {
		t.Fatalf("ReplaceNavidromePlaylistSnapshot() error = %v", err)
	}

	header, err := queries.NavidromePlaylistSnapshot(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("NavidromePlaylistSnapshot() error = %v", err)
	}
	if header.NavidromePlaylistID != "nd-1" || !header.Adopted || !header.PushedAt.Valid {
		t.Fatalf("header = %#v, want the remote list it was pushed to", header)
	}

	tracks, err := queries.NavidromePlaylistSnapshotTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("NavidromePlaylistSnapshotTracks() error = %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %#v, want the one that was pushed", tracks)
	}
	if tracks[0].NavidromeSongID != "song-1" || tracks[0].LibraryPath != "/music/motorola.flac" {
		t.Fatalf("track = %#v, want the id and the path it was pushed under", tracks[0])
	}
	if tracks[0].LibraryFileID.UUID != fileID || tracks[0].PlaylistEntryID.UUID != entry.ID {
		t.Fatalf("track = %#v, want the file and entry it was pushed for", tracks[0])
	}
}

// A push says what the list is now, not what changed. A track that is no longer
// pushed leaves the snapshot with it, or the next sync would look for an id
// nobody sent this time and read its absence as somebody's doing.
func TestReplacingASnapshotForgetsTracksNoLongerPushed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-replace", "nd-replace")

	if err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlistID, NavidromePlaylistID: "nd-replace"},
		[]NavidromePlaylistSnapshotTrackParams{
			{Position: 1, NavidromeSongID: "song-1", LibraryPath: "/music/one.flac"},
			{Position: 2, NavidromeSongID: "song-2", LibraryPath: "/music/two.flac"},
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlistID, NavidromePlaylistID: "nd-replace"},
		[]NavidromePlaylistSnapshotTrackParams{
			{Position: 1, NavidromeSongID: "song-2", LibraryPath: "/music/two.flac"},
		},
	); err != nil {
		t.Fatalf("ReplaceNavidromePlaylistSnapshot() error = %v", err)
	}

	tracks, err := queries.NavidromePlaylistSnapshotTracks(ctx, playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].NavidromeSongID != "song-2" {
		t.Fatalf("tracks = %#v, want only what the second push said", tracks)
	}
}

// One remote list belongs to one want list. Two lists pushing into the same
// Navidrome playlist would each read the other's tracks as the user's additions.
func TestTwoListsCannotClaimOneNavidromePlaylist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _ := pushedList(ctx, t, pool, "spot-first", "nd-shared")

	other, err := queries.UpsertSpotifyPlaylist(ctx, "spot-second", "another")
	if err != nil {
		t.Fatal(err)
	}
	err = queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: other.ID, NavidromePlaylistID: "nd-shared"}, nil)
	if refused := refusedConstraint(t, err); refused != "navidrome_playlist_snapshots_remote_idx" {
		t.Fatalf("refused by %q, want the remote list to be claimed once", refused)
	}
}

// A remote list already answered here belongs to a Schall list, which is what
// tells adoption apart from joining the same playlist twice.
func TestARemoteListAlreadyJoinedIsFoundByItsNavidromeID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-join", "nd-join")

	found, err := queries.SnapshotByNavidromeID(ctx, "nd-join")
	if err != nil {
		t.Fatalf("SnapshotByNavidromeID() error = %v", err)
	}
	if found.PlaylistID != playlistID {
		t.Fatalf("snapshot = %#v, want the list that pushed it (%s)", found, playlistID)
	}
}

// A remote list nobody has joined is answered with nothing, which is what
// leaves it to be adopted.
func TestARemoteListNobodyHasJoinedIsNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, err := New(pool).SnapshotByNavidromeID(ctx, "nd-unknown")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SnapshotByNavidromeID() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// A list nobody has pushed has no snapshot, and that is what says the first
// sync has everything still to do.
func TestAListNobodyHasPushedHasNoSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, err := New(pool).NavidromePlaylistSnapshot(ctx, uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("NavidromePlaylistSnapshot() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// Every joined list is readable in one read, because a sweep syncs all of them
// and has to know which they are before it opens any.
func TestEveryJoinedListIsListed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, first := pushedList(ctx, t, pool, "spot-a", "nd-a")
	_, second := pushedList(ctx, t, pool, "spot-b", "nd-b")

	snapshots, err := queries.NavidromePlaylistSnapshots(ctx)
	if err != nil {
		t.Fatalf("NavidromePlaylistSnapshots() error = %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %#v, want both joined lists", snapshots)
	}
	joined := map[uuid.UUID]bool{snapshots[0].PlaylistID: true, snapshots[1].PlaylistID: true}
	if !joined[first] || !joined[second] {
		t.Fatalf("snapshots = %#v, want %s and %s", snapshots, first, second)
	}
}

// Deleting a want list forgets what it was pushed as. Nothing is left claiming
// a remote object no list stands behind.
func TestDeletingAPlaylistTakesItsSnapshotWithIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-gone", "nd-gone")
	if err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlistID, NavidromePlaylistID: "nd-gone"},
		[]NavidromePlaylistSnapshotTrackParams{
			{Position: 1, NavidromeSongID: "song-1", LibraryPath: "/music/one.flac"},
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := queries.DeletePlaylist(ctx, playlistID); err != nil {
		t.Fatal(err)
	}

	var tracks int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM navidrome_playlist_snapshot_tracks`).Scan(&tracks); err != nil {
		t.Fatal(err)
	}
	if tracks != 0 {
		t.Fatalf("pushed tracks left behind = %d, want none", tracks)
	}
	if _, err := queries.SnapshotByNavidromeID(ctx, "nd-gone"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("snapshot error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// A file leaving the library must not take the record of what was pushed with
// it: the id and the path are the whole evidence 0010 reads, and losing the row
// would turn the entry it was pushed for into a removal the user never made.
func TestDeletingALibraryFileKeepsThePushedPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-file", "nd-file")
	fileID := seedLibraryFile(t, ctx, pool, "/music/one.flac")
	if err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlistID, NavidromePlaylistID: "nd-file"},
		[]NavidromePlaylistSnapshotTrackParams{{
			Position: 1, NavidromeSongID: "song-1", LibraryPath: "/music/one.flac",
			LibraryFileID: uuid.NullUUID{UUID: fileID, Valid: true},
		}},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM library_files WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}

	tracks, err := queries.NavidromePlaylistSnapshotTracks(ctx, playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].LibraryFileID.Valid {
		t.Fatalf("tracks = %#v, want the row kept and the file unclaimed", tracks)
	}
	if tracks[0].LibraryPath != "/music/one.flac" || tracks[0].NavidromeSongID != "song-1" {
		t.Fatalf("track = %#v, want what was pushed still readable", tracks[0])
	}
}

// A position is a place in the list Navidrome was given, and there is no
// zeroth place.
func TestAPushedTrackWithoutAPositionIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-pos", "nd-pos")

	err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlistID, NavidromePlaylistID: "nd-pos"},
		[]NavidromePlaylistSnapshotTrackParams{
			{Position: 0, NavidromeSongID: "song-1", LibraryPath: "/music/one.flac"},
		},
	)
	if refused := refusedConstraint(t, err); refused != "navidrome_playlist_snapshot_tracks_position_positive" {
		t.Fatalf("refused by %q, want the position refused", refused)
	}
}

// A pushed track with no path cannot be checked against getSong, which makes it
// a row that could only ever be read as a removal.
func TestAPushedTrackWithoutAPathIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-path", "nd-path")

	err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlistID, NavidromePlaylistID: "nd-path"},
		[]NavidromePlaylistSnapshotTrackParams{
			{Position: 1, NavidromeSongID: "song-1", LibraryPath: "  "},
		},
	)
	if refused := refusedConstraint(t, err); refused != "navidrome_playlist_snapshot_tracks_path_present" {
		t.Fatalf("refused by %q, want the empty path refused", refused)
	}
}

// A snapshot that does not say which remote list it is says nothing at all.
func TestASnapshotWithoutARemoteListIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-blank", "musik")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlist.ID, NavidromePlaylistID: " "}, nil)
	if refused := refusedConstraint(t, err); refused != "navidrome_playlist_snapshots_id_present" {
		t.Fatalf("refused by %q, want the empty remote id refused", refused)
	}
}

// A healed identity is the same file at the same place under a new id. Rewriting
// the path would turn the repair into a claim that a different file was pushed.
func TestARepairChangesTheSongIDAndNothingElse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-repair", "nd-repair")
	if err := queries.ReplaceNavidromePlaylistSnapshot(ctx,
		NavidromePlaylistSnapshotParams{PlaylistID: playlistID, NavidromePlaylistID: "nd-repair"},
		[]NavidromePlaylistSnapshotTrackParams{
			{Position: 1, NavidromeSongID: "song-broken", LibraryPath: "/music/one.flac"},
			{Position: 2, NavidromeSongID: "song-two", LibraryPath: "/music/two.flac"},
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := queries.RepairNavidromeSnapshotTrack(ctx, playlistID, 1, "song-healed"); err != nil {
		t.Fatalf("RepairNavidromeSnapshotTrack() error = %v", err)
	}

	tracks, err := queries.NavidromePlaylistSnapshotTracks(ctx, playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %#v, want both still pushed", tracks)
	}
	if tracks[0].NavidromeSongID != "song-healed" || tracks[0].LibraryPath != "/music/one.flac" {
		t.Fatalf("repaired track = %#v, want the new id at the old path", tracks[0])
	}
	if tracks[0].Position != 1 || tracks[1].NavidromeSongID != "song-two" {
		t.Fatalf("tracks = %#v, want nothing else moved", tracks)
	}
}

// Repairing a track nobody pushed is refused rather than written nowhere.
func TestRepairingATrackNobodyPushedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-norepair", "nd-norepair")

	err := queries.RepairNavidromeSnapshotTrack(ctx, playlistID, 7, "song-healed")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RepairNavidromeSnapshotTrack() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// A list that leaves Navidrome forgets what it was pushed as; the list and its
// wants stay, because leaving the player is not the statement "stop wanting
// this music".
func TestASnapshotIsForgottenWithoutTouchingTheList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-leave", "nd-leave")

	if err := queries.DeleteNavidromePlaylistSnapshot(ctx, playlistID); err != nil {
		t.Fatalf("DeleteNavidromePlaylistSnapshot() error = %v", err)
	}

	if _, err := queries.NavidromePlaylistSnapshot(ctx, playlistID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("snapshot error = %v, want %v", err, pgx.ErrNoRows)
	}
	if _, err := queries.Playlist(ctx, playlistID); err != nil {
		t.Fatalf("Playlist() error = %v, want the list untouched", err)
	}
}

// Forgetting a snapshot nobody recorded is refused rather than reported as done.
func TestForgettingASnapshotNobodyRecordedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	err := New(pool).DeleteNavidromePlaylistSnapshot(ctx, uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("DeleteNavidromePlaylistSnapshot() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// An entry the library already answered when the list was imported is part of
// the acquired subset: the file is held, whoever proved it.
func TestAcquiredTracksIncludeAnEntryOwnedAtImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-owned", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "$NOT", EntryTitle: "Motorola",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/motorola.flac")
	if err := queries.MarkPlaylistEntryOwned(
		ctx, entry.ID, uuid.NullUUID{UUID: fileID, Valid: true}); err != nil {
		t.Fatal(err)
	}

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 1 {
		t.Fatalf("acquired = %#v, want the owned entry", acquired)
	}
	if acquired[0].EntryID != entry.ID || acquired[0].LibraryFileID != fileID {
		t.Fatalf("acquired = %#v, want the entry and the file it is owned by", acquired[0])
	}
	if acquired[0].LibraryPath != "/music/motorola.flac" || acquired[0].Position != 1 {
		t.Fatalf("acquired = %#v, want the path and position to push it at", acquired[0])
	}
}

// The player indexes tags, so a push has to ask it in words. The file's own
// tags are the words it was filed under there, and they travel with the row a
// push is made from rather than being looked up again per track.
func TestAcquiredTracksCarryTheFilesOwnTags(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-tagged", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "as spotify wrote it", EntryTitle: "as spotify wrote it",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/036 - tagged.flac")
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET artist_tag = 'Raär', title_tag = 'Sometimes I Hear Sirens'
		WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if err := queries.MarkPlaylistEntryOwned(
		ctx, entry.ID, uuid.NullUUID{UUID: fileID, Valid: true}); err != nil {
		t.Fatal(err)
	}

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 1 {
		t.Fatalf("acquired = %#v, want the owned entry", acquired)
	}
	if acquired[0].Artist != "Raär" || acquired[0].Title != "Sometimes I Hear Sirens" {
		t.Fatalf("acquired = %#v, want the words the file itself carries", acquired[0])
	}
}

// A file with no tags at all still has to be asked about in words, and the
// entry's own are the only ones left. They are the list's words for the same
// recording, which is a question worth asking; nothing is decided by them.
func TestAcquiredTracksFallBackToTheEntrysWordsForAnUntaggedFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-untagged", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "BONES", EntryTitle: "HDMI",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/untagged.flac")
	if err := queries.MarkPlaylistEntryOwned(
		ctx, entry.ID, uuid.NullUUID{UUID: fileID, Valid: true}); err != nil {
		t.Fatal(err)
	}

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 1 {
		t.Fatalf("acquired = %#v, want the owned entry", acquired)
	}
	if acquired[0].Artist != "BONES" || acquired[0].Title != "HDMI" {
		t.Fatalf("acquired = %#v, want the entry's words where the file has none", acquired[0])
	}
}

// Ownership is only re-asked when the list is imported, so a want acquired
// since then has no ownership record. It is still music the library holds, and
// a push that read ownership alone would leave it out until the next import.
func TestAcquiredTracksIncludeAnEntryAcquiredAfterItsImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-acquired", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "BONES", EntryTitle: "HDMI",
	})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "BONES", EntryTitle: "HDMI", Summary: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/hdmi.flac")
	seedTargetFile(ctx, t, pool, targetID, `Music\BONES\hdmi.flac`, "accepted",
		uuid.NullUUID{UUID: fileID, Valid: true}, time.Now())

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 1 || acquired[0].LibraryFileID != fileID {
		t.Fatalf("acquired = %#v, want the copy the want obtained", acquired)
	}
	if acquired[0].LibraryPath != "/music/hdmi.flac" {
		t.Fatalf("acquired = %#v, want the path the copy landed at", acquired[0])
	}
}

// A want whose entry resolved onto another want is pursued on the survivor, so
// the copy is found by following the trail 0006 built rather than by stopping
// at the row the join points at.
func TestAcquiredTracksFollowASupersededWantToItsSurvivor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-superseded", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "A", EntryTitle: "Song",
	})
	if err != nil {
		t.Fatal(err)
	}
	loserID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "A", EntryTitle: "Song", Summary: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	recording := uuid.New()
	survivorID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "manual", EntryArtist: "A", EntryTitle: "Song", Summary: "wanted",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recording, Valid: true},
		ResolutionMethod:       pgtype.Text{String: "manual", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ResolveAcquisitionTarget(ctx, ResolveAcquisitionTargetParams{
		ID: loserID, MusicBrainzRecordingID: recording,
		ResolutionMethod: "isrc", Summary: "resolved",
		SupersededSummary: "superseded", SupersededNotWanted: "superseded not wanted",
		Detail: "detail", NextAttemptAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/song.flac")
	seedTargetFile(ctx, t, pool, survivorID, `Music\A\song.flac`, "accepted",
		uuid.NullUUID{UUID: fileID, Valid: true}, time.Now())

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 1 || acquired[0].LibraryFileID != fileID {
		t.Fatalf("acquired = %#v, want the copy the surviving want obtained", acquired)
	}
}

// A copy nobody could admit is not music the library holds. Held and discarded
// copies are answers about a file, never a file to hand a player.
func TestAcquiredTracksExcludeCopiesThatWereNotAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-refused", "musik")
	if err != nil {
		t.Fatal(err)
	}
	for index, verdict := range []string{"held", "discarded_audio"} {
		entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
			PlaylistID: playlist.ID, Position: int32(index + 1),
			SourceTrackID: verdict, EntryArtist: "A", EntryTitle: verdict,
		})
		if err != nil {
			t.Fatal(err)
		}
		targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
			Origin: "playlist", EntryArtist: "A", EntryTitle: verdict, Summary: "wanted",
		})
		if err != nil {
			t.Fatal(err)
		}
		seedTargetFile(ctx, t, pool, targetID, `Music\A\`+verdict+`.flac`, verdict,
			uuid.NullUUID{}, time.Now())
	}

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 0 {
		t.Fatalf("acquired = %#v, want nothing pushed for a copy nobody admitted", acquired)
	}
}

// An accepted copy names its library file only once the scan that follows the
// import has made one. Until then there is no path to push and nothing to say.
func TestAcquiredTracksExcludeAnAcceptedCopyWithNoLibraryFileYet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-unscanned", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "A", EntryTitle: "Song",
	})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "A", EntryTitle: "Song", Summary: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTargetFile(ctx, t, pool, targetID, `Music\A\song.flac`, "accepted",
		uuid.NullUUID{}, time.Now())

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 0 {
		t.Fatalf("acquired = %#v, want nothing until the file exists", acquired)
	}
}

// The acquired subset is pushed in the order the list is in, and an entry with
// no file is simply absent rather than a gap anybody has to account for.
func TestAcquiredTracksAreReturnedInPositionOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-order", "musik")
	if err != nil {
		t.Fatal(err)
	}
	positions := []int32{3, 1, 2}
	owned := map[int32]uuid.UUID{}
	for _, position := range positions {
		entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
			PlaylistID: playlist.ID, Position: position,
			SourceTrackID: "t" + string(rune('0'+position)),
			EntryArtist:   "A", EntryTitle: "Song",
		})
		if err != nil {
			t.Fatal(err)
		}
		// The middle one is still wanted, so it has no file and is left out.
		if position == 2 {
			continue
		}
		fileID := seedLibraryFile(t, ctx, pool,
			"/music/"+string(rune('0'+position))+".flac")
		if err := queries.MarkPlaylistEntryOwned(
			ctx, entry.ID, uuid.NullUUID{UUID: fileID, Valid: true}); err != nil {
			t.Fatal(err)
		}
		owned[position] = fileID
	}

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 2 {
		t.Fatalf("acquired = %#v, want only the two the library holds", acquired)
	}
	if acquired[0].Position != 1 || acquired[1].Position != 3 {
		t.Fatalf("acquired = %#v, want them in the order the list is in", acquired)
	}
	if acquired[0].LibraryFileID != owned[1] || acquired[1].LibraryFileID != owned[3] {
		t.Fatalf("acquired = %#v, want each entry's own file", acquired)
	}
}

// Two accepted copies are two copies of proven music, not an ambiguity: each was
// shown to be that recording before it was accepted. Which one the player is
// handed is a question about the copy, and the most recently decided one wins.
func TestAcquiredTracksPreferTheMostRecentlyDecidedCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-two-copies", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "A", EntryTitle: "Song",
	})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "A", EntryTitle: "Song", Summary: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	older := seedLibraryFile(t, ctx, pool, "/music/song-mp3.mp3")
	newer := seedLibraryFile(t, ctx, pool, "/music/song-flac.flac")
	seedTargetFile(ctx, t, pool, targetID, `Music\A\song.mp3`, "accepted",
		uuid.NullUUID{UUID: older, Valid: true}, time.Now().Add(-time.Hour))
	seedTargetFile(ctx, t, pool, targetID, `Music\A\song.flac`, "accepted",
		uuid.NullUUID{UUID: newer, Valid: true}, time.Now())

	acquired, err := queries.AcquiredPlaylistTracks(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("AcquiredPlaylistTracks() error = %v", err)
	}
	if len(acquired) != 1 || acquired[0].LibraryFileID != newer {
		t.Fatalf("acquired = %#v, want the copy decided most recently (%s)", acquired, newer)
	}
}

// A list created in Navidrome and adopted afterwards is a want list like any
// other, and the remote playlist it came from is what it is.
func TestAPlaylistBornInNavidromeIsAccepted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO playlists (source, source_id, name)
		VALUES ('navidrome', 'nd-adopted', 'made in the player')
	`); err != nil {
		t.Fatalf("adopting a Navidrome list = %v, want it accepted", err)
	}
}

// A remote list that does not say which remote object it is cannot be synced
// with anything, adopted or not.
func TestAPlaylistBornInNavidromeWithoutARemoteIDIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, err := pool.Exec(ctx, `
		INSERT INTO playlists (source, name) VALUES ('navidrome', 'nameless')
	`)
	if refused := refusedConstraint(t, err); refused != "playlists_source_id_matches_source" {
		t.Fatalf("refused by %q, want the missing remote id refused", refused)
	}
}

// One live pass per list: a second push while one is in flight would diff
// against a snapshot the first is in the middle of replacing.
func TestOnePlaylistCanOnlyHaveOneSyncQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, playlistID := pushedList(ctx, t, pool, "spot-sync-1", "nd-sync-1")

	for range 2 {
		if err := queries.QueueNavidromePlaylistSync(ctx, playlistID); err != nil {
			t.Fatalf("QueueNavidromePlaylistSync() error = %v", err)
		}
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sync_navidrome_playlists'
		  AND payload ->> 'playlistId' = $1::text
		  AND status IN ('queued', 'running')
	`, playlistID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want the pass that already exists", queued)
	}
}

// Two lists each get their own pass: the index is keyed on the list a pass
// diffs against, and nothing about one list's push concerns the other.
func TestTwoPlaylistsEachGetTheirOwnSync(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, first := pushedList(ctx, t, pool, "spot-sync-2", "nd-sync-2")
	second, err := queries.UpsertSpotifyPlaylist(ctx, "spot-sync-3", "anderes")
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []uuid.UUID{first, second.ID} {
		if err := queries.QueueNavidromePlaylistSync(ctx, id); err != nil {
			t.Fatalf("QueueNavidromePlaylistSync() error = %v", err)
		}
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'sync_navidrome_playlists'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("queued = %d, want one pass per list", queued)
	}
}

// A caller who asks about a list that is not there hears about it, rather than
// a job failing somewhere else later.
func TestSyncingAListThatIsNotThereIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	err := queries.QueueNavidromePlaylistSync(ctx, uuid.New())

	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("error = %v, want pgx.ErrNoRows", err)
	}
}

// One sweep at a time. The sweep queues its own replacement, so a second one
// does not run twice — it doubles the rate of every pass after it, and again
// with the next. The index 00038 holds cannot express this, because the column
// it keys on is null for every sweep and null never collides.
func TestASecondSweepMovesTheWaitingOneRatherThanJoiningIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(6 * time.Hour)
	if err := queries.QueueNavidromePlaylistSweep(ctx, later); err != nil {
		t.Fatal(err)
	}
	// Somebody presses the button while a sweep waits six hours out. A pass
	// that far away is not an answer to being asked now.
	soon := time.Now()
	if err := queries.QueueNavidromePlaylistSweep(ctx, soon); err != nil {
		t.Fatal(err)
	}

	var sweeps int
	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(run_after) FROM jobs
		WHERE kind = 'sync_navidrome_playlists' AND payload ->> 'playlistId' IS NULL
	`).Scan(&sweeps, &runAfter); err != nil {
		t.Fatal(err)
	}
	if sweeps != 1 {
		t.Fatalf("sweeps = %d, want one", sweeps)
	}
	if runAfter.After(soon.Add(time.Second)) {
		t.Fatalf("run_after = %s, want the waiting sweep moved to %s", runAfter, soon)
	}
}

// A pass over one list is keyed on that list, so it never collides with a
// sweep and a sweep never collides with it.
func TestASweepAndAPassOverOneListDoNotCollide(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.QueueNavidromePlaylistSweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := queries.QueueNavidromePlaylistSync(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}

	var jobs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'sync_navidrome_playlists'
	`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 {
		t.Fatalf("jobs = %d, want a sweep and a pass over one list", jobs)
	}
}

// The sweep names no playlist, so it is written as a job the worker reads as
// "look at the whole player".
func TestASweepIsQueuedWithNoPlaylist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if err := queries.QueueNavidromePlaylistSweep(ctx, time.Now()); err != nil {
		t.Fatalf("QueueNavidromePlaylistSweep() error = %v", err)
	}

	var payload string
	if err := pool.QueryRow(ctx, `
		SELECT payload::text FROM jobs WHERE kind = 'sync_navidrome_playlists'
	`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != "{}" {
		t.Fatalf("payload = %s, want a sweep naming no playlist", payload)
	}
}
