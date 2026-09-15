package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/dbtest"
)

// The queries behind the four things Schall now writes and reads beside the
// music it already holds: where a release's folder is, where an artist's is,
// what the catalogue says a file is so lyrics can be asked for, and how many
// questions are waiting for a person.
//
// These run against PostgreSQL because that is the only place the SQL is real.
// Each one is a statement no unit test can make: a regexp that cuts a folder off
// a path, a DISTINCT that collapses a release spread over two discs, and a
// count that has to agree with the screen it describes.

func TestAReleaseFolderIsReadFromWhereItsFilesAre(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	_, albumID := seedArtistWithCatalogue(t, ctx, pool, "Objekt", 2, 2)
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		VALUES ($1, '\x00'::bytea, 'image/jpeg', 'coverartarchive')
	`, albumID); err != nil {
		t.Fatalf("seed a cached cover: %v", err)
	}

	rows, err := queries.ReleaseFoldersToPicture(ctx)
	if err != nil {
		t.Fatalf("ReleaseFoldersToPicture() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("read %d folders; want one for a release filed in one place", len(rows))
	}
	if rows[0].Folder != "/music/Objekt" {
		t.Fatalf("folder = %q; want the path with the file name cut off", rows[0].Folder)
	}
	if rows[0].ID != albumID {
		t.Fatalf("album = %s, want %s", rows[0].ID, albumID)
	}
	// The archive that answered comes with the row: only the Cover Art Archive
	// holds a bigger copy of what it gave, so the source decides whether the
	// picture written beside the music is fetched or taken from the cache.
	if rows[0].Source != "coverartarchive" {
		t.Fatalf("source = %q, want the archive the cached picture came from", rows[0].Source)
	}
}

// A row that names an archive and carries no bytes is not a picture. It was
// written by a fetch that found nothing and recorded the archive's name anyway,
// and reading it as a picture sends the disc pass to the archive for a cover
// that was never there, every pass, forever.
func TestAReleaseWhoseCachedPictureHasNoBytesIsNotListed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	_, albumID := seedArtistWithCatalogue(t, ctx, pool, "Objekt", 1, 1)
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		VALUES ($1, ''::bytea, 'image/jpeg', 'coverartarchive')
	`, albumID); err != nil {
		t.Fatalf("seed a row with no bytes: %v", err)
	}

	rows, err := queries.ReleaseFoldersToPicture(ctx)
	if err != nil {
		t.Fatalf("ReleaseFoldersToPicture() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("read %d folders for a release whose picture has no bytes", len(rows))
	}
}

// A release the archive was asked about and had no picture for is not asked
// about again. The absence is a row with no image, and this reads rows that have
// one.
func TestAReleaseTheArchiveHadNoPictureForIsNotListed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	_, albumID := seedArtistWithCatalogue(t, ctx, pool, "Objekt", 1, 1)
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		VALUES ($1, NULL, '', 'nobody')
	`, albumID); err != nil {
		t.Fatalf("seed a recorded absence: %v", err)
	}

	rows, err := queries.ReleaseFoldersToPicture(ctx)
	if err != nil {
		t.Fatalf("ReleaseFoldersToPicture() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("read %d folders for a release with no picture", len(rows))
	}
}

func TestAnArtistsFoldersAreReadFromWhereTheirMusicIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	artistID, _ := seedArtistWithCatalogue(t, ctx, pool, "Objekt", 2, 2)
	if _, err := pool.Exec(ctx, `
		INSERT INTO artist_images (artist_id, image, content_type, source)
		VALUES ($1, '\x00'::bytea, 'image/jpeg', 'deezer')
	`, artistID); err != nil {
		t.Fatalf("seed a cached artist picture: %v", err)
	}

	rows, err := queries.ArtistFoldersToPicture(ctx)
	if err != nil {
		t.Fatalf("ArtistFoldersToPicture() error = %v", err)
	}
	if len(rows) != 1 || rows[0].ID != artistID || rows[0].Folder != "/music/Objekt" {
		t.Fatalf("rows = %+v; want one folder for the artist", rows)
	}
}

// The music, described by the catalogue rather than by its own tags, paged by
// the cursor a sweep carries from one pass to the next.
func TestTheMusicToWordIsPagedByTheFilesOwnID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	seedArtistWithCatalogue(t, ctx, pool, "Objekt", 3, 3)
	if _, err := pool.Exec(ctx, `UPDATE tracks SET duration_ms = 240000`); err != nil {
		t.Fatalf("give the tracks a length: %v", err)
	}

	first, err := queries.TracksToLyric(ctx, TracksToLyricParams{Batch: 2})
	if err != nil {
		t.Fatalf("TracksToLyric() error = %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("read %d files; want a full page of two", len(first))
	}
	if first[0].ArtistName != "Objekt" || first[0].AlbumTitle != "Objekt LP" {
		t.Fatalf("first row = %+v; want the catalogue's own account", first[0])
	}

	next, err := queries.TracksToLyric(ctx, TracksToLyricParams{
		After: first[len(first)-1].LibraryFileID, Batch: 2,
	})
	if err != nil {
		t.Fatalf("TracksToLyric() error = %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("read %d files after the cursor; want the one that was left", len(next))
	}
	if next[0].LibraryFileID == first[0].LibraryFileID {
		t.Fatal("the second page repeated the first")
	}
}

// A track with no length is skipped rather than asked about without one: LRCLIB
// falls back to matching names alone, which is the fuzzy agreement this codebase
// refuses.
func TestATrackWithNoLengthIsNotOfferedForLyrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	seedArtistWithCatalogue(t, ctx, pool, "Objekt", 1, 1)

	rows, err := queries.TracksToLyric(ctx, TracksToLyricParams{Batch: 50})
	if err != nil {
		t.Fatalf("TracksToLyric() error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("offered %d files whose length nothing knows", len(rows))
	}
}

// Asking for a release to be matched again queues one pass, and a second ask
// while it is queued adds nothing.
func TestAskingForAReleaseToBeMatchedAgainQueuesOnePass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	_, albumID := seedArtistWithCatalogue(t, ctx, pool, "Objekt", 1, 0)

	queued, askable, err := queries.QueueAlbumRefresh(ctx, albumID)
	if err != nil || !queued || !askable {
		t.Fatalf("QueueAlbumRefresh() = %v, %v, %v", queued, askable, err)
	}

	queued, askable, err = queries.QueueAlbumRefresh(ctx, albumID)
	if err != nil {
		t.Fatalf("QueueAlbumRefresh() error = %v", err)
	}
	if queued {
		t.Fatal("asking twice queued a second pass for one release")
	}
	if !askable {
		t.Fatal("a release with a pass already queued was reported unaskable")
	}
}

// A release the catalogue does not hold is reported apart from one whose pass is
// already running, because the two read the same and are not the same.
func TestAskingAboutAReleaseNobodyHoldsIsReportedApart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	queued, askable, err := queries.QueueAlbumRefresh(ctx, uuid.New())
	if err != nil {
		t.Fatalf("QueueAlbumRefresh() error = %v", err)
	}
	if queued || askable {
		t.Fatalf("QueueAlbumRefresh() = %v, %v; want nothing queued and nothing to ask", queued, askable)
	}
}

func TestTheFailedJobsOfASelectionAreReadPerRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	_, mine := seedArtistWithCatalogue(t, ctx, pool, "Objekt", 1, 0)
	_, other := seedArtistWithCatalogue(t, ctx, pool, "Anetha", 1, 0)

	for _, albumID := range []uuid.UUID{mine, other} {
		payload, _ := json.Marshal(map[string]string{"albumId": albumID.String()})
		if _, err := pool.Exec(ctx, `
			INSERT INTO jobs (kind, payload, status, completed_at)
			VALUES ('refresh_album_metadata', $1::jsonb, 'failed', now())
		`, payload); err != nil {
			t.Fatalf("seed a failed job: %v", err)
		}
	}

	byRelease, err := queries.FailedReleaseJobs(ctx, []uuid.UUID{mine})
	if err != nil {
		t.Fatalf("FailedReleaseJobs() error = %v", err)
	}
	if len(byRelease) != 1 || len(byRelease[mine]) != 1 {
		t.Fatalf("read %v; want only the selected release's failure", byRelease)
	}
}

// The lyrics sweep is one pass, and asking for another moves the one that is
// queued rather than adding a second.
func TestOneLyricsSweepIsQueuedAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(12 * time.Hour)
	if err := queries.QueueLyricsSweep(ctx, later, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("QueueLyricsSweep() error = %v", err)
	}
	position, _ := json.Marshal(map[string]string{"afterFileId": uuid.NewString()})
	if err := queries.QueueLyricsSweep(ctx, time.Now(), position); err != nil {
		t.Fatalf("QueueLyricsSweep() error = %v", err)
	}

	var count int
	var payload []byte
	if err := pool.QueryRow(ctx, `
		SELECT count(*) OVER (), payload::text::bytea
		FROM jobs WHERE kind = 'sweep_lyrics' AND status = 'queued'
	`).Scan(&count, &payload); err != nil {
		t.Fatalf("read the queued sweep: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d lyrics sweeps are queued; want one", count)
	}
	if !strings.Contains(string(payload), "afterFileId") {
		t.Fatalf("payload = %s; want the position the newer ask carried", payload)
	}
}

// Startup asks only that a pass exists. One already queued keeps the time and
// the position it holds, so restarting does not restart the walk.
func TestStartupLeavesAQueuedLyricsSweepAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	if err := queries.QueueLyricsSweep(ctx, later, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("QueueLyricsSweep() error = %v", err)
	}
	if err := queries.EnsureLyricsSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureLyricsSweepQueued() error = %v", err)
	}

	var due time.Time
	if err := pool.QueryRow(ctx, `
		SELECT run_after FROM jobs WHERE kind = 'sweep_lyrics' AND status = 'queued'
	`).Scan(&due); err != nil {
		t.Fatalf("read the queued sweep: %v", err)
	}
	if !due.UTC().Truncate(time.Second).Equal(later) {
		t.Fatalf("due = %s; want the time the queued pass already held (%s)", due, later)
	}
}

// The count behind the notification. It reads the same held copies the review
// queue itself reads, so a message and the screen it points at cannot disagree.
func TestTheQuestionsWaitingAreCountedTheWayTheQueueCountsThem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	empty, err := queries.WaitingQuestions(ctx)
	if err != nil {
		t.Fatalf("WaitingQuestions() error = %v", err)
	}
	if empty.HeldCopies != 0 || empty.PausedImports != 0 {
		t.Fatalf("an empty database has %+v waiting", empty)
	}
}

// Nothing configured means nothing is sent, which is what every installation
// starts with. Saving is what brings the row into being.
func TestNotificationSettingsStartAbsentAndKeepTheirToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.NotificationSettings(ctx); err != pgx.ErrNoRows {
		t.Fatalf("NotificationSettings() error = %v; want pgx.ErrNoRows", err)
	}

	saved, err := queries.SaveNotificationSettings(ctx, SaveNotificationSettingsParams{
		Kind: NotifyNtfy, Endpoint: "https://ntfy.sh/topic", Token: "tk_secret", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveNotificationSettings() error = %v", err)
	}
	if saved.Token.String != "tk_secret" {
		t.Fatalf("token = %q", saved.Token.String)
	}

	// An empty token keeps the stored one, exactly as the slskd key is kept.
	again, err := queries.SaveNotificationSettings(ctx, SaveNotificationSettingsParams{
		Kind: NotifyNtfy, Endpoint: "https://ntfy.sh/topic", Enabled: false,
	})
	if err != nil {
		t.Fatalf("SaveNotificationSettings() error = %v", err)
	}
	if again.Token.String != "tk_secret" {
		t.Fatalf("token = %q; an empty save lost the stored one", again.Token.String)
	}
	if again.Enabled {
		t.Fatal("the setting stayed on")
	}
}

// Switching on with nowhere to send to is refused by the table, not only by the
// handler: a setting that cannot do what it says must not be storable at all.
func TestNotificationsCannotBeSwitchedOnWithNoAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.SaveNotificationSettings(ctx, SaveNotificationSettingsParams{
		Kind: NotifyNtfy, Enabled: true,
	}); err == nil {
		t.Fatal("a setting that is on with no address was stored")
	}
}

// The file a player is pointed at, and the rule that a file the last scan found
// missing is no answer at all.
func TestALibraryFileTheScanLostIsNotFoundByID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	fileID := seedLibraryFile(t, ctx, pool, "/music/gone.flac")
	if _, err := queries.LibraryFileByID(ctx, fileID); err != nil {
		t.Fatalf("LibraryFileByID() error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, fileID); err != nil {
		t.Fatalf("lose the file: %v", err)
	}

	if _, err := queries.LibraryFileByID(ctx, fileID); err != pgx.ErrNoRows {
		t.Fatalf("LibraryFileByID() error = %v; want pgx.ErrNoRows for a file that is gone", err)
	}
}

// The two states the scanner records, as the file list reads them.
//
// They run against PostgreSQL because the predicate reads three different
// columns through one parameter, and which column answers is the whole of what
// makes these two filters different from the four matching states.

func TestTheFileListSeparatesMissingFromUnreadable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	present := seedLibraryFile(t, ctx, pool, "/music/present.flac")
	gone := seedLibraryFile(t, ctx, pool, "/music/gone.flac")
	unreadable := seedLibraryFile(t, ctx, pool, "/music/unreadable.flac")
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET scan_error = 'could not read the tags' WHERE id = $1
	`, unreadable); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []struct {
		status string
		file   uuid.UUID
	}{
		{StatusMissing, gone},
		{StatusUnreadable, unreadable},
	} {
		page, err := queries.ListLibraryFiles(ctx, ListLibraryFilesParams{
			Status: expected.status, Limit: 50,
		})
		if err != nil {
			t.Fatalf("ListLibraryFiles(%s) error = %v", expected.status, err)
		}
		if page.Total != 1 || len(page.Items) != 1 {
			t.Fatalf("%s listed %d files; want one", expected.status, page.Total)
		}
		if page.Items[0].ID != expected.file {
			t.Fatalf("%s listed %s; want %s", expected.status, page.Items[0].ID, expected.file)
		}
	}

	// A file the scan lost is not offered as unreadable: it is not on disc to be
	// unreadable, and it has its own answer.
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET scan_error = 'was unreadable before it went' WHERE id = $1
	`, gone); err != nil {
		t.Fatal(err)
	}
	page, err := queries.ListLibraryFiles(ctx, ListLibraryFilesParams{
		Status: StatusUnreadable, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].ID != unreadable {
		t.Fatalf("unreadable listed %d files; a file that is gone was counted among them", page.Total)
	}
	_ = present
}

// The matching states still read match_status and nothing else, so adding the
// two scanner states did not quietly narrow them.
func TestTheFileListStillReadsTheMatchingStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	matched := seedLibraryFile(t, ctx, pool, "/music/matched.flac")
	seedLibraryFile(t, ctx, pool, "/music/unmatched.flac")
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET match_status = 'matched' WHERE id = $1
	`, matched); err != nil {
		t.Fatal(err)
	}

	page, err := queries.ListLibraryFiles(ctx, ListLibraryFilesParams{Status: "matched", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].ID != matched {
		t.Fatalf("matched listed %d files", page.Total)
	}

	all, err := queries.ListLibraryFiles(ctx, ListLibraryFilesParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 {
		t.Fatalf("an empty status listed %d files; want every one", all.Total)
	}
}
