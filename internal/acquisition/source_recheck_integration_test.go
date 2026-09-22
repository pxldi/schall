package acquisition

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// A file an anchor proved is asked about again on the unfound ladder, and moves
// onto a recording once MusicBrainz proves one (ADR 0037 §6).

// recheckProvider is MusicBrainz for these tests: the recordings it can look
// up, and what every search answers. It counts what it was asked, because a
// file nobody may ask about must cost no request.
type recheckProvider struct {
	recordings map[uuid.UUID]musicbrainz.Recording
	results    []musicbrainz.Recording
	asked      int
}

func (provider *recheckProvider) Recording(
	_ context.Context, id uuid.UUID,
) (musicbrainz.Recording, error) {
	provider.asked++
	found, ok := provider.recordings[id]
	if !ok {
		return musicbrainz.Recording{}, musicbrainz.ErrNotFound
	}
	return found, nil
}

func (provider *recheckProvider) ResolveRecordingID(
	_ context.Context, id uuid.UUID,
) (uuid.UUID, error) {
	provider.asked++
	if _, ok := provider.recordings[id]; !ok {
		return uuid.Nil, musicbrainz.ErrNotFound
	}
	return id, nil
}

func (provider *recheckProvider) SearchRecordings(
	context.Context, musicbrainz.RecordingQuery, int,
) ([]musicbrainz.Recording, error) {
	provider.asked++
	return provider.results, nil
}

// previewISRC is the code the want's Deezer preview was fetched by.
const previewISRC = "GBAAA9400123"

// sourceFile is one library file admitted on its want's anchor, due another
// look now, and the want it settled.
type sourceFile struct {
	fileID, targetID uuid.UUID
}

// admittedOnAnchor records a file with an automatic source identity from
// source and the acquired want it answered. taggedAs is the recording ID the
// file's own tags carry, or uuid.Nil for a file whose tags say nothing.
func admittedOnAnchor(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, source string, manual bool,
	taggedAs uuid.UUID,
) sourceFile {
	t.Helper()
	file := sourceFile{fileID: uuid.New(), targetID: uuid.New()}
	var tag any
	if taggedAs != uuid.Nil {
		tag = taggedAs
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, musicbrainz_recording_id,
		                           resolution_status)
		VALUES ($1, '/music/Portishead/Glory Box.flac', 1024, now(), $2, 'source')
	`, file.fileID, tag); err != nil {
		t.Fatal(err)
	}
	key, isrc, schedule := "deezer:2178654", any(previewISRC), any(time.Now().Add(-time.Second))
	serviceName, anchor := "deezer", "deezer"
	if source == "youtube-topic" {
		key, isrc, serviceName, anchor = "youtube:dQw4w9WgXcQ", nil, "youtube", "youtube-topic"
	}
	if manual {
		schedule = nil
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, provider, source, external_id, external_url, isrc,
		    method, confidence, is_manual, summary, source_recheck_after
		)
		VALUES ($1, 'source', $2, $2, $3, 'https://example.test/track', $4,
		        'published-sample', 1, $5, 'Proven by the anchor.', $6)
	`, file.fileID, serviceName, key, isrc, manual, schedule); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
		    id, origin, entry_artist, entry_title, entry_isrc, entry_duration_ms,
		    status, acquired_at, acquired_library_file_id,
		    anchor_fingerprint, anchor_source, anchor_reference, anchor_seconds
		)
		VALUES ($1, 'playlist', 'Portishead', 'Glory Box', $2, 301000,
		        'acquired', now(), $3, 'AQAAfingerprint', $4, '2178654', 30)
	`, file.targetID, previewISRC, file.fileID, anchor); err != nil {
		t.Fatal(err)
	}
	return file
}

// rechecking is the acquisition service with the real identity service asking
// the provider.
func rechecking(pool *pgxpool.Pool, provider *recheckProvider) *Service {
	return newTestService(pool).
		WithSourceRechecker(identity.NewService(pool, provider, zerolog.Nop()))
}

// identityOf reads what the library says a file is.
func identityOf(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID,
) (kind string, recording uuid.NullUUID, evidence string, attempts int, contradiction string) {
	t.Helper()
	var said *string
	if err := pool.QueryRow(ctx, `
		SELECT kind, musicbrainz_recording_id, evidence::text, source_recheck_attempts,
		       source_contradiction
		FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&kind, &recording, &evidence, &attempts, &said); err != nil {
		t.Fatal(err)
	}
	if said != nil {
		contradiction = *said
	}
	return kind, recording, evidence, attempts, contradiction
}

func TestASourceFileMovesOntoTheRecordingItsOwnTagsProve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	recordingID := uuid.New()
	found := recording(recordingID, "Glory Box", "Portishead", 301000, previewISRC)
	provider := &recheckProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{recordingID: found},
		results:    []musicbrainz.Recording{found},
	}
	file := admittedOnAnchor(ctx, t, pool, "deezer", false, recordingID)

	if err := rechecking(pool, provider).Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	kind, filed, evidence, _, _ := identityOf(ctx, t, pool, file.fileID)
	if kind != "external" || filed.UUID != recordingID {
		t.Fatalf("identity = %s %v, want the file on the recording its tags prove", kind, filed)
	}
	if !strings.Contains(evidence, "deezer:2178654") {
		t.Errorf("evidence = %s, want the source key kept", evidence)
	}
	want, err := newTestService(pool).Target(ctx, file.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if want.Status != "acquired" || want.MusicBrainzRecordingID.UUID != recordingID {
		t.Errorf("want = %s %v, want it acquired on the same recording", want.Status,
			want.MusicBrainzRecordingID)
	}
}

// Where the file proves nothing of its own, a Deezer preview was published for
// the recording its want resolves to when that recording carries the ISRC the
// preview was fetched by (ADR 0024).
func TestADeezerFileMovesOntoTheRecordingItsPreviewWasPublishedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	recordingID := uuid.New()
	found := recording(recordingID, "Glory Box", "Portishead", 301000, previewISRC)
	provider := &recheckProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{recordingID: found},
		results:    []musicbrainz.Recording{found},
	}
	file := admittedOnAnchor(ctx, t, pool, "deezer", false, uuid.Nil)

	if err := rechecking(pool, provider).Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	kind, filed, _, _, _ := identityOf(ctx, t, pool, file.fileID)
	if kind != "external" || filed.UUID != recordingID {
		t.Fatalf("identity = %s %v, want the file on the recording its preview was published for",
			kind, filed)
	}
	var method string
	if err := pool.QueryRow(ctx, `
		SELECT method FROM library_file_identities WHERE library_file_id = $1
	`, file.fileID).Scan(&method); err != nil {
		t.Fatal(err)
	}
	if method != identity.MethodPublishedSample {
		t.Errorf("method = %q, want the published sample named as the proof", method)
	}
}

// A Topic upload was found by name and names no recording, so the file waits
// for its own proof. It climbs a rung.
func TestATopicFileDoesNotMoveOnItsWantsResolutionAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	recordingID := uuid.New()
	found := recording(recordingID, "Glory Box", "Portishead", 301000, previewISRC)
	provider := &recheckProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{recordingID: found},
		results:    []musicbrainz.Recording{found},
	}
	file := admittedOnAnchor(ctx, t, pool, "youtube-topic", false, uuid.Nil)

	if err := rechecking(pool, provider).Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	kind, _, _, attempts, _ := identityOf(ctx, t, pool, file.fileID)
	if kind != "source" || attempts != 1 {
		t.Fatalf("identity = %s after %d looks, want the source identity kept and one look counted",
			kind, attempts)
	}
	var next time.Time
	if err := pool.QueryRow(ctx, `
		SELECT source_recheck_after FROM library_file_identities WHERE library_file_id = $1
	`, file.fileID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(next); wait < 71*time.Hour || wait > 73*time.Hour {
		t.Errorf("next look in %v, want the second rung, three days", wait)
	}
	want, err := newTestService(pool).Target(ctx, file.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if want.MusicBrainzRecordingID.Valid {
		t.Errorf("want recording = %v, want none while its file waits", want.MusicBrainzRecordingID)
	}
}

// A person's source identity is never asked about again (ADR 0026 §2).
func TestAManualSourceIdentityIsNeverAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	recordingID := uuid.New()
	found := recording(recordingID, "Glory Box", "Portishead", 301000, previewISRC)
	provider := &recheckProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{recordingID: found},
		results:    []musicbrainz.Recording{found},
	}
	file := admittedOnAnchor(ctx, t, pool, "deezer", true, recordingID)

	service := rechecking(pool, provider)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	result, err := service.sources.RecheckSource(ctx, file.fileID, nil)
	if err != nil {
		t.Fatalf("RecheckSource() error = %v", err)
	}

	if result.Moved || provider.asked != 0 {
		t.Fatalf("moved = %v after %d requests, want a person's identity left unasked",
			result.Moved, provider.asked)
	}
	if kind, _, _, _, _ := identityOf(ctx, t, pool, file.fileID); kind != "source" {
		t.Fatalf("identity = %s, want the person's source identity kept", kind)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_file_identities SET source_recheck_after = now()
		WHERE library_file_id = $1
	`, file.fileID); err == nil {
		t.Error("the schema let a person's source identity be scheduled for another look")
	}
}

// A recording that does not carry the ISRC the preview was fetched by is not
// the track the file was proven to be. The file stays as it is and the
// contradiction is written on it.
func TestAContradictionLeavesTheSourceIdentityInPlace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	recordingID := uuid.New()
	other := recording(recordingID, "Glory Box", "Portishead", 301000, "USAAA0000001")
	provider := &recheckProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{recordingID: other},
	}
	file := admittedOnAnchor(ctx, t, pool, "deezer", false, recordingID)

	if err := rechecking(pool, provider).Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	kind, filed, _, attempts, contradiction := identityOf(ctx, t, pool, file.fileID)
	if kind != "source" || filed.Valid {
		t.Fatalf("identity = %s %v, want the source identity left in place", kind, filed)
	}
	if !strings.Contains(contradiction, previewISRC) || attempts != 1 {
		t.Errorf("contradiction = %q after %d looks, want the ISRC it failed on written down",
			contradiction, attempts)
	}
	want, err := newTestService(pool).Target(ctx, file.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if want.MusicBrainzRecordingID.Valid {
		t.Errorf("want recording = %v, want none", want.MusicBrainzRecordingID)
	}
}
