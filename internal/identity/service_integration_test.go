package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/matching"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// resolutionFixture is one library file nobody follows an artist for, which is
// the whole point of resolution.
type resolutionFixture struct {
	pool   *pgxpool.Pool
	fileID uuid.UUID
}

type recordingUploadReplacer struct {
	calls []uuid.UUID
}

func (replacer *recordingUploadReplacer) ReplaceOwnedWithUpload(
	_ context.Context, fileID uuid.UUID,
) (bool, error) {
	replacer.calls = append(replacer.calls, fileID)
	return false, nil
}

func setupResolution(t *testing.T, ctx context.Context) resolutionFixture {
	t.Helper()
	pool := dbtest.Setup(t)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag,
		    duration_ms
		) VALUES ($1, '/music/bought/01 Roads.flac', 1024, now(), 'Portishead',
		          'Dummy', 'Roads', 302000)
	`, fileID); err != nil {
		t.Fatal(err)
	}
	return resolutionFixture{pool: pool, fileID: fileID}
}

func (fixture resolutionFixture) status(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	var status string
	var summary *string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT resolution_status, resolution_summary FROM library_files WHERE id = $1
	`, fixture.fileID).Scan(&status, &summary); err != nil {
		t.Fatal(err)
	}
	if summary == nil {
		return status, ""
	}
	return status, *summary
}

// A file whose artist nobody follows is resolved by asking the provider
// directly, and the answer is stored where matching can use it later.
func TestResolutionStoresAProvenIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	recordingID := uuid.New()
	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
		Releases: []musicbrainz.RecordingRelease{{
			ID: uuid.New(), ReleaseGroupID: uuid.New(), Title: "Dummy", Status: "Official",
		}},
	}}}
	service := NewService(fixture.pool, provider, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	status, summary := fixture.status(t, ctx)
	if status != StatusResolved || summary == "" {
		t.Fatalf("status = %q summary = %q", status, summary)
	}
	resolution, err := service.File(ctx, fixture.fileID)
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	if resolution.Identity == nil || resolution.Identity.RecordingID != recordingID {
		t.Fatalf("identity = %+v", resolution.Identity)
	}
	if resolution.Identity.IsManual || resolution.Identity.Kind != "external" {
		t.Errorf("identity = %+v, want an automatic external identity", resolution.Identity)
	}
	if len(resolution.Candidates) != 0 {
		t.Errorf("candidates = %+v, want none once the identity is proven", resolution.Candidates)
	}
}

func TestAutomaticResolutionConsumesAnUploadMarkerOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)
	uploadID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET arrived_by_upload_id = $2 WHERE id = $1
	`, fixture.fileID, uploadID); err != nil {
		t.Fatal(err)
	}

	recordingID := uuid.New()
	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
	}}}
	replacer := &recordingUploadReplacer{}
	service := NewService(fixture.pool, provider, zerolog.Nop()).WithUploadReplacer(replacer)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("first Resolve() error = %v", err)
	}
	if len(replacer.calls) != 1 || replacer.calls[0] != fixture.fileID {
		t.Fatalf("replacement calls = %v, want one call for the uploaded file", replacer.calls)
	}
	var marker uuid.NullUUID
	if err := fixture.pool.QueryRow(ctx,
		`SELECT arrived_by_upload_id FROM library_files WHERE id = $1`, fixture.fileID,
	).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker.Valid {
		t.Fatal("the upload marker was not cleared")
	}

	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'pending' WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if len(replacer.calls) != 1 {
		t.Fatalf("replacement calls after second resolution = %v, want one call", replacer.calls)
	}
}

// A copy acquisition proved by its audio leaves behind an identity and a
// settled resolution. The job the scan queued before that proof landed must
// leave it alone: the providers know the file only by the peer's tags, and
// acting on their answer once deleted the proof and left the file local_only.
func TestResolutionLeavesAnAcquisitionProvenIdentityAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	recordingID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, method, confidence,
		    is_manual, summary
		) VALUES ($1, 'external', $2, 'fingerprint', 1, false,
		          'Proven to be the wanted recording and imported.')
	`, fixture.fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'resolved' WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	// A provider that has never heard of the file. Acting on this answer would
	// conclude local_only, and concluding starts by deleting the identity.
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	resolution, err := service.File(ctx, fixture.fileID)
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	if resolution.Status != StatusResolved {
		t.Errorf("status = %q, want %q untouched", resolution.Status, StatusResolved)
	}
	if resolution.Identity == nil || resolution.Identity.RecordingID != recordingID {
		t.Fatalf("identity = %+v, want the acquisition proof kept", resolution.Identity)
	}
}

// The question and the answer: a file the evidence cannot settle keeps its
// candidates, a person picks one, and the decision outlives later attempts.
func TestAManualDecisionOutlivesLaterAttempts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	first, second := uuid.New(), uuid.New()
	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{
		{ID: first, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length},
		{ID: second, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length},
	}}
	service := NewService(fixture.pool, provider, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if status, _ := fixture.status(t, ctx); status != StatusNeedsReview {
		t.Fatalf("status = %q, want %q", status, StatusNeedsReview)
	}

	if err := service.Accept(ctx, fixture.fileID, second); err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	resolution, err := service.File(ctx, fixture.fileID)
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	if resolution.Status != StatusResolved || resolution.Identity == nil ||
		resolution.Identity.RecordingID != second || !resolution.Identity.IsManual {
		t.Fatalf("resolution = %+v", resolution)
	}
	if len(resolution.Candidates) != 0 {
		t.Errorf("candidates = %+v, want the question closed", resolution.Candidates)
	}

	// Asking again must not overwrite what a person decided.
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	again, err := service.File(ctx, fixture.fileID)
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	if again.Identity == nil || again.Identity.RecordingID != second {
		t.Fatalf("identity after a second attempt = %+v", again.Identity)
	}

	if err := service.Clear(ctx, fixture.fileID); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if status, _ := fixture.status(t, ctx); status != StatusPending {
		t.Errorf("status after withdrawal = %q, want %q", status, StatusPending)
	}
	if err := service.Clear(ctx, fixture.fileID); !errors.Is(err, ErrNoDecision) {
		t.Errorf("second Clear() error = %v, want ErrNoDecision", err)
	}
}

func TestAcceptingSomethingNobodyOfferedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop())

	if err := service.Accept(ctx, fixture.fileID, uuid.New()); !errors.Is(err, ErrUnknownCandidate) {
		t.Fatalf("Accept() error = %v, want ErrUnknownCandidate", err)
	}
}

func TestLocalOnlyIsRememberedAsADecision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop())

	if err := service.MarkLocalOnly(ctx, fixture.fileID); err != nil {
		t.Fatalf("MarkLocalOnly() error = %v", err)
	}
	resolution, err := service.File(ctx, fixture.fileID)
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	if resolution.Status != StatusLocalOnly || resolution.Identity == nil ||
		resolution.Identity.Kind != "local_only" || !resolution.Identity.IsManual {
		t.Fatalf("resolution = %+v", resolution)
	}

	// Asking again leaves the decision alone, which is the whole point of
	// recording it.
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if status, _ := fixture.status(t, ctx); status != StatusLocalOnly {
		t.Errorf("status = %q", status)
	}
}

// A resolved identity is evidence the matcher may act on: the file was bought
// outside Schall, and the day its release enters the catalogue it maps by
// itself.
func TestAResolvedIdentityMatchesTheCatalogue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	recordingID := uuid.New()
	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 'Roads', 1, 10)
	`, trackID, albumID, recordingID); err != nil {
		t.Fatal(err)
	}

	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
	}}}
	matcher := matching.NewService(fixture.pool, zerolog.Nop())
	service := NewService(fixture.pool, provider, zerolog.Nop(), matcher)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	var mappedTrack uuid.UUID
	var method, matchStatus string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT track_mappings.track_id, track_mappings.method, library_files.match_status
		FROM track_mappings
		JOIN library_files ON library_files.id = track_mappings.library_file_id
		WHERE track_mappings.library_file_id = $1
	`, fixture.fileID).Scan(&mappedTrack, &method, &matchStatus); err != nil {
		t.Fatalf("the resolved file was not matched: %v", err)
	}
	if mappedTrack != trackID || method != "resolved_identity" || matchStatus != "matched" {
		t.Fatalf("mapping = (%s, %q, %q)", mappedTrack, method, matchStatus)
	}
}

// Two files that resolved to the same recording are not two copies of the
// truth: neither may be mapped automatically. They are not a question about
// which track fits either, since both already answer the same recording, so
// both report themselves as a duplicate rather than as ambiguous
// (internal/matching's heldBySameRecording).
func TestTwoFilesResolvedToOneRecordingStayContested(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	recordingID := uuid.New()
	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead');
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 'Roads', 1, 10)
	`, trackID, albumID, recordingID); err != nil {
		t.Fatal(err)
	}
	secondFileID := uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag, duration_ms
		) VALUES ($1, '/music/bought/01 Roads (copy).flac', 1024, now(), 'Portishead',
		          'Dummy', 'Roads', 302000)
	`, secondFileID); err != nil {
		t.Fatal(err)
	}

	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
	}}}
	matcher := matching.NewService(fixture.pool, zerolog.Nop())
	service := NewService(fixture.pool, provider, zerolog.Nop(), matcher)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := service.Resolve(ctx, secondFileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	var mappings int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM track_mappings WHERE track_id = $1
	`, trackID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 {
		t.Fatalf("mappings = %d, want the contested track left unmapped", mappings)
	}
	var statuses []string
	rows, err := fixture.pool.Query(ctx, `
		SELECT match_status FROM library_files ORDER BY path
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	for _, status := range statuses {
		if status != "duplicate" {
			t.Fatalf("statuses = %v, want both files reported as duplicates", statuses)
		}
	}
}

// ingestsQueuedFor reports how many ingest jobs are waiting for one release
// group. The count matters: two files from the same album must queue one
// question between them, not two.
func (fixture resolutionFixture) ingestsQueuedFor(
	t *testing.T, ctx context.Context, releaseGroupID uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'ingest_release_group'
		  AND payload->>'releaseGroupId' = $1::text
	`, releaseGroupID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Proving what a file is only pays off if the release it belongs to can reach
// the catalogue. Until it does there is no track to map the file to, and the
// proof sits unused.
func TestAProvenIdentityAsksForItsReleaseToBeIngested(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	recordingID, releaseGroupID := uuid.New(), uuid.New()
	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
		Releases: []musicbrainz.RecordingRelease{{
			ID: uuid.New(), ReleaseGroupID: releaseGroupID, Title: "Dummy", Status: "Official",
		}},
	}}}
	service := NewService(fixture.pool, provider, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if queued := fixture.ingestsQueuedFor(t, ctx, releaseGroupID); queued != 1 {
		t.Fatalf("ingests queued = %d, want 1", queued)
	}
}

// Candidates are a question. A question must not put an artist in the
// catalogue, because a release ingested for the wrong recording is a wrong
// answer nobody asked for.
func TestAnUnsettledFileIngestsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	first, second, releaseGroupID := uuid.New(), uuid.New(), uuid.New()
	length := 302000
	release := []musicbrainz.RecordingRelease{{
		ID: uuid.New(), ReleaseGroupID: releaseGroupID, Title: "Dummy", Status: "Official",
	}}
	provider := &stubProvider{results: []musicbrainz.Recording{
		{ID: first, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length, Releases: release},
		{ID: second, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length, Releases: release},
	}}
	service := NewService(fixture.pool, provider, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if status, _ := fixture.status(t, ctx); status != StatusNeedsReview {
		t.Fatalf("status = %q, want %q", status, StatusNeedsReview)
	}
	if queued := fixture.ingestsQueuedFor(t, ctx, releaseGroupID); queued != 0 {
		t.Fatalf("ingests queued = %d, want none for an unanswered question", queued)
	}

	// Answering it is what asks for the release.
	if err := service.Accept(ctx, fixture.fileID, second); err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	if queued := fixture.ingestsQueuedFor(t, ctx, releaseGroupID); queued != 1 {
		t.Fatalf("ingests queued after the decision = %d, want 1", queued)
	}
}

// Every ingest is a rate-limited provider request. A release the catalogue
// already holds is not worth one, because the answer is already known.
func TestAReleaseAlreadyInTheCatalogueIsNotIngestedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	recordingID, releaseGroupID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		WITH held_artist AS (
		    INSERT INTO artists (musicbrainz_id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		    RETURNING id
		)
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		SELECT id, $2, 'Dummy' FROM held_artist
	`, uuid.New(), releaseGroupID); err != nil {
		t.Fatal(err)
	}

	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
		Releases: []musicbrainz.RecordingRelease{{
			ID: uuid.New(), ReleaseGroupID: releaseGroupID, Title: "Dummy", Status: "Official",
		}},
	}}}
	service := NewService(fixture.pool, provider, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if queued := fixture.ingestsQueuedFor(t, ctx, releaseGroupID); queued != 0 {
		t.Fatalf("ingests queued = %d, want none for a release already held", queued)
	}
}

// A file the providers know nothing about is owned music with no external
// identity. There is no release behind it to ingest.
func TestALocalOnlyDecisionIngestsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop())

	if err := service.MarkLocalOnly(ctx, fixture.fileID); err != nil {
		t.Fatalf("MarkLocalOnly() error = %v", err)
	}
	var queued int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'ingest_release_group'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("ingests queued = %d, want none", queued)
	}
}

// stubListener stands in for fpcalc and AcoustID together. They are the process
// boundary here: one decodes a file the test has not got, the other is a
// rate-limited web service.
type stubListener struct {
	configured   bool
	fingerprints int
	looked       []string
	print        string
	acoustic     *Acoustic
	// rejectFirst is what the first lookup answers, for the tests where a kept
	// fingerprint turns out not to be one this listener can use.
	rejectFirst error
	// unreachable is the service being down rather than silent, which is the one
	// answer that is about Schall and not about the music.
	unreachable error
}

func (listener *stubListener) Configured(context.Context) bool { return listener.configured }

func (listener *stubListener) Fingerprint(_ context.Context, _ string) (string, error) {
	listener.fingerprints++
	return listener.print, nil
}

func (listener *stubListener) Lookup(_ context.Context, fingerprint string) (*Acoustic, error) {
	listener.looked = append(listener.looked, fingerprint)
	if listener.rejectFirst != nil && len(listener.looked) == 1 {
		return nil, listener.rejectFirst
	}
	if listener.unreachable != nil {
		return nil, listener.unreachable
	}
	return listener.acoustic, nil
}

// setupUntaggedFile is the file this path exists for: audio with nothing written
// on it, which no question about a tag can reach.
func setupUntaggedFile(t *testing.T, ctx context.Context) resolutionFixture {
	t.Helper()
	pool := dbtest.Setup(t)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/bought/track01.wav', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	return resolutionFixture{pool: pool, fileID: fileID}
}

func (fixture resolutionFixture) fingerprint(t *testing.T, ctx context.Context) string {
	t.Helper()
	var kept *string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT fingerprint FROM library_files WHERE id = $1
	`, fixture.fileID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept == nil {
		return ""
	}
	return *kept
}

func TestAFileWithNothingWrittenOnItIsIdentifiedByItsAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)

	recordingID := uuid.New()
	length := 302000
	provider := &stubProvider{recording: musicbrainz.Recording{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
	}}
	listener := &stubListener{
		configured: true, print: "302:AQADtEmSJEmSJEmS",
		acoustic: &Acoustic{Clusters: []AcousticCluster{
			{Recordings: []uuid.UUID{recordingID}, Similarity: 0.99},
		}},
	}
	service := NewService(fixture.pool, provider, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if status, _ := fixture.status(t, ctx); status != StatusResolved {
		t.Fatalf("status = %q, want %q", status, StatusResolved)
	}
	resolution, err := service.File(ctx, fixture.fileID)
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	if resolution.Identity == nil || resolution.Identity.RecordingID != recordingID {
		t.Fatalf("identity = %+v", resolution.Identity)
	}
}

// The fingerprint is derived from the audio and costs seconds of decoding, so it
// is written down where the next question can use it.
func TestAComputedFingerprintIsKept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)

	listener := &stubListener{
		configured: true, print: "302:AQADtEmSJEmSJEmS", acoustic: &Acoustic{},
	}
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if kept := fixture.fingerprint(t, ctx); kept != listener.print {
		t.Fatalf("fingerprint = %q, want %q", kept, listener.print)
	}
}

// A fingerprint already written down is used as it stands. Decoding the audio a
// second time would buy the same answer for another few seconds of CPU.
func TestAKeptFingerprintIsNotComputedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET fingerprint = '302:AQADtEmSJEmSJEmS' WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	listener := &stubListener{configured: true, acoustic: &Acoustic{}}
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if listener.fingerprints != 0 {
		t.Errorf("the audio was decoded again although its fingerprint was kept")
	}
	if len(listener.looked) != 1 || listener.looked[0] != "302:AQADtEmSJEmSJEmS" {
		t.Errorf("looked up %v, want the kept fingerprint", listener.looked)
	}
}

// What was kept is not a fingerprint this listener can use, which says nothing
// about the audio. The answer is to decode it again rather than to give up on the
// file for good.
func TestAKeptFingerprintNobodyCanReadIsComputedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files SET fingerprint = 'not a fingerprint' WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	listener := &stubListener{
		configured: true, print: "302:AQADtEmSJEmSJEmS", acoustic: &Acoustic{},
		rejectFirst: ErrFingerprintUnreadable,
	}
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if kept := fixture.fingerprint(t, ctx); kept != listener.print {
		t.Fatalf("fingerprint = %q, want the unreadable one replaced", kept)
	}
	if status, _ := fixture.status(t, ctx); status != StatusLocalOnly {
		t.Fatalf("status = %q, want the file answered rather than failed", status)
	}
}

// AcoustID has never heard most self-released music. An answer with nothing in it
// is a fact about the database, so the file stays what it was: owned music with no
// external identity, which is a true answer and not a failure to retry.
func TestAudioNobodyRecognisedLeavesTheFileAsItWas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)

	listener := &stubListener{
		configured: true, print: "302:AQADtEmSJEmSJEmS", acoustic: &Acoustic{},
	}
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if status, _ := fixture.status(t, ctx); status != StatusLocalOnly {
		t.Fatalf("status = %q, want %q", status, StatusLocalOnly)
	}
}

// A service that could not be reached said nothing about the music, and reading
// it as "nobody recognised this" would turn an outage into a permanent answer. It
// is reported as the failure it is, for the job to retry.
func TestAListenerThatCouldNotBeReachedIsNotSilence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)

	listener := &stubListener{
		configured: true, print: "302:AQADtEmSJEmSJEmS",
		unreachable: errors.New("connection refused"),
	}
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err == nil {
		t.Fatal("Resolve() error = nil, want the failure reported for retry")
	}

	if status, _ := fixture.status(t, ctx); status != StatusPending {
		t.Fatalf("status = %q, want the file left unanswered", status)
	}
}

// An installation that never entered a key resolves exactly the files it always
// did, from exactly the tags it always read, and leaves no trace of a check that
// did not run.
func TestAListenerThatIsSwitchedOffChangesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)

	listener := &stubListener{configured: false, print: "302:AQADtEmSJEmSJEmS"}
	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if status, _ := fixture.status(t, ctx); status != StatusLocalOnly {
		t.Fatalf("status = %q, want %q", status, StatusLocalOnly)
	}
	if listener.fingerprints != 0 || fixture.fingerprint(t, ctx) != "" {
		t.Errorf("a check that was switched off left a trace behind")
	}
}

// Listening answers what a file's tags could not, and nothing else. A file the
// tags identified is already decided, and re-grading it on new evidence could only
// overturn a decision that stands — so the audio is never put to it at all.
func TestAFileTheTagsIdentifiedIsNotListenedTo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: uuid.New(), Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
		Releases: []musicbrainz.RecordingRelease{{
			ID: uuid.New(), ReleaseGroupID: uuid.New(), Title: "Dummy", Status: "Official",
		}},
	}}}
	listener := &stubListener{configured: true, print: "302:AQADtEmSJEmSJEmS"}
	service := NewService(fixture.pool, provider, zerolog.Nop()).WithListener(listener)
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if status, _ := fixture.status(t, ctx); status != StatusResolved {
		t.Fatalf("status = %q, want %q", status, StatusResolved)
	}
	if listener.fingerprints != 0 {
		t.Errorf("a file its tags had already identified was listened to anyway")
	}
}

// The tagging pass rewrites an acquired file's tags the moment it is imported,
// so the next scan sees bytes it did not record and puts the file back in the
// resolution queue. By then matching has mapped it to its catalogue track, and
// resolution took the matched branch — which wrote its answer through the store
// that deletes every identity nobody decided by hand. The proof that admitted
// the copy went with it, and the want holding the file read the null as the
// library calling the file something else and stopped for ever.
func TestResolvingAMatchedFileKeepsTheIdentityItAlreadyHas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	recordingID := uuid.New()
	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 'Roads', 1, 10)
	`, trackID, albumID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method)
		VALUES ($1, $2, 'resolved_identity')
	`, trackID, fixture.fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, method, confidence,
		    is_manual, summary
		) VALUES ($1, 'external', $2, 'fingerprint', 1, false,
		          'Proven to be the wanted recording and imported.')
	`, fixture.fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	// What the scan leaves behind when the tagging pass has changed the bytes.
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE library_files
		SET match_status = 'matched', resolution_status = 'pending'
		WHERE id = $1
	`, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	service := NewService(fixture.pool, &stubProvider{}, zerolog.Nop())
	if err := service.Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	resolution, err := service.File(ctx, fixture.fileID)
	if err != nil {
		t.Fatalf("File() error = %v", err)
	}
	if resolution.Status != StatusResolved {
		t.Errorf("status = %q, want %q", resolution.Status, StatusResolved)
	}
	if resolution.Identity == nil || resolution.Identity.RecordingID != recordingID {
		t.Fatalf("identity = %+v, want the acquisition proof kept", resolution.Identity)
	}
}

// A want whose copy became this file stopped because the library called it
// something else, and it has no next look of its own. The library answering
// what the file is is the fact it was waiting for, so the want is given its
// look back and judged again by the rules a want reaching that state today is
// judged by. Nothing is admitted here: the want goes back to the same filing
// rules, which may well stop it again.
func TestResolvingAFileGivesBackTheLookOfTheWantHoldingIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupResolution(t, ctx)

	targetID, wantedID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at, recheck_reason
		)
		VALUES ($1, 'playlist', 'Portishead', 'Roads', $2, 'pending', 'isrc',
		        now(), NULL, 'library_identity')
	`, targetID, wantedID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary, library_file_id
		)
		VALUES ($1, 'slskd', 'peer', 'Music/Portishead/01 Roads.flac',
		        '01 Roads.flac', 1024, 'accepted', 'schall', 'Proven by its audio.', $2)
	`, targetID, fixture.fileID); err != nil {
		t.Fatal(err)
	}

	length := 302000
	provider := &stubProvider{results: []musicbrainz.Recording{{
		ID: uuid.New(), Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
		Releases: []musicbrainz.RecordingRelease{{
			ID: uuid.New(), ReleaseGroupID: uuid.New(), Title: "Dummy", Status: "Official",
		}},
	}}}
	if err := NewService(fixture.pool, provider, zerolog.Nop()).
		Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	var (
		next   *time.Time
		reason *string
	)
	if err := fixture.pool.QueryRow(ctx, `
		SELECT next_attempt_at, recheck_reason FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&next, &reason); err != nil {
		t.Fatal(err)
	}
	if next == nil {
		t.Error("the want has no next look; the library has just answered what its file is")
	}
	if reason != nil {
		t.Errorf("recheck_reason = %q, want nothing left to wait for", *reason)
	}
}

// A file the library has just proven is a witness the held copies of that same
// recording were judged without, so they are put back through validation. The
// wake reads resolution_status, so it has to run after the status is written:
// asking beside the identity queued nothing for a file that had just resolved.
//
// The file is settled by its audio, because that is one of the proofs a library
// file may speak on (db.settledProofs). A file identified by its paperwork
// speaks for nobody and wakes nothing.
func TestResolvingAFileJudgesTheHeldCopiesOfItsRecordingAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := setupUntaggedFile(t, ctx)

	recordingID, targetID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at
		)
		VALUES ($1, 'playlist', 'Portishead', 'Roads', $2, 'pending', 'isrc',
		        now(), now())
	`, targetID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary
		)
		VALUES ($1, 'slskd', 'peer', 'Music/Portishead/03.flac', '03.flac', 9000000,
		        'held', 'schall',
		        'Nothing could decide whether this copy is the wanted recording.')
	`, targetID); err != nil {
		t.Fatal(err)
	}

	length := 302000
	provider := &stubProvider{recording: musicbrainz.Recording{
		ID: recordingID, Title: "Roads", ArtistCredit: "Portishead", DurationMS: &length,
	}}
	listener := &stubListener{
		configured: true, print: "302:AQADtEmSJEmSJEmS",
		acoustic: &Acoustic{Clusters: []AcousticCluster{
			{Recordings: []uuid.UUID{recordingID}, Similarity: 0.99},
		}},
	}
	if err := NewService(fixture.pool, provider, zerolog.Nop()).
		WithListener(listener).Resolve(ctx, fixture.fileID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if status, _ := fixture.status(t, ctx); status != StatusResolved {
		t.Fatalf("status = %q, want the file settled", status)
	}
	var queued int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'judge_want_copies'
		  AND status = 'queued'
		  AND payload->>'acquisition_target_id' = $1::text
	`, targetID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued judges = %d, want the one the new witness asks for", queued)
	}
}
