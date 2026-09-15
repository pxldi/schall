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

func TestSpotifySettingsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SpotifySettings(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unconfigured settings error = %v, want pgx.ErrNoRows", err)
	}

	stored, err := queries.SaveSpotifySettings(ctx, "client-1", "secret-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ClientSecret != "secret-1" || stored.RefreshToken.Valid {
		t.Fatalf("stored = %#v", stored)
	}

	authorized, err := queries.SaveSpotifyAuthorization(ctx, "refresh-1", "Poldi")
	if err != nil {
		t.Fatal(err)
	}
	if !authorized.RefreshToken.Valid || authorized.ConnectionStatus != "ok" {
		t.Fatalf("authorized = %#v", authorized)
	}

	// An omitted secret keeps the stored one, and an unchanged client pair
	// keeps the authorization it earned.
	kept, err := queries.SaveSpotifySettings(ctx, "client-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if kept.ClientSecret != "secret-1" || !kept.RefreshToken.Valid || kept.AccountName.String != "Poldi" {
		t.Fatalf("kept = %#v", kept)
	}

	// A refresh that rotated the token keeps the account it did not re-learn.
	rotated, err := queries.SaveSpotifyAuthorization(ctx, "refresh-2", "")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken.String != "refresh-2" || rotated.AccountName.String != "Poldi" {
		t.Fatalf("rotated = %#v", rotated)
	}

	// A changed client pair drops the authorization: the token was minted for
	// the old pair and can only fail later.
	changed, err := queries.SaveSpotifySettings(ctx, "client-2", "secret-2")
	if err != nil {
		t.Fatal(err)
	}
	if changed.RefreshToken.Valid || changed.AccountName.Valid || changed.ConnectionStatus != "unknown" {
		t.Fatalf("changed = %#v", changed)
	}
}

func TestPlaylistReconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	again, err := queries.UpsertSpotifyPlaylist(ctx, "spot-1", "musik (renamed)")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != playlist.ID || again.Name != "musik (renamed)" {
		t.Fatalf("re-adding the same source list made %#v, want the same row renamed", again)
	}

	first, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "$NOT", EntryTitle: "Motorola",
		EntryISRC: pgtype.Text{String: "QM42K1858315", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 2, SourceTrackID: "t2",
		EntryArtist: "BONES", EntryTitle: "HDMI",
	}); err != nil {
		t.Fatal(err)
	}

	// Re-importing the same track reconciles by source identity: same row,
	// refreshed evidence and position.
	moved, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 5, SourceTrackID: "t1",
		EntryArtist: "$NOT", EntryTitle: "Motorola - Remastered",
		EntryISRC: pgtype.Text{String: "QM42K1858315", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != first.ID || moved.Position != 5 || moved.EntryTitle != "Motorola - Remastered" {
		t.Fatalf("reconciled entry = %#v, want the same row moved and refreshed", moved)
	}

	// A target joined to an entry survives the entry disappearing from the
	// remote list; only the entry and its join row go.
	targetID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "BONES", EntryTitle: "HDMI", Summary: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	second := entries[1]
	if err := queries.LinkPlaylistEntryTarget(ctx, second.ID, targetID); err != nil {
		t.Fatal(err)
	}
	if err := queries.LinkPlaylistEntryTarget(ctx, second.ID, targetID); err != nil {
		t.Fatal(err)
	}

	pruned, err := queries.PrunePlaylistEntries(ctx, playlist.ID, []string{"t1"})
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	var targets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 1 {
		t.Fatal("pruning an entry must never touch the target")
	}

	// The list view counts what the entries know.
	listed, err := queries.Playlists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].EntryCount != 1 {
		t.Fatalf("listed = %#v", listed)
	}

	// Deleting the list forgets entries and joins, never wants.
	if err := queries.DeletePlaylist(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 1 {
		t.Fatal("deleting a playlist must never touch the targets")
	}
	if err := queries.DeletePlaylist(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleting a missing playlist = %v, want pgx.ErrNoRows", err)
	}
}

func TestPlaylistEntriesReportTheSurvivingTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-2", "list")
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

	loserID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
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
	if err := queries.LinkPlaylistEntryTarget(ctx, entry.ID, loserID); err != nil {
		t.Fatal(err)
	}
	// The loser resolves to the recording the survivor already holds and is
	// pointed at it, exactly as ResolveAcquisitionTarget supersedes.
	if _, err := queries.ResolveAcquisitionTarget(ctx, ResolveAcquisitionTargetParams{
		ID: loserID, MusicBrainzRecordingID: recording,
		ResolutionMethod: "isrc", Summary: "resolved",
		SupersededSummary: "superseded", SupersededNotWanted: "superseded not wanted",
		Detail: "detail", NextAttemptAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].TargetID.Valid {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].TargetID.UUID != survivorID {
		t.Fatalf("entry reports target %s, want the survivor %s", entries[0].TargetID.UUID, survivorID)
	}
}

// A single entry is read the same way the list reads each of its rows: the
// live target, following a supersession to its survivor.
func TestPlaylistEntryReadsOneEntryById(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertFilePlaylist(ctx, "list.csv", "list")
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

	before, err := queries.PlaylistEntry(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.PlaylistID != playlist.ID || before.TargetID.Valid || before.AnsweringFileID.Valid {
		t.Fatalf("entry = %#v, want an unanswered entry on %s", before, playlist.ID)
	}

	targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "A", EntryTitle: "Song", Summary: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}

	after, err := queries.PlaylistEntry(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.TargetID.Valid || after.TargetID.UUID != targetID {
		t.Fatalf("entry target = %#v, want %s", after.TargetID, targetID)
	}

	if _, err := queries.PlaylistEntry(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows", err)
	}
}

// One list is read on its own, with how much of it the library already answers,
// because that is the question the page opens with.
func TestOnePlaylistIsReadBackWithHowMuchOfItIsOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-read", "musik")
	if err != nil {
		t.Fatal(err)
	}
	owned, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "$NOT", EntryTitle: "Motorola",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 2, SourceTrackID: "t2",
		EntryArtist: "BONES", EntryTitle: "HDMI",
	}); err != nil {
		t.Fatal(err)
	}
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/motorola.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if err := queries.MarkPlaylistEntryOwned(
		ctx, owned.ID, uuid.NullUUID{UUID: fileID, Valid: true},
	); err != nil {
		t.Fatalf("MarkPlaylistEntryOwned() error = %v", err)
	}

	read, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Playlist() error = %v", err)
	}
	if read.EntryCount != 2 || read.OwnedCount != 1 {
		t.Fatalf("playlist = %d entries of which %d owned, want 2 and 1",
			read.EntryCount, read.OwnedCount)
	}
}

// entryWithWant is one entry and the want it asked for, joined: what an import
// leaves behind for a track the library did not already answer.
func entryWithWant(
	ctx context.Context, t *testing.T, queries *Queries,
	playlistID uuid.UUID, position int32, trackID, title string,
) (PlaylistEntryRow, uuid.UUID) {
	t.Helper()
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlistID, Position: position, SourceTrackID: trackID,
		EntryArtist: "BONES", EntryTitle: title,
	})
	if err != nil {
		t.Fatalf("seed the entry %s: %v", trackID, err)
	}
	targetID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "BONES", EntryTitle: title, Summary: "wanted",
	})
	if err != nil {
		t.Fatalf("seed the want for %s: %v", trackID, err)
	}
	if err := queries.LinkPlaylistEntryTarget(ctx, entry.ID, targetID); err != nil {
		t.Fatalf("join the entry %s to its want: %v", trackID, err)
	}
	return entry, targetID
}

// Ownership was written down only by an import, so music Schall went and
// fetched itself did not count until the list happened to be imported again.
// The copy it accepted is a row Schall wrote; reading it settles nothing and
// compares nothing.
func TestAWantWhoseCopyArrivedIsOwnedBeforeTheNextImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-acquired", "musik")
	if err != nil {
		t.Fatal(err)
	}
	_, targetID := entryWithWant(ctx, t, queries, playlist.ID, 1, "t1", "HDMI")
	fileID := seedLibraryFile(t, ctx, pool, "/music/hdmi.flac")
	seedTargetFile(ctx, t, pool, targetID, "peer/hdmi.flac", "accepted",
		uuid.NullUUID{UUID: fileID, Valid: true}, time.Now())

	read, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Playlist() error = %v", err)
	}
	if read.OwnedCount != 1 {
		t.Fatalf("playlist = %d of %d owned, want the acquired copy counted",
			read.OwnedCount, read.EntryCount)
	}
}

// The header and the rows are the same reading, so an entry says the file its
// want acquired answers it. The stored ownership record is left alone: no
// import has run, and the answer was not arrived at by writing one.
func TestAnEntryNamesTheFileItsWantAcquired(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-names", "musik")
	if err != nil {
		t.Fatal(err)
	}
	_, targetID := entryWithWant(ctx, t, queries, playlist.ID, 1, "t1", "HDMI")
	fileID := seedLibraryFile(t, ctx, pool, "/music/hdmi.flac")
	seedTargetFile(ctx, t, pool, targetID, "peer/hdmi.flac", "accepted",
		uuid.NullUUID{UUID: fileID, Valid: true}, time.Now())

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("PlaylistEntries() error = %v", err)
	}
	if len(entries) != 1 || entries[0].AnsweringFileID.UUID != fileID {
		t.Fatalf("entries = %#v, want the acquired file %s", entries, fileID)
	}
	if entries[0].OwnedFileID.Valid {
		t.Fatalf("entry = %#v, want the stored ownership record untouched", entries[0])
	}
}

// A copy is accepted before the scan makes a library file of it. Until that row
// exists there is nothing to own and nothing to send a player, so the entry is
// still open — the want says it is being taken in, and this says nothing.
func TestACopyThatHasNotReachedTheLibraryYetIsNotOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-importing", "musik")
	if err != nil {
		t.Fatal(err)
	}
	_, targetID := entryWithWant(ctx, t, queries, playlist.ID, 1, "t1", "HDMI")
	seedTargetFile(ctx, t, pool, targetID, "peer/hdmi.flac", "accepted",
		uuid.NullUUID{}, time.Now())

	read, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Playlist() error = %v", err)
	}
	if read.OwnedCount != 0 {
		t.Fatalf("playlist = %d owned, want a copy with no library file uncounted", read.OwnedCount)
	}
}

// A superseded want is the same want under another row, and the copy was
// acquired on the survivor. Reading only the joined target would leave the
// music out of the count on exactly the entries that were merged.
func TestAWantAcquiredOnTheSurvivingTargetIsOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-merged", "musik")
	if err != nil {
		t.Fatal(err)
	}
	_, loserID := entryWithWant(ctx, t, queries, playlist.ID, 1, "t1", "HDMI")
	recording := uuid.New()
	survivorID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "manual", EntryArtist: "BONES", EntryTitle: "HDMI", Summary: "wanted",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recording, Valid: true},
		ResolutionMethod:       pgtype.Text{String: "manual", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The joined want resolves to the recording the survivor already holds and
	// is pointed at it, exactly as ResolveAcquisitionTarget supersedes.
	if _, err := queries.ResolveAcquisitionTarget(ctx, ResolveAcquisitionTargetParams{
		ID: loserID, MusicBrainzRecordingID: recording,
		ResolutionMethod: "isrc", Summary: "resolved",
		SupersededSummary: "superseded", SupersededNotWanted: "superseded not wanted",
		Detail: "detail", NextAttemptAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/hdmi.flac")
	seedTargetFile(ctx, t, pool, survivorID, "peer/hdmi.flac", "accepted",
		uuid.NullUUID{UUID: fileID, Valid: true}, time.Now())

	read, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Playlist() error = %v", err)
	}
	if read.OwnedCount != 1 {
		t.Fatalf("playlist = %d owned, want the copy the survivor acquired counted",
			read.OwnedCount)
	}
}

// The count in a page's header and the state on each of its rows are one
// question asked twice, and a page that answers it two ways is a page nobody
// can read. Every way an entry can be answered, and every way it cannot, in one
// list.
func TestTheOwnedCountIsExactlyTheEntriesThatAreAnswered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-agree", "musik")
	if err != nil {
		t.Fatal(err)
	}
	// One the library already answered at import time.
	imported, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "$NOT", EntryTitle: "Motorola",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.MarkPlaylistEntryOwned(ctx, imported.ID, uuid.NullUUID{
		UUID: seedLibraryFile(t, ctx, pool, "/music/motorola.flac"), Valid: true,
	}); err != nil {
		t.Fatal(err)
	}
	// One Schall went and fetched since.
	_, acquiredTarget := entryWithWant(ctx, t, queries, playlist.ID, 2, "t2", "HDMI")
	seedTargetFile(ctx, t, pool, acquiredTarget, "peer/hdmi.flac", "accepted",
		uuid.NullUUID{UUID: seedLibraryFile(t, ctx, pool, "/music/hdmi.flac"), Valid: true},
		time.Now())
	// One still being fetched, and one nothing has happened to.
	_, openTarget := entryWithWant(ctx, t, queries, playlist.ID, 3, "t3", "Dirt")
	seedTargetFile(ctx, t, pool, openTarget, "peer/dirt.flac", "fetching",
		uuid.NullUUID{}, time.Now())
	entryWithWant(ctx, t, queries, playlist.ID, 4, "t4", "Sodium")

	read, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Playlist() error = %v", err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("PlaylistEntries() error = %v", err)
	}
	answered := int64(0)
	for _, entry := range entries {
		if entry.AnsweringFileID.Valid {
			answered++
		}
	}
	if read.OwnedCount != answered {
		t.Fatalf("header says %d owned, rows say %d", read.OwnedCount, answered)
	}
	if read.OwnedCount != 2 || read.EntryCount != 4 {
		t.Fatalf("playlist = %d of %d owned, want 2 of 4", read.OwnedCount, read.EntryCount)
	}
}

// A file that stopped answering an entry stops being recorded against it, or the
// list would claim music the library no longer holds.
func TestAnEntryForgetsAFileThatStoppedAnsweringIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-forget", "musik")
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
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/motorola.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if err := queries.MarkPlaylistEntryOwned(
		ctx, entry.ID, uuid.NullUUID{UUID: fileID, Valid: true},
	); err != nil {
		t.Fatal(err)
	}

	if err := queries.MarkPlaylistEntryOwned(ctx, entry.ID, uuid.NullUUID{}); err != nil {
		t.Fatalf("MarkPlaylistEntryOwned() error = %v", err)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].OwnedFileID.Valid {
		t.Fatalf("entries = %#v, want the file no longer claimed", entries)
	}
}

// Recording ownership against an entry nobody has is refused rather than written
// nowhere.
func TestMarkingAnEntryNobodyHasAsOwnedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	err := New(pool).MarkPlaylistEntryOwned(ctx, uuid.New(), uuid.NullUUID{})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("MarkPlaylistEntryOwned() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// The revision is written only once every entry has landed, so a crash mid-import
// leaves the old one in place and the next run does the work again rather than
// believing it already happened.
func TestAFinishedImportRecordsTheRevisionItRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-done", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if playlist.SourceRevision.Valid || playlist.ImportedAt.Valid {
		t.Fatalf("playlist = %#v, want nothing imported yet", playlist)
	}

	done, err := queries.CompletePlaylistImport(
		ctx, playlist.ID, "snapshot-7", "musik", "the good ones", "Poldi", 42)
	if err != nil {
		t.Fatalf("CompletePlaylistImport() error = %v", err)
	}
	if done.SourceRevision.String != "snapshot-7" || !done.ImportedAt.Valid {
		t.Fatalf("playlist = %#v, want the revision it read recorded", done)
	}
	if done.TrackCount != 42 || done.OwnerName != "Poldi" {
		t.Fatalf("playlist = %#v, want what the source said about it", done)
	}
}

// The want and the join to the entry that asked for it are one transaction: a
// crash between them would leave a target no entry can see, and the next import
// would ask for the same music a second time.
func TestAnEntrysWantIsRecordedTogetherWithItsJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-want", "musik")
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

	targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "$NOT", EntryTitle: "Motorola", Summary: "wanted",
	})
	if err != nil {
		t.Fatalf("CreatePlaylistEntryTarget() error = %v", err)
	}

	joined, err := queries.PlaylistEntryTargetID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("PlaylistEntryTargetID() error = %v", err)
	}
	if !joined.Valid || joined.UUID != targetID {
		t.Fatalf("joined want = %v, want the one just recorded (%s)", joined, targetID)
	}
}

// An entry that has never created a want reports none, which is what tells the
// next import there is work to do rather than a want to reuse.
func TestAnEntryThatHasNeverCreatedAWantReportsNone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-none", "musik")
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

	joined, err := queries.PlaylistEntryTargetID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("PlaylistEntryTargetID() error = %v", err)
	}
	if joined.Valid {
		t.Fatalf("joined want = %v, want none", joined)
	}
}

// One live import per list. Asking again while one is queued is answered by the
// import that already exists rather than by a second walk of the same playlist.
func TestOnePlaylistImportIsQueuedHoweverOftenItIsAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-queue", "musik")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := queries.QueuePlaylistImport(ctx, playlist.ID); err != nil {
			t.Fatalf("QueuePlaylistImport() error = %v", err)
		}
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'import_playlist' AND status IN ('queued', 'running')
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued imports = %d, want exactly one", queued)
	}
}

// ISRC agreement is an identifier agreement, the same proof the matching rules
// accept. A file the library already holds under this ISRC answers the entry
// without anybody going looking for a second copy.
func TestAnISRCTheLibraryAlreadyHoldsAnswersTheEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/motorola.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, isrc, method, summary
		)
		VALUES ($1, 'external', $2, 'QM42K1858315', 'isrc', 'proven by identifier')
	`, fileID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// The stored identifier is compared without regard to case, because the two
	// sides are different people's transcriptions of the same identifier.
	owned, err := New(pool).OwnedFileForISRC(ctx, "qm42k1858315")
	if err != nil {
		t.Fatalf("OwnedFileForISRC() error = %v", err)
	}
	if !owned.Valid || owned.UUID != fileID {
		t.Fatalf("owned file = %v, want %s", owned, fileID)
	}
}

// An ISRC no file carries answers nothing, which is what leaves the entry to be
// wanted rather than quietly marked as owned.
func TestAnISRCNoFileCarriesAnswersNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	owned, err := New(pool).OwnedFileForISRC(ctx, "QM42K1858315")
	if err != nil {
		t.Fatalf("OwnedFileForISRC() error = %v", err)
	}
	if owned.Valid {
		t.Fatalf("owned file = %v, want none", owned)
	}
}

// A failure talking to Spotify is recorded on the settings, so the page can say
// when the authorization last worked and why it stopped.
func TestAFailedSpotifyCallIsRecordedOnTheSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveSpotifySettings(ctx, "client-1", "secret-1"); err != nil {
		t.Fatal(err)
	}

	recorded, err := queries.RecordSpotifyConnection(ctx, "failed", "401 unauthorized")
	if err != nil {
		t.Fatalf("RecordSpotifyConnection() error = %v", err)
	}
	if recorded.ConnectionStatus != "failed" || recorded.ConnectionError.String != "401 unauthorized" {
		t.Fatalf("settings = %#v, want the failure readable", recorded)
	}
	if !recorded.LastCheckedAt.Valid {
		t.Fatal("no moment recorded, want the check stamped")
	}
}

// A song the user added on the player's side becomes an entry answered by the
// file itself: no want to search for, and a title only so a person can read it.
func TestAnAppendedEntryLandsAtTheEndAnsweredByItsFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-append", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1", EntryTitle: "First",
	}); err != nil {
		t.Fatal(err)
	}
	fileID := seedLibraryFile(t, ctx, pool, "/music/appended.flac")

	entry, err := queries.AppendPlaylistEntry(ctx, AppendPlaylistEntryParams{
		PlaylistID: playlist.ID, EntryTitle: "Second", LibraryFileID: fileID,
	})
	if err != nil {
		t.Fatalf("AppendPlaylistEntry() error = %v", err)
	}
	if entry.Position != 2 {
		t.Fatalf("position = %d, want the end of the list", entry.Position)
	}
	if !entry.OwnedFileID.Valid || entry.OwnedFileID.UUID != fileID {
		t.Fatalf("entry = %#v, want the file that answers it", entry)
	}
	if entry.SourceTrackID.Valid {
		t.Fatalf("source track = %#v, want none invented", entry.SourceTrackID)
	}
}

// A removal the user made in the player takes the entry off the list. The want
// it created is left standing, exactly as a pruned import leaves one.
func TestADeletedEntryLeavesItsWantStanding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "spot-delete", "musik")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1", EntryTitle: "Gone",
	})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "$NOT", EntryTitle: "Gone", Summary: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := queries.DeletePlaylistEntry(ctx, entry.ID); err != nil {
		t.Fatalf("DeletePlaylistEntry() error = %v", err)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want the removed one gone", entries)
	}
	var wants int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM acquisition_targets WHERE id = $1`, targetID).Scan(&wants); err != nil {
		t.Fatal(err)
	}
	if wants != 1 {
		t.Fatalf("wants = %d, want the target left standing", wants)
	}
}

func TestDeletingAnEntryThatIsNotThereIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	err := New(pool).DeletePlaylistEntry(ctx, uuid.New())

	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("error = %v, want pgx.ErrNoRows", err)
	}
}

// A playlist created in Navidrome is a want list with a source like any other,
// and adopting it twice is one list.
func TestAdoptingANavidromePlaylistTwiceIsOneList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	first, err := queries.UpsertNavidromePlaylist(ctx, "nd-adopt", "abends")
	if err != nil {
		t.Fatalf("UpsertNavidromePlaylist() error = %v", err)
	}
	second, err := queries.UpsertNavidromePlaylist(ctx, "nd-adopt", "abends, umbenannt")
	if err != nil {
		t.Fatalf("UpsertNavidromePlaylist() error = %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("ids = %s / %s, want one list", first.ID, second.ID)
	}
	if second.Source != "navidrome" || !second.SourceID.Valid || second.SourceID.String != "nd-adopt" {
		t.Fatalf("list = %#v, want the remote list named as its source", second)
	}
	if second.Name != "abends, umbenannt" {
		t.Fatalf("name = %q, want the player's copy", second.Name)
	}
}
