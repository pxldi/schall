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

// fetchedForAWant records one want, one request serving it, and one completed
// transfer — the state the world is in when a candidate has arrived and nobody
// has looked at it yet.
func fetchedForAWant(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID, uuid.UUID) {
	t.Helper()
	targetID, recordingID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at, attempts
		)
		VALUES ($1, 'playlist', 'Anetha', 'Candy from Strangers', $2,
		        'pending', 'isrc', now(), now(), 2)
	`, targetID, recordingID); err != nil {
		t.Fatalf("seed want: %v", err)
	}

	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Anetha', 1, 1, 9000000, 'flac', 0.77,
		        $3::jsonb)
	`, requestID, targetID, `[{"path":"Music\\Anetha\\03.flac","name":"03.flac","sizeBytes":9000000}]`,
	); err != nil {
		t.Fatalf("record the request: %v", err)
	}

	queries := New(pool)
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatalf("start the request: %v", err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{{
		Path: `Music\Anetha\03.flac`, State: "completed",
		SizeBytes: 9000000, TransferredBytes: 9000000,
	}}); err != nil {
		t.Fatalf("settle the transfer: %v", err)
	}
	return queries, targetID, requestID
}

// A want may also carry an anchor: the short sample its recording's distributor
// publishes, fetched once when the want was created. The copy is measured
// against it while it is being judged, so the claim has to bring it along —
// looking it up separately would be a second round trip for a fact the claim
// already had in its hands.
func TestAFetchedCandidateIsClaimedWithTheSampleItsWantIsAnchoredTo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAnchor(ctx, AnchorParams{
		TargetID: targetID, Fingerprint: "AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC",
		Source: "deezer", Reference: "2178654", Seconds: 30,
	}); err != nil {
		t.Fatalf("record the anchor: %v", err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.AnchorFingerprint != "AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC" {
		t.Errorf("fingerprint = %q, want the want's stored sample", claimed.AnchorFingerprint)
	}
	if claimed.AnchorSource != "deezer" || claimed.AnchorReference != "2178654" ||
		claimed.AnchorSeconds != 30 {
		t.Errorf("claimed = %#v, want the sample's provenance with it", claimed)
	}
}

// A want with no anchor claims empty rather than failing. Most music has no
// sample published for it, and a copy for such a want is judged exactly as it
// was before anchors existed.
func TestAFetchedCandidateForAWantWithNoSampleClaimsWithoutOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, requestID := fetchedForAWant(ctx, t, pool)

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.AnchorFingerprint != "" || claimed.AnchorSeconds != 0 {
		t.Errorf("claimed = %#v, want no sample", claimed)
	}
}

// A fetched candidate is claimed together with the recording it was fetched for.
// Nothing else can say what the file was supposed to be, and it is never guessed
// at from the file itself.
func TestAFetchedCandidateIsClaimedWithTheRecordingItWasFetchedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	serves, err := queries.DownloadRequestServesWant(ctx, requestID)
	if err != nil || !serves {
		t.Fatalf("serves = %v (%v), want a request that serves a want", serves, err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.AcquisitionTargetID != targetID || claimed.MusicBrainzRecordingID == uuid.Nil {
		t.Fatalf("claimed = %#v", claimed)
	}
	if claimed.File.Name != "03.flac" || claimed.EntryTitle != "Candy from Strangers" {
		t.Fatalf("claimed file = %#v", claimed.File)
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "validating" {
		t.Errorf("import status = %q, want the claim to have taken it", stored.ImportStatus)
	}
}

// A candidate is claimed with the release its want was resolved through, taken
// from the catalogue the same way a whole release's import takes it: the album
// held for that release group, and the edition chosen for that album. Without
// it the file reaches the user's music server as an album nobody has heard of.
func TestAFetchedCandidateIsClaimedWithTheReleaseItsWantWasResolvedThrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	groupID, releaseID := uuid.New(), uuid.New()
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Anetha', 'Anetha')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3, 'Mata Hari')
	`, albumID, artistID, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_editions (
			album_id, musicbrainz_release_id, title, release_date, selection_reason
		)
		VALUES ($1, $2, 'Mata Hari', '2019-11-15', 'Only release')
	`, albumID, releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET musicbrainz_release_group_id = $2, entry_album = 'mata hari'
		WHERE id = $1
	`, targetID, groupID); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.AlbumTitle != "Mata Hari" || claimed.ReleaseDate != "2019-11-15" {
		t.Fatalf("claimed release = %#v", claimed)
	}
	// Who that album is by, which is the folder the copy is filed under. It is
	// the album's own artist and never the credit on the track, so a guest
	// appearance does not open a second folder for the album.
	if claimed.AlbumArtist != "Anetha" {
		t.Fatalf("the artist the album is filed under = %q", claimed.AlbumArtist)
	}
	if !claimed.ReleaseID.Valid || claimed.ReleaseID.UUID != releaseID {
		t.Fatalf("claimed release identifier = %#v", claimed.ReleaseID)
	}
	if claimed.EntryAlbum != "mata hari" {
		t.Fatalf("what the entry called the album = %q", claimed.EntryAlbum)
	}
}

// A want whose release group the catalogue has never heard of is claimed with
// no release at all. Nothing here knows what album the track is on, and an
// empty answer is the answer — the entry's own account of it is all that is
// left, and it is kept as the entry's rather than dressed up as the
// catalogue's.
func TestAFetchedCandidateForAnUncataloguedReleaseIsClaimedWithoutOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET musicbrainz_release_group_id = $2
		WHERE id = $1
	`, targetID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.AlbumTitle != "" || claimed.ReleaseDate != "" || claimed.ReleaseID.Valid {
		t.Fatalf("a release was found for a want that has none: %#v", claimed)
	}
	if claimed.AlbumArtist != "" {
		t.Fatalf("an artist was found for a want with no album: %q", claimed.AlbumArtist)
	}
}

// A candidate is claimed with the position the catalogued album gives the
// recording it was fetched for — the track row that album holds for it, which
// is the same row a whole release's import would have numbered the file from.
// Without it the file arrives unnumbered beside its numbered siblings, and a
// music server files it as a disc of its own.
func TestAFetchedCandidateIsClaimedWithItsPositionOnTheCataloguedAlbum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	albumID := catalogueAlbumForTheWant(ctx, t, pool, targetID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		SELECT $1, musicbrainz_recording_id, 'Candy from Strangers', 2, 7
		FROM acquisition_targets WHERE id = $2
	`, albumID, targetID); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.DiscNumber != 2 || claimed.TrackNumber != 7 {
		t.Fatalf("the position claimed was disc %d track %d", claimed.DiscNumber, claimed.TrackNumber)
	}
}

// A want whose recording is on no track of the catalogued album is claimed
// without a position. The album is known and the place on it is not, and a file
// numbered from a track nobody matched would be numbered from nothing.
func TestAFetchedCandidateForARecordingNotOnTheAlbumIsClaimedWithoutAPosition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	albumID := catalogueAlbumForTheWant(ctx, t, pool, targetID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, 'Someone Else Entirely', 1, 3)
	`, albumID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.AlbumTitle != "Mata Hari" {
		t.Fatalf("the album claimed was %q", claimed.AlbumTitle)
	}
	if claimed.DiscNumber != 0 || claimed.TrackNumber != 0 {
		t.Fatalf("a position was found where the album has no track: %#v", claimed)
	}
}

// An album that carries the same recording twice names two positions for it,
// and two positions are not one. Neither is claimed, because whichever row came
// back first would be an answer nothing here chose.
func TestAFetchedCandidateForARecordingTheAlbumCarriesTwiceIsClaimedWithoutAPosition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	albumID := catalogueAlbumForTheWant(ctx, t, pool, targetID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		SELECT $1, musicbrainz_recording_id, 'Candy from Strangers', 1, position
		FROM acquisition_targets, generate_series(3, 4) AS position
		WHERE id = $2
	`, albumID, targetID); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}
	if claimed.DiscNumber != 0 || claimed.TrackNumber != 0 {
		t.Fatalf("one of two positions was picked: %#v", claimed)
	}
}

// A candidate is claimed with the credit the catalogued album gives the track
// it was fetched for, and with the album's own credit beside it — the same two
// lists a whole release's import would have written into the same file.
func TestAFetchedCandidateIsClaimedWithTheCreditItsTrackCarries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	albumID := catalogueAlbumForTheWant(ctx, t, pool, targetID)
	var trackID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		SELECT $1, musicbrainz_recording_id, 'Candy from Strangers', 2, 7
		FROM acquisition_targets WHERE id = $2
		RETURNING id
	`, albumID, targetID).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	anetha, mandar := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_artist_credits (
			track_id, position, credited_name, musicbrainz_artist_id, join_phrase
		)
		VALUES ($1, 1, 'Anetha', $2, ' & '), ($1, 2, 'Mandar', $3, '')
	`, trackID, anetha, mandar); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO album_artist_credits (album_id, position, credited_name, musicbrainz_artist_id)
		VALUES ($1, 1, 'Anetha', $2)
	`, albumID, anetha); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}

	if len(claimed.TrackCredits) != 2 {
		t.Fatalf("the track was claimed as credited to %#v", claimed.TrackCredits)
	}
	if claimed.TrackCredits[0].Name != "Anetha" || claimed.TrackCredits[0].ArtistID != anetha {
		t.Fatalf("the first artist credited reads %#v", claimed.TrackCredits[0])
	}
	if claimed.TrackCredits[1].Name != "Mandar" || claimed.TrackCredits[1].ArtistID != mandar {
		t.Fatalf("the second artist credited reads %#v", claimed.TrackCredits[1])
	}
	if len(claimed.AlbumCredits) != 1 || claimed.AlbumCredits[0].Name != "Anetha" {
		t.Fatalf("the album was claimed as credited to %#v", claimed.AlbumCredits)
	}
}

// An album that carries the same recording twice credits it twice, and two
// credits are not one credit. Neither is claimed, for the reason the position is
// not: whichever row came back first would be an answer nothing here chose.
func TestAFetchedCandidateForARecordingTheAlbumCarriesTwiceIsClaimedWithoutACredit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	albumID := catalogueAlbumForTheWant(ctx, t, pool, targetID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		SELECT $1, musicbrainz_recording_id, 'Candy from Strangers', 1, position
		FROM acquisition_targets, generate_series(3, 4) AS position
		WHERE id = $2
	`, albumID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_artist_credits (track_id, position, credited_name, musicbrainz_artist_id)
		SELECT tracks.id, 1, 'Anetha', $2 FROM tracks WHERE tracks.album_id = $1
	`, albumID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}

	if len(claimed.TrackCredits) != 0 {
		t.Fatalf("one of two credits was picked: %#v", claimed.TrackCredits)
	}
}

// A want whose release group the catalogue has never heard of is claimed with no
// credit at all. There is no release here to be credited on, and the artist the
// playlist typed is the playlist's word rather than a list of MusicBrainz
// artists.
func TestAFetchedCandidateForAnUncataloguedReleaseIsClaimedWithoutACredit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_release_group_id = $2 WHERE id = $1
	`, targetID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimTargetImport() error = %v", err)
	}

	if len(claimed.TrackCredits) != 0 || len(claimed.AlbumCredits) != 0 {
		t.Fatalf("a credit was found for a want with no catalogued release: %#v", claimed)
	}
}

// catalogueAlbumForTheWant gives the want a release group the catalogue answers
// for, with an edition chosen for it — the state a want resolved through a
// catalogued release is in.
func catalogueAlbumForTheWant(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) uuid.UUID {
	t.Helper()
	groupID, artistID, albumID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Anetha', 'Anetha')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3, 'Mata Hari')
	`, albumID, artistID, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_editions (
			album_id, musicbrainz_release_id, title, release_date, selection_reason
		)
		VALUES ($1, $2, 'Mata Hari', '2019-11-15', 'Only release')
	`, albumID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_release_group_id = $2 WHERE id = $1
	`, targetID, groupID); err != nil {
		t.Fatal(err)
	}
	return albumID
}

// A candidate the audio refused closes its own request and puts the want straight
// back in the queue — without counting an attempt. attempts is how many times
// Schall went looking, and working through what one search offered is one
// attempt however many copies it holds.
func TestARefusedCandidateKeepsTheWantLookingWithoutSpendingAnAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	nextAttempt := time.Now().Add(time.Second).UTC().Truncate(time.Millisecond)
	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
			Verdict:           AcquiredFileDiscardedAudio,
			Summary:           "The audio is a different recording.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus:  "discarded",
		ImportError:   "The audio is a different recording.",
		Evidence:      &AcquiredFileEvidence{Name: "03.flac", Agrees: []string{}, Differs: []string{"audio"}},
		TargetSummary: "Not in your library yet. The next copy will be tried.",
		NextAttemptAt: nextAttempt,
		Outcome:       "rejected",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Verdict != AcquiredFileDiscardedAudio {
		t.Fatalf("offers = %+v, want the refusal remembered", offers)
	}
	if offers[0].DecidedBy != "schall" || offers[0].Evidence == nil {
		t.Fatalf("offer = %+v, want it to say who decided and why", offers[0])
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "discarded" {
		t.Errorf("import status = %q, want the request closed", stored.ImportStatus)
	}

	var (
		status   string
		attempts int32
		next     time.Time
		recorded int
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts, next_attempt_at,
		       (SELECT count(*) FROM acquisition_target_attempts
		        WHERE acquisition_target_id = $1 AND outcome = 'rejected')
		FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&status, &attempts, &next, &recorded); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 2 {
		t.Errorf("target = %s attempts = %d, want it still pending on its own attempt",
			status, attempts)
	}
	if !next.UTC().Truncate(time.Millisecond).Equal(nextAttempt) {
		t.Errorf("next attempt = %s, want %s", next, nextAttempt)
	}
	if recorded != 1 {
		t.Errorf("attempt rows = %d, want what this candidate came to recorded once", recorded)
	}
}

// MusicBrainz deleting or merging away the recording a want names is not this
// copy's fault, and fetching another copy would only fail at the same step. So
// the copy is discarded exactly as a refusal is, and the want goes back to
// 'unresolved' with its recording cleared rather than back into the search.
func TestAGoneRecordingSendsTheWantBackToResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}

	nextAttempt := time.Now().Add(time.Second).UTC().Truncate(time.Millisecond)
	if err := queries.SettleAcquiredFileForGoneRecording(ctx, RecordingGoneParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
			Verdict:           AcquiredFileUndelivered,
			Summary:           "MusicBrainz no longer has the recording this entry named.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		RecordingID:   claimed.MusicBrainzRecordingID,
		ImportError:   "MusicBrainz no longer has the recording this entry named.",
		Summary:       "It will be resolved again.",
		ManualSummary: "You resolved this entry by hand, but MusicBrainz no longer has it.",
		NextAttemptAt: nextAttempt,
	}); err != nil {
		t.Fatalf("SettleAcquiredFileForGoneRecording() error = %v", err)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Verdict != AcquiredFileUndelivered {
		t.Fatalf("offers = %+v, want the copy discarded but left retriable", offers)
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "discarded" {
		t.Errorf("import status = %q, want the request closed", stored.ImportStatus)
	}

	var (
		status    string
		recording uuid.NullUUID
		method    pgtype.Text
		summary   string
		next      time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, musicbrainz_recording_id, resolution_method, summary, next_attempt_at
		FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&status, &recording, &method, &summary, &next); err != nil {
		t.Fatal(err)
	}
	if status != "unresolved" {
		t.Errorf("status = %q, want the want unresolved again", status)
	}
	if recording.Valid {
		t.Errorf("recording = %s, want it cleared", recording.UUID)
	}
	if method.Valid {
		t.Errorf("resolution method = %q, want it cleared", method.String)
	}
	if summary != "It will be resolved again." {
		t.Errorf("summary = %q, want the resolver's own summary", summary)
	}
	if !next.UTC().Truncate(time.Millisecond).Equal(nextAttempt) {
		t.Errorf("next attempt = %s, want %s, due for the resolver's next pass", next, nextAttempt)
	}

	var recordedRejection int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_target_rejected_recordings WHERE acquisition_target_id = $1
	`, targetID).Scan(&recordedRejection); err != nil {
		t.Fatal(err)
	}
	if recordedRejection != 0 {
		t.Error("a recording MusicBrainz took away was recorded as one a person rejected")
	}
}

// A want resolved by a person's own hand loses its recording exactly the same
// way: their choice was not wrong, MusicBrainz stopped carrying it. It is sent
// back with a summary that says so rather than the resolver's ordinary one.
func TestAGoneRecordingReopensAManuallyResolvedWantWithItsOwnSummary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET resolution_method = 'manual' WHERE id = $1
	`, targetID); err != nil {
		t.Fatalf("mark the want resolved by hand: %v", err)
	}
	claimed, err := queries.ClaimTargetImport(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}

	if err := queries.SettleAcquiredFileForGoneRecording(ctx, RecordingGoneParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
			Verdict:           AcquiredFileUndelivered,
			Summary:           "MusicBrainz no longer has the recording this entry named.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		RecordingID:   claimed.MusicBrainzRecordingID,
		ImportError:   "MusicBrainz no longer has the recording this entry named.",
		Summary:       "It will be resolved again.",
		ManualSummary: "You resolved this entry by hand, but MusicBrainz no longer has it.",
		NextAttemptAt: time.Now(),
	}); err != nil {
		t.Fatalf("SettleAcquiredFileForGoneRecording() error = %v", err)
	}

	var summary string
	if err := pool.QueryRow(ctx, `
		SELECT summary FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != "You resolved this entry by hand, but MusicBrainz no longer has it." {
		t.Errorf("summary = %q, want the one that says a person chose this recording by hand",
			summary)
	}
}

// A verdict somebody recorded stands. The loop may not overwrite it, exactly as a
// manual mapping is never replaced by an automatic one.
func TestAPersonsVerdictSurvivesALaterMachineOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := fetchedForAWant(ctx, t, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, verdict, decided_by, summary
		)
		VALUES ($1, 'slskd', 'peer', $2, '03.flac', 'discarded_audio', 'user',
		        'I listened; this is the wrong take.')
	`, targetID, `Music\Anetha\03.flac`); err != nil {
		t.Fatal(err)
	}

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		Verdict: AcquiredFileHeld, Summary: "Nothing could decide.",
	}); err != nil {
		t.Fatalf("RecordAcquiredFile() error = %v", err)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].DecidedBy != "user" ||
		offers[0].Verdict != AcquiredFileDiscardedAudio {
		t.Fatalf("offers = %+v, want the person's verdict kept", offers)
	}
}

// The import and the verdict that admitted the file are one transaction. A crash
// between them would leave music in the library that no want knows about, and the
// want would go looking for another copy of what it already has.
func TestAnAcceptedCandidateIsImportedWithItsVerdict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	if err := queries.CompleteTargetImport(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict: AcquiredFileAccepted, Summary: "Proven by its audio.",
		Evidence: &AcquiredFileEvidence{
			Name: "03.flac", Method: "fingerprint", Confidence: 1,
			Agrees: []string{"audio"}, Differs: []string{}, Problems: []string{},
		},
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}, "It is being taken into your library.",
		"/music", "/music/Anetha/Don't Rush to Grow Up", map[string]ImportedDownloadFile{
			`Music\Anetha\03.flac`: {LocalPath: "/music/Anetha/Don't Rush to Grow Up/03.flac"},
		}); err != nil {
		t.Fatalf("CompleteTargetImport() error = %v", err)
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "imported" || !stored.ImportedAt.Valid {
		t.Fatalf("stored = %#v, want the request imported", stored)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Verdict != AcquiredFileAccepted {
		t.Fatalf("offers = %+v, want the acceptance recorded", offers)
	}
	if offers[0].Evidence == nil || offers[0].Evidence.Method != "fingerprint" {
		t.Fatalf("evidence = %+v, want what proved it kept for the library file",
			offers[0].Evidence)
	}
	// The library row does not exist yet: the scan this queued creates it, and
	// completing the offer is what settles the want.
	if offers[0].LibraryFileID.Valid {
		t.Error("an accepted offer cannot name a library file before the scan has run")
	}
	var scans int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'scan_library' AND status = 'queued'
	`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Errorf("queued scans = %d, want one", scans)
	}
}

// A want stands down for half an hour when a transfer starts — the time it is
// prepared to hear nothing for. Hearing something is what has just happened, so
// the import brings it back to now. Left stood down, a want holding its music
// would describe itself as still waiting for a copy until that half hour ran out.
func TestImportingAProvenCopyBringsTheWantBackToNow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := queries.RescheduleAcquisitionTarget(
		ctx, targetID, "A copy is on its way.", time.Now().Add(30*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	before, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}

	if err := queries.CompleteTargetImport(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict: AcquiredFileAccepted, Summary: "Proven by its audio.",
		Evidence: &AcquiredFileEvidence{
			Name: "03.flac", Agrees: []string{"audio"}, Differs: []string{}, Problems: []string{},
		},
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}, "It is being taken into your library.",
		"/music", "/music/Anetha/Don't Rush to Grow Up", map[string]ImportedDownloadFile{
			`Music\Anetha\03.flac`: {LocalPath: "/music/Anetha/Don't Rush to Grow Up/03.flac"},
		}); err != nil {
		t.Fatalf("CompleteTargetImport() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if !target.NextAttemptAt.Valid || target.NextAttemptAt.Time.After(time.Now().Add(time.Minute)) {
		t.Errorf("next attempt = %v, want the want due now that its copy has landed",
			target.NextAttemptAt.Time)
	}
	if target.Attempts != before.Attempts {
		t.Errorf("attempts = %d, want the %d it had: an import attempts nothing",
			target.Attempts, before.Attempts)
	}
}

// The sentence a want wears once MusicBrainz has had no recording for it. It is
// written by internal/acquisition, which this package cannot import, so the tests
// spell it out — and a copy edit there that forgot this reader would show up as
// the unfound half of the queue quietly emptying.
const unfoundSummary = "No recording MusicBrainz knows of matches this entry yet. " +
	"It will be asked about again."

// The queue exists to be read: a copy nothing could decide about is a question
// waiting for somebody, and until it can be listed it is a question nobody can
// see. This is the reader that makes held copies reachable at all.
func TestTheReviewQueueOffersTheCopiesNothingCouldDecideAbout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict:           AcquiredFileHeld,
		Summary:           "Nothing could identify the audio.",
		Evidence:          &AcquiredFileEvidence{Name: "03.flac", Agrees: []string{}, Differs: []string{}},
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatalf("hold the copy: %v", err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("queue = %d items of %d, want the one want waiting", len(page.Items), page.Total)
	}
	item := page.Items[0]
	if item.Target.ID != targetID {
		t.Fatalf("target = %s, want %s", item.Target.ID, targetID)
	}
	if len(item.Files) != 1 || item.Files[0].Verdict != AcquiredFileHeld {
		t.Fatalf("copies = %+v, want the held one", item.Files)
	}
	if item.Files[0].Evidence == nil {
		t.Fatal("a copy offered for a decision arrives without the evidence to decide on")
	}
}

// A copy the loop refused on the audio was answered, not asked. Offering it for
// review would be asking somebody to overturn a proof, which is the one thing
// this queue must never invite.
func TestTheReviewQueuePassesOverCopiesAlreadyRuledOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict:           AcquiredFileDiscardedAudio,
		Summary:           "The audio is a different recording.",
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatalf("refuse the copy: %v", err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("queue = %+v, want nothing waiting on a person", page.Items)
	}
}

// Accepting is a decision, and the import is what acts on it. The decision is
// written first on purpose: a crash before the import leaves something to be
// honoured rather than a file nobody decided about.
func TestAcceptingAHeldCopyRecordsTheDecisionAndReopensTheImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)

	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(ctx, copies[0].ID, "you said it is the one"); err != nil {
		t.Fatalf("AcceptHeldCopy() error = %v", err)
	}

	after, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Verdict != AcquiredFileAccepted || after[0].DecidedBy != "user" {
		t.Fatalf("copy = %s by %s, want it accepted by the user", after[0].Verdict, after[0].DecidedBy)
	}

	accepted, err := queries.UserAcceptedCopy(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted {
		t.Fatal("the import cannot tell that a person already decided this copy")
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "pending" {
		t.Fatalf("import status = %q, want the import re-opened", stored.ImportStatus)
	}
}

// A decision a person took is not re-asked. The second attempt is refused with
// a sentence rather than quietly writing the same answer again.
func TestAcceptingACopyThatWasAlreadyAnsweredIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)

	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(ctx, copies[0].ID, "you said it is the one"); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(
		ctx, copies[0].ID, "you said it again",
	); !errors.Is(err, ErrNotWaitingForADecision) {
		t.Fatalf("accepting twice = %v, want ErrNotWaitingForADecision", err)
	}
}

// None of these is a refusal, so it goes into the same store the loop writes
// its discards to. Two stores would let the loop offer back a file somebody had
// just rejected, which is the one thing the queue must never do.
func TestRefusingEveryHeldCopyRemembersItAndKeepsTheWantLooking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)

	if err := queries.RefuseHeldCopies(
		ctx, targetID, "you said it is not the one", "looking for another", time.Now(),
	); err != nil {
		t.Fatalf("RefuseHeldCopies() error = %v", err)
	}

	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if copies[0].Verdict != AcquiredFileDiscardedAudio || copies[0].DecidedBy != "user" {
		t.Fatalf("copy = %s by %s, want it refused by the user",
			copies[0].Verdict, copies[0].DecidedBy)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatal("a refused copy is still being offered for a decision")
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM acquisition_targets WHERE id = $1`, targetID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("target = %s, want the want still looking", status)
	}
}

// holdOneCopy leaves one copy for the want in the state the review queue reads:
// fetched, checked, and undecided.
func holdOneCopy(
	ctx context.Context, t *testing.T, queries *Queries, targetID, requestID uuid.UUID,
) {
	t.Helper()
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
			Verdict:           AcquiredFileHeld,
			Summary:           "Nothing could identify the audio.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus:  "needs_review",
		ImportError:   "Nothing could identify the audio.",
		Evidence:      &AcquiredFileEvidence{Name: "03.flac", Agrees: []string{}, Differs: []string{}},
		TargetSummary: "Not in your library yet.",
		NextAttemptAt: time.Now().Add(time.Hour),
		Outcome:       "inconclusive",
	}); err != nil {
		t.Fatalf("hold the copy: %v", err)
	}
}

// settleTarget marks a want satisfied without going through an import, which is
// the state the assertions below care about: the want has stopped asking, and
// its unpicked candidates are still on record because nobody listened to them.
func settleTarget(ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'acquired', acquired_at = now(), next_attempt_at = NULL,
		    summary = 'A copy was fetched, proven by its audio, and imported.',
		    updated_at = now()
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatalf("settle the want: %v", err)
	}
}

// Accepting one copy rules on that copy and no other, so a satisfied want keeps
// its unpicked candidates. They are not questions any more: the want they were
// held for has been answered, and listing it again put music the user had just
// confirmed back in front of them under the same name.
func TestTheReviewQueuePassesOverAWantThatHasAlreadyBeenSatisfied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	settleTarget(ctx, t, pool, targetID)

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}

	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("queue = %+v, want a satisfied want left out of it", page.Items)
	}
}

// The queue not showing it is not enough. A held copy of a satisfied want must
// not be acceptable at all, or the same answer given twice imports a second copy
// of music already in the library.
func TestAWantThatHasAlreadyBeenSatisfiedTakesNoSecondAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	settleTarget(ctx, t, pool, targetID)

	_, err = queries.AcceptHeldCopy(ctx, copies[0].ID, "you said it is the one")

	if !errors.Is(err, ErrNotWaitingForADecision) {
		t.Fatalf("AcceptHeldCopy() error = %v, want %v", err, ErrNotWaitingForADecision)
	}
	after, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Verdict != AcquiredFileHeld {
		t.Errorf("copy = %s, want it left held rather than accepted a second time",
			after[0].Verdict)
	}
}

// A refusal is permanent. Recording one against a want that stopped asking would
// rule on files nobody listened to, on behalf of a question already answered.
func TestASatisfiedWantTakesNoRefusalEither(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	settleTarget(ctx, t, pool, targetID)

	err := queries.RefuseHeldCopies(ctx, targetID, "none of these are it",
		"Wanted. Waiting for a copy to be found.", time.Now())

	if !errors.Is(err, ErrNotWaitingForADecision) {
		t.Fatalf("RefuseHeldCopies() error = %v, want %v", err, ErrNotWaitingForADecision)
	}
	after, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Verdict != AcquiredFileHeld {
		t.Errorf("copy = %s, want it left held rather than discarded", after[0].Verdict)
	}
}

// A want still being pursued is unaffected: the copies held for it are exactly
// the questions the queue exists to ask.
func TestTheReviewQueueStillOffersAWantThatIsStillBeingPursued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}

	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Target.ID != targetID {
		t.Fatalf("queue = %+v, want the want still waiting on a person", page.Items)
	}
}

// holdASecondCopy records another peer's offer for the same want, so the want
// carries two questions and answering one leaves the other behind.
func holdASecondCopy(
	ctx context.Context, t *testing.T, queries *Queries, targetID, requestID uuid.UUID,
) {
	t.Helper()
	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "other peer",
		RemotePath: `Music\Anetha\04.flac`, FileName: "04.flac", SizeBytes: 9100000,
		Verdict:           AcquiredFileHeld,
		Summary:           "Nothing could identify the audio.",
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatalf("hold the second copy: %v", err)
	}
}

// The want is answered the moment a copy is confirmed, but it only turns
// 'acquired' once the import lands. In between, the sibling nobody ruled on used
// to put the same song back on the queue, which is how the same song was
// confirmed twice.
func TestAWantWhoseAnswerIsStillImportingLeavesTheQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	holdASecondCopy(ctx, t, queries, targetID, requestID)
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(ctx, copies[0].ID, "that is the one"); err != nil {
		t.Fatalf("AcceptHeldCopy() error = %v", err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}

	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("queue = %+v, want an answered want left out of it", page.Items)
	}
}

// Confirming the sibling while the first answer is still importing is what put
// the same recording in the library twice.
func TestAWantWhoseAnswerIsStillImportingTakesNoSecondAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	holdASecondCopy(ctx, t, queries, targetID, requestID)
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(ctx, copies[0].ID, "that is the one"); err != nil {
		t.Fatalf("AcceptHeldCopy() error = %v", err)
	}

	_, err = queries.AcceptHeldCopy(ctx, copies[1].ID, "and so is this one")

	if !errors.Is(err, ErrNotWaitingForADecision) {
		t.Fatalf("AcceptHeldCopy() error = %v, want %v", err, ErrNotWaitingForADecision)
	}
}

// A failed import is not an answer. The question comes back rather than being
// buried by a decision that never produced any music.
func TestAWantWhoseAnswerFailedToImportComesBackToTheQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	holdASecondCopy(ctx, t, queries, targetID, requestID)
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(ctx, copies[0].ID, "that is the one"); err != nil {
		t.Fatalf("AcceptHeldCopy() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET import_error = 'the file went away' WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}

	if page.Total != 1 {
		t.Fatalf("queue total = %d, want the question asked again", page.Total)
	}
}

// A copy a person accepted is a manual decision, and manual decisions are
// permanent. Recorded as anything else, the resolution job deletes it -- its
// first act is to drop every identity nobody decided by hand -- and the file is
// left to be graded on the peer's own tags, which is what ADR 0002 forbids. In
// production that took one second: a copy accepted at 16:37:20 read "no
// recording MusicBrainz knows of matches this file" at 16:37:21.
func TestAnIdentityAcceptedByHandIsRecordedAsAManualDecision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(ctx, copies[0].ID, "that is the one"); err != nil {
		t.Fatalf("AcceptHeldCopy() error = %v", err)
	}
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/Anetha/03.flac', 9000000, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET library_file_id = $2 WHERE download_request_id = $1
	`, requestID, fileID); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was fetched and imported.", "You said it is the one."); err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}

	var isManual bool
	if err := pool.QueryRow(ctx, `
		SELECT is_manual FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&isManual); err != nil {
		t.Fatalf("read the identity written for the accepted copy: %v", err)
	}
	if !isManual {
		t.Fatal("identity is_manual = false, want the decision recorded as the user's")
	}
}

// askedWhichRecording records the other kind of question: an entry that fits
// more than one recording, stored with the answers it could have and stopped.
func askedWhichRecording(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID, []uuid.UUID) {
	t.Helper()
	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, entry_album, status, attempts,
			last_attempt_at, summary
		)
		VALUES ($1, 'playlist', 'nthng', 'It Never Ends', 'It Never Ends',
		        'unresolved', 0, now(), 'Waiting to be resolved.')
	`, targetID); err != nil {
		t.Fatalf("seed the entry: %v", err)
	}

	queries := New(pool)
	first, second := uuid.New(), uuid.New()
	if _, err := queries.SendAcquisitionTargetToReview(ctx, ReviewAcquisitionTargetParams{
		ID:      targetID,
		Summary: "2 recordings fit this entry equally well; the evidence does not choose between them.",
		Detail:  "Two recordings fit.",
		Candidates: []AcquisitionTargetCandidateRow{
			{
				MusicBrainzRecordingID: first, ArtistName: "nthng", TrackTitle: "It Never Ends",
				Rank: 1, Agrees: []string{"artist", "title"}, Differs: []string{},
				Summary: "Named the same, on a different release.",
			},
			{
				MusicBrainzRecordingID: second, ArtistName: "nthng", TrackTitle: "It Never Ends",
				Rank: 2, Agrees: []string{"artist", "title"}, Differs: []string{},
				Summary: "Named the same, on the album.",
			},
		},
	}); err != nil {
		t.Fatalf("ask the question: %v", err)
	}
	return queries, targetID, []uuid.UUID{first, second}
}

// A want MusicBrainz has nothing for, and one that has simply not been asked yet.
// Both are 'unresolved'; only the first has stopped.
func unresolvedWant(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, summary string,
) uuid.UUID {
	t.Helper()
	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, entry_album, status, attempts,
			last_attempt_at, summary
		)
		VALUES ($1, 'playlist', 'Porter Robinson', 'WannaCry', 'WannaCry',
		        'unresolved', 4, now(), $2)
	`, targetID, summary); err != nil {
		t.Fatalf("seed the entry: %v", err)
	}
	return targetID
}

// An entry MusicBrainz holds no recording for is not a question. Nothing about
// it is uncertain, there is nothing to choose between, and the only way out is
// for somebody to write the release into MusicBrainz — which the list's own page
// offers, on the entry itself. It used to be read here, and one imported playlist
// of 314 entries then filled the queue with 253 rows nobody could answer.
func TestTheReviewQueuePassesOverTheEntriesMusicBrainzHasNothingFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	unresolvedWant(ctx, t, pool, unfoundSummary)

	page, err := New(pool).AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("queue = %d items of %d, want none: nobody can answer this",
			len(page.Items), page.Total)
	}
}

// A want still waiting for its first attempt is not a question either, and never
// was. It is here so that the two unresolved kinds are both pinned: neither the
// one that has been asked about and found nothing, nor the one that has not been
// asked yet, is put in front of a person.
func TestTheReviewQueuePassesOverAWantStillWaitingToBeResolved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	unresolvedWant(ctx, t, pool, "Waiting to be resolved.")

	page, err := New(pool).AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("queue = %d items of %d, want none: this want has not been asked about yet",
			len(page.Items), page.Total)
	}
}

// The queue holds both kinds of question. An entry nothing could resolve is a
// want that has stopped exactly as a held copy is, and listing only the copies
// left it stored, answerable and invisible.
func TestTheReviewQueueOffersTheEntriesNothingCouldResolve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, recordings := askedWhichRecording(ctx, t, pool)

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("queue = %d items of %d, want the entry waiting on an answer",
			len(page.Items), page.Total)
	}
	item := page.Items[0]
	if item.Target.ID != targetID {
		t.Fatalf("target = %s, want %s", item.Target.ID, targetID)
	}
	if len(item.Candidates) != len(recordings) {
		t.Fatalf("candidates = %d, want the %d the question offered",
			len(item.Candidates), len(recordings))
	}
	if item.Candidates[0].MusicBrainzRecordingID != recordings[0] {
		t.Fatalf("candidates are out of the order they were ranked in: %+v", item.Candidates)
	}
	if len(item.Files) != 0 {
		t.Fatalf("copies = %+v, want none: nothing is fetched for a want with no recording",
			item.Files)
	}
}

// Both kinds of question are one queue, oldest first. Which kind a want is has
// nothing to do with how long somebody has been meant to answer it.
func TestTheReviewQueueOrdersBothKindsOfQuestionByAge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, copyTarget, requestID := fetchedForAWant(ctx, t, pool)
	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: copyTarget, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict:           AcquiredFileHeld,
		Summary:           "Nothing could identify the audio.",
		Evidence:          &AcquiredFileEvidence{Name: "03.flac", Agrees: []string{}, Differs: []string{}},
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatalf("hold the copy: %v", err)
	}
	_, askedTarget, _ := askedWhichRecording(ctx, t, pool)

	// The entry was asked about an hour before the copy arrived, so it leads.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET last_attempt_at = now() - interval '1 hour'
		WHERE id = $1
	`, askedTarget); err != nil {
		t.Fatal(err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("queue = %d items of %d, want both questions", len(page.Items), page.Total)
	}
	if page.Items[0].Target.ID != askedTarget {
		t.Fatalf("first = %s, want the question that has been waiting longest (%s)",
			page.Items[0].Target.ID, askedTarget)
	}
	if page.Items[1].Target.ID != copyTarget {
		t.Fatalf("second = %s, want %s", page.Items[1].Target.ID, copyTarget)
	}
}

// A want somebody stopped pursuing is not a question any more, whichever kind it
// was. The decision to stop is the answer.
func TestTheReviewQueuePassesOverAnEntryNobodyWantsAnswered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := askedWhichRecording(ctx, t, pool)

	if _, err := queries.StopPursuingAcquisitionTarget(ctx, targetID, "No longer wanted."); err != nil {
		t.Fatalf("stop pursuing it: %v", err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("queue = %d, want nothing: a want nobody wants is not a question", page.Total)
	}
}

// A tag is arbitrary bytes, and a NUL inside one used to take the whole verdict
// down with it: jsonb refused the document, so the refusal was never recorded
// and the loop fetched the same copy again on every wave.
func TestAVerdictIsRecordedThoughThePeersTagsCarryNUL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict: AcquiredFileDiscardedAudio,
		Summary: "The audio is another recording.",
		Evidence: &AcquiredFileEvidence{
			Name:      "03.flac",
			Observed:  &ImportTags{Artist: "Niki Istrefi\x00", Title: "Red\x00Armor"},
			Agrees:    []string{},
			Differs:   []string{"artist"},
			Problems:  []string{},
			SizeBytes: 9000000,
		},
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatalf("RecordAcquiredFile() error = %v", err)
	}

	tried, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatalf("read what was tried: %v", err)
	}
	if len(tried) != 1 {
		t.Fatalf("copies = %d, want the one verdict", len(tried))
	}
	if tried[0].Verdict != AcquiredFileDiscardedAudio {
		t.Fatalf("verdict = %q, want the refusal recorded", tried[0].Verdict)
	}
	// Everything the tag said apart from the byte no text type can hold.
	if got := tried[0].Evidence.Observed.Artist; got != "Niki Istrefi" {
		t.Fatalf("artist = %q, want the tag with only the NUL taken out", got)
	}
	if got := tried[0].Evidence.Observed.Title; got != "RedArmor" {
		t.Fatalf("title = %q, want the tag with only the NUL taken out", got)
	}
	if tried[0].Evidence.SizeBytes != 9000000 {
		t.Fatalf("size = %d, want the number it was written with", tried[0].Evidence.SizeBytes)
	}
}

// A want is pursued one candidate at a time. A pass that finds the open request
// stands back instead of recording a second one and failing on the index nobody
// can read.
func TestTheRequestAlreadyFetchingForAWantIsFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'started' WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	open, err := queries.OpenTargetRequest(ctx, targetID)
	if err != nil {
		t.Fatalf("OpenTargetRequest() error = %v", err)
	}
	if !open.Valid || open.UUID != requestID {
		t.Fatalf("open request = %v, want the one in flight (%s)", open, requestID)
	}
}

// A request whose transfer has finished is not done with: the copy is waiting to
// be checked, and fetching a second one in that window is two copies of the same
// music arriving for one want.
func TestACopyWaitingToBeCheckedIsStillTheWantsOpenRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	open, err := queries.OpenTargetRequest(ctx, targetID)
	if err != nil {
		t.Fatalf("OpenTargetRequest() error = %v", err)
	}
	if !open.Valid || open.UUID != requestID {
		t.Fatalf("open request = %v, want the copy waiting to be checked (%s)", open, requestID)
	}
}

// A want nothing is fetching for has no open request, which is what lets the next
// pass go looking rather than standing back forever.
func TestAWantWithNothingInFlightHasNoOpenRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	open, err := queries.OpenTargetRequest(ctx, targetID)
	if err != nil {
		t.Fatalf("OpenTargetRequest() error = %v", err)
	}
	if open.Valid {
		t.Fatalf("open request = %v, want none", open)
	}
}

// The review queue plays a copy before somebody decides about it, so one offer
// has to be readable on its own.
func TestOneCopyIsReadBackByName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	held, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}

	copied, err := queries.AcquiredCopy(ctx, held[0].ID)
	if err != nil {
		t.Fatalf("AcquiredCopy() error = %v", err)
	}
	if copied.ID != held[0].ID || copied.FileName != "03.flac" {
		t.Fatalf("copy = %+v, want the held offer %s", copied, held[0].ID)
	}
	if copied.Evidence == nil {
		t.Fatal("a copy read for a decision arrives without the evidence to decide on")
	}
}

// A copy nobody recorded is missing rather than empty, so a request for one that
// never existed is refused instead of answered with a blank row.
func TestReadingACopyThatWasNeverRecordedIsMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, err := New(pool).AcquiredCopy(ctx, uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("AcquiredCopy() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// The bytes never arrived as offered, which is a fact about the transfer and not
// about the music. The offer goes back on the shelf rather than being struck off
// on something that says nothing about the recording.
func TestAFetchThatSettledWithoutArrivingIsFreedForAnotherDay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict:           AcquiredFileFetching,
		Summary:           "Being fetched.",
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'failed' WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	if err := queries.ReleaseStaleFetches(ctx, targetID); err != nil {
		t.Fatalf("ReleaseStaleFetches() error = %v", err)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Verdict != AcquiredFileUndelivered {
		t.Fatalf("offers = %+v, want the reservation released as a transport fact", offers)
	}
}

// A peer that would not take the request now is not a copy that failed. It is
// recorded in words the loop reads back, so the want comes round to this peer
// again in minutes rather than striking the copy off for a day.
func TestACopyAPeerWouldNotTakeNowSaysSoInItsOwnWords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict:           AcquiredFileFetching,
		Summary:           "Being fetched.",
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	if err := queries.DeferAcquiredFetch(ctx, requestID, "a peer is busy"); err != nil {
		t.Fatalf("DeferAcquiredFetch() error = %v", err)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Summary != DeferredByPeerSummary {
		t.Fatalf("offers = %+v, want the copy recorded as merely put off", offers)
	}
}

// Being told to wait one's turn is not an attempt at finding the music, so the
// ladder does not move. A press big enough to be refused would otherwise punish
// itself for being made.
func TestAPeerBeingBusySpendsNoneOfAWantsAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	var before int32
	if err := pool.QueryRow(ctx, `
		SELECT attempts FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&before); err != nil {
		t.Fatal(err)
	}

	if err := queries.DeferAcquiredFetch(ctx, requestID, "a peer is busy"); err != nil {
		t.Fatalf("DeferAcquiredFetch() error = %v", err)
	}

	var after int32
	var summary string
	if err := pool.QueryRow(ctx, `
		SELECT attempts, summary FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&after, &summary); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("attempts = %d, want the want's ladder untouched at %d", after, before)
	}
	if summary != "a peer is busy" {
		t.Errorf("summary = %q, want the want to say why it is waiting", summary)
	}
}

// The bound is on the peer, so it has to be read from what is actually open at
// each of them rather than from anything one pass remembers.
func TestTheFilesOpenAtAPeerAreCountedAcrossEveryRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, requestID := fetchedForAWant(ctx, t, pool)

	// fetchedForAWant leaves one request completed, which is nothing still open.
	open, err := queries.OpenFilesByPeer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count := open[Peer{Provider: "slskd", Username: "peer"}]; count != 0 {
		t.Fatalf("counted %d files open, want a settled request counted as nothing", count)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'started' WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}
	open, err = queries.OpenFilesByPeer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count := open[Peer{Provider: "slskd", Username: "peer"}]; count != 1 {
		t.Fatalf("counted %d files open, want the one still being asked for", count)
	}
}

// A transfer still moving keeps its reservation: freeing it would let the same
// pass choose the copy that is already on its way.
func TestAFetchStillInFlightKeepsItsReservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict:           AcquiredFileFetching,
		Summary:           "Being fetched.",
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	if err := queries.ReleaseStaleFetches(ctx, targetID); err != nil {
		t.Fatalf("ReleaseStaleFetches() error = %v", err)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Verdict != AcquiredFileFetching {
		t.Fatalf("offers = %+v, want the copy still reserved for its transfer", offers)
	}
}

// The download source refusing to be asked is its rate limiter, not a statement
// about whether anybody is sharing the music, so the want spends no attempt on it.
func TestDeferringAWantMovesItsTurnWithoutSpendingAnAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	nextAttempt := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	if err := queries.DeferAcquisitionTarget(
		ctx, targetID, "The download source is busy.", "429 too many requests", nextAttempt,
	); err != nil {
		t.Fatalf("DeferAcquisitionTarget() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Attempts != 0 {
		t.Fatalf("attempts = %d, want none spent on a question nobody asked", target.Attempts)
	}
	if !target.NextAttemptAt.Valid || !target.NextAttemptAt.Time.Equal(nextAttempt) {
		t.Fatalf("next attempt = %v, want %v", target.NextAttemptAt, nextAttempt)
	}
	if target.LastError.String != "429 too many requests" {
		t.Fatalf("last error = %q, want the refusal readable", target.LastError.String)
	}
}

// A provider refusal keeps a parked want on its weekly rung instead of using
// the short delay passed by the generic defer path.
func TestDeferringAParkedWantKeepsTheWeeklyRung(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET below_floor_streak = 3, below_floor_since = now()
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	nextAttempt := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	if err := queries.DeferAcquisitionTarget(
		ctx, targetID, "The download source is busy.", "429 too many requests", nextAttempt,
	); err != nil {
		t.Fatalf("DeferAcquisitionTarget() error = %v", err)
	}
	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if !target.NextAttemptAt.Valid || target.NextAttemptAt.Time.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("next attempt = %v, want the parked weekly rung", target.NextAttemptAt)
	}
}

// Rejecting a fetched copy and losing a transfer are outcomes distinct from
// an all-below-floor answer, so both clear the consecutive floor-answer state.
func TestANonFloorOutcomeClearsBelowFloorStreak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET below_floor_streak = 2, below_floor_since = now()
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Verdict:           AcquiredFileDiscardedAudio,
			Summary:           "The audio is a different recording.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus: "discarded", TargetSummary: "Not in your library yet.",
		NextAttemptAt: time.Now(), Outcome: "rejected",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}
	cleared, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.BelowFloorStreak != 0 || cleared.BelowFloorSince.Valid {
		t.Errorf("rejected target = streak %d, since %v; want cleared state",
			cleared.BelowFloorStreak, cleared.BelowFloorSince)
	}

	queries, failedID, _ := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `

		UPDATE acquisition_targets
		SET below_floor_streak = 2, below_floor_since = now()
		WHERE id = $1
	`, failedID); err != nil {
		t.Fatal(err)
	}
	if err := queries.RescheduleFailedTransfer(ctx, failedID, "The copy did not arrive."); err != nil {
		t.Fatalf("RescheduleFailedTransfer() error = %v", err)
	}
	cleared, err = queries.AcquisitionTarget(ctx, failedID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.BelowFloorStreak != 0 || cleared.BelowFloorSince.Valid {
		t.Errorf("failed target = streak %d, since %v; want cleared state",
			cleared.BelowFloorStreak, cleared.BelowFloorSince)
	}
}

// scannedAcceptedCopy is the world once a proven copy has been imported and the
// scan that followed has created its library row.
func scannedAcceptedCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, queries *Queries,
	targetID, requestID uuid.UUID, evidence *AcquiredFileEvidence,
) uuid.UUID {
	t.Helper()

	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := queries.CompleteTargetImport(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict: AcquiredFileAccepted, Summary: "Proven by its audio.",
		Evidence:          evidence,
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}, "It is being taken into your library.",
		"/music", "/music/Anetha", map[string]ImportedDownloadFile{
			`Music\Anetha\03.flac`: {LocalPath: "/music/Anetha/03.flac"},
		}); err != nil {
		t.Fatalf("import the proven copy: %v", err)
	}

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/Anetha/03.flac', 9000000, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET library_file_id = $2 WHERE download_request_id = $1
	`, requestID, fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// The loop closes here: the file is given the identity verification proved, taken
// out of the queue of files nobody has asked about, and the want settles against
// it.
func TestCompletingAnAcceptedCopyGivesTheFileTheProofThatAdmittedIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	fileID := scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{
			Name: "03.flac", Method: "fingerprint", Confidence: 1,
			Wanted:  &ImportTags{Artist: "Anetha", Album: "Mata Hari", Title: "Salsed Punk"},
			Agrees:  []string{"audio"},
			Differs: []string{}, Problems: []string{},
		})

	outcome, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was imported.", "Proven by its audio.")
	if err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}
	if !outcome.Settled || outcome.Disagreed {
		t.Fatalf("outcome = %+v, want the want settled against the copy", outcome)
	}
	if !outcome.LibraryFileID.Valid || outcome.LibraryFileID.UUID != fileID {
		t.Fatalf("library file = %v, want %s", outcome.LibraryFileID, fileID)
	}

	var (
		method     string
		artist     string
		resolution string
	)
	if err := pool.QueryRow(ctx, `
		SELECT identities.method, coalesce(identities.artist_name, ''), files.resolution_status
		FROM library_file_identities identities
		JOIN library_files files ON files.id = identities.library_file_id
		WHERE identities.library_file_id = $1
	`, fileID).Scan(&method, &artist, &resolution); err != nil {
		t.Fatalf("read the identity written for the copy: %v", err)
	}
	if method != "fingerprint" || artist != "Anetha" {
		t.Fatalf("identity = %s by %q, want what the verification concluded", method, artist)
	}
	if resolution != "resolved" {
		t.Fatalf("resolution status = %q, want the proof out of reach of the tag-only path",
			resolution)
	}
}

// The release behind an acquired copy is asked for, so the file has a catalogue
// track to be mapped to rather than sitting in the library belonging to nothing.
func TestCompletingAnAcceptedCopyAsksForTheReleaseBehindIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	releaseGroupID := uuid.New()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_release_group_id = $2 WHERE id = $1
	`, targetID, releaseGroupID); err != nil {
		t.Fatal(err)
	}
	scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})

	if _, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was imported.", "Proven by its audio."); err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'ingest_release_group' AND payload->>'releaseGroupId' = $1::text
	`, releaseGroupID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued ingests = %d, want the release asked for once", queued)
	}
}

// The copy is imported and the scan has not made a library row of it yet. The
// want has its music and only the bookkeeping is missing, so nothing may go
// looking for a second copy in the meantime.
func TestAWantWhoseCopyHasNoLibraryRowYetIsStillWaitingOnOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	fileID := scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET library_file_id = NULL WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}

	outcome, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was imported.", "Proven by its audio.")
	if err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}
	if !outcome.Awaiting || outcome.AwaitingSince.IsZero() {
		t.Fatalf("outcome = %+v, want the want told its copy is still being scanned", outcome)
	}
	if outcome.Settled || outcome.LibraryFileID.Valid {
		t.Fatalf("outcome = %+v, want nothing settled while there is no row to settle against",
			outcome)
	}
}

// A want nobody has accepted a copy for is not waiting on one, which is what
// tells the loop it is free to go looking.
func TestAWantWithNoAcceptedCopyIsNotWaitingOnOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	outcome, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was imported.", "Proven by its audio.")
	if err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}
	if outcome.Awaiting || outcome.Settled || outcome.LibraryFileID.Valid {
		t.Fatalf("outcome = %+v, want nothing at all reported for a want with no copy", outcome)
	}
}

// The library already says that file is a different recording. Nothing here
// overwrites a decision already recorded: the want stays in the queue with the
// disagreement as its reason rather than settling on it.
func TestCompletingACopyTheLibraryCallsSomethingElseLeavesTheWantOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	fileID := scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})

	somethingElse := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, is_manual, summary
		)
		VALUES ($1, 'external', $2, 'manual', true, 'decided by hand')
	`, fileID, somethingElse); err != nil {
		t.Fatal(err)
	}

	outcome, err := queries.CompleteAcquiredFile(ctx, targetID,
		"The library says that file is a different recording.", "Proven by its audio.")
	if err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}
	if !outcome.Disagreed || outcome.Settled {
		t.Fatalf("outcome = %+v, want the disagreement reported and nothing settled", outcome)
	}

	var stored uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT musicbrainz_recording_id FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != somethingElse {
		t.Fatalf("identity = %s, want the decision already recorded kept", stored)
	}
}

// stoppedOnADisagreement is the world after a proven copy was imported, the scan
// made a library file of it, and the library turned out to call that file a
// different recording. The want stops there: still wanted, no next look, and
// nothing automatic able to move it.
func stoppedOnADisagreement(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	// A want resolved to a recording knows the release that recording belongs
	// to, whether or not the catalogue holds it.
	releaseGroupID := uuid.New()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_release_group_id = $2 WHERE id = $1
	`, targetID, releaseGroupID); err != nil {
		t.Fatalf("seed the release behind the want: %v", err)
	}
	fileID := scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'recording-id', 'the file said so itself')
	`, fileID, uuid.New()); err != nil {
		t.Fatalf("seed what the library says the file is: %v", err)
	}
	outcome, err := queries.CompleteAcquiredFile(ctx, targetID,
		"your library calls that file a different recording", "Proven by its audio.")
	if err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}
	if !outcome.Disagreed {
		t.Fatalf("outcome = %+v, want the disagreement reported", outcome)
	}
	return queries, targetID, fileID, releaseGroupID
}

// A want that stopped has no next look, so nothing will reach it again on its
// own. It has to be asked about, or it is a want that went quiet.
func TestAWantStoppedOnADisagreementIsAskedAbout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _, _ := stoppedOnADisagreement(ctx, t, pool)

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Target.ID != targetID {
		t.Fatalf("queue = %+v, want the want that stopped (%s)", page.Items, targetID)
	}
}

// The question is about one copy, so the copy comes with it. A sentence with no
// evidence under it is one nobody can act on.
func TestTheQueueCarriesTheCopyAWantStoppedOn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, fileID, _ := stoppedOnADisagreement(ctx, t, pool)

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	files := page.Items[0].Files
	if len(files) != 1 || files[0].Verdict != AcquiredFileAccepted {
		t.Fatalf("copies = %+v, want the accepted copy shown", files)
	}
	if !files[0].LibraryFileID.Valid || files[0].LibraryFileID.UUID != fileID {
		t.Fatalf("copy names file %v, want the file it became (%s)",
			files[0].LibraryFileID, fileID)
	}
}

// A want nobody has scheduled yet also has no next look, and it is asking
// nothing: nothing has been fetched for it and there is nothing to decide.
func TestAWantWithNoNextLookAndNoCopyIsNotAskedAbout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = NULL WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("queue = %+v, want nothing asked about a want holding no copy", page.Items)
	}
}

// The one question every path about to go looking has to ask. A want whose copy
// is on the disc has its music, and fetching more of it is a second copy of what
// is already here.
func TestAWantWhoseCopyWasImportedIsReportedAsHoldingOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})
	// The scan having made a library row is not the whole story: what settles the
	// question is CompleteAcquiredFile linking the copy to that row, which is what
	// actually happens once the scan's pass reaches it.
	if _, err := queries.CompleteAcquiredFile(ctx, targetID,
		"your library calls that file a different recording", "Proven by its audio."); err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}

	holds, err := queries.AcquisitionTargetHasAcceptedCopy(ctx, targetID)
	if err != nil {
		t.Fatalf("AcquisitionTargetHasAcceptedCopy() error = %v", err)
	}
	if !holds {
		t.Fatal("a want whose copy was accepted and imported reads as holding none")
	}
}

// A copy whose import failed is not an answer. The want has nothing on the disc,
// so it keeps looking, exactly as the review queue keeps asking about it.
func TestAWantWhoseImportFailedIsNotReportedAsHoldingACopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})
	// The shape a failed import leaves: the request is still waiting to be
	// imported and says what went wrong. There is an accepted copy on record and
	// nothing on the disc to show for it.
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests
		SET import_status = 'pending', import_error = 'the folder was gone',
		    imported_at = NULL, import_path = NULL
		WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	holds, err := queries.AcquisitionTargetHasAcceptedCopy(ctx, targetID)
	if err != nil {
		t.Fatalf("AcquisitionTargetHasAcceptedCopy() error = %v", err)
	}
	if holds {
		t.Fatal("a want whose import failed reads as though its music is on the disc")
	}
}

// The copy's link to its file is set to nothing when the file is deleted, so a
// want whose copy was deleted holds nothing any more. It has to read that way
// everywhere: a rule that answered from the import instead would go on refusing
// to look for music the library no longer has.
func TestAWantWhoseFileWasDeletedIsNotReportedAsHoldingACopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, fileID, _ := stoppedOnADisagreement(ctx, t, pool)

	if _, err := pool.Exec(ctx, `DELETE FROM library_files WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}

	holds, err := queries.AcquisitionTargetHasAcceptedCopy(ctx, targetID)
	if err != nil {
		t.Fatalf("AcquisitionTargetHasAcceptedCopy() error = %v", err)
	}
	if holds {
		t.Fatal("a want whose copy was deleted still reads as having its music")
	}
}

// Stopping is not giving up. The want stays wanted and keeps its copy; only its
// next look goes, and the sentence says why.
func TestStoppingAWantTakesItsNextLookAndSaysWhy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	const summary = "your library calls that file a different recording"
	if err := queries.StopLookingForAcquisitionTarget(ctx, targetID, summary); err != nil {
		t.Fatalf("StopLookingForAcquisitionTarget() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "pending" || target.NextAttemptAt.Valid {
		t.Fatalf("target = %s / next look %v, want it still wanted and no longer due",
			target.Status, target.NextAttemptAt)
	}
	if target.Summary != summary || target.Attempts != 0 {
		t.Fatalf("target says %q after %d attempts, want the reason and no attempt spent",
			target.Summary, target.Attempts)
	}
}

// The way back. Somebody says what the file is, and the want that stopped on it
// gets its next look back in the same breath — without which the decision
// changes the library and nothing ever looks at the want again.
func TestADecisionAboutAFileGivesBackTheNextLookOfTheWantHoldingIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, fileID, _ := stoppedOnADisagreement(ctx, t, pool)

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if err := RearmWantsHoldingFile(ctx, transaction, fileID, RearmedByADecisionSummary); err != nil {
		t.Fatalf("RearmWantsHoldingFile() error = %v", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if !target.NextAttemptAt.Valid {
		t.Fatal("the want still has no next look, so nothing will read the decision")
	}
	if target.Summary != RearmedByADecisionSummary {
		t.Errorf("summary = %q, want %q", target.Summary, RearmedByADecisionSummary)
	}
}

// The next look is only worth anything if something takes it, and the sweeper
// sleeps until it is asked. So the decision asks.
func TestADecisionAboutAFileAsksForASweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, _, fileID, _ := stoppedOnADisagreement(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		DELETE FROM jobs WHERE kind = 'sweep_acquisition_targets'
	`); err != nil {
		t.Fatal(err)
	}

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if err := RearmWantsHoldingFile(ctx, transaction, fileID, RearmedByADecisionSummary); err != nil {
		t.Fatalf("RearmWantsHoldingFile() error = %v", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued sweeps = %d, want the one that will read the decision", queued)
	}
}

// Matching the file to a track by hand is one of the two ways out of a want that
// stopped, and it needs a track to match to. The catalogue holds none for a
// release nobody has ingested, so the release is asked for here exactly as it is
// when a copy settles — otherwise the way out that reads best on screen leads to
// a search that finds nothing.
func TestAWantThatStopsAsksForTheReleaseBehindItsCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, _, _, releaseGroupID := stoppedOnADisagreement(ctx, t, pool)

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'ingest_release_group'
		  AND payload->>'releaseGroupId' = $1::text
	`, releaseGroupID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued ingests = %d, want the release asked for so the file has a track",
			queued)
	}
}

// A whole release somebody chose a folder for is validated against its edition.
// The two paths are held apart by a fact about the request rather than by
// anything inferred from what it happens to contain.
func TestClaimingAReleasesRequestAsAWantsCopyIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Anetha', 'Anetha')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Mata Hari')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, album_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Anetha', 1, 1, 9000000, 'flac', 0.7,
		        '[]'::jsonb, 'completed', 'pending')
	`, requestID, albumID); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.ClaimTargetImport(ctx, requestID); !errors.Is(err, ErrNotATargetImport) {
		t.Fatalf("ClaimTargetImport() error = %v, want the release's request left alone", err)
	}

	// And the request itself is untouched: it is still waiting for the import
	// that validates a whole release against its edition.
	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "pending" {
		t.Fatalf("import status = %q, want the release's own import still waiting",
			stored.ImportStatus)
	}
}

// One file, one recording. A request covering several files is not one copy
// fetched for a want, and guessing which of them the want meant is exactly what
// must not happen.
func TestClaimingARequestCoveringSeveralFilesAsOneCopyIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET files = $2::jsonb, import_status = 'pending' WHERE id = $1
	`, requestID, `[{"path":"a.flac","name":"a.flac"},{"path":"b.flac","name":"b.flac"}]`); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.ClaimTargetImport(ctx, requestID); !errors.Is(err, ErrNotATargetImport) {
		t.Fatalf("ClaimTargetImport() error = %v, want ErrNotATargetImport", err)
	}
}

// A want with no recording has nothing to verify a copy against. It cannot happen
// while a request exists, but a resolution the review queue replaced could clear
// it, and guessing what the file was supposed to be is the one thing forbidden.
func TestClaimingACopyForAWantThatNamesNoRecordingIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'unresolved', musicbrainz_recording_id = NULL, resolution_method = NULL,
		    resolved_at = NULL
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.ClaimTargetImport(ctx, requestID); !errors.Is(err, ErrNotATargetImport) {
		t.Fatalf("ClaimTargetImport() error = %v, want ErrNotATargetImport", err)
	}
}

// Only an accepted copy may name a library file. A refused one naming a file in
// the library would be a record that the library holds music somebody proved it
// does not.
func TestOnlyAnAcceptedCopyMayNameALibraryFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := fetchedForAWant(ctx, t, pool)
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/elsewhere.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		Verdict: AcquiredFileDiscardedAudio, Summary: "The audio is another recording.",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET library_file_id = $2
		WHERE acquisition_target_id = $1
	`, targetID, fileID); err == nil {
		t.Fatal("a refused copy was allowed to name a file in the library")
	}
}

// The verdicts one offer can reach are an enumerated set, and the database is
// what holds it: a verdict nothing understands would be a decision nobody could
// read back.
func TestAVerdictOutsideTheKnownSetIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		Verdict: "probably_fine", Summary: "It looked right.",
	}); err == nil {
		t.Fatal("a verdict outside the enumerated set was recorded")
	}
}

// Who decided is what separates the loop's judgement from a person's, and it is
// also what makes a person's permanent. Anything else would be a verdict with no
// author.
func TestOnlySchallOrAPersonMayDecideACopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, targetID, _ := fetchedForAWant(ctx, t, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, verdict, decided_by, summary
		)
		VALUES ($1, 'slskd', 'peer', $2, '03.flac', 'held', 'the importer', 'nothing decided')
	`, targetID, `Music\Anetha\03.flac`); err == nil {
		t.Fatal("a verdict was recorded with an author nobody can account for")
	}
}

// An offer judged with nothing to say about it is still a judgement: the verdict
// is what stops the loop fetching it again, and it must not depend on there being
// evidence to store.
func TestAVerdictWithNoEvidenceIsStillRecorded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := fetchedForAWant(ctx, t, pool)

	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		Verdict: AcquiredFileUndelivered, Summary: "The transfer could not be started.",
	}); err != nil {
		t.Fatalf("RecordAcquiredFile() error = %v", err)
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Verdict != AcquiredFileUndelivered {
		t.Fatalf("offers = %+v, want the verdict recorded without evidence", offers)
	}
	if offers[0].SizeBytes.Valid {
		t.Fatalf("size = %v, want nothing claimed about a copy that never arrived",
			offers[0].SizeBytes)
	}
}

// A verdict is settled against the import that is holding the copy. Settling one
// nobody is validating would close a request that is not open and put a want back
// in a queue it never left.
func TestSettlingACandidateNobodyIsValidatingIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Verdict:           AcquiredFileDiscardedAudio,
			Summary:           "The audio is a different recording.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus:  "discarded",
		TargetSummary: "Not in your library yet.",
		NextAttemptAt: time.Now(),
		Outcome:       "rejected",
	})
	if err == nil {
		t.Fatal("a verdict was settled against an import nobody was validating")
	}

	offers, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 0 {
		t.Fatalf("offers = %+v, want the whole settlement rolled back", offers)
	}
}

// A want somebody decided about while its copy was being checked is left exactly
// as they left it. The verdict about the file is still recorded — it is a fact
// about the bytes — but nothing puts the want back in a queue it has left.
func TestSettlingACandidateLeavesAWantSomebodyStoppedAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, targetID, "No longer wanted."); err != nil {
		t.Fatal(err)
	}

	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Verdict:           AcquiredFileDiscardedAudio,
			Summary:           "The audio is a different recording.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus:  "discarded",
		TargetSummary: "Not in your library yet.",
		NextAttemptAt: time.Now().Add(time.Hour),
		Outcome:       "rejected",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "not_wanted" || target.NextAttemptAt.Valid {
		t.Fatalf("target = %s due %v, want the decision to stop left standing",
			target.Status, target.NextAttemptAt)
	}
	var recorded int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_target_attempts WHERE acquisition_target_id = $1
	`, targetID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Fatalf("attempt rows = %d, want none appended to a want nobody is pursuing", recorded)
	}
}

// A refused copy gives the want back a time to be looked at again, and that
// time is only worth anything if the sweeper is asked for. Before this,
// nothing was: a refusal at next_attempt_at = now() waited out whatever pass
// was already scheduled instead of being picked up by the one right after.
func TestARefusedCandidateAsksForASweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM jobs WHERE kind = 'sweep_acquisition_targets'
	`); err != nil {
		t.Fatal(err)
	}

	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Verdict:           AcquiredFileDiscardedAudio,
			Summary:           "The audio is a different recording.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus:  "discarded",
		TargetSummary: "Not in your library yet.",
		NextAttemptAt: time.Now(),
		Outcome:       "rejected",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued sweeps = %d, want the one that will pick up the refused want", queued)
	}
}

// The attempt a candidate is recorded against is the want's own count, and a want
// whose first search has not been counted yet still has to number its first
// candidate: an attempt row numbered zero belongs to no attempt.
func TestTheFirstCandidateOfAnUncountedWantIsRecordedAsTheFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET attempts = 0 WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			Verdict:           AcquiredFileDiscardedAudio,
			Summary:           "The audio is a different recording.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus:  "discarded",
		TargetSummary: "Not in your library yet.",
		NextAttemptAt: time.Now().Add(time.Hour),
		Outcome:       "rejected",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}

	var attempt int32
	if err := pool.QueryRow(ctx, `
		SELECT attempt FROM acquisition_target_attempts WHERE acquisition_target_id = $1
	`, targetID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if attempt != 1 {
		t.Fatalf("attempt = %d, want the first candidate numbered as the first attempt", attempt)
	}
}

// A page past the end of the queue is a page with nothing on it, not an error and
// not the last page over again.
func TestAPagePastTheEndOfTheReviewQueueIsEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 50)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("total = %d, want the question still counted", page.Total)
	}
	if len(page.Items) != 0 {
		t.Fatalf("items = %+v, want nothing past the end of the queue", page.Items)
	}
}

// A want whose resolution was withdrawn while a copy sat accepted has nothing to
// verify that copy against. The identity is refused rather than guessed at from
// the file, which is the one thing that must never happen.
func TestACopyIsNotGivenAnIdentityOnceTheWantNamesNoRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	fileID := scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'unresolved', musicbrainz_recording_id = NULL, resolution_method = NULL,
		    resolved_at = NULL
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was imported.", "Proven by its audio."); err == nil {
		t.Fatal("a copy was completed against a want that names no recording")
	}

	var identities int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("identities = %d, want nothing written for a copy nothing names", identities)
	}
}

// The fingerprint below is real: one piece of audio as fpcalc 1.6.1 printed it,
// the same value internal/chromaprint's own tests are built on.
const heldCopyFingerprint = "AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wdaD_y4XikD69yNJeCHzZqFX-E6QT1oxf24GmMM3rwOMWR7PBzpLeC3DmEI0TRPMGDI0eyP_ABny6u4_gvHGGO59ARUzmFXsdbotYS5M0J_Xh2RPqLM4J_MCuPPAr6EIrPCLp4lDy-4ccvnMdUpYaeSEg1sWAkAMMAIgQwAhhBVAoLhjBOIAOBAkw4I4ihBAEECDUOMQWUAM4EIBEWAhEhAQAMMUGMA8oRQAgzAhIDGUQA"

// A copy held as a question keeps what its audio sounds like, and the queue that
// asks about it reads the fingerprint back. Nothing about the verdict, the
// summary or the evidence changes: the fingerprint is a measurement kept beside
// them.
func TestAHeldCopyKeepsWhatItsAudioSoundsLike(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
			Verdict: AcquiredFileHeld, Summary: "Nothing could decide.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus: "needs_review", ImportError: "nothing could decide",
		Evidence: &AcquiredFileEvidence{
			Name: "03.flac", Agrees: []string{}, Differs: []string{}, Problems: []string{},
			AudioFingerprint: heldCopyFingerprint, AudioFingerprintSeconds: 240,
		},
		TargetSummary: "The next copy will be tried.",
		NextAttemptAt: time.Now(), Outcome: "inconclusive",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("AcquisitionReviewQueue() error = %v", err)
	}
	if len(page.Items) != 1 || len(page.Items[0].Files) != 1 {
		t.Fatalf("page = %+v, want the one held copy", page.Items)
	}
	held := page.Items[0].Files[0]
	if held.Evidence == nil || held.Evidence.AudioFingerprint != heldCopyFingerprint {
		t.Fatalf("fingerprint = %+v, want what was measured from the copy", held.Evidence)
	}
	if held.Evidence.AudioFingerprintSeconds != 240 {
		t.Errorf("seconds = %d, want how much audio was read",
			held.Evidence.AudioFingerprintSeconds)
	}
	if held.Verdict != AcquiredFileHeld || held.Summary != "Nothing could decide." {
		t.Errorf("copy = %+v, want the verdict and its sentence untouched", held)
	}
}

// The backfill offers the held copies nothing has measured yet, and stops
// offering one the moment it has been measured. A measurement is written on its
// own: the verdict a person is about to answer is not touched by it.
func TestTheBackfillOffersOnlyTheCopiesWithNoFingerprint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := queries.ClaimTargetImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := queries.SettleAcquiredFile(ctx, SettleAcquiredFileParams{
		RecordAcquiredFileParams: RecordAcquiredFileParams{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
			Verdict: AcquiredFileHeld, Summary: "Nothing could decide.",
			DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		},
		ImportStatus: "needs_review", ImportError: "nothing could decide",
		Evidence: &AcquiredFileEvidence{
			Name: "03.flac", Agrees: []string{}, Differs: []string{}, Problems: []string{},
		},
		TargetSummary: "The next copy will be tried.",
		NextAttemptAt: time.Now(), Outcome: "inconclusive",
	}); err != nil {
		t.Fatalf("SettleAcquiredFile() error = %v", err)
	}

	waiting, err := queries.CopiesMissingFingerprint(ctx, 20)
	if err != nil {
		t.Fatalf("CopiesMissingFingerprint() error = %v", err)
	}
	if len(waiting) != 1 {
		t.Fatalf("waiting = %+v, want the one copy nothing has measured", waiting)
	}
	if waiting[0].SourceDirectory != "Music/Anetha" || waiting[0].FileName != "03.flac" {
		t.Errorf("copy = %+v, want where its bytes are", waiting[0])
	}

	if err := queries.RecordCopyFingerprint(
		ctx, waiting[0].CopyID, heldCopyFingerprint, 240,
	); err != nil {
		t.Fatalf("RecordCopyFingerprint() error = %v", err)
	}

	again, err := queries.CopiesMissingFingerprint(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("waiting = %+v, want a measured copy offered no more", again)
	}

	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 || copies[0].Evidence == nil {
		t.Fatalf("copies = %+v, want the one copy with its evidence", copies)
	}
	if copies[0].Evidence.AudioFingerprint != heldCopyFingerprint {
		t.Errorf("fingerprint = %q, want the measurement written back",
			copies[0].Evidence.AudioFingerprint)
	}
	if copies[0].Verdict != AcquiredFileHeld || copies[0].Summary != "Nothing could decide." {
		t.Errorf("copy = %+v, want the question untouched by the measurement", copies[0])
	}
}
