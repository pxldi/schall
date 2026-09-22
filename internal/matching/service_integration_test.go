package matching

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

func TestConservativeMatchingAndManualPrecedence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	recordingID := uuid.New()
	trackByRecording, trackByISRC, trackByPosition, trackByProvenance := uuid.New(), uuid.New(), uuid.New(), uuid.New()
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (
		    id, album_id, musicbrainz_recording_id, title, disc_number, track_number
		) VALUES ($1, $2, $3, 'Mysterons', 1, 1)
	`, trackByRecording, albumID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (
		    id, album_id, title, disc_number, track_number, isrc
		) VALUES ($1, $2, 'Sour Times', 1, 2, 'GB-AAA-94-00002')
	`, trackByISRC, albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (
		    id, album_id, title, disc_number, track_number, duration_ms
		) VALUES ($1, $2, 'Strangers', 1, 3, 236000)
	`, trackByPosition, albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (
		    id, album_id, title, disc_number, track_number
		) VALUES ($1, $2, 'It Could Be Sweet', 1, 4)
	`, trackByProvenance, albumID); err != nil {
		t.Fatal(err)
	}

	recordingFile, isrcFile, positionFile, provenanceFile := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, musicbrainz_recording_id
		) VALUES ($1, '/music/01.flac', 1, now(), $2)
	`, recordingFile, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, isrc
		) VALUES ($1, '/music/02.flac', 1, now(), 'gb-aaa-94-00002')
	`, isrcFile); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, artist_tag, album_tag,
		    disc_number, track_number, duration_ms
		) VALUES ($1, '/music/03.flac', 1, now(), ' portishead ', 'DUMMY', 1, 3, 238400)
	`, positionFile); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/provenance.flac', 1, now())
	`, provenanceFile); err != nil {
		t.Fatal(err)
	}
	requestID, _, err := db.New(pool).CreateDownloadRequest(ctx, db.CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Dummy", FileCount: 1, ExpectedTrackCount: 4,
		TotalSizeBytes: 1, Format: "flac", Score: 1,
		Files: []db.DownloadRequestFile{{
			Path: `Music\Dummy\03.flac`, Name: "03.flac", SizeBytes: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests
		SET status = 'completed', import_status = 'imported',
		    import_path = '/music', imported_at = now()
		WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (
		    download_request_id, album_id, track_id, provider, remote_path,
		    source_username, source_path, size_bytes, transferred_bytes,
		    status, local_path, library_file_id
		) VALUES (
		    $1, $2, $3, 'slskd', 'Music\Dummy\03.flac',
		    'peer', 'Music/Dummy', 1, 1, 'completed',
		    '/music/provenance.flac', $4
		)
	`, requestID, albumID, trackByProvenance, provenanceFile); err != nil {
		t.Fatal(err)
	}

	service := NewService(pool, zerolog.Nop())
	for _, fileID := range []uuid.UUID{recordingFile, isrcFile, positionFile, provenanceFile} {
		if err := service.ReconcileFile(ctx, fileID); err != nil {
			t.Fatalf("ReconcileFile(%s): %v", fileID, err)
		}
	}
	assertMapping(t, ctx, pool, recordingFile, trackByRecording, "musicbrainz_recording_id", false)
	assertMapping(t, ctx, pool, isrcFile, trackByISRC, "isrc", false)
	assertMapping(t, ctx, pool, positionFile, trackByPosition, "album_disc_track", false)
	assertMapping(t, ctx, pool, provenanceFile, trackByProvenance, "verified_download", false)
	files, err := db.New(pool).ListLibraryFiles(ctx, db.ListLibraryFilesParams{Limit: 20})
	if err != nil {
		t.Fatalf("ListLibraryFiles(): %v", err)
	}
	if len(files.Items) != 4 || files.Items[0].MatchStatus == "" {
		t.Fatalf("library files = %#v", files.Items)
	}
	filtered, err := db.New(pool).ListLibraryFiles(ctx, db.ListLibraryFilesParams{
		Limit: 1, Status: "matched", Query: "02",
	})
	if err != nil {
		t.Fatalf("filtered ListLibraryFiles(): %v", err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].ID != isrcFile {
		t.Fatalf("filtered library files = %#v, total %d", filtered.Items, filtered.Total)
	}
	searchResults, err := service.SearchTracks(ctx, positionFile, "Sour Times", 25)
	if err != nil {
		t.Fatalf("SearchTracks(): %v", err)
	}
	if len(searchResults) != 1 || searchResults[0].TrackID != trackByISRC ||
		searchResults[0].Method != "catalogue_search" {
		t.Fatalf("track search results = %#v", searchResults)
	}

	var status string
	duplicateFile := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, isrc
		) VALUES ($1, '/music/duplicate.flac', 1, now(), 'GB-AAA-94-00002')
	`, duplicateFile); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, duplicateFile); err != nil {
		t.Fatal(err)
	}
	for _, fileID := range []uuid.UUID{isrcFile, duplicateFile} {
		if err := pool.QueryRow(ctx, `
			SELECT match_status FROM library_files WHERE id = $1
		`, fileID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "ambiguous" {
			t.Fatalf("duplicate file %s status = %q", fileID, status)
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, duplicateFile); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, duplicateFile); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, isrcFile, trackByISRC, "isrc", false)

	secondAlbumID, duplicateTrackID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Live')
	`, secondAlbumID, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (
		    id, album_id, musicbrainz_recording_id, title, disc_number, track_number
		) VALUES ($1, $2, $3, 'Mysterons', 1, 1)
	`, duplicateTrackID, secondAlbumID, recordingID); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, recordingFile); err != nil {
		t.Fatal(err)
	}
	var candidateCount, mappingCount int
	if err := pool.QueryRow(ctx, `
		SELECT match_status, match_candidate_count,
		       (SELECT count(*) FROM track_mappings WHERE library_file_id = $1)
		FROM library_files WHERE id = $1
	`, recordingFile).Scan(&status, &candidateCount, &mappingCount); err != nil {
		t.Fatal(err)
	}
	// The two tracks were reached through the same recording id, so they are one
	// recording on two releases rather than two candidates disagreeing about what
	// the file is. The file answers both.
	if status != "matched" || candidateCount != 2 || mappingCount != 2 {
		t.Fatalf("same recording on two releases = status %q, candidates %d, mappings %d",
			status, candidateCount, mappingCount)
	}

	if err := service.SetManual(ctx, recordingFile, trackByRecording, false); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, recordingFile); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, recordingFile, trackByRecording, "manual", true)

	if err := service.SetManual(ctx, isrcFile, trackByPosition, false); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, isrcFile, trackByPosition, "manual", true)
	if err := pool.QueryRow(ctx, `
		SELECT match_status FROM library_files WHERE id = $1
	`, positionFile).Scan(&status); err != nil {
		t.Fatal(err)
	}
	// Displaced by a decision, so it is a duplicate rather than a question: the
	// track it answers was given to another file, permanently, and there is
	// nothing left for anybody to decide about this one. Taking it back is still
	// possible by hand, which is what the next lines do.
	if status != "duplicate" {
		t.Fatalf("displaced file status = %q", status)
	}
	if err := service.SetManual(ctx, positionFile, trackByPosition, false); !errors.Is(err, ErrManualConflict) {
		t.Fatalf("manual conflict error = %v", err)
	}
	if err := service.SetManual(ctx, positionFile, trackByPosition, true); err != nil {
		t.Fatalf("forced manual reassignment: %v", err)
	}
	assertMapping(t, ctx, pool, positionFile, trackByPosition, "manual", true)
	assertMapping(t, ctx, pool, isrcFile, trackByISRC, "isrc", false)
	if err := service.ClearManual(ctx, positionFile); err != nil {
		t.Fatalf("ClearManual(): %v", err)
	}
	assertMapping(t, ctx, pool, positionFile, trackByPosition, "album_disc_track", false)

	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, recordingFile); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, recordingFile); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, recordingFile, trackByRecording, "manual", true)
}

func assertMapping(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fileID, trackID uuid.UUID,
	method string,
	manual bool,
) {
	t.Helper()
	var gotTrackID uuid.UUID
	var gotMethod string
	var gotManual bool
	if err := pool.QueryRow(ctx, `
		SELECT track_id, method, is_manual
		FROM track_mappings
		WHERE library_file_id = $1
	`, fileID).Scan(&gotTrackID, &gotMethod, &gotManual); err != nil {
		t.Fatal(err)
	}
	if gotTrackID != trackID || gotMethod != method || gotManual != manual {
		t.Fatalf("mapping = track %s, method %q, manual %v", gotTrackID, gotMethod, gotManual)
	}
}

// Searching the catalogue for a track to map a file to builds an ILIKE
// pattern out of what the user typed, and a query is text rather than a
// pattern: % and _ have to match themselves, and so does the escape character
// that makes them. Each case here has a decoy beside it — the track an
// unescaped pattern would have offered as a candidate too.
func TestSearchTracksMatchesWildcardCharactersLiterally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, fileID := uuid.New(), uuid.New(), uuid.New()
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
	titles := []string{
		"50% Chance", "5000 Volts", "A_B Sound", "AXB Sound", `A\_B Tape`, `A\XB Sound`,
	}
	for position, title := range titles {
		if _, err := pool.Exec(ctx, `
			INSERT INTO tracks (id, album_id, title, disc_number, track_number)
			VALUES ($1, $2, $3, 1, $4)
		`, uuid.New(), albumID, title, position+1); err != nil {
			t.Fatalf("seed track %s: %v", title, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/unmatched.flac', 1, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	service := NewService(pool, zerolog.Nop())

	for _, testCase := range []struct {
		name, query, want string
	}{
		{"per cent sign", "50%", "50% Chance"},
		{"underscore", "a_b", "A_B Sound"},
		{"the escape character itself", `\_`, `A\_B Tape`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			results, err := service.SearchTracks(ctx, fileID, testCase.query, 25)
			if err != nil {
				t.Fatalf("SearchTracks(%q) error = %v", testCase.query, err)
			}
			if len(results) != 1 || results[0].Title != testCase.want {
				t.Errorf("search for %q = %v, want just %q",
					testCase.query, candidateTitles(results), testCase.want)
			}
		})
	}
}

func candidateTitles(results []Candidate) []string {
	titles := make([]string, 0, len(results))
	for _, candidate := range results {
		titles = append(titles, candidate.Title)
	}
	return titles
}

// A file the library no longer holds is the normal case for a reconcile job
// that was queued before a scan removed the row it names. There is nothing left
// to decide, and nothing went wrong.
func TestReconcilingAFileTheLibraryNoLongerHasIsNotAnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, uuid.New()); err != nil {
		t.Fatalf("ReconcileFile(a file that is not there) = %v, want no error", err)
	}
}

// A title is the one tag a cover version shares with the recording it covers,
// so a title on its own is never agreement — not even when the catalogue holds
// exactly one track bearing it.
func TestATitleAloneNeverMatchesATrack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Johnny Cash", "American IV")
	seedTrack(t, ctx, pool, trackFixture{albumID: albumID, title: "Hurt", number: ptr(int32(6))})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/nin-hurt.flac", titleTag: ptr("Hurt"),
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	assertUnmatched(t, ctx, pool, fileID)
}

// An absent tag is silence. Artist and album agreeing says which release the
// file probably came off; without a track number nothing says which track it
// is, and a release holding exactly one track is not permission to guess.
func TestAFileWithoutATrackNumberMatchesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{albumID: albumID, title: "Mysterons", number: ptr(int32(1))})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/only-one.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"), disc: ptr(int32(1)),
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	assertUnmatched(t, ctx, pool, fileID)
}

// Two releases of the same name by the same artist really do carry two
// different tracks at position three, and tag agreement cannot tell them
// apart. Nothing chooses; the question goes to the user.
func TestTwoTracksAtTheSamePositionLeaveTheFileAmbiguous(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, originalID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	reissueID := seedAlbum(t, ctx, pool, artistID, "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: originalID, title: "Strangers",
		number: ptr(int32(3)), durationMs: ptr(int32(236000)),
	})
	seedTrack(t, ctx, pool, trackFixture{
		albumID: reissueID, title: "Strangers (remaster)",
		number: ptr(int32(3)), durationMs: ptr(int32(238000)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(237000)),
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, ctx, pool, fileID, "ambiguous", 2)
	assertNoMapping(t, ctx, pool, fileID)
}

// An identifier is proof and a position is a guess about which release the
// tags describe. Where both offer an answer the identifier's is the only one
// taken, and the track the tags pointed at is left alone.
func TestARecordingIDOutranksATagAgreement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	artistID, dummyID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	liveID := seedAlbum(t, ctx, pool, artistID, "Roseland NYC Live")
	byRecording := seedTrack(t, ctx, pool, trackFixture{
		albumID: liveID, title: "Strangers", number: ptr(int32(1)), recordingID: &recordingID,
	})
	byPosition := seedTrack(t, ctx, pool, trackFixture{
		albumID: dummyID, title: "Strangers", number: ptr(int32(3)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:        "/music/strangers.flac",
		artistTag:   ptr("Portishead"),
		albumTag:    ptr("Dummy"),
		disc:        ptr(int32(1)),
		number:      ptr(int32(3)),
		recordingID: &recordingID,
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, fileID, byRecording, "musicbrainz_recording_id", false)
	assertMappingCount(t, ctx, pool, fileID, 1)
	var claimed bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM track_mappings WHERE track_id = $1)
	`, byPosition).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("the track the tags pointed at was claimed as well")
	}
}

// One file for that track: a track another file already answers is not free,
// whatever this file can prove about itself.
func TestATrackAnotherFileAlreadyAnswersIsNotTaken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)), isrc: ptr("GB-AAA-94-00002"),
	})
	holder := seedFile(t, ctx, pool, fileFixture{path: "/music/holder.flac"})
	claimant := seedFile(t, ctx, pool, fileFixture{
		path: "/music/claimant.flac", isrc: ptr("gb-aaa-94-00002"),
	})

	service := NewService(pool, zerolog.Nop())
	if err := service.SetManual(ctx, holder, trackID, false); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, claimant); err != nil {
		t.Fatal(err)
	}
	// A duplicate rather than a question. The track is held and a manual
	// decision is permanent, so nothing about this file is undecided: it is a
	// second copy of music the library already has in place.
	assertStatus(t, ctx, pool, claimant, "duplicate", 1)
	assertNoMapping(t, ctx, pool, claimant)
	assertMapping(t, ctx, pool, holder, trackID, "manual", true)
}

// The opposite case, and the reason the two are told apart: a track nobody
// holds that two files both answer is undecided, and stays a question.
func TestATrackNobodyHoldsThatTwoFilesAnswerStaysAQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)),
	})
	first := seedFile(t, ctx, pool, fileFixture{
		path: "/music/first.flac", artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(2)),
	})
	second := seedFile(t, ctx, pool, fileFixture{
		path: "/music/second.flac", artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(2)),
	})

	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, second); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, ctx, pool, first, "ambiguous", 2)
	assertStatus(t, ctx, pool, second, "ambiguous", 2)
}

// Two files that both resolve to the one MusicBrainz recording are not
// disagreeing about which track fits it; they are that recording twice. The
// question that fits is the duplicate one, not a choice between tracks.
func TestASecondFileNamingTheSameRecordingIsADuplicate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recording := uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Playboi Carti", "In Abundance")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Robber", number: ptr(int32(1)), recordingID: &recording,
	})

	first := seedFile(t, ctx, pool, fileFixture{path: "/music/In Abundance/Robber.mp3"})
	insertIdentity(t, ctx, pool, first, recording)
	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, first); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, first, trackID, "resolved_identity", false)

	second := seedFile(t, ctx, pool, fileFixture{path: "/music/Robber/Robber.mp3"})
	insertIdentity(t, ctx, pool, second, recording)
	if err := service.ReconcileFile(ctx, second); err != nil {
		t.Fatal(err)
	}

	// Two files now resolve to the recording, and the candidate count says so.
	// first is a peer of second (they share a recording) and is reconciled
	// alongside it, so it loses the claim it held alone a moment ago and
	// lands on the same answer: neither is a question about which track fits,
	// both are the one recording twice.
	assertStatus(t, ctx, pool, second, "duplicate", 2)
	assertNoMapping(t, ctx, pool, second)
	assertStatus(t, ctx, pool, first, "duplicate", 2)
	assertNoMapping(t, ctx, pool, first)
}

// The counter-case for the same fixture: MusicBrainz can enter one
// performance as two recordings it never merges, both carrying the same ISRC
// (ADR 0025). A second file sharing the ISRC tag is not proven to
// be the first file's recording the way a shared musicbrainz_recording_id
// would be, and reconcile has no sample or AcoustID cluster on hand to tell
// the two apart, so this stays a question rather than becoming a duplicate.
func TestASecondFileNamingADifferentRecordingStaysAQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recording := uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "88GLAM", "88GLAM2")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Racks", number: ptr(int32(1)),
		recordingID: &recording, isrc: ptr("USUM71819697"),
	})

	first := seedFile(t, ctx, pool, fileFixture{path: "/music/88GLAM2/Racks.mp3"})
	insertIdentity(t, ctx, pool, first, recording)
	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, first); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, first, trackID, "resolved_identity", false)

	// The second file carries the shared ISRC as a plain tag and resolves, on
	// its own audio, to a different recording entirely.
	other := uuid.New()
	second := seedFile(t, ctx, pool, fileFixture{
		path: "/music/88GLAM 2.5/Racks.mp3", isrc: ptr("usum71819697"),
	})
	insertIdentity(t, ctx, pool, second, other)
	if err := service.ReconcileFile(ctx, second); err != nil {
		t.Fatal(err)
	}

	assertStatus(t, ctx, pool, second, "ambiguous", 1)
	assertNoMapping(t, ctx, pool, second)
}

// The same fixture again, with the second file carrying no identity at all —
// only tags that happen to fit the position the first file's track sits at.
// Nothing proves it is that recording, so it stays a question.
func TestASecondFileWithNoIdentityStaysAQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recording := uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Playboi Carti", "In Abundance")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Robber", number: ptr(int32(1)), recordingID: &recording,
	})

	first := seedFile(t, ctx, pool, fileFixture{path: "/music/In Abundance/Robber.mp3"})
	insertIdentity(t, ctx, pool, first, recording)
	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, first); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, first, trackID, "resolved_identity", false)

	second := seedFile(t, ctx, pool, fileFixture{
		path: "/music/Robber/Robber.mp3", artistTag: ptr("Playboi Carti"),
		albumTag: ptr("In Abundance"), disc: ptr(int32(1)), number: ptr(int32(1)),
	})
	if err := service.ReconcileFile(ctx, second); err != nil {
		t.Fatal(err)
	}

	assertStatus(t, ctx, pool, second, "ambiguous", 1)
	assertNoMapping(t, ctx, pool, second)
}

// insertIdentity records the audio as resolved to a recording, the shape
// AcoustID resolution leaves behind on a library file.
func insertIdentity(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID, recording uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'acoustid', 'the audio names one recording')
	`, fileID, recording); err != nil {
		t.Fatalf("insert identity for %s: %v", fileID, err)
	}
}

// Being asked to take a track means being asked to take it away from something.
// The candidate names what that something is, because nobody can weigh the
// question without it.
func TestACandidateNamesTheFileHoldingTheTrack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)), isrc: ptr("GB-AAA-94-00002"),
	})
	holder := seedFile(t, ctx, pool, fileFixture{path: "/music/holder.flac"})
	claimant := seedFile(t, ctx, pool, fileFixture{
		path: "/music/claimant.flac", isrc: ptr("gb-aaa-94-00002"),
	})

	service := NewService(pool, zerolog.Nop())
	if err := service.SetManual(ctx, holder, trackID, false); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, claimant); err != nil {
		t.Fatal(err)
	}

	candidates, err := service.Candidates(ctx, claimant)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want the one contested track", len(candidates))
	}
	if candidates[0].HeldByPath != "/music/holder.flac" {
		t.Fatalf("held by = %q, want the file that holds the track", candidates[0].HeldByPath)
	}
	if candidates[0].HeldByFileID.UUID != holder {
		t.Fatalf("held by id = %v, want %v", candidates[0].HeldByFileID, holder)
	}
}

// Settling one file is a reason to look at every file that contests the same
// recording, and a manual decision is what those second looks must never
// touch.
func TestAManualDecisionSurvivesReconcilingAPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Mysterons", number: ptr(int32(1)), recordingID: &recordingID,
	})
	decided := seedFile(t, ctx, pool, fileFixture{
		path: "/music/decided.flac", recordingID: &recordingID,
	})
	peer := seedFile(t, ctx, pool, fileFixture{
		path: "/music/peer.flac", recordingID: &recordingID,
	})

	service := NewService(pool, zerolog.Nop())
	if err := service.SetManual(ctx, decided, trackID, false); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileFile(ctx, peer); err != nil {
		t.Fatal(err)
	}
	assertMapping(t, ctx, pool, decided, trackID, "manual", true)
}

// A file resolved to a recording before any release carrying it was ingested
// is exactly the file a newly ingested release should now be able to answer.
func TestReconcileAlbumMatchesAFileResolvedBeforeTheReleaseArrived(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	fileID := seedFile(t, ctx, pool, fileFixture{path: "/music/resolved.flac"})
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'acoustid', 'the audio names one recording')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	if err := db.New(pool).SetLibraryFileAside(ctx, fileID); err != nil {
		t.Fatalf("SetLibraryFileAside(): %v", err)
	}

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Mysterons", number: ptr(int32(1)), recordingID: &recordingID,
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileAlbum(ctx, albumID); err != nil {
		t.Fatalf("ReconcileAlbum(): %v", err)
	}
	assertMapping(t, ctx, pool, fileID, trackID, "resolved_identity", false)
}

// Schall writes artist, album, disc and track onto the files it manages, so on
// one of those files those four fields are its own handwriting. The length is
// not: it is measured off the audio and no tag pass moves it. With no length on
// either side there is nothing outside the tags to check them against, and a
// lone candidate that would have been taken before is put to a person instead.
func TestTagsWithNoLengthToCheckThemAreOfferedRatherThanTaken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Strangers", number: ptr(int32(3)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(236000)),
	})

	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatalf("ReconcileFile(): %v", err)
	}
	assertStatus(t, ctx, pool, fileID, "ambiguous", 1)
	assertNoMapping(t, ctx, pool, fileID)

	// Still shown, and saying which evidence put it there. A question with no
	// candidate under it is not one a person can answer.
	candidates, err := service.Candidates(ctx, fileID)
	if err != nil {
		t.Fatalf("Candidates(): %v", err)
	}
	if len(candidates) != 1 || candidates[0].Method != "album_disc_track_untimed" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

// The other direction. A length that disagrees is not weak evidence to weigh
// against four fields that agree — it is a contradiction, and a contradiction
// voids the pair. There is nothing here to ask anybody about.
func TestALengthThatDisagreesVoidsTheTagsThatAgree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Strangers",
		number: ptr(int32(3)), durationMs: ptr(int32(236000)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(301000)),
	})

	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatalf("ReconcileFile(): %v", err)
	}
	assertUnmatched(t, ctx, pool, fileID)
	candidates, err := service.Candidates(ctx, fileID)
	if err != nil {
		t.Fatalf("Candidates(): %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %v, want the pair voided", candidateTitles(candidates))
	}
}

// Five seconds is the tolerance everywhere else in the project, and it is the
// same one here: a copy that is four seconds longer than MusicBrainz says is
// the ordinary difference between two masterings, not a different piece of
// music.
func TestALengthWithinFiveSecondsStillProvesTheTags(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Strangers",
		number: ptr(int32(3)), durationMs: ptr(int32(236000)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(240900)),
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatalf("ReconcileFile(): %v", err)
	}
	assertMapping(t, ctx, pool, fileID, trackID, "album_disc_track", false)
}

// What a file has been proven to be outranks what it says it is. The audio
// named one recording; the track sitting at the position its tags point to is a
// different one. Everything a tag can say agrees, and it is refused anyway,
// because a differing recording id is a contradiction whatever else lines up.
func TestAFileProvenToBeAnotherRecordingIsNotTagMatched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Strangers", number: ptr(int32(3)),
		durationMs: ptr(int32(236000)), recordingID: ptr(uuid.New()),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(236500)),
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'acoustid', 'the audio names one recording')
	`, fileID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatalf("ReconcileFile(): %v", err)
	}
	assertUnmatched(t, ctx, pool, fileID)
	candidates, err := service.Candidates(ctx, fileID)
	if err != nil {
		t.Fatalf("Candidates(): %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %v, want the pair voided", candidateTitles(candidates))
	}
}

// A file somebody named from SoundCloud carries a person's word that it holds
// no MusicBrainz recording. The tags Adopt writes onto it — a title, the
// uploader as artist — can happen to satisfy an exact-tag combination against
// an unrelated catalogue track, and a reconcile that let that create a mapping
// would contradict the decision and have the tagging pass overwrite the
// SoundCloud tags with the catalogue's.
func TestASourceIdentifiedFileIsNeverTagMatched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Strangers", number: ptr(int32(3)),
		durationMs: ptr(int32(236000)), recordingID: ptr(uuid.New()),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(236500)),
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, source, external_id, method, is_manual, summary
		)
		VALUES ($1, 'source', 'soundcloud', 'soundcloud:1', 'source_url', true, 'named by hand')
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'source' WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}

	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatalf("ReconcileFile(): %v", err)
	}
	assertNoMapping(t, ctx, pool, fileID)
}

// A local-only identity somebody recorded is a settled answer that the file
// has no catalogue recording. Reconcile must leave it alone so the tagging
// pass cannot replace the person's tags with a catalogue match.
func TestAManualLocalOnlyFileIsNeverTagMatched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Strangers", number: ptr(int32(3)),
		durationMs: ptr(int32(236000)), recordingID: ptr(uuid.New()),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(236500)),
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, method, is_manual, summary)
		VALUES ($1, 'local_only', 'manual', true, 'recorded by hand')
	`, fileID); err != nil {
		t.Fatal(err)
	}

	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatalf("ReconcileFile(): %v", err)
	}
	assertNoMapping(t, ctx, pool, fileID)
}

// The same refusal from the file's own tag rather than from a resolution, and
// the same one for an ISRC. Both are identifiers the file carries; a track that
// carries a different one is not it.
func TestAnIdentifierOnTheFileVoidsATrackCarryingAnother(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Strangers", number: ptr(int32(3)),
		durationMs: ptr(int32(236000)), recordingID: ptr(uuid.New()),
	})
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Wandering Star", number: ptr(int32(4)),
		durationMs: ptr(int32(236000)), isrc: ptr("GB-AAA-94-00004"),
	})
	byRecording := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(236500)),
		recordingID: ptr(uuid.New()),
	})
	byISRC := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/04.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(4)), durationMs: ptr(int32(236500)),
		isrc: ptr("GB-AAA-94-09999"),
	})

	service := NewService(pool, zerolog.Nop())
	for _, fileID := range []uuid.UUID{byRecording, byISRC} {
		if err := service.ReconcileFile(ctx, fileID); err != nil {
			t.Fatalf("ReconcileFile(%s): %v", fileID, err)
		}
		assertUnmatched(t, ctx, pool, fileID)
	}
}

// The candidates are what the user is shown when nothing may be concluded, so
// an ambiguous file has to be able to say what it was torn between.
func TestCandidatesListEveryTrackAFileCouldAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, originalID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	reissueID := seedAlbum(t, ctx, pool, artistID, "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: originalID, title: "Strangers",
		number: ptr(int32(3)), durationMs: ptr(int32(236000)),
	})
	seedTrack(t, ctx, pool, trackFixture{
		albumID: reissueID, title: "Strangers (remaster)",
		number: ptr(int32(3)), durationMs: ptr(int32(238000)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path:      "/music/03.flac",
		artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(3)), durationMs: ptr(int32(237000)),
	})

	candidates, err := NewService(pool, zerolog.Nop()).Candidates(ctx, fileID)
	if err != nil {
		t.Fatalf("Candidates(): %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %v, want both tracks at that position", candidateTitles(candidates))
	}
	for _, candidate := range candidates {
		if candidate.Method != "album_disc_track" || candidate.AlreadyMapped {
			t.Fatalf("candidate %#v", candidate)
		}
	}
}

// A candidate that another file already holds by hand is still worth showing —
// it is the reason this file cannot have it — but it has to say so.
func TestCandidatesMarkATrackAnotherFileHoldsManually(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)), isrc: ptr("GB-AAA-94-00002"),
	})
	holder := seedFile(t, ctx, pool, fileFixture{path: "/music/holder.flac"})
	claimant := seedFile(t, ctx, pool, fileFixture{
		path: "/music/claimant.flac", isrc: ptr("GB-AAA-94-00002"),
	})

	service := NewService(pool, zerolog.Nop())
	if err := service.SetManual(ctx, holder, trackID, false); err != nil {
		t.Fatal(err)
	}
	candidates, err := service.Candidates(ctx, claimant)
	if err != nil {
		t.Fatalf("Candidates(): %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %v, want the one track the ISRC names", candidateTitles(candidates))
	}
	if !candidates[0].AlreadyMapped || !candidates[0].ManuallyMapped {
		t.Fatalf("candidate = %#v, want it reported as manually held elsewhere", candidates[0])
	}
}

func TestSearchTracksRefusesAFileTheLibraryDoesNotHave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := NewService(pool, zerolog.Nop()).SearchTracks(ctx, uuid.New(), "Sour Times", 25); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SearchTracks(a file that is not there) = %v, want pgx.ErrNoRows", err)
	}
}

func TestSetManualRefusesATrackTheCatalogueDoesNotHave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := seedFile(t, ctx, pool, fileFixture{path: "/music/present.flac"})
	if err := NewService(pool, zerolog.Nop()).SetManual(ctx, fileID, uuid.New(), false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SetManual(a track that is not there) = %v, want pgx.ErrNoRows", err)
	}
}

func TestClearManualRefusesAFileWithNoManualDecision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := seedFile(t, ctx, pool, fileFixture{path: "/music/present.flac"})
	if err := NewService(pool, zerolog.Nop()).ClearManual(ctx, fileID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ClearManual(a file nobody decided) = %v, want pgx.ErrNoRows", err)
	}
}

// A request that was abandoned has decided nothing, and saying so is the whole
// difference between "this file is unmatched" and "nobody looked".
func TestACancelledRequestIsReportedRatherThanAnsweredEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Mysterons", number: ptr(int32(1)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{path: "/music/present.flac"})
	service := NewService(pool, zerolog.Nop())

	cancelled, stop := context.WithCancel(ctx)
	stop()
	for _, testCase := range []struct {
		name string
		call func(context.Context) error
	}{
		{"reconcile a file", func(ctx context.Context) error {
			return service.ReconcileFile(ctx, fileID)
		}},
		{"reconcile a release", func(ctx context.Context) error {
			return service.ReconcileAlbum(ctx, albumID)
		}},
		{"list the candidates", func(ctx context.Context) error {
			_, err := service.Candidates(ctx, fileID)
			return err
		}},
		{"search the catalogue", func(ctx context.Context) error {
			_, err := service.SearchTracks(ctx, fileID, "Mysterons", 25)
			return err
		}},
		{"record a manual decision", func(ctx context.Context) error {
			return service.SetManual(ctx, fileID, trackID, false)
		}},
		{"clear a manual decision", func(ctx context.Context) error {
			return service.ClearManual(ctx, fileID)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(cancelled); !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func ptr[T any](value T) *T { return &value }

// seedRelease inserts an artist and one release of theirs.
func seedRelease(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, artist, album string,
) (artistID, albumID uuid.UUID) {
	t.Helper()
	artistID = uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, $2, $2)
	`, artistID, artist); err != nil {
		t.Fatalf("seed artist %s: %v", artist, err)
	}
	return artistID, seedAlbum(t, ctx, pool, artistID, album)
}

func seedAlbum(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, artistID uuid.UUID, title string,
) uuid.UUID {
	t.Helper()
	albumID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, $3)
	`, albumID, artistID, title); err != nil {
		t.Fatalf("seed album %s: %v", title, err)
	}
	return albumID
}

// trackFixture is one catalogue track. Every identifier is optional, because
// what these tests turn on is which of them are absent.
type trackFixture struct {
	albumID     uuid.UUID
	title       string
	disc        int32
	number      *int32
	durationMs  *int32
	recordingID *uuid.UUID
	isrc        *string
}

func seedTrack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture trackFixture) uuid.UUID {
	t.Helper()
	return seedTrackAs(t, ctx, pool, uuid.New(), fixture)
}

// seedTrackAs is seedTrack with the identifier chosen, for a test that turns on
// the order two tracks are claimed in.
func seedTrackAs(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, trackID uuid.UUID, fixture trackFixture,
) uuid.UUID {
	t.Helper()
	disc := fixture.disc
	if disc == 0 {
		disc = 1
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (
		    id, album_id, title, disc_number, track_number, duration_ms,
		    musicbrainz_recording_id, isrc
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, trackID, fixture.albumID, fixture.title, disc, fixture.number, fixture.durationMs,
		fixture.recordingID, fixture.isrc); err != nil {
		t.Fatalf("seed track %s: %v", fixture.title, err)
	}
	return trackID
}

// fileFixture is one file on disk as the scanner recorded it. An absent field
// is a NULL column: a tag the file does not carry, never a blank one.
type fileFixture struct {
	path        string
	artistTag   *string
	albumTag    *string
	titleTag    *string
	disc        *int32
	number      *int32
	durationMs  *int32
	recordingID *uuid.UUID
	isrc        *string
}

func seedFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture fileFixture) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag,
		    disc_number, track_number, duration_ms, musicbrainz_recording_id, isrc
		) VALUES ($1, $2, 1, now(), $3, $4, $5, $6, $7, $8, $9, $10)
	`, fileID, fixture.path, fixture.artistTag, fixture.albumTag, fixture.titleTag,
		fixture.disc, fixture.number, fixture.durationMs,
		fixture.recordingID, fixture.isrc); err != nil {
		t.Fatalf("seed file %s: %v", fixture.path, err)
	}
	return fileID
}

func assertStatus(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	fileID uuid.UUID, status string, candidateCount int,
) {
	t.Helper()
	var gotStatus string
	var gotCount int
	if err := pool.QueryRow(ctx, `
		SELECT match_status, match_candidate_count FROM library_files WHERE id = $1
	`, fileID).Scan(&gotStatus, &gotCount); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotCount != candidateCount {
		t.Fatalf("file = status %q with %d candidates, want %q with %d",
			gotStatus, gotCount, status, candidateCount)
	}
}

func assertMappingCount(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID uuid.UUID, want int,
) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM track_mappings WHERE library_file_id = $1
	`, fileID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("mappings = %d, want %d", got, want)
	}
}

func assertNoMapping(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID uuid.UUID) {
	t.Helper()
	assertMappingCount(t, ctx, pool, fileID, 0)
}

func assertUnmatched(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID uuid.UUID) {
	t.Helper()
	assertStatus(t, ctx, pool, fileID, "unmatched", 0)
	assertNoMapping(t, ctx, pool, fileID)
}

// seedMatchableFile is a catalogue track and a file carrying the recording
// identifier that proves it is that track: the least a reconcile needs to
// conclude anything at all.
func seedMatchableFile(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string,
) (trackID, fileID uuid.UUID) {
	t.Helper()
	recordingID := uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Nils Frahm", "Spaces")
	trackID = seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Says", number: ptr(int32(4)), recordingID: &recordingID,
	})
	fileID = seedFile(t, ctx, pool, fileFixture{path: path, recordingID: &recordingID})
	return trackID, fileID
}

// tagWritesQueued is how many writes of the catalogue's account into this file
// the queue holds, whatever became of them.
func tagWritesQueued(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID uuid.UUID) int {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'tag_file' AND payload->>'fileId' = $1::text
	`, fileID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The user's own bug: a file unmatched when the pass over the library ran kept
// the year its sender had typed, and the album it belongs to arrived in their
// music server as two albums. Matching is continuous and the pass is a button,
// so a file is asked to be described when it becomes a catalogue track's.
func TestAFileThatBecomesMatchedIsQueuedToBeDescribed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, fileID := seedMatchableFile(t, ctx, pool, "/music/frahm-says.flac")

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertStatus(t, ctx, pool, fileID, "matched", 1)
	if got := tagWritesQueued(t, ctx, pool, fileID); got != 1 {
		t.Fatalf("queued writes = %d, want the matched file described once", got)
	}
}

// A file is reconciled again by every scan that touches it and by every peer
// settled beside it. The mappings are rewritten each time and none of that is
// news, so a file that answers what it already answered is not asked about
// again — a job per reconcile would be a job per scan, per file, forever.
func TestAFileMatchedToWhatItAlreadyAnsweredIsNotQueuedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, fileID := seedMatchableFile(t, ctx, pool, "/music/frahm-says.flac")
	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	// The first write is done and out of the way, so nothing but the mappings
	// themselves can say whether the second reconcile learned anything.
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'completed', completed_at = now() WHERE kind = 'tag_file'
	`); err != nil {
		t.Fatal(err)
	}

	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	if got := tagWritesQueued(t, ctx, pool, fileID); got != 1 {
		t.Fatalf("queued writes = %d, want the second reconcile to have asked for nothing", got)
	}
}

// A file nothing could match is not described, because nothing says what it is.
func TestAFileNothingCouldMatchIsNotQueuedToBeDescribed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID := seedRelease(t, ctx, pool, "Johnny Cash", "American IV")
	seedTrack(t, ctx, pool, trackFixture{albumID: albumID, title: "Hurt", number: ptr(int32(6))})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/nin-hurt.flac", titleTag: ptr("Hurt"),
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertUnmatched(t, ctx, pool, fileID)
	if got := tagWritesQueued(t, ctx, pool, fileID); got != 0 {
		t.Fatalf("queued writes = %d, want nothing asked about a file nothing matched", got)
	}
}

// Writing the tags is a courtesy to the music server and the match is the
// answer. A queue that would not take the request costs the file its tags until
// the next pass, and never the match that was proved: a reconcile that failed
// over this would be a proof thrown away for a side effect.
func TestAMatchIsKeptWhenTheDescriptionCouldNotBeQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	trackID, fileID := seedMatchableFile(t, ctx, pool, "/music/frahm-says.flac")
	refuseTagWrites(t, ctx, pool)

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatalf("ReconcileFile with a queue that refuses the write = %v, want no error", err)
	}

	assertStatus(t, ctx, pool, fileID, "matched", 1)
	assertMapping(t, ctx, pool, fileID, trackID, "musicbrainz_recording_id", false)
}

// A file somebody matched by hand is a catalogue track's as surely as one
// matching reached on its own, and its tags are just as much the sender's.
func TestAFileMatchedByHandIsQueuedToBeDescribed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID := seedRelease(t, ctx, pool, "Johnny Cash", "American IV")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Hurt", number: ptr(int32(6)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/nin-hurt.flac", titleTag: ptr("Hurt"),
	})

	if err := NewService(pool, zerolog.Nop()).SetManual(ctx, fileID, trackID, false); err != nil {
		t.Fatal(err)
	}

	if got := tagWritesQueued(t, ctx, pool, fileID); got != 1 {
		t.Fatalf("queued writes = %d, want the file described once", got)
	}
}

// The other direction. A file Schall described is described in its own tags,
// and a music server reads nothing else — so a file whose manual match is
// withdrawn goes on telling the player it is a track nothing gives it any more.
// The job settles which way it is written; what is asked for here is that it be
// asked at all.
func TestAFileWhoseManualMatchIsWithdrawnIsQueuedToBeWrittenAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID := seedRelease(t, ctx, pool, "Johnny Cash", "American IV")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Hurt", number: ptr(int32(6)),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{path: "/music/nin-hurt.flac"})
	service := NewService(pool, zerolog.Nop())
	if err := service.SetManual(ctx, fileID, trackID, false); err != nil {
		t.Fatal(err)
	}
	// The write the decision asked for is done and out of the way, so the
	// second request is the one this test is about.
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'completed', completed_at = now() WHERE kind = 'tag_file'
	`); err != nil {
		t.Fatal(err)
	}

	if err := service.ClearManual(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertUnmatched(t, ctx, pool, fileID)
	if got := tagWritesQueued(t, ctx, pool, fileID); got != 2 {
		t.Fatalf("queued writes = %d, want the withdrawn decision asked about once more", got)
	}
}

// The same for the file the track was taken from. Somebody decides by hand that
// another file is that track, and the file that held it keeps the account
// Schall wrote into it while holding nothing.
func TestAFileWhoseTrackIsGivenAwayIsQueuedToBeWrittenAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	trackID, heldFileID := seedMatchableFile(t, ctx, pool, "/music/frahm-says.flac")
	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, heldFileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'completed', completed_at = now() WHERE kind = 'tag_file'
	`); err != nil {
		t.Fatal(err)
	}
	chosenFileID := seedFile(t, ctx, pool, fileFixture{path: "/music/frahm-says-2.flac"})

	if err := service.SetManual(ctx, chosenFileID, trackID, false); err != nil {
		t.Fatal(err)
	}

	assertNoMapping(t, ctx, pool, heldFileID)
	if got := tagWritesQueued(t, ctx, pool, heldFileID); got != 2 {
		t.Fatalf("queued writes = %d, want the file that lost the track asked about once more", got)
	}
}

// And where nobody decided anything: a second copy of the same recording turns
// a settled match into a question for the user, and the file that was matched
// holds tags saying it is a track the catalogue no longer gives it.
func TestAFileThatStopsHoldingItsTrackIsQueuedToBeWrittenAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID := seedRelease(t, ctx, pool, "Nils Frahm", "Spaces")
	recordingID := uuid.New()
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Says", number: ptr(int32(4)), recordingID: &recordingID,
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/frahm-says.flac", recordingID: &recordingID,
	})
	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'completed', completed_at = now() WHERE kind = 'tag_file'
	`); err != nil {
		t.Fatal(err)
	}
	seedFile(t, ctx, pool, fileFixture{
		path: "/music/frahm-says-2.flac", recordingID: &recordingID,
	})

	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertNoMapping(t, ctx, pool, fileID)
	if got := tagWritesQueued(t, ctx, pool, fileID); got != 2 {
		t.Fatalf("queued writes = %d, want the file that lost its track asked about once more", got)
	}
}

// A write already under way read the file's mappings before they changed, so
// it is answering the question as it stood. Held back against it, the request
// to give the file its tags back would be lost for good: the pass over the
// library covers the files that are mapped to a track, and this one is not one
// of them any more.
func TestARequestArrivingWhileAWriteIsUnderWayIsKept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID := seedRelease(t, ctx, pool, "Nils Frahm", "Spaces")
	recordingID := uuid.New()
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Says", number: ptr(int32(4)), recordingID: &recordingID,
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/frahm-says.flac", recordingID: &recordingID,
	})
	service := NewService(pool, zerolog.Nop())
	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}
	// The write of the match is being worked at the moment the match is undone.
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running', started_at = now() WHERE kind = 'tag_file'
	`); err != nil {
		t.Fatal(err)
	}
	seedFile(t, ctx, pool, fileFixture{
		path: "/music/frahm-says-2.flac", recordingID: &recordingID,
	})

	if err := service.ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertNoMapping(t, ctx, pool, fileID)
	if got := tagWritesQueued(t, ctx, pool, fileID); got != 2 {
		t.Fatalf("queued writes = %d, want the request kept beside the one being worked", got)
	}
}

// refuseTagWrites makes the queue refuse every request to describe a file, the
// way a database with no room left or a lock nobody let go of would.
func refuseTagWrites(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION refuse_tag_writes() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			IF new.kind = 'tag_file' THEN
				RAISE EXCEPTION 'the queue would not take it';
			END IF;
			RETURN new;
		END $$
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER refuse_tag_writes BEFORE INSERT ON jobs
		FOR EACH ROW EXECUTE FUNCTION refuse_tag_writes()
	`); err != nil {
		t.Fatal(err)
	}
	// The suite empties tables between tests and leaves everything else standing,
	// so a trigger this test hung on the queue would refuse every later one too.
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `
			DROP TRIGGER IF EXISTS refuse_tag_writes ON jobs
		`); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(context.Background(), `
			DROP FUNCTION IF EXISTS refuse_tag_writes()
		`); err != nil {
			t.Error(err)
		}
	})
}

// ascending returns three identifiers in the order PostgreSQL sorts them. A
// group is claimed in track order, so a test that turns on which track is
// reached first has to say which is which.
func ascending(t *testing.T) (first, second, third uuid.UUID) {
	t.Helper()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	sort.Slice(ids, func(left, right int) bool {
		return bytes.Compare(ids[left][:], ids[right][:]) < 0
	})
	return ids[0], ids[1], ids[2]
}

// waitForABlockedStatement returns once some connection is waiting on a lock.
// It is how this test knows the reconcile under way has taken the first track
// of its group and is now stopped at the second.
func waitForABlockedStatement(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND state = 'active'
		`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no statement ever blocked, so the race this test needs did not happen")
}

// One recording legitimately appears on more than one release — an album and a
// compilation — so a file proven to be that recording answers every track of
// the group together. The group is claimed all at once or not at all: a file
// that loses the race for one of its tracks is a file somebody else is already
// answering, and half an answer is not one.
//
// The half that did land used to stay written. Each mapping is inserted with
// ON CONFLICT DO NOTHING, so a track another file has already taken simply
// inserts nothing — and the file was then called ambiguous while keeping the
// mapping it had won. The release counted that track as owned on the strength
// of a decision the same pass had called not-an-answer.
//
// The race is played out rather than hoped for. Another file's claim on the
// second track of the group is written and held open, so the reconcile takes
// the first track, stops at the second, and finds it gone the moment the other
// claim lands.
func TestAFileThatLosesTheRaceForOneTrackHoldsNoneOfTheGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recording := uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	first, second, _ := ascending(t)
	seedTrackAs(t, ctx, pool, first, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)), recordingID: &recording,
	})
	seedTrackAs(t, ctx, pool, second, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(4)), recordingID: &recording,
	})

	claimant := seedFile(t, ctx, pool, fileFixture{path: "/music/claimant.flac", recordingID: &recording})
	// The other file. It carries no identifier, so it is not a candidate for
	// anything and does not change what the claimant's group looks like.
	rival := seedFile(t, ctx, pool, fileFixture{path: "/music/rival.flac"})

	// The rival's claim on the second track, written and left uncommitted. It
	// is invisible to the reconcile that is about to read what is available,
	// and in its way the moment that reconcile reaches the same track.
	rivalTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rivalTx.Rollback(ctx) }()
	if _, err := rivalTx.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence, is_manual)
		VALUES ($1, $2, 'musicbrainz_recording_id', 1.0, false)
	`, second, rival); err != nil {
		t.Fatal(err)
	}

	service := NewService(pool, zerolog.Nop())
	reconciled := make(chan error, 1)
	go func() { reconciled <- service.ReconcileFile(ctx, claimant) }()

	waitForABlockedStatement(t, ctx, pool)
	if err := rivalTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-reconciled; err != nil {
		t.Fatal(err)
	}

	assertStatus(t, ctx, pool, claimant, "ambiguous", 2)
	assertMappingCount(t, ctx, pool, claimant, 0)
	// And the file that won its track keeps it.
	assertMappingCount(t, ctx, pool, rival, 1)
}

// mergeRecording records what MusicBrainz says when it is asked which recording
// a retired identifier names now. Only the identity path writes these in
// production; a test writes one directly, because what is under test is the two
// comparisons that read it.
func mergeRecording(t *testing.T, ctx context.Context, pool *pgxpool.Pool, retired, surviving uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO recording_aliases (retired_id, surviving_id) VALUES ($1, $2)
	`, retired, surviving); err != nil {
		t.Fatalf("record merge: %v", err)
	}
}

// MusicBrainz merges recordings. A file ripped and tagged in 2019 carries the
// identifier it was given then; a catalogue refreshed after the merge holds the
// surviving one. Read as two strings that is nothing at all — the strongest
// evidence Schall has, silently absent, and a question put to a person that
// MusicBrainz had already answered.
func TestAFileTaggedBeforeAMergeMatchesTheSurvivingTrack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	retired, surviving := uuid.New(), uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)), recordingID: &surviving,
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/sour times.flac", recordingID: &retired,
	})
	mergeRecording(t, ctx, pool, retired, surviving)

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertStatus(t, ctx, pool, fileID, "matched", 1)
	assertMapping(t, ctx, pool, fileID, trackID, "musicbrainz_recording_id", false)
}

// The merge can leave the retired identifier on either side: a catalogue
// refreshed before the merge holds it while a freshly tagged file holds the
// survivor. Both are the same recording and both have to meet.
func TestATrackHoldingTheRetiredIdentifierMatchesToo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	retired, surviving := uuid.New(), uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)), recordingID: &retired,
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/sour times.flac", recordingID: &surviving,
	})
	mergeRecording(t, ctx, pool, retired, surviving)

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertMapping(t, ctx, pool, fileID, trackID, "musicbrainz_recording_id", false)
}

// This is what makes the rest of it safe. An identifier MusicBrainz has not
// tied to this recording is still a different recording, it still contradicts,
// and a contradiction still voids the pair whatever else agrees.
func TestAGenuinelyDifferentRecordingStillFailsToMeet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)), recordingID: ptr(uuid.New()),
	})
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/something else.flac", recordingID: ptr(uuid.New()),
	})

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertStatus(t, ctx, pool, fileID, "unmatched", 0)
	assertNoMapping(t, ctx, pool, fileID)
}

// The tag arm reads the file's known recording against the track's, and a
// difference there voids the pair whatever the tags say. A merge is not a
// difference, so tags that agree still conclude — and this is the half that
// would have gone on refusing if only the agreement had been widened.
func TestTagsStillConcludeWhenTheIdentifiersDifferOnlyByAMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	retired, surviving := uuid.New(), uuid.New()
	_, albumID := seedRelease(t, ctx, pool, "Portishead", "Dummy")
	// The track carries the surviving identifier and a length, so the tag arm
	// has something to check the tags against.
	trackID := seedTrack(t, ctx, pool, trackFixture{
		albumID: albumID, title: "Sour Times", number: ptr(int32(2)),
		durationMs: ptr(int32(254000)), recordingID: &surviving,
	})
	// The file's own tags name the release, and its identity — proven earlier —
	// is the identifier the merge retired.
	fileID := seedFile(t, ctx, pool, fileFixture{
		path: "/music/02 sour times.flac", artistTag: ptr("Portishead"), albumTag: ptr("Dummy"),
		disc: ptr(int32(1)), number: ptr(int32(2)), durationMs: ptr(int32(254000)),
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, is_manual, summary
		)
		VALUES ($1, 'external', $2, 'musicbrainz', false, 'proven earlier')
	`, fileID, retired); err != nil {
		t.Fatal(err)
	}
	mergeRecording(t, ctx, pool, retired, surviving)

	if err := NewService(pool, zerolog.Nop()).ReconcileFile(ctx, fileID); err != nil {
		t.Fatal(err)
	}

	assertMapping(t, ctx, pool, fileID, trackID, "resolved_identity", false)
}
