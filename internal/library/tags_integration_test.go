package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/tagging"
	"github.com/rs/zerolog"
	"go.senan.xyz/taglib"
)

// taggedRelease is the smallest catalogue a file can be written from: an
// artist, an album, the edition Schall chose for it, and one track.
type taggedRelease struct {
	trackID   uuid.UUID
	albumID   uuid.UUID
	groupID   uuid.UUID
	releaseID uuid.UUID
}

func catalogueRelease(t *testing.T, pool *pgxpool.Pool, artist, album, title string) taggedRelease {
	t.Helper()
	ctx := context.Background()
	release := taggedRelease{groupID: uuid.New(), releaseID: uuid.New()}
	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name) VALUES ($1, $2, $2) RETURNING id
	`, uuid.New(), artist).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3) RETURNING id
	`, artistID, release.groupID, album).Scan(&release.albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_editions (
			album_id, musicbrainz_release_id, title, release_date, selection_reason
		)
		VALUES ($1, $2, $3, '2023-11-03', 'chosen by the test')
	`, release.albumID, release.releaseID, album); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 1, 4) RETURNING id
	`, release.albumID, uuid.New(), title).Scan(&release.trackID); err != nil {
		t.Fatal(err)
	}
	return release
}

// peersCopy lays down a file tagged the way a stranger's copy is and records it
// the way a scan would.
func peersCopy(t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string) uuid.UUID {
	t.Helper()
	writeFLAC(t, path, 48000, 144000,
		"TITLE=walk to the closed bodega", "ARTIST=peer artist", "ALBUM=peer album",
		"TRACKNUMBER=7")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag,
			track_number, duration_ms
		)
		VALUES ($1, $2, $3, $4, 'peer artist', 'peer album', 'walk to the closed bodega', 7, 3000)
		RETURNING id
	`, rootID, path, info.Size(), databaseTime(info.ModTime())).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func mapFileToTrack(t *testing.T, pool *pgxpool.Pool, fileID, trackID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence)
		VALUES ($1, $2, 'musicbrainz_recording_id', 1)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}
}

// queueTagging asks for a pass and hands back the job carrying it.
func queueTagging(t *testing.T, tagger *Tagger) uuid.UUID {
	t.Helper()
	job, err := tagger.QueueTagging(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return job.ID
}

// finishTagging closes the job a pass was carried by, which is what the worker
// does when the batch comes back with nothing left to do.
func finishTagging(t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE jobs SET status = 'completed', completed_at = now() WHERE id = $1
	`, jobID); err != nil {
		t.Fatal(err)
	}
}

func taggingRun(t *testing.T, tagger *Tagger) TagStatus {
	t.Helper()
	status, err := tagger.LatestTagging(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func newTagger(pool *pgxpool.Pool) *Tagger {
	return NewTagger(pool, zerolog.Nop())
}

// The whole point: the music already in the library is described by the
// catalogue, so the music server groups it as one album under one name rather
// than as whatever each sender happened to type.
func TestAMatchedFileIsWrittenAsTheCatalogueTrackItAnswers(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	written := tagging.Tags{
		Title:          "Walk to the Closed Bodega",
		Album:          "Halfway House Near Me",
		Artist:         "AgonyOST",
		AlbumArtist:    "AgonyOST",
		TrackNumber:    4,
		DiscNumber:     1,
		Date:           "2023-11-03",
		ReleaseID:      release.releaseID,
		ReleaseGroupID: release.groupID,
	}
	if !tagging.AlreadySays(path, written) {
		t.Fatalf("the file does not say what the catalogue says: %#v", readTags(path))
	}
	if run := taggingRun(t, tagger).Run; run.Written != 1 || run.Unchanged != 0 || run.Failed != 0 {
		t.Fatalf("the pass counted %+v", run)
	}
}

// creditRelease credits the album and its one track the way the catalogue holds
// a collaboration: the album under the one artist it is filed by, the track
// under both of the artists MusicBrainz names on it.
func creditRelease(t *testing.T, pool *pgxpool.Pool, release taggedRelease) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	porter, nina := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO album_artist_credits (album_id, position, credited_name, musicbrainz_artist_id)
		VALUES ($1, 1, 'Porter Robinson', $2)
	`, release.albumID, porter); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_artist_credits (
			track_id, position, credited_name, musicbrainz_artist_id, join_phrase
		)
		VALUES ($1, 1, 'Porter Robinson', $2, ' & '), ($1, 2, 'Ninajirachi', $3, '')
	`, release.trackID, porter, nina); err != nil {
		t.Fatal(err)
	}
	return porter, nina
}

// The music already here is credited as the catalogue credits it, so a
// collaboration reaches the music server as the two artists it is by rather than
// as a third artist named after the pair.
func TestAMatchedFileIsWrittenWithTheCreditTheCatalogueHoldsForItsTrack(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "Porter Robinson", "SMILE! :D", "Everything to Me")
	porter, nina := creditRelease(t, pool, release)
	path := filepath.Join(rootPath, "track.flac")
	mapFileToTrack(t, pool, peersCopy(t, pool, rootID, path), release.trackID)
	tagger := newTagger(pool)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	if !tagging.AlreadySays(path, taggedAs(release, []tagging.Credit{
		{Name: "Porter Robinson", MusicBrainzArtistID: porter},
		{Name: "Ninajirachi", MusicBrainzArtistID: nina},
	}, []tagging.Credit{{Name: "Porter Robinson", MusicBrainzArtistID: porter}})) {
		t.Fatalf("the file is not credited as the catalogue credits it: %#v", readTags(path))
	}
}

// A track the catalogue holds no credit for is written with no list at all. One
// made by cutting the joined string at its commas would turn a band with a comma
// in its name into two artists forever.
func TestATrackTheCatalogueHoldsNoCreditForIsWrittenWithNoList(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "Godspeed You! Black Emperor", "Lift Your Skinny Fists", "Storm")
	path := filepath.Join(rootPath, "track.flac")
	mapFileToTrack(t, pool, peersCopy(t, pool, rootID, path), release.trackID)
	tagger := newTagger(pool)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	written, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(written["ARTISTS"]) != 0 || len(written["MUSICBRAINZ_ARTISTID"]) != 0 {
		t.Fatalf("a credit was invented: %q %q", written["ARTISTS"], written["MUSICBRAINZ_ARTISTID"])
	}
	if got := written["ARTIST"]; len(got) != 1 || got[0] != "Godspeed You! Black Emperor" {
		t.Fatalf("the credit as one string reads %q", got)
	}
}

// A file tagged before the catalogue held any credit gains one on the next pass,
// and a file that already carries its credit is read and left. Both are the same
// question — whether a file already says what Schall would write — and the credit
// list is part of that answer.
func TestAFileTaggedBeforeItsCreditWasHeldGainsItOnTheNextPass(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "Porter Robinson", "SMILE! :D", "Everything to Me")
	path := filepath.Join(rootPath, "track.flac")
	mapFileToTrack(t, pool, peersCopy(t, pool, rootID, path), release.trackID)
	tagger := newTagger(pool)
	jobID := queueTagging(t, tagger)
	if _, err := tagger.Tag(ctx, jobID); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}
	finishTagging(t, pool, jobID)
	porter, nina := creditRelease(t, pool, release)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags again: %v", err)
	}

	if run := taggingRun(t, tagger).Run; run.Written != 1 || run.Unchanged != 0 {
		t.Fatalf("the second pass counted %+v, wanting the file written again for its credit", run)
	}
	if !tagging.AlreadySays(path, taggedAs(release, []tagging.Credit{
		{Name: "Porter Robinson", MusicBrainzArtistID: porter},
		{Name: "Ninajirachi", MusicBrainzArtistID: nina},
	}, []tagging.Credit{{Name: "Porter Robinson", MusicBrainzArtistID: porter}})) {
		t.Fatalf("the file did not gain its credit: %#v", readTags(path))
	}
}

// And a pass over a library that is already credited rewrites nothing. A rewrite
// costs a copy of the whole file and changes everything that watches its size and
// its time, so a credit that is already in the file is read and left alone.
func TestASecondPassOverACreditedFileRewritesNothing(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "Porter Robinson", "SMILE! :D", "Everything to Me")
	creditRelease(t, pool, release)
	path := filepath.Join(rootPath, "track.flac")
	mapFileToTrack(t, pool, peersCopy(t, pool, rootID, path), release.trackID)
	tagger := newTagger(pool)
	jobID := queueTagging(t, tagger)
	if _, err := tagger.Tag(ctx, jobID); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}
	finishTagging(t, pool, jobID)
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags again: %v", err)
	}

	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ModTime().Equal(first.ModTime()) {
		t.Fatal("a file that already carried its credit was written again")
	}
	if run := taggingRun(t, tagger).Run; run.Unchanged != 1 || run.Written != 0 {
		t.Fatalf("the second pass counted %+v", run)
	}
}

// taggedAs is the whole account of one file: what the catalogue release says
// about it, with the credit it is by.
func taggedAs(release taggedRelease, artists, albumArtists []tagging.Credit) tagging.Tags {
	return tagging.Tags{
		Title:          "Everything to Me",
		Album:          "SMILE! :D",
		Artist:         "Porter Robinson",
		Artists:        artists,
		AlbumArtist:    "Porter Robinson",
		AlbumArtists:   albumArtists,
		TrackNumber:    4,
		DiscNumber:     1,
		Date:           "2023-11-03",
		ReleaseID:      release.releaseID,
		ReleaseGroupID: release.groupID,
	}
}

// A file nobody matched is not read, not copied and not touched. Silence about
// what a file is, is not permission to invent an album for it.
func TestAFileMappedToNothingIsLeftExactlyAsItIs(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	path := filepath.Join(rootPath, "stranger.flac")
	peersCopy(t, pool, rootID, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tagger := newTagger(pool)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a file nobody matched was written to")
	}
	if run := taggingRun(t, tagger).Run; run.Written != 0 {
		t.Fatalf("the pass counted %+v", run)
	}
}

// A pass may be run whenever somebody presses the button. A file that already
// says what Schall would write is read and left, because rewriting it costs a
// copy of the whole file and changes everything that watches its size and time.
func TestASecondPassRewritesNothing(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)
	jobID := queueTagging(t, tagger)
	if _, err := tagger.Tag(ctx, jobID); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}
	finishTagging(t, pool, jobID)
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags again: %v", err)
	}

	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ModTime().Equal(first.ModTime()) {
		t.Fatal("a file that already said what Schall proved was written again")
	}
	if run := taggingRun(t, tagger).Run; run.Unchanged != 1 || run.Written != 0 {
		t.Fatalf("the second pass counted %+v", run)
	}
}

// A pass that has already been asked for is the one that answers, so pressing
// the button twice is one pass rather than two processes copying one file.
func TestAPassAskedForWhileOneIsRunningIsTheOneAlreadyRunning(t *testing.T) {
	pool := dbtest.Setup(t)
	tagger := newTagger(pool)

	first := queueTagging(t, tagger)
	second := queueTagging(t, tagger)

	if first != second {
		t.Fatalf("a second pass %s was queued beside %s", second, first)
	}
}

// A scheduled run finding a pressed one already under way changes nothing:
// migration 00077's index is the guard both paths go through, so asking again
// is answered with the pressed run rather than a second row or an error. The
// next weekly pass is not lost by the skip — it is guaranteed once the pressed
// run finishes and asks for its own successor, which is what the second half
// of this test stands in for.
func TestAScheduledSweepWhileAPressedRunIsInFlightIsSkippedWithoutError(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	tagger := newTagger(pool)

	pressed := queueTagging(t, tagger)

	if err := tagger.QueueLibrarySweep(ctx, time.Now().Add(7*24*time.Hour)); err != nil {
		t.Fatalf("queue the weekly sweep beside a pressed run: %v", err)
	}

	var inFlight int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'tag_library' AND status IN ('queued', 'running')
	`).Scan(&inFlight); err != nil {
		t.Fatal(err)
	}
	if inFlight != 1 {
		t.Fatalf("tag_library jobs queued or running = %d, want the one pressed run alone", inFlight)
	}

	finishTagging(t, pool, pressed)
	if err := tagger.QueueLibrarySweep(ctx, time.Now().Add(7*24*time.Hour)); err != nil {
		t.Fatalf("queue the weekly sweep once the pressed run finished: %v", err)
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'tag_library' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("tag_library jobs queued = %d, want the next pass on the books", queued)
	}
}

// A pass already on the books, whichever asked for it, keeps the time it
// holds. Startup's job is only to guarantee one exists, never to move one
// that is already there.
func TestEnsureLibrarySweepQueuedLeavesAnExistingPassAlone(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	tagger := newTagger(pool)

	due := time.Now().Add(3 * time.Hour).Truncate(time.Millisecond)
	if err := tagger.QueueLibrarySweep(ctx, due); err != nil {
		t.Fatalf("queue the weekly sweep: %v", err)
	}
	if err := tagger.EnsureLibrarySweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("ensure the weekly sweep is queued: %v", err)
	}

	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT run_after FROM jobs WHERE kind = 'tag_library' AND status = 'queued'
	`).Scan(&runAfter); err != nil {
		t.Fatal(err)
	}
	if !runAfter.Equal(due) {
		t.Fatalf("run_after = %s, want the existing pass's own time %s", runAfter, due)
	}
}

// A restart between a run finishing and it asking for its successor is a
// window the worker cannot close on its own, because completing the job and
// scheduling the next one are two separate statements. EnsureLibrarySweepQueued
// is what closes it: called at startup with nothing queued or running, it puts
// a pass straight back on the books rather than leaving the weekly schedule
// dead until somebody notices and presses the button.
func TestEnsureLibrarySweepQueuedRecoversFromNothingScheduled(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	tagger := newTagger(pool)

	if err := tagger.EnsureLibrarySweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("ensure the weekly sweep is queued: %v", err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'tag_library' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("tag_library jobs queued = %d, want one put on the books", queued)
	}
}

// The file is a different size with a different time on it after a write, and
// the next scan would otherwise read that as somebody having changed it: it
// would re-read the tags, queue the file for resolution again and reconcile it
// — for every file in the library, learning nothing. What a scan reads out of a
// managed copy is the account it arrived with, which the write preserved, so
// the scan finds the file exactly as it left it.
func TestAScanAfterAPassFindsTheLibraryAsItLeftIt(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'resolved' WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	tagger := newTagger(pool)
	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	matcher := &fakeMatcher{}
	result, err := NewScanner(pool, matcher).Scan(ctx)
	if err != nil {
		t.Fatalf("scan the library: %v", err)
	}

	if result.Unchanged != 1 || result.Updated != 0 {
		t.Fatalf("the scan read the tagged file as %+v", result)
	}
	var mapped int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM track_mappings WHERE library_file_id = $1
	`, fileID).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if mapped != 1 {
		t.Fatalf("the file holds %d mappings after the scan", mapped)
	}
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT resolution_status FROM library_files WHERE id = $1
	`, fileID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" {
		t.Fatalf("the file went back to %q after being tagged", status)
	}
}

// And what a scan does read out of the file is still the stranger's account of
// it, so nothing Schall wrote can ever come back to it as evidence.
func TestAScanAfterAPassStillReadsTheAccountTheFileArrivedWith(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)
	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	arrived := readTags(path)

	if arrived.Album != "peer album" || arrived.Title != "walk to the closed bodega" ||
		arrived.Artist != "peer artist" {
		t.Fatalf("a scan reads the tagged file as %#v, which is Schall's own account", arrived)
	}
}

// One file answers the same recording on every release that lists it. Which of
// them the user owns a copy of is a question nothing in a mapping answers, and
// an album picked from two would file one album under two names.
func TestAFileAnsweringTwoReleasesIsLeftAloneOnDisc(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	single := catalogueRelease(t, pool, "Himera", "Tears", "Tears")
	compilation := catalogueRelease(t, pool, "Various Artists", "Danceteria", "Tears")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, single.trackID)
	mapFileToTrack(t, pool, fileID, compilation.trackID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tagger := newTagger(pool)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a file nothing chose a release for was written anyway")
	}
	if run := taggingRun(t, tagger).Run; run.Skipped != 1 || run.Written != 0 {
		t.Fatalf("the pass counted %+v", run)
	}
}

// A file that would not take the write is a line in the log and nothing more.
// A pass that stopped at the first one would leave the library in two accounts
// of itself, and the file it stopped at is exactly the one nobody can do
// anything about.
func TestAFileThatWillNotTakeTheWriteDoesNotStopThePass(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	other := catalogueRelease(t, pool, "Himera", "Tears", "Tears")
	path := filepath.Join(rootPath, "track.flac")
	mapFileToTrack(t, pool, peersCopy(t, pool, rootID, path), release.trackID)

	// A file whose bytes are not audio at all: nothing can read tags off it, so
	// nothing can write them into it either.
	broken := filepath.Join(rootPath, "broken.flac")
	brokenID := peersCopy(t, pool, rootID, broken)
	if err := os.WriteFile(broken, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	mapFileToTrack(t, pool, brokenID, other.trackID)
	tagger := newTagger(pool)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("a file that would not take the write failed the pass: %v", err)
	}

	run := taggingRun(t, tagger).Run
	if run.Failed != 1 || run.Written != 1 {
		t.Fatalf("the pass counted %+v, wanting the readable file written and the other one refused", run)
	}
	if !tagging.AlreadySays(path, tagging.Tags{
		Title:          "Walk to the Closed Bodega",
		Album:          "Halfway House Near Me",
		Artist:         "AgonyOST",
		AlbumArtist:    "AgonyOST",
		TrackNumber:    4,
		DiscNumber:     1,
		Date:           "2023-11-03",
		ReleaseID:      release.releaseID,
		ReleaseGroupID: release.groupID,
	}) {
		t.Fatal("the file beside the refused one was not written")
	}
}

// A pass is resumed rather than repeated, because where it reached is recorded
// as it goes rather than at the end of a batch.
func TestAPassCarriesOnFromTheFileItReached(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)
	jobID := queueTagging(t, tagger)
	if _, err := tagger.Tag(ctx, jobID); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	// The same job again is what a worker that died halfway is retried as.
	more, err := tagger.Tag(ctx, jobID)
	if err != nil {
		t.Fatalf("carry the pass on: %v", err)
	}

	if more {
		t.Fatal("a pass that reached the end of the library asked for another batch")
	}
	if run := taggingRun(t, tagger).Run; run.Written != 1 || run.Unchanged != 0 {
		t.Fatalf("the retried pass counted %+v, having read the same file twice", run)
	}
}

// Nothing to report is not an error: a library nobody has ever asked about is
// idle, and it still says how many files a pass would cover.
func TestALibraryNobodyHasAskedAboutReadsAsIdle(t *testing.T) {
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	fileID := peersCopy(t, pool, rootID, filepath.Join(rootPath, "track.flac"))
	mapFileToTrack(t, pool, fileID, release.trackID)

	status := taggingRun(t, newTagger(pool))

	if status.Status != "idle" {
		t.Fatalf("a library nobody asked about reads as %q", status.Status)
	}
	if status.Eligible != 1 {
		t.Fatalf("a pass would cover %d files", status.Eligible)
	}
}

// A pass covers the library as it stood when the button was pressed, and
// matching does not stop when it is let go of. A file matched afterwards is
// written where it is: one file left saying 2023 beside an album saying
// 2023-11-03 is the same album twice in a music server.
func TestAFileMatchedAfterAPassIsWrittenWithoutAnother(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	tagger := newTagger(pool)
	// The pass runs while the file is matched to nothing, exactly as the user's
	// did: it leaves the file alone and finishes.
	jobID := queueTagging(t, tagger)
	if _, err := tagger.Tag(ctx, jobID); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}
	finishTagging(t, pool, jobID)
	mapFileToTrack(t, pool, fileID, release.trackID)

	written, err := tagger.TagFile(ctx, fileID)
	if err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}

	if !written {
		t.Fatal("a file that had just been matched was not written")
	}
	if !tagging.AlreadySays(path, tagging.Tags{
		Title:          "Walk to the Closed Bodega",
		Album:          "Halfway House Near Me",
		Artist:         "AgonyOST",
		AlbumArtist:    "AgonyOST",
		TrackNumber:    4,
		DiscNumber:     1,
		Date:           "2023-11-03",
		ReleaseID:      release.releaseID,
		ReleaseGroupID: release.groupID,
	}) {
		t.Fatalf("the file does not say what the catalogue says: %#v", readTags(path))
	}
}

// The button and the match write the same file, and the second of them finds
// nothing to do. Two writes would cost a second copy of the file and a second
// change to its size and time, which is a scan re-reading a library that nobody
// changed.
func TestAPassOverAFileWrittenWhenItWasMatchedFindsNothingToChange(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)
	if _, err := tagger.TagFile(ctx, fileID); err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ModTime().Equal(first.ModTime()) {
		t.Fatal("a file written when it was matched was written again by the pass")
	}
	if run := taggingRun(t, tagger).Run; run.Unchanged != 1 || run.Written != 0 {
		t.Fatalf("the pass counted %+v", run)
	}
}

// The write of one file refuses exactly what a pass refuses. Which release the
// user owns a copy of is a question nothing in a mapping answers, and being
// matched a moment ago does not answer it either.
func TestAFileMatchedToTwoReleasesIsLeftAloneWhenItIsMatched(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	single := catalogueRelease(t, pool, "Himera", "Tears", "Tears")
	compilation := catalogueRelease(t, pool, "Various Artists", "Danceteria", "Tears")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, single.trackID)
	mapFileToTrack(t, pool, fileID, compilation.trackID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	written, err := newTagger(pool).TagFile(ctx, fileID)
	if err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}

	if written {
		t.Fatal("a file nothing chose a release for was reported as written")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a file nothing chose a release for was written anyway")
	}
}

// A file Schall never wrote into and never matched is nobody's business: there
// is no account of the catalogue in it to correct, and its tags are the
// stranger's own. See the two tests below for the file Schall did write into.
func TestAFileThatLostItsMappingIsLeftExactlyAsItIs(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	path := filepath.Join(rootPath, "stranger.flac")
	fileID := peersCopy(t, pool, rootID, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	written, err := newTagger(pool).TagFile(ctx, fileID)
	if err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}

	if written {
		t.Fatal("a file mapped to nothing was reported as written")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a file mapped to nothing was written to")
	}
}

// The defect this closes. A file Schall described stops being the track it was
// described from — the decision is withdrawn, the track goes to another file —
// and the description stays in the file, where the music server is still
// reading it. The file says again what it said when it arrived.
func TestAFileThatIsNoLongerATracksIsGivenBackTheTagsItArrivedWith(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)
	if _, err := tagger.TagFile(ctx, fileID); err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM track_mappings WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}

	written, err := tagger.TagFile(ctx, fileID)
	if err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}

	if !written {
		t.Fatal("a file that is no longer a track's was reported as left alone")
	}
	tags, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"TITLE": "walk to the closed bodega", "ALBUM": "peer album",
		"ARTIST": "peer artist", "TRACKNUMBER": "7",
	} {
		if got := tags[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s reads %q, and the file arrived saying %q", key, got, want)
		}
	}
	for _, key := range []string{"MUSICBRAINZ_ALBUMID", "MUSICBRAINZ_RELEASEGROUPID", "DATE", "SCHALL_TAGGED_AT"} {
		if got := tags[key]; len(got) != 0 {
			t.Errorf("%s reads %q, and nothing maps the file to that release", key, got)
		}
	}
}

// A file the library has lost is left alone in this direction too. Losing the
// mapping is not why the file is gone, and there is nothing on the disc to
// give anything back to.
func TestAFileTheLibraryLostIsNotGivenItsTagsBack(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)
	if _, err := tagger.TagFile(ctx, fileID); err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM track_mappings WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	written, err := tagger.TagFile(ctx, fileID)
	if err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}

	if written {
		t.Fatal("a file the library has lost was reported as written")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a file the library has lost was written to")
	}
}

// And the scan finds the library as the write left it. Giving a file its tags
// back rewrites the bytes, so the row is brought up to date with what Schall
// itself did — otherwise the next scan reads every restored file as one
// somebody edited, and sends the whole set back through resolution for nothing.
func TestAScanAfterAFileWasGivenItsTagsBackFindsTheLibraryAsItLeftIt(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	path := filepath.Join(rootPath, "track.flac")
	fileID := peersCopy(t, pool, rootID, path)
	mapFileToTrack(t, pool, fileID, release.trackID)
	tagger := newTagger(pool)
	if _, err := tagger.TagFile(ctx, fileID); err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM track_mappings WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'resolved' WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := tagger.TagFile(ctx, fileID); err != nil {
		t.Fatalf("write the tags of one file: %v", err)
	}

	result, err := NewScanner(pool, &fakeMatcher{}).Scan(ctx)
	if err != nil {
		t.Fatalf("scan the library: %v", err)
	}

	if result.Unchanged != 1 || result.Updated != 0 {
		t.Fatalf("the scan read the restored file as %+v", result)
	}
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT resolution_status FROM library_files WHERE id = $1
	`, fileID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" {
		t.Fatalf("the file went back to %q after its tags were restored", status)
	}
}

// "Left alone" covers two different silences, and a run that reports only the
// total leaves the reader guessing which one they are looking at. A file that
// answers two releases is counted as the reason it is.
func TestAPassSaysAFileWasLeftAloneForAnsweringTwoReleases(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	single := catalogueRelease(t, pool, "Himera", "Tears", "Tears")
	compilation := catalogueRelease(t, pool, "Various Artists", "Danceteria", "Tears")
	fileID := peersCopy(t, pool, rootID, filepath.Join(rootPath, "track.flac"))
	mapFileToTrack(t, pool, fileID, single.trackID)
	mapFileToTrack(t, pool, fileID, compilation.trackID)
	tagger := newTagger(pool)

	if _, err := tagger.Tag(ctx, queueTagging(t, tagger)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	run := taggingRun(t, tagger).Run
	if run.SkippedAmbiguous != 1 || run.SkippedUnproved != 0 {
		t.Fatalf("the pass counted %+v", run)
	}
}
