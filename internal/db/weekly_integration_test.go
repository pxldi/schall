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

// weeklyFixture is one song the weekly playlist obtained: a want that was
// filled, the file it delivered, and the playlist entry that asked for it.
type weeklyFixture struct {
	playlistID  uuid.UUID
	entryID     uuid.UUID
	targetID    uuid.UUID
	fileID      uuid.UUID
	path        string
	recordingID uuid.UUID
}

func seedWeeklySong(ctx context.Context, t *testing.T, queries *Queries, title string) weeklyFixture {
	t.Helper()

	playlist, err := queries.EnsureWeeklyPlaylist(ctx, "Schall Weekly")
	if err != nil {
		t.Fatal(err)
	}

	fixture := weeklyFixture{
		playlistID:  playlist.ID,
		fileID:      uuid.New(),
		recordingID: uuid.New(),
		path:        "/music/Weekly/" + title + ".flac",
	}

	if _, err := queries.db.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1024, now())
	`, fixture.fileID, fixture.path); err != nil {
		t.Fatal(err)
	}

	entry, err := queries.AppendWeeklyPlaylistEntry(ctx, AppendWeeklyPlaylistEntryParams{
		PlaylistID:             playlist.ID,
		MusicBrainzRecordingID: fixture.recordingID,
		EntryArtist:            "Portishead",
		EntryTitle:             title,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.entryID = entry.ID

	targetID, err := queries.CreatePlaylistEntryTarget(ctx, entry.ID, CreateAcquisitionTargetParams{
		Origin:                 "recommendation",
		EntryArtist:            "Portishead",
		EntryTitle:             title,
		MusicBrainzRecordingID: uuid.NullUUID{UUID: fixture.recordingID, Valid: true},
		ResolutionMethod:       pgtype.Text{String: "manual", Valid: true},
		Summary:                "wanted for the weekly playlist",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.targetID = targetID
	return fixture
}

// acquire marks the want as obtained, which is what makes the song an arrival
// the next refresh puts on trial.
func (fixture weeklyFixture) acquire(ctx context.Context, t *testing.T, queries *Queries) {
	t.Helper()
	if _, err := queries.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'acquired', acquired_at = now(), acquired_library_file_id = $2,
		    next_attempt_at = NULL
		WHERE id = $1
	`, fixture.targetID, fixture.fileID); err != nil {
		t.Fatal(err)
	}
}

func TestTheWeeklyPlaylistIsOneListForTheLifeOfTheInstallation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.WeeklyPlaylist(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a weekly playlist existed before any refresh: %v", err)
	}

	first, err := queries.EnsureWeeklyPlaylist(ctx, "Schall Weekly")
	if err != nil {
		t.Fatal(err)
	}
	second, err := queries.EnsureWeeklyPlaylist(ctx, "Schall Weekly")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("a second refresh made a second weekly playlist: %s and %s", first.ID, second.ID)
	}
	if first.SourceID.Valid {
		t.Fatal("the weekly playlist names a remote object it does not have")
	}
}

func TestOneWeeklyRefreshRunsAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	first, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{Mode: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{Mode: "remove"}); !errors.Is(err, ErrWeeklyRunInFlight) {
		t.Fatalf("a second refresh started beside the first: %v", err)
	}

	if _, err := queries.FinishWeeklyPlaylistRun(ctx, FinishWeeklyPlaylistRunParams{
		ID: first.ID, Status: "complete",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{Mode: "remove"}); err != nil {
		t.Fatalf("a refresh could not start once the first had finished: %v", err)
	}
}

func TestARefreshLeftOpenByARestartDoesNotBlockTheNextOneForever(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{Mode: "remove"}); err != nil {
		t.Fatal(err)
	}
	closed, err := queries.AbandonRunningWeeklyPlaylistRuns(ctx, "Schall stopped in the middle of this refresh")
	if err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("closed %d runs, want 1", closed)
	}
	if _, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{Mode: "remove"}); err != nil {
		t.Fatalf("a refresh could not start after the abandoned one was closed: %v", err)
	}
}

func TestASongArrivesOnceAndIsNotOfferedAgainOnceItIsOnTrial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	fixture := seedWeeklySong(ctx, t, queries, "Glory Box")

	arrivals, err := queries.WeeklyPlaylistArrivals(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arrivals) != 0 {
		t.Fatalf("a song nobody has obtained was offered as an arrival: %+v", arrivals)
	}

	fixture.acquire(ctx, t, queries)
	arrivals, err = queries.WeeklyPlaylistArrivals(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arrivals) != 1 || arrivals[0].LibraryFileID != fixture.fileID {
		t.Fatalf("the obtained song was not offered as an arrival: %+v", arrivals)
	}

	run, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{PlaylistID: uuid.NullUUID{UUID: fixture.playlistID, Valid: true}, Mode: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.GrantLibraryFileLease(ctx, GrantLibraryFileLeaseParams{
		LibraryFileID:          fixture.fileID,
		LibraryPath:            fixture.path,
		MusicBrainzRecordingID: fixture.recordingID,
		RunID:                  run.ID,
		AcquisitionTargetID:    fixture.targetID,
		ExpiresAt:              time.Now().Add(7 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	arrivals, err = queries.WeeklyPlaylistArrivals(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arrivals) != 0 {
		t.Fatalf("a song already on trial was offered as an arrival again: %+v", arrivals)
	}
}

func TestALeaseTravelsFromHeldToRemovedAndKeepsSayingWhatItRemoved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	fixture := seedWeeklySong(ctx, t, queries, "Glory Box")
	fixture.acquire(ctx, t, queries)

	run, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{PlaylistID: uuid.NullUUID{UUID: fixture.playlistID, Valid: true}, Mode: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := queries.GrantLibraryFileLease(ctx, GrantLibraryFileLeaseParams{
		LibraryFileID:          fixture.fileID,
		LibraryPath:            fixture.path,
		MusicBrainzRecordingID: fixture.recordingID,
		RunID:                  run.ID,
		AcquisitionTargetID:    fixture.targetID,
		ExpiresAt:              time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.State != "held" || lease.EntryTitle != "Glory Box" {
		t.Fatalf("lease = %#v", lease)
	}

	if err := queries.AnnounceLibraryFileLeaseDeparture(
		ctx, lease.ID, run.ID, time.Now().Add(7*24*time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	// The file goes first, as it does in the service: the row is deleted, and
	// the lease is left pointing at nothing.
	if _, err := pool.Exec(ctx, `DELETE FROM library_files WHERE id = $1`, fixture.fileID); err != nil {
		t.Fatal(err)
	}
	if err := queries.RemoveLibraryFileLease(ctx, lease.ID, run.ID); err != nil {
		t.Fatal(err)
	}

	removed, err := queries.LibraryFileLease(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removed.State != "removed" || removed.LibraryFileID.Valid {
		t.Fatalf("removed lease = %#v", removed)
	}
	if removed.LibraryPath != fixture.path {
		t.Fatalf("the lease forgot what it removed: %q", removed.LibraryPath)
	}

	live, err := queries.LiveLibraryFileLeases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("a settled lease is still on trial: %+v", live)
	}
}

func TestAnObtainedWantThatWasRemovedKeepsSayingItArrived(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	fixture := seedWeeklySong(ctx, t, queries, "Glory Box")

	// A want nobody obtained cannot be settled this way. The transition exists
	// for one caller, and "Stop looking" is not it.
	if err := queries.SettleWeeklyTargetNotWanted(ctx, fixture.targetID, "unkept"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a want that was never obtained was settled as unkept: %v", err)
	}

	fixture.acquire(ctx, t, queries)
	if err := queries.SettleWeeklyTargetNotWanted(ctx, fixture.targetID, "obtained and not kept"); err != nil {
		t.Fatal(err)
	}

	target, err := queries.AcquisitionTarget(ctx, fixture.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "not_wanted" {
		t.Fatalf("the want ended %q, want not_wanted", target.Status)
	}
	if !target.AcquiredAt.Valid {
		t.Fatal("the record stopped saying the song ever arrived")
	}
	if !target.NotWantedAt.Valid {
		t.Fatal("the want does not say when it was settled")
	}
}

func TestARecordingNobodyKeptTwiceCountsUpAndGetsItsWindowAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	recordingID := uuid.New()

	first := time.Now().Add(180 * 24 * time.Hour)
	if err := queries.RecordRecommendationUnkept(ctx, recordingID, uuid.NullUUID{}, first); err != nil {
		t.Fatal(err)
	}
	second := time.Now().Add(200 * 24 * time.Hour)
	if err := queries.RecordRecommendationUnkept(ctx, recordingID, uuid.NullUUID{}, second); err != nil {
		t.Fatal(err)
	}

	var occurrences int
	var suppressedUntil time.Time
	if err := pool.QueryRow(ctx, `
		SELECT occurrences, suppressed_until FROM recommendation_unkept
		WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&occurrences, &suppressedUntil); err != nil {
		t.Fatal(err)
	}
	if occurrences != 2 {
		t.Fatalf("occurrences = %d, want 2", occurrences)
	}
	if suppressedUntil.Before(second.Add(-time.Second)) {
		t.Fatalf("the window did not start again: %s", suppressedUntil)
	}
}

func TestAPressedKeepIsRememberedWithoutNamingARefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	fixture := seedWeeklySong(ctx, t, queries, "Glory Box")
	fixture.acquire(ctx, t, queries)

	run, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{PlaylistID: uuid.NullUUID{UUID: fixture.playlistID, Valid: true}, Mode: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := queries.GrantLibraryFileLease(ctx, GrantLibraryFileLeaseParams{
		LibraryFileID:          fixture.fileID,
		LibraryPath:            fixture.path,
		MusicBrainzRecordingID: fixture.recordingID,
		RunID:                  run.ID,
		AcquisitionTargetID:    fixture.targetID,
		ExpiresAt:              time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := queries.RecordLibraryFileKeepRead(ctx, RecordLibraryFileKeepReadParams{
		LeaseID: lease.ID, Signal: "schall_keep", Outcome: "kept",
	}); err != nil {
		t.Fatal(err)
	}
	pressed, err := queries.PressedKeeps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pressed[lease.ID]; !ok {
		t.Fatalf("the pressed Keep was not remembered: %+v", pressed)
	}

	kept, err := queries.KeepLibraryFileLease(ctx, lease.ID, "schall_keep", uuid.NullUUID{})
	if err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Fatal("the lease was not settled by the pressed Keep")
	}

	// Pressing again on a settled lease is not a failure. The person cannot see
	// which refresh happened between their two presses.
	again, err := queries.KeepLibraryFileLease(ctx, lease.ID, "schall_keep", uuid.NullUUID{})
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("a second press settled the lease a second time")
	}
}

func TestOneSignalGivesOneAnswerPerRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	fixture := seedWeeklySong(ctx, t, queries, "Glory Box")
	fixture.acquire(ctx, t, queries)

	run, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{PlaylistID: uuid.NullUUID{UUID: fixture.playlistID, Valid: true}, Mode: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := queries.GrantLibraryFileLease(ctx, GrantLibraryFileLeaseParams{
		LibraryFileID:          fixture.fileID,
		LibraryPath:            fixture.path,
		MusicBrainzRecordingID: fixture.recordingID,
		RunID:                  run.ID,
		AcquisitionTargetID:    fixture.targetID,
		ExpiresAt:              time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	runID := uuid.NullUUID{UUID: run.ID, Valid: true}
	if err := queries.RecordLibraryFileKeepRead(ctx, RecordLibraryFileKeepReadParams{
		LeaseID: lease.ID, RunID: runID, Signal: "navidrome_star",
		Outcome: "unreadable", Detail: "navidrome did not answer",
	}); err != nil {
		t.Fatal(err)
	}
	// The same refresh asking again replaces its own earlier answer rather than
	// leaving two answers from one signal.
	if err := queries.RecordLibraryFileKeepRead(ctx, RecordLibraryFileKeepReadParams{
		LeaseID: lease.ID, RunID: runID, Signal: "navidrome_star", Outcome: "absent",
	}); err != nil {
		t.Fatal(err)
	}

	reads, err := queries.LibraryFileKeepReads(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 || reads[0].Outcome != "absent" {
		t.Fatalf("reads = %+v", reads)
	}
}

func TestASettledSongLeavesTheWeeklyPlaylistAndItsWantStays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	fixture := seedWeeklySong(ctx, t, queries, "Glory Box")
	fixture.acquire(ctx, t, queries)

	run, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{PlaylistID: uuid.NullUUID{UUID: fixture.playlistID, Valid: true}, Mode: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := queries.GrantLibraryFileLease(ctx, GrantLibraryFileLeaseParams{
		LibraryFileID:          fixture.fileID,
		LibraryPath:            fixture.path,
		MusicBrainzRecordingID: fixture.recordingID,
		RunID:                  run.ID,
		AcquisitionTargetID:    fixture.targetID,
		ExpiresAt:              time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	dropped, err := queries.DropSettledWeeklyPlaylistEntries(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("a song still on trial was taken off the list: %d", dropped)
	}

	if _, err := queries.KeepLibraryFileLease(ctx, lease.ID, "navidrome_star",
		uuid.NullUUID{UUID: run.ID, Valid: true}); err != nil {
		t.Fatal(err)
	}
	dropped, err = queries.DropSettledWeeklyPlaylistEntries(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("the kept song stayed on this week's list: %d", dropped)
	}

	target, err := queries.AcquisitionTarget(ctx, fixture.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "acquired" {
		t.Fatalf("taking a song off the list changed its want to %q", target.Status)
	}
}

func TestOneWeeklyRefreshIsQueuedAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if err := queries.QueueWeeklyPlaylistRefresh(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := queries.QueueWeeklyPlaylistRefresh(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'refresh_weekly_playlist'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("%d refreshes are queued, want 1", queued)
	}
}

func TestTheWeeklySettingsShipSwitchedOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.WeeklyPlaylistSettings(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("an installation nobody configured had weekly settings: %v", err)
	}

	saved, err := queries.SaveWeeklyPlaylistSettings(ctx, SaveWeeklyPlaylistSettingsParams{
		Enabled: true, SongsPerWeek: 20, Mode: "remove",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Enabled || saved.SongsPerWeek != 20 || saved.Mode != "remove" {
		t.Fatalf("saved = %#v", saved)
	}

	off, err := queries.SaveWeeklyPlaylistSettings(ctx, SaveWeeklyPlaylistSettingsParams{
		Enabled: false, SongsPerWeek: 20, Mode: "report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled || off.Mode != "report" {
		t.Fatalf("off = %#v", off)
	}
}

// seedOwnedSong puts one identified file in the library with nothing else
// attached: no want, no lease, not on any playlist.
func seedOwnedSong(
	ctx context.Context, t *testing.T, queries *Queries, artist, title string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	fileID, recordingID := uuid.New(), uuid.New()
	if _, err := queries.db.Exec(ctx, `
		INSERT INTO library_files (
			id, path, size_bytes, modified_at,
			artist_tag, title_tag, album_tag, musicbrainz_recording_id
		)
		VALUES ($1, $2, 1024, now(), $3, $4, 'Album', $5)
	`, fileID, "/music/"+artist+"/"+title+".flac", artist, title, recordingID); err != nil {
		t.Fatal(err)
	}
	return fileID, recordingID
}

func TestTheLibraryPoolOffersASongTheCollectionHolds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	playlist, err := queries.EnsureWeeklyPlaylist(ctx, "Schall Weekly")
	if err != nil {
		t.Fatal(err)
	}
	_, recordingID := seedOwnedSong(ctx, t, queries, "Boards of Canada", "Roygbiv")

	candidates, err := queries.WeeklyLibraryPoolCandidates(
		ctx, playlist.ID, time.Now().Add(-180*24*time.Hour), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].MusicBrainzRecordingID != recordingID {
		t.Fatalf("the library pool offered %d songs, want the one in the collection", len(candidates))
	}
}

func TestTheLibraryPoolNeverOffersASongThatIsOnTrial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	fixture := seedWeeklySong(ctx, t, queries, "Glory Box")
	fixture.acquire(ctx, t, queries)
	if _, err := queries.db.Exec(ctx, `
		UPDATE library_files
		SET artist_tag = 'Portishead', title_tag = 'Glory Box',
		    musicbrainz_recording_id = $2
		WHERE id = $1
	`, fixture.fileID, fixture.recordingID); err != nil {
		t.Fatal(err)
	}
	run, err := queries.StartWeeklyPlaylistRun(ctx, StartWeeklyPlaylistRunParams{
		PlaylistID: uuid.NullUUID{UUID: fixture.playlistID, Valid: true}, Mode: "remove",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.GrantLibraryFileLease(ctx, GrantLibraryFileLeaseParams{
		LibraryFileID: fixture.fileID, LibraryPath: fixture.path,
		MusicBrainzRecordingID: fixture.recordingID, RunID: run.ID,
		AcquisitionTargetID: fixture.targetID,
		ExpiresAt:           time.Now().Add(7 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := queries.WeeklyLibraryPoolCandidates(
		ctx, fixture.playlistID, time.Now().Add(-180*24*time.Hour), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("the library pool offered %d songs that are already on trial, want 0", len(candidates))
	}
}

func TestASongTheLibraryPoolAlreadyOfferedWaitsOutItsWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	playlist, err := queries.EnsureWeeklyPlaylist(ctx, "Schall Weekly")
	if err != nil {
		t.Fatal(err)
	}
	fileID, recordingID := seedOwnedSong(ctx, t, queries, "Aphex Twin", "Xtal")
	if err := queries.RecordWeeklyLibraryPick(ctx, recordingID, fileID, uuid.NullUUID{}); err != nil {
		t.Fatal(err)
	}

	inside, err := queries.WeeklyLibraryPoolCandidates(
		ctx, playlist.ID, time.Now().Add(-180*24*time.Hour), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(inside) != 0 {
		t.Fatalf("a song offered this week was offered again: %d candidates", len(inside))
	}

	after, err := queries.WeeklyLibraryPoolCandidates(ctx, playlist.ID, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("a song past its window was offered %d times, want 1", len(after))
	}
}

func TestRotatingTheLibrarySongsLeavesTheObtainedOnesAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	fixture := seedWeeklySong(ctx, t, queries, "Sour Times")
	fileID, recordingID := seedOwnedSong(ctx, t, queries, "Autechre", "Eutow")
	entry, err := queries.AppendWeeklyPlaylistEntry(ctx, AppendWeeklyPlaylistEntryParams{
		PlaylistID:             fixture.playlistID,
		MusicBrainzRecordingID: recordingID,
		EntryArtist:            "Autechre",
		EntryTitle:             "Eutow",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.MarkPlaylistEntryOwned(
		ctx, entry.ID, uuid.NullUUID{UUID: fileID, Valid: true},
	); err != nil {
		t.Fatal(err)
	}

	dropped, err := queries.DropWeeklyLibraryEntries(ctx, fixture.playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("the rotation took %d entries off the list, want 1", dropped)
	}

	var files int
	if err := queries.db.QueryRow(ctx,
		`SELECT count(*) FROM library_files WHERE id = $1`, fileID,
	).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatal("rotating a library song off the list deleted the file")
	}

	var entries int
	if err := queries.db.QueryRow(ctx,
		`SELECT count(*) FROM playlist_entries WHERE id = $1`, fixture.entryID,
	).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatal("the rotation took an obtained song off the list")
	}
}

func TestAWantWithATransferInFlightIsNeverCalledStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	fixture := seedWeeklySong(ctx, t, queries, "Wandering Star")
	if _, err := queries.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'searching', next_attempt_at = NULL,
		    created_at = now() - interval '30 days'
		WHERE id = $1
	`, fixture.targetID); err != nil {
		t.Fatal(err)
	}

	stale, err := queries.WeeklyPlaylistStaleWants(ctx, fixture.playlistID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stale != 1 {
		t.Fatalf("a want searched for 30 days counted %d times as stale, want 1", stale)
	}

	if _, err := queries.db.Exec(ctx, `
		INSERT INTO download_requests (
			acquisition_target_id, status, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score
		)
		VALUES ($1, 'started', 'slskd', 'peer', '/share/album', 1, 1, 1024, 'flac', 0)
	`, fixture.targetID); err != nil {
		t.Fatal(err)
	}

	stale, err = queries.WeeklyPlaylistStaleWants(ctx, fixture.playlistID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("a want whose transfer is moving counted %d times as stale, want 0", stale)
	}
}

func TestAWantSomebodyWasAskedAboutKeepsItsSlot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	fixture := seedWeeklySong(ctx, t, queries, "Roads")
	if _, err := queries.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'awaiting_review', review_reason = 'candidate',
		    next_attempt_at = NULL, created_at = now() - interval '30 days'
		WHERE id = $1
	`, fixture.targetID); err != nil {
		t.Fatal(err)
	}

	stale, err := queries.WeeklyPlaylistStaleWants(ctx, fixture.playlistID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("a want a person was asked about counted %d times as stale, want 0", stale)
	}
}
