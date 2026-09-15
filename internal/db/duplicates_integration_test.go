package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// The library is asked what it already holds against one release. The owned
// half must be a fact, the unresolved half must be held to the identifiers and
// the exact tag comparison the matcher uses, and music that only resembles the
// release must not appear at all.
func TestAlbumDuplicateEvidenceReportsOwnedAndUnresolvedFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, otherAlbumID, unownedAlbumID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	recordingID := uuid.New()
	ownedTrackID, ambiguousTrackID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
		), album AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, 'Dummy')
		), other AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($3, $1, 'Roseland NYC Live')
		), unowned AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($7, $1, 'Third')
		)
		INSERT INTO tracks (id, album_id, title, disc_number, track_number, musicbrainz_recording_id)
		VALUES ($4, $2, 'Mysterons', 1, 1, NULL),
		       ($5, $2, 'Sour Times', 1, 2, $6)
	`, artistID, albumID, otherAlbumID, ownedTrackID, ambiguousTrackID, recordingID,
		unownedAlbumID); err != nil {
		t.Fatal(err)
	}

	ownedFileID, taggedFileID, recordingFileID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag,
		    disc_number, track_number, musicbrainz_recording_id, match_status
		)
		VALUES
		    -- Owned: matched to a track of this release.
		    ($1, '/music/Dummy/01 Mysterons.flac', 1, now(), 'Portishead', 'Dummy',
		     'Mysterons', 1, 1, NULL, 'matched'),
		    -- Unresolved: tagged as this release, no track proven.
		    ($2, '/music/inbox/sour times.flac', 1, now(), 'portishead', ' Dummy ',
		     'Sour Times', NULL, NULL, NULL, 'unmatched'),
		    -- Unresolved: carries a recording ID from this release.
		    ($3, '/music/inbox/unnamed.flac', 1, now(), NULL, NULL, NULL,
		     NULL, NULL, $4, 'ambiguous'),
		    -- A different release by the same artist says nothing about this one.
		    ($5, '/music/Roseland/01 Humming.flac', 1, now(), 'Portishead',
		     'Roseland NYC Live', 'Humming', 1, 1, NULL, 'unmatched'),
		    -- A file that is gone is not evidence that anything is owned.
		    ($6, '/music/Dummy/03 Strangers.flac', 1, now(), 'Portishead', 'Dummy',
		     'Strangers', 1, 3, NULL, 'unmatched')
	`, ownedFileID, taggedFileID, recordingFileID, recordingID, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE path = '/music/Dummy/03 Strangers.flac'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence, is_manual)
		VALUES ($1, $2, 'album_disc_track', 0.95, false)
	`, ownedTrackID, ownedFileID); err != nil {
		t.Fatal(err)
	}

	evidence, err := New(pool).AlbumDuplicateEvidence(ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Any() {
		t.Fatal("a library holding this release reported nothing")
	}
	if evidence.TrackCount != 2 || evidence.OwnedTrackCount != 1 || evidence.UnresolvedCount != 2 {
		t.Fatalf("counts = %#v", evidence)
	}
	if len(evidence.Files) != 3 {
		t.Fatalf("files = %#v", evidence.Files)
	}
	if evidence.Files[0].State != "owned" || evidence.Files[0].Track != "1. Mysterons" {
		t.Fatalf("owned file = %#v", evidence.Files[0])
	}
	if evidence.Files[0].Evidence !=
		"is already matched by artist, album, and track position to 1. Mysterons." {
		t.Fatalf("owned evidence = %q", evidence.Files[0].Evidence)
	}

	// The tag comparison ignores case and surrounding space, exactly as the
	// matcher does, and nothing looser than that.
	byPath := make(map[string]DuplicateFile, len(evidence.Files))
	for _, file := range evidence.Files {
		byPath[file.Path] = file
	}
	tagged, ok := byPath["/music/inbox/sour times.flac"]
	if !ok || tagged.State != "unresolved" ||
		tagged.Evidence != "is tagged as this release, but no catalogue track was proven for it." {
		t.Fatalf("tagged file = %#v", tagged)
	}
	carried, ok := byPath["/music/inbox/unnamed.flac"]
	if !ok || carried.Evidence !=
		"carries a MusicBrainz recording ID from this release, and more than one catalogue track fits it." {
		t.Fatalf("recording file = %#v", carried)
	}
	if _, listed := byPath["/music/Roseland/01 Humming.flac"]; listed {
		t.Fatal("a different release was reported as this one")
	}
	if _, listed := byPath["/music/Dummy/03 Strangers.flac"]; listed {
		t.Fatal("a missing file was reported as held")
	}
	if evidence.Summary != "The library already holds 1 of the 2 tracks of this release, "+
		"and 2 files that look like this release without a proven track identity." {
		t.Fatalf("summary = %q", evidence.Summary)
	}

	// A release the library says nothing about must ask nothing. The Roseland
	// file is deliberately not that case: it is tagged as its own release, and
	// it is that release it counts against.
	quiet, err := New(pool).AlbumDuplicateEvidence(ctx, unownedAlbumID)
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Any() {
		t.Fatalf("an unowned release reported %#v", quiet.Files)
	}
}

// The acknowledgement is what allows a request to start, so it has to survive
// being written and read back whole.
func TestAcknowledgeDuplicatesRecordsTheEvidenceShown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
		)
		INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, 'Dummy')
	`, artistID, albumID); err != nil {
		t.Fatal(err)
	}

	queries := New(pool)
	requestID, created, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Dummy", FileCount: 1, ExpectedTrackCount: 11,
		TotalSizeBytes: 1, Format: "flac", Score: 0.9,
		Files: []DownloadRequestFile{{Path: "p/01.flac", Name: "01.flac", SizeBytes: 1}},
	})
	if err != nil || !created {
		t.Fatalf("created = %t, err = %v", created, err)
	}

	// A request nobody has answered for carries no acknowledgement at all.
	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DuplicateAcknowledgedAt.Valid || stored.DuplicateEvidence != nil {
		t.Fatalf("unanswered request = %#v", stored.DuplicateEvidence)
	}

	shown := DuplicateEvidence{
		AlbumID: albumID, AlbumTitle: "Dummy", ArtistName: "Portishead",
		TrackCount: 11, OwnedTrackCount: 1, Summary: "The library already holds 1 of the 11 tracks of this release.",
		Files: []DuplicateFile{{
			Path: "/music/Dummy/01 Mysterons.flac", State: "owned", MatchStatus: "matched",
			Track: "1. Mysterons", Evidence: "is already matched by ISRC to 1. Mysterons.",
		}},
	}
	answered, err := queries.AcknowledgeDuplicates(ctx, requestID, shown)
	if err != nil {
		t.Fatal(err)
	}
	if !answered.DuplicateAcknowledgedAt.Valid || answered.DuplicateEvidence == nil {
		t.Fatalf("answered request = %#v", answered)
	}
	if answered.DuplicateEvidence.Summary != shown.Summary ||
		len(answered.DuplicateEvidence.Files) != 1 ||
		answered.DuplicateEvidence.Files[0].Evidence != shown.Files[0].Evidence {
		t.Fatalf("stored evidence = %#v", answered.DuplicateEvidence)
	}
}

// The question is about one release. A release the catalogue does not hold is
// missing rather than a release the library holds nothing against, because the
// two would read the same and only one of them means "go ahead".
func TestAskingWhatIsHeldAgainstAReleaseNobodyHasIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := New(pool).AlbumDuplicateEvidence(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("AlbumDuplicateEvidence() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// The acknowledgement is what allows a request to start, so it can only be given
// to one that has not started. Answering for a transfer already moving would be a
// permission granted after the fact.
func TestAcknowledgingDuplicatesForARequestThatHasStartedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	queries := New(pool)
	requestID, _, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Dummy", FileCount: 1, ExpectedTrackCount: 1,
		TotalSizeBytes: 100, Format: "flac", Score: 0.9,
		Files: []DownloadRequestFile{{Path: `Music\Dummy\01.flac`, Name: "01.flac", SizeBytes: 100}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	_, err = queries.AcknowledgeDuplicates(ctx, requestID, DuplicateEvidence{
		AlbumID: albumID, AlbumTitle: "Dummy", ArtistName: "Portishead",
		Summary: "The library holds nothing that looks like this release.",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("AcknowledgeDuplicates() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// heldTwiceFixture is the case the whole feature exists for: an album copy
// matched to the catalogue track, and a single fetched earlier for a want,
// resolved to the same recording. The catalogue holds one track row per release,
// so nothing about the two files collides and nothing noticed them.
type heldTwiceFixture struct {
	recordingID uuid.UUID
	albumTrack  uuid.UUID
	albumCopy   uuid.UUID
	single      uuid.UUID
}

func seedRecordingHeldTwice(ctx context.Context, t *testing.T, pool *pgxpool.Pool) heldTwiceFixture {
	t.Helper()
	fixture := heldTwiceFixture{
		recordingID: uuid.New(), albumTrack: uuid.New(),
		albumCopy: uuid.New(), single: uuid.New(),
	}
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name)
		    VALUES ($1, 'Massive Attack', 'Massive Attack')
		), album AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, 'Mezzanine')
		)
		INSERT INTO tracks (id, album_id, title, disc_number, track_number, musicbrainz_recording_id)
		VALUES ($3, $2, 'Teardrop', 1, 6, $4)
	`, artistID, albumID, fixture.albumTrack, fixture.recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag,
		    disc_number, track_number, duration_ms, match_status
		)
		VALUES
		    ($1, '/music/Massive Attack/Mezzanine/06 Teardrop.flac', 34000000, now(),
		     'Massive Attack', 'Mezzanine', 'Teardrop', 1, 6, 330000, 'matched'),
		    ($2, '/music/singles/Teardrop.mp3', 8000000, now(),
		     'Massive Attack', 'Teardrop', 'Teardrop', 1, 1, 331000, 'matched')
	`, fixture.albumCopy, fixture.single); err != nil {
		t.Fatal(err)
	}
	// The album copy answers a catalogue track; the single answers the same
	// recording through the identity resolved for it. Two different routes to
	// one recording is the whole shape of the problem.
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence, is_manual)
		VALUES ($1, $2, 'album_disc_track', 0.95, false)
	`, fixture.albumTrack, fixture.albumCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, artist_name,
		    track_title, method, confidence, summary
		)
		VALUES ($1, 'external', $2, 'Massive Attack', 'Teardrop', 'acoustid', 0.99,
		        'Identified by fingerprint.')
	`, fixture.single, fixture.recordingID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// The library holds one recording on disc twice — an album copy and a single —
// and says so, with enough about each copy for somebody to see which they would
// keep.
func TestARecordingTwoFilesAnswerIsReportedAsHeldTwice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fixture := seedRecordingHeldTwice(ctx, t, pool)

	held, err := New(pool).RecordingsHeldTwice(ctx, false, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if held.Total != 1 || len(held.Recordings) != 1 {
		t.Fatalf("held = %#v", held)
	}
	recording := held.Recordings[0]
	if recording.RecordingID != fixture.recordingID ||
		recording.Title != "Teardrop" || recording.Artist != "Massive Attack" {
		t.Fatalf("recording = %#v", recording)
	}
	if len(recording.Copies) != 2 {
		t.Fatalf("copies = %#v", recording.Copies)
	}
	album, single := recording.Copies[0], recording.Copies[1]
	if album.ID != fixture.albumCopy || album.Format != "FLAC" ||
		album.SizeBytes != 34000000 || album.Release != "Mezzanine" ||
		album.Reason != "Matched to a catalogue track" {
		t.Fatalf("album copy = %#v", album)
	}
	if album.DurationMS == nil || *album.DurationMS != 330000 {
		t.Fatalf("album copy duration = %#v", album.DurationMS)
	}
	if single.ID != fixture.single || single.Format != "MP3" ||
		single.SizeBytes != 8000000 || single.Release != "Teardrop" ||
		single.Reason != "Identified as this recording" {
		t.Fatalf("single = %#v", single)
	}
	if album.KeptAt != nil || single.KeptAt != nil {
		t.Fatal("a pair nobody answered was reported as kept on purpose")
	}
}

// seedNamedRecordingHeldTwice is seedRecordingHeldTwice with the artist,
// title and paths parameterised, so a test wanting more than one held-twice
// group can seed several without their paths colliding, and can pick titles
// that fix the order the list reads them back in.
func seedNamedRecordingHeldTwice(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, artist, title string,
) heldTwiceFixture {
	t.Helper()
	fixture := heldTwiceFixture{
		recordingID: uuid.New(), albumTrack: uuid.New(),
		albumCopy: uuid.New(), single: uuid.New(),
	}
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name) VALUES ($1, $5, $5)
		), album AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, $6)
		)
		INSERT INTO tracks (id, album_id, title, disc_number, track_number, musicbrainz_recording_id)
		VALUES ($3, $2, $6, 1, 6, $4)
	`, artistID, albumID, fixture.albumTrack, fixture.recordingID, artist, title); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag,
		    disc_number, track_number, duration_ms, match_status
		)
		VALUES
		    ($1, '/music/' || $3 || '/album/' || $4 || '.flac', 34000000, now(),
		     $3, $4, $4, 1, 6, 330000, 'matched'),
		    ($2, '/music/' || $3 || '/single/' || $4 || '.mp3', 8000000, now(),
		     $3, $4, $4, 1, 1, 331000, 'matched')
	`, fixture.albumCopy, fixture.single, artist, title); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence, is_manual)
		VALUES ($1, $2, 'album_disc_track', 0.95, false)
	`, fixture.albumTrack, fixture.albumCopy); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, artist_name,
		    track_title, method, confidence, summary
		)
		VALUES ($1, 'external', $2, $3, $4, 'acoustid', 0.99, 'Identified by fingerprint.')
	`, fixture.single, fixture.recordingID, artist, title); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// limit and offset page the recording groups, and total keeps reporting every
// group there is, including the ones a page short of the end never reaches —
// a window function scoped to the page alone would read zero there instead.
func TestRecordingsHeldTwicePagesGroupsWithATruthfulTotal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	// Titles fix the order the list reads them back in: it sorts by artist,
	// then title.
	first := seedNamedRecordingHeldTwice(ctx, t, pool, "Aardvark", "Aardvark Song")
	second := seedNamedRecordingHeldTwice(ctx, t, pool, "Zebra", "Zebra Song")

	page, err := New(pool).RecordingsHeldTwice(ctx, false, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Recordings) != 1 ||
		page.Recordings[0].RecordingID != first.recordingID {
		t.Fatalf("first page = %#v", page)
	}

	next, err := New(pool).RecordingsHeldTwice(ctx, false, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if next.Total != 2 || len(next.Recordings) != 1 ||
		next.Recordings[0].RecordingID != second.recordingID {
		t.Fatalf("second page = %#v", next)
	}

	past, err := New(pool).RecordingsHeldTwice(ctx, false, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if past.Total != 2 || len(past.Recordings) != 0 {
		t.Fatalf("past the end of the list = %#v", past)
	}
}

// One recording, several releases, one file. That is the ordinary case the
// catalogue is built for — an EP and the compilation that gathers it — and
// calling it a duplicate would report every proven file in the library twice.
func TestARecordingOnTwoReleasesWithOneFileIsNotHeldTwice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, epID, compilationID := uuid.New(), uuid.New(), uuid.New()
	recordingID := uuid.New()
	epTrackID, compilationTrackID, fileID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Burial', 'Burial')
		), ep AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, 'Untrue')
		), compilation AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($3, $1, 'Tunes 2011-2019')
		)
		INSERT INTO tracks (id, album_id, title, disc_number, track_number, musicbrainz_recording_id)
		VALUES ($4, $2, 'Archangel', 1, 2, $6),
		       ($5, $3, 'Archangel', 1, 4, $6)
	`, artistID, epID, compilationID, epTrackID, compilationTrackID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, match_status)
		VALUES ($1, '/music/Burial/Untrue/02 Archangel.flac', 30000000, now(), 'matched')
	`, fileID); err != nil {
		t.Fatal(err)
	}
	// One file is the honest answer to every release carrying the recording
	// (migration 00033), so it holds both mappings.
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence, is_manual)
		VALUES ($1, $3, 'musicbrainz_recording_id', 1, false),
		       ($2, $3, 'musicbrainz_recording_id', 1, false)
	`, epTrackID, compilationTrackID, fileID); err != nil {
		t.Fatal(err)
	}

	held, err := New(pool).RecordingsHeldTwice(ctx, false, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if held.Total != 0 || len(held.Recordings) != 0 {
		t.Fatalf("one file on two releases reported %#v", held)
	}
}

// A file the last scan found gone is a record of something that was there.
// Counting it would ask somebody to choose between a file and a memory.
func TestAMissingFileDoesNotCountTowardAPair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fixture := seedRecordingHeldTwice(ctx, t, pool)

	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, fixture.single); err != nil {
		t.Fatal(err)
	}

	held, err := New(pool).RecordingsHeldTwice(ctx, false, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if held.Total != 0 || len(held.Recordings) != 0 {
		t.Fatalf("a missing copy was counted: %#v", held)
	}
}

// An answer nobody writes down is a question asked again tomorrow. Once
// somebody says the pair is meant to be there, it stops being listed.
func TestAPairKeptOnPurposeStopsBeingListed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fixture := seedRecordingHeldTwice(ctx, t, pool)
	queries := New(pool)

	answered, err := queries.KeepRecordingHeldTwice(ctx, fixture.recordingID, true)
	if err != nil {
		t.Fatal(err)
	}
	if answered != 2 {
		t.Fatalf("answered = %d, want both copies", answered)
	}

	held, err := queries.RecordingsHeldTwice(ctx, false, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if held.Total != 0 || len(held.Recordings) != 0 {
		t.Fatalf("an answered pair was asked about again: %#v", held)
	}
}

// The answer has to be visible somewhere to be withdrawn, so the pairs somebody
// kept are a list of their own.
func TestAPairKeptOnPurposeIsListedAmongTheOnesKept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fixture := seedRecordingHeldTwice(ctx, t, pool)
	queries := New(pool)

	if _, err := queries.KeepRecordingHeldTwice(ctx, fixture.recordingID, true); err != nil {
		t.Fatal(err)
	}

	kept, err := queries.RecordingsHeldTwice(ctx, true, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Total != 1 || len(kept.Recordings) != 1 || len(kept.Recordings[0].Copies) != 2 {
		t.Fatalf("kept = %#v", kept)
	}
	for _, copied := range kept.Recordings[0].Copies {
		if copied.KeptAt == nil {
			t.Fatalf("copy %s says nothing about when it was kept", copied.Path)
		}
	}
}

// Keeping a pair is a decision, and a decision somebody can undo. Withdrawing
// puts the question back exactly as it was.
func TestKeepingAPairIsWithdrawnAndTheQuestionComesBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fixture := seedRecordingHeldTwice(ctx, t, pool)
	queries := New(pool)

	if _, err := queries.KeepRecordingHeldTwice(ctx, fixture.recordingID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.KeepRecordingHeldTwice(ctx, fixture.recordingID, false); err != nil {
		t.Fatal(err)
	}

	held, err := queries.RecordingsHeldTwice(ctx, false, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if held.Total != 1 || len(held.Recordings) != 1 {
		t.Fatalf("a withdrawn answer left the pair unlisted: %#v", held)
	}
	kept, err := queries.RecordingsHeldTwice(ctx, true, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Total != 0 {
		t.Fatalf("a withdrawn answer was still recorded: %#v", kept)
	}
}

// A recording the library holds nothing for cannot be answered about, and
// saying so is what lets the route report it rather than succeed silently.
func TestKeepingARecordingTheLibraryDoesNotHoldAnswersNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	answered, err := New(pool).KeepRecordingHeldTwice(ctx, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	if answered != 0 {
		t.Fatalf("answered = %d, want nothing", answered)
	}
}

// A third copy arriving after somebody kept a pair is a new question, and the
// answer they gave was not about it. Counting only the files on one side of
// that answer would leave the new copy in neither list.
func TestACopyArrivingAfterAPairWasKeptIsAskedAbout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fixture := seedRecordingHeldTwice(ctx, t, pool)
	queries := New(pool)

	if _, err := queries.KeepRecordingHeldTwice(ctx, fixture.recordingID, true); err != nil {
		t.Fatal(err)
	}

	thirdID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, duration_ms, match_status)
		VALUES ($1, '/music/inbox/teardrop.flac', 33000000, now(), 330000, 'unmatched')
	`, thirdID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, artist_name,
		    track_title, method, confidence, summary
		)
		VALUES ($1, 'external', $2, 'Massive Attack', 'Teardrop', 'acoustid', 0.99,
		        'Identified by fingerprint.')
	`, thirdID, fixture.recordingID); err != nil {
		t.Fatal(err)
	}

	held, err := queries.RecordingsHeldTwice(ctx, false, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if held.Total != 1 || len(held.Recordings) != 1 {
		t.Fatalf("a new copy of an answered pair was listed nowhere: %#v", held)
	}
	// The whole group comes back, the answered copies included: what somebody
	// is deciding is which of three copies to hold.
	if len(held.Recordings[0].Copies) != 3 {
		t.Fatalf("copies = %#v", held.Recordings[0].Copies)
	}
	kept, err := queries.RecordingsHeldTwice(ctx, true, MaxHeldTwiceRecordings, 0)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Total != 0 {
		t.Fatalf("a group with an unanswered copy was reported as kept: %#v", kept)
	}
}

// "Yes, on purpose" answers a question, and there is no question about a file
// that is the only copy of anything.
func TestALoneCopyCannotBeKeptOnPurpose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	recordingID, fileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Talk Talk', 'Talk Talk')
		), album AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, 'Laughing Stock')
		)
		INSERT INTO tracks (id, album_id, title, disc_number, track_number, musicbrainz_recording_id)
		VALUES ($3, $2, 'Myrrhman', 1, 1, $4)
	`, artistID, albumID, trackID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, match_status)
		VALUES ($1, '/music/Talk Talk/Laughing Stock/01 Myrrhman.flac', 40000000, now(), 'matched')
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence, is_manual)
		VALUES ($1, $2, 'musicbrainz_recording_id', 1, false)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}

	queries := New(pool)
	answered, err := queries.KeepRecordingHeldTwice(ctx, recordingID, true)
	if err != nil {
		t.Fatal(err)
	}
	if answered != 0 {
		t.Fatalf("answered = %d, want a lone copy left alone", answered)
	}

	var keptAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT duplicate_kept_at FROM library_files WHERE id = $1
	`, fileID).Scan(&keptAt); err != nil {
		t.Fatal(err)
	}
	if keptAt.Valid {
		t.Fatal("a file that is the only copy of its recording was marked kept on purpose")
	}
}
