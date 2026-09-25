package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

const testRecommendationSource = "listenbrainz"

var testObservedAt = time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)

func recommendationService(t *testing.T) (*Service, *db.Queries, *pgxpool.Pool, func(time.Time)) {
	t.Helper()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	service := NewService(queries, "schall-test/0.0 (test)", zerolog.Nop())
	now := testObservedAt
	service.now = func() time.Time { return now }
	return service, queries, pool, func(value time.Time) { now = value }
}

func recommendationCandidate(
	recordingID, releaseGroupID uuid.UUID, artistIDs ...uuid.UUID,
) CandidateInput {
	return CandidateInput{
		MusicBrainzRecordingID:    recordingID,
		MusicBrainzReleaseGroupID: releaseGroupID,
		MusicBrainzArtistIDs:      artistIDs,
		RecordingTitle:            "A recording",
		ReleaseTitle:              "A release",
		ArtistName:                "An artist",
		ReasonCodes:               []string{"cf_raw"},
		ReasonContext:             map[string]any{"model": "test-model"},
	}
}

func replaceRecommendationCandidates(t *testing.T, service *Service, candidates ...CandidateInput) {
	t.Helper()
	if err := service.ReplaceSnapshot(context.Background(), Snapshot{
		Source:           testRecommendationSource,
		SourceSnapshotID: "model-1",
		Status:           "complete",
		FetchedAt:        testObservedAt,
		Candidates:       candidates,
	}); err != nil {
		t.Fatalf("ReplaceSnapshot() error = %v", err)
	}
}

func evaluatedRules(t *testing.T, service *Service) map[uuid.UUID]SuppressionRule {
	t.Helper()
	rows, err := service.Evaluate(context.Background(), testRecommendationSource)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	result := make(map[uuid.UUID]SuppressionRule, len(rows))
	for _, row := range rows {
		result[row.MusicBrainzRecordingID] = row.SuppressedBy
	}
	return result
}

func TestOwnedSuppressionUsesPresentDecidedEvidenceOnly(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID, releaseGroupID, artistID := uuid.New(), uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, releaseGroupID, artistID))

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
			id, path, size_bytes, modified_at, musicbrainz_recording_id
		) VALUES ($1, '/music/raw-tag.flac', 10, $2, $3)
	`, fileID, testObservedAt, recordingID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("raw file tag suppressed by %q, want no rule", got)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'recording_mbid', 'identified')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedOwned {
		t.Fatalf("decided identity suppressed by %q, want owned", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE library_files SET missing_at = $2 WHERE id = $1`, fileID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("missing identity file suppressed by %q, want no rule", got)
	}

	// A present file mapped on another release's track carries the recording
	// and therefore answers ownership for this candidate too.
	localArtistID, albumID, trackID, mappedFileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
		VALUES ($1, $2, 'Local artist', 'Local artist', NULL, 'held by the library')
	`, localArtistID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3, 'Another release')
	`, albumID, localArtistID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title)
		VALUES ($1, $2, $3, 'The same recording')
	`, trackID, albumID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/mapped.flac', 10, $2)
	`, mappedFileID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, mappedFileID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedOwned {
		t.Fatalf("carried recording suppressed by %q, want owned", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE library_files SET missing_at = $2 WHERE id = $1`, mappedFileID, testObservedAt); err != nil {
		t.Fatal(err)
	}

	// Release ownership is the equivalent decided identity probe, independent
	// of whether the identity names this particular recording.
	releaseFileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/release.flac', 10, $2)
	`, releaseFileID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id,
			musicbrainz_release_group_id, method, summary
		) VALUES ($1, 'external', $2, $3, 'recording_mbid', 'identified')
	`, releaseFileID, uuid.New(), releaseGroupID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedOwned {
		t.Fatalf("owned release suppressed by %q, want owned", got)
	}
}

// The fourth way the library can already hold a suggestion: no identity row at
// all, only a file mapped to a track, and it is that track's album that names
// the release group. It is the one ownership path the test above never walks,
// and the one a reader notices when it breaks — a suggestion from a release
// they own, offered back to them.
func TestOwnedSuppressionFollowsTheMappedTrackToItsAlbum(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID, releaseGroupID := uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, releaseGroupID, uuid.New()))

	artistID, albumID, trackID, fileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, catalogue_summary)
		VALUES ($1, $2, 'Local artist', 'Local artist', 'held by the library')
	`, artistID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3, 'The suggested release')
	`, albumID, artistID, releaseGroupID); err != nil {
		t.Fatal(err)
	}
	// Another recording from the same release: ownership here is the release's,
	// not this recording's.
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title)
		VALUES ($1, $2, $3, 'Another recording')
	`, trackID, albumID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/same-release.flac', 10, $2)
	`, fileID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("unmapped file suppressed by %q, want no rule", got)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedOwned {
		t.Fatalf("album release group suppressed by %q, want owned", got)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET missing_at = $2 WHERE id = $1`, fileID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("missing mapped file suppressed by %q, want no rule", got)
	}
}

func TestRequestedSuppressionIncludesOnlyActiveTargets(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), uuid.New()))

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_title, musicbrainz_recording_id,
			resolution_method, resolved_at, status, summary
		) VALUES ($1, 'manual', 'Wanted', $2, 'manual', $3, 'pending', 'wanted')
	`, targetID, recordingID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedRequested {
		t.Fatalf("active target suppressed by %q, want requested", got)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET status = 'searching', updated_at = $2 WHERE id = $1
	`, targetID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedRequested {
		t.Fatalf("searching target suppressed by %q, want requested", got)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'awaiting_review', review_reason = 'candidate', updated_at = $2
		WHERE id = $1
	`, targetID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedRequested {
		t.Fatalf("target awaiting review suppressed by %q, want requested", got)
	}
}

// A want can be marked not wanted, which stops the search. It is a decision
// about acquisition and not about taste, so it is its own reason rather than a
// dismissal — but the list still holds the recording back, because the only two
// things the list offers are "find me a copy" and "never suggest this again",
// and somebody who already answered the first has nothing left to press
// (issue #338).
func TestAWantSomebodyStoppedHoldsItsRecordingBack(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), uuid.New()))

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_title, musicbrainz_recording_id,
			resolution_method, resolved_at, status, summary, not_wanted_at
		) VALUES ($1, 'manual', 'Stopped', $2, 'manual', $3, 'not_wanted', 'stopped', $3)
	`, targetID, recordingID, testObservedAt); err != nil {
		t.Fatal(err)
	}

	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedNotWanted {
		t.Fatalf("not_wanted target suppressed by %q, want not_wanted", got)
	}

	// Taking the decision back is done where it was taken, and the suggestion
	// comes back with it.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'pending', not_wanted_at = NULL, updated_at = $2 WHERE id = $1
	`, targetID, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedRequested {
		t.Fatalf("target wanted again suppressed by %q, want requested", got)
	}
}

// A song the weekly playlist obtained, the reader had for a week and nobody
// kept is held back for a while. It is the sixth rule, and it is the weakest
// permanent-looking one on purpose: the reader never said "not interested".
func TestARecordingNobodyKeptIsHeldBackUntilItsWindowEnds(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), uuid.New()))

	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("a suggestion nothing has happened to was suppressed by %q", got)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO recommendation_unkept (
			musicbrainz_recording_id, unkept_at, suppressed_until
		) VALUES ($1, $2, $3)
	`, recordingID, testObservedAt, testObservedAt.Add(180*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedUnkept {
		t.Fatalf("an unkept recording suppressed by %q, want unkept", got)
	}
}

func TestARecordingNobodyKeptIsOfferedAgainOnceItsWindowHasPassed(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), uuid.New()))

	if _, err := pool.Exec(ctx, `
		INSERT INTO recommendation_unkept (
			musicbrainz_recording_id, unkept_at, suppressed_until
		) VALUES ($1, $2, $3)
	`, recordingID, testObservedAt.Add(-200*24*time.Hour), testObservedAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("a window that has passed still suppressed the suggestion, by %q", got)
	}
}

func TestAnyFollowedCreditSuppressesAndAnUnfollowRemovesTheRule(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID, primaryID, guestID := uuid.New(), uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), primaryID, guestID))

	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, 'Followed guest', 'Followed guest')
	`, guestID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedFollowedArtist {
		t.Fatalf("followed guest credit suppressed by %q, want followed_artist", got)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE artists SET followed_at = NULL, catalogue_summary = 'catalogue only'
		WHERE musicbrainz_id = $1
	`, guestID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("unfollowed credit suppressed by %q, want no rule", got)
	}
}

// A release from a label the reader follows is suppressed the same way one
// from a followed artist is: the follow feed already covers it, so this list
// offering it too would be an offer to fetch it twice.
func TestAFollowedLabelSuppressesItsRelease(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID, releaseGroupID, artistID := uuid.New(), uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, releaseGroupID, artistID))

	// followed_at defaults to now() on this table, so the artist is told apart
	// as held rather than followed the way seedFollowedRelease does it: NULL,
	// set explicitly.
	var artistRowID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
		VALUES ($1, 'An unfollowed artist', 'An unfollowed artist', NULL,
			'held because the library has their music')
		RETURNING id
	`, artistID).Scan(&artistRowID); err != nil {
		t.Fatal(err)
	}
	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO albums (artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, 'A release')
		RETURNING id
	`, artistRowID, releaseGroupID).Scan(&albumID); err != nil {
		t.Fatal(err)
	}

	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("suppressed by %q before any label followed it, want no rule", got)
	}

	var labelID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO labels (musicbrainz_id, name, followed_at)
		VALUES ($1, 'A followed label', $2)
		RETURNING id
	`, uuid.New(), testObservedAt).Scan(&labelID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO label_releases (label_id, album_id) VALUES ($1, $2)
	`, labelID, albumID); err != nil {
		t.Fatal(err)
	}

	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedFollowedLabel {
		t.Fatalf("suppressed by %q, want followed_label", got)
	}

	if _, err := pool.Exec(ctx, `UPDATE labels SET followed_at = NULL WHERE id = $1`, labelID); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("unfollowed label's release suppressed by %q, want no rule", got)
	}
}

func TestDismissalsApplyOnlyAtTheLevelChosen(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := recommendationService(t)
	artistID, otherArtistID := uuid.New(), uuid.New()
	recordingA, recordingB, recordingC := uuid.New(), uuid.New(), uuid.New()
	releaseA, releaseB, releaseC := uuid.New(), uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingA, releaseA, artistID),
		recommendationCandidate(recordingB, releaseB, artistID),
		recommendationCandidate(recordingC, releaseC, otherArtistID),
	)

	if _, err := service.Dismiss(ctx, DismissRecording, recordingA); err != nil {
		t.Fatal(err)
	}
	rules := evaluatedRules(t, service)
	if rules[recordingA] != SuppressedDismissed || rules[recordingB] != "" || rules[recordingC] != "" {
		t.Fatalf("recording dismissal rules = %v", rules)
	}
	listed, err := service.List(ctx, testRecommendationSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].MusicBrainzRecordingID == recordingA ||
		listed[1].MusicBrainzRecordingID == recordingA || listed[0].Rank >= listed[1].Rank {
		t.Fatalf("renderable list after dismissal = %#v", listed)
	}
	if _, err := service.Dismiss(ctx, DismissArtist, artistID); err != nil {
		t.Fatal(err)
	}
	rules = evaluatedRules(t, service)
	if rules[recordingB] != SuppressedDismissed || rules[recordingC] != "" {
		t.Fatalf("artist dismissal rules = %v", rules)
	}
	if _, err := service.Dismiss(ctx, DismissRelease, releaseC); err != nil {
		t.Fatal(err)
	}
	if got := evaluatedRules(t, service)[recordingC]; got != SuppressedDismissed {
		t.Fatalf("release dismissal suppressed by %q, want dismissed", got)
	}
}

func TestRecommendationFeedbackAppendsAndCopiesCurrentCandidateContext(t *testing.T) {
	ctx := context.Background()
	service, queries, _, _ := recommendationService(t)
	recordingID, releaseID, artistID, seedID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service, CandidateInput{
		MusicBrainzRecordingID:    recordingID,
		MusicBrainzReleaseGroupID: releaseID,
		MusicBrainzArtistIDs:      []uuid.UUID{artistID},
		RecordingTitle:            "A recording",
		ArtistName:                "An artist",
		ReasonCodes:               []string{"similar_to"},
		ReasonContext:             map[string]any{"seed_recording_ids": []string{seedID.String()}},
	})

	first, err := service.RecordFeedback(ctx, testRecommendationSource, recordingID, FeedbackMoreLikeThis)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RecordFeedback(ctx, testRecommendationSource, recordingID, FeedbackLessLikeThis)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("feedback IDs = %s and %s, want two events", first.ID, second.ID)
	}
	rows, err := queries.ListRecommendationFeedback(ctx, testRecommendationSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("feedback rows = %d, want 2", len(rows))
	}
	// The context comes back as jsonb, which Postgres re-serialises with its own
	// spacing, so it is compared as a decoded document rather than as bytes.
	wantContext := map[string]any{"seed_recording_ids": []any{seedID.String()}}
	for _, row := range rows {
		var context map[string]any
		if err := json.Unmarshal(row.ReasonContext, &context); err != nil {
			t.Fatalf("decode feedback context %s: %v", row.ReasonContext, err)
		}
		if row.MusicbrainzRecordingID != recordingID ||
			row.MusicbrainzReleaseGroupID != releaseID ||
			!reflect.DeepEqual(row.MusicbrainzArtistIds, []uuid.UUID{artistID}) ||
			!reflect.DeepEqual(row.ReasonCodes, []string{"similar_to"}) ||
			!reflect.DeepEqual(context, wantContext) {
			t.Fatalf("feedback row = %#v", row)
		}
	}

	replaceRecommendationCandidates(t, service, recommendationCandidate(uuid.New(), uuid.New(), uuid.New()))
	if _, err := service.RecordFeedback(ctx, testRecommendationSource, recordingID, FeedbackMoreLikeThis); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale feedback error = %v, want pgx.ErrNoRows", err)
	}
	rows, err = queries.ListRecommendationFeedback(ctx, testRecommendationSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("feedback rows after stale press = %d, want 2", len(rows))
	}
}

func TestRecommendationFeedbackCanBeClearedWithoutClearingDismissals(t *testing.T) {
	ctx := context.Background()
	service, queries, _, _ := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service, recommendationCandidate(recordingID, uuid.New(), uuid.New()))
	if _, err := service.RecordFeedback(ctx, testRecommendationSource, recordingID, FeedbackMoreLikeThis); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dismiss(ctx, DismissRecording, recordingID); err != nil {
		t.Fatal(err)
	}
	if err := service.ClearFeedback(ctx, testRecommendationSource); err != nil {
		t.Fatal(err)
	}
	rows, err := queries.ListRecommendationFeedback(ctx, testRecommendationSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("feedback rows after clear = %d, want 0", len(rows))
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedDismissed {
		t.Fatalf("dismissal after clear = %q, want dismissed", got)
	}
}

func TestImpressionFatigueCountsThreeWithAGapThenRestartsAfterNinetyDays(t *testing.T) {
	ctx := context.Background()
	service, _, _, setNow := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), uuid.New()))

	first, err := service.RecordImpression(ctx, recordingID)
	if err != nil {
		t.Fatal(err)
	}
	setNow(testObservedAt.Add(time.Hour))
	repeated, err := service.RecordImpression(ctx, recordingID)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.IgnoredImpressions != 1 || !repeated.LastShownAt.Time.Equal(first.LastShownAt.Time) {
		t.Fatalf("inside-gap impression = %#v, want first row unchanged", repeated)
	}

	setNow(testObservedAt.Add(24 * time.Hour))
	second, err := service.RecordImpression(ctx, recordingID)
	if err != nil {
		t.Fatal(err)
	}
	if second.IgnoredImpressions != 2 || second.SuppressedUntil.Valid {
		t.Fatalf("second impression = %#v", second)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("second impression suppressed by %q", got)
	}

	thirdAt := testObservedAt.Add(48 * time.Hour)
	setNow(thirdAt)
	third, err := service.RecordImpression(ctx, recordingID)
	if err != nil {
		t.Fatal(err)
	}
	if third.IgnoredImpressions != 3 || !third.SuppressedUntil.Valid ||
		!third.SuppressedUntil.Time.Equal(thirdAt.Add(impressionSuppression)) {
		t.Fatalf("third impression = %#v", third)
	}
	if got := evaluatedRules(t, service)[recordingID]; got != SuppressedImpressionFatigue {
		t.Fatalf("third impression suppressed by %q, want impression_fatigue", got)
	}

	setNow(third.SuppressedUntil.Time)
	if got := evaluatedRules(t, service)[recordingID]; got != "" {
		t.Fatalf("lapsed window suppressed by %q, want no rule", got)
	}
	restarted, err := service.RecordImpression(ctx, recordingID)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.IgnoredImpressions != 1 || restarted.SuppressedUntil.Valid {
		t.Fatalf("restarted cycle = %#v, want one impression and a cleared window", restarted)
	}
}

// The fatigue count decides whether a suggestion is held back for ninety days,
// and the route that writes it takes any UUID a caller sends. Only a recording
// a stored list offers may be counted: nothing else could have shown it
// (issue #327).
func TestOnlyARecordingTheStoredListOffersIsCountedAsShown(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	listed, unlisted := uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(listed, uuid.New(), uuid.New()))

	recorded, err := service.RecordImpressions(ctx, []uuid.UUID{listed, unlisted})
	if err != nil {
		t.Fatalf("RecordImpressions() error = %v", err)
	}
	if recorded != 1 {
		t.Fatalf("recorded = %d, want only the recording the list offers", recorded)
	}

	var counted []uuid.UUID
	rows, err := pool.Query(ctx, `SELECT musicbrainz_recording_id FROM recommendation_impressions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var recordingID uuid.UUID
		if err := rows.Scan(&recordingID); err != nil {
			t.Fatal(err)
		}
		counted = append(counted, recordingID)
	}
	if len(counted) != 1 || counted[0] != listed {
		t.Fatalf("impressions = %v, want only %v", counted, listed)
	}
}

func TestReviewQueueRejectionDoesNotAffectRecommendations(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), uuid.New()))
	before := evaluatedRules(t, service)[recordingID]

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (id, origin, entry_title, status, summary)
		VALUES ($1, 'manual', 'A question', 'unresolved', 'waiting')
	`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_rejected_recordings (
			acquisition_target_id, musicbrainz_recording_id, summary
		) VALUES ($1, $2, 'wrong target')
	`, targetID, recordingID); err != nil {
		t.Fatal(err)
	}
	after := evaluatedRules(t, service)[recordingID]
	if before != "" || after != before {
		t.Fatalf("rule before rejection = %q, after = %q", before, after)
	}
}

func TestRecommendationFeedbackNeverSuppresses(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, uuid.New(), uuid.New()))
	if _, err := service.RecordFeedback(ctx, testRecommendationSource, recordingID, FeedbackLessLikeThis); err != nil {
		t.Fatal(err)
	}

	assertUnchanged := func(label string) {
		t.Helper()
		listed, err := service.List(ctx, testRecommendationSource)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].MusicBrainzRecordingID != recordingID {
			t.Fatalf("%s list = %#v, want the candidate", label, listed)
		}
		rows, err := service.Evaluate(ctx, testRecommendationSource)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].SuppressedBy != "" {
			t.Fatalf("%s evaluation = %#v, want no suppression", label, rows)
		}
	}
	assertUnchanged("feedback")

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (id, origin, entry_title, status, summary)
		VALUES ($1, 'manual', 'A question', 'unresolved', 'waiting')
	`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_rejected_recordings (
			acquisition_target_id, musicbrainz_recording_id, summary
		) VALUES ($1, $2, 'wrong target')
	`, targetID, recordingID); err != nil {
		t.Fatal(err)
	}
	assertUnchanged("feedback and review rejection")
}

func TestSnapshotReplacementIsAtomicAndConstraintsRejectMalformedArrays(t *testing.T) {
	ctx := context.Background()
	service, queries, _, _ := recommendationService(t)
	recordingID, releaseID, artistID := uuid.New(), uuid.New(), uuid.New()
	replaceRecommendationCandidates(t, service,
		recommendationCandidate(recordingID, releaseID, artistID))
	stored, err := queries.RecommendationCandidates(ctx, testRecommendationSource)
	if err != nil || len(stored) != 1 {
		t.Fatalf("RecommendationCandidates() = %#v, %v", stored, err)
	}

	newFetchedAt := testObservedAt.Add(time.Hour)
	header := db.UpsertRecommendationSnapshotParams{
		Source:    testRecommendationSource,
		Status:    "complete",
		FetchedAt: pgtype.Timestamptz{Time: newFetchedAt, Valid: true},
	}
	malformed := db.InsertRecommendationCandidateParams{
		MusicbrainzRecordingID:    uuid.New(),
		MusicbrainzReleaseGroupID: uuid.New(),
		MusicbrainzArtistIds:      []uuid.UUID{},
		RecordingTitle:            "Malformed",
		ArtistName:                "Nobody",
		Rank:                      1,
		ReasonCodes:               []string{"cf_raw"},
		ReasonContext:             []byte(`{}`),
	}
	if err := queries.ReplaceRecommendationSnapshot(ctx, header, []db.InsertRecommendationCandidateParams{malformed}); err == nil {
		t.Fatal("ReplaceRecommendationSnapshot() accepted an empty artist credit")
	}
	snapshot, err := queries.RecommendationSnapshot(ctx, testRecommendationSource)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = queries.RecommendationCandidates(ctx, testRecommendationSource)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.FetchedAt.Time.Equal(testObservedAt) || len(stored) != 1 ||
		stored[0].MusicbrainzRecordingID != recordingID {
		t.Fatalf("failed replacement changed snapshot %#v or candidates %#v", snapshot, stored)
	}

	malformed.MusicbrainzArtistIds = []uuid.UUID{uuid.New()}
	malformed.ReasonCodes = []string{""}
	if _, err := queries.InsertRecommendationCandidate(ctx, malformed); err == nil {
		t.Fatal("InsertRecommendationCandidate() accepted an empty reason code")
	}

	if err := service.ReplaceSnapshot(ctx, Snapshot{
		Source: testRecommendationSource, Status: "complete", FetchedAt: newFetchedAt,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err = queries.RecommendationCandidates(ctx, testRecommendationSource)
	if err != nil || len(stored) != 0 {
		t.Fatalf("successful empty replacement = %#v, %v", stored, err)
	}
}

// The recommendation list is read from one snapshot, which a sweep rebuilds over
// many bounded passes across about half an hour. Reading the number of records
// in it once a pass, from the first pass to the last, it never goes down: the
// list somebody sees is yesterday's whole answer until today's whole answer
// replaces it (issue #305).
func TestThePublishedRecommendationListNeverShrinksWhileASweepRuns(t *testing.T) {
	ctx := context.Background()
	service, queries, _, _ := recommendationService(t)
	yesterday := make([]CandidateInput, 0, 3*maximumCandidateExpansions)
	for range 3 * maximumCandidateExpansions {
		yesterday = append(yesterday,
			recommendationCandidate(uuid.New(), uuid.New(), uuid.New()))
	}
	replaceRecommendationCandidates(t, service, yesterday...)
	answer, provider := listeningAccount(t, service, queries)
	offerRecordings(answer, provider, 3*maximumCandidateExpansions)

	position := SweepPosition{}
	for pass := 1; ; pass++ {
		result, err := service.SweepPage(ctx, position)
		if err != nil {
			t.Fatalf("pass %d SweepPage() error = %v", pass, err)
		}
		published, err := queries.RecommendationCandidates(ctx, testRecommendationSource)
		if err != nil {
			t.Fatalf("pass %d RecommendationCandidates() error = %v", pass, err)
		}
		if len(published) < len(yesterday) {
			t.Fatalf("pass %d left %d records published, want no fewer than yesterday's %d",
				pass, len(published), len(yesterday))
		}
		if !result.Resume {
			break
		}
		position = result.Position
		if pass == 2*len(yesterday) {
			t.Fatalf("the chain had not finished after %d passes", pass)
		}
	}

	published, err := queries.RecommendationCandidates(ctx, testRecommendationSource)
	if err != nil {
		t.Fatalf("RecommendationCandidates() error = %v", err)
	}
	kept := make(map[uuid.UUID]struct{}, len(yesterday))
	for _, candidate := range yesterday {
		kept[candidate.MusicBrainzRecordingID] = struct{}{}
	}
	for _, candidate := range published {
		if _, old := kept[candidate.MusicbrainzRecordingID]; old {
			t.Fatalf("record %s is still published, want the finished sweep's own list",
				candidate.MusicbrainzRecordingID)
		}
	}
}

// A chain that never reaches its end leaves the snapshot it was building behind.
// Nobody can read it, but the next chain must not take it for its own work, and
// it must not stay in the database for ever.
func TestAChainDiscardsWhatAnAbandonedChainWasBuilding(t *testing.T) {
	ctx := context.Background()
	service, queries, _, _ := recommendationService(t)
	answer, provider := listeningAccount(t, service, queries)
	offerRecordings(answer, provider, maximumCandidateExpansions+5)
	abandoned, err := service.SweepPage(ctx, SweepPosition{})
	if err != nil {
		t.Fatalf("first pass SweepPage() error = %v", err)
	}
	if !abandoned.Resume {
		t.Fatalf("first pass = %#v, want a chain with more to do", abandoned)
	}

	offerRecordings(answer, provider, 1)
	result, err := service.SweepPage(ctx, SweepPosition{})
	if err != nil {
		t.Fatalf("fresh chain SweepPage() error = %v", err)
	}
	if !result.Report.Published {
		t.Fatalf("fresh chain = %#v, want a short list walked and published", result)
	}

	published, err := queries.RecommendationCandidates(ctx, testRecommendationSource)
	if err != nil {
		t.Fatalf("RecommendationCandidates() error = %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("published %d records, want only the fresh chain's one", len(published))
	}
	staged, err := queries.RecommendationCandidates(ctx, stagedSource(testRecommendationSource))
	if err != nil {
		t.Fatalf("RecommendationCandidates() error = %v", err)
	}
	if len(staged) != 0 {
		t.Fatalf("staged %d records, want nothing left beside the published list", len(staged))
	}
	if _, err := queries.RecommendationSnapshot(ctx, stagedSource(testRecommendationSource)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("staged snapshot error = %v, want no staged snapshot at all", err)
	}
}

// A sweep that is named no records at all leaves the published list alone. Both
// ListenBrainz services answer that way when they are quiet, and an account with
// nothing to suggest is answered the same way, so publishing it would delete a
// whole list on the strength of an answer nobody can read (issue #317).
func TestASweepNamedNoRecordsAtAllKeepsThePublishedList(t *testing.T) {
	ctx := context.Background()
	service, queries, _, _ := recommendationService(t)
	yesterday := recommendationCandidate(uuid.New(), uuid.New(), uuid.New())
	replaceRecommendationCandidates(t, service, yesterday)
	// The account is configured and both services are asked; neither names a
	// record, because offerRecordings is not called.
	listeningAccount(t, service, queries)

	result, err := service.SweepPage(ctx, SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if result.Report.Published || !result.Report.KeptPublished {
		t.Fatalf("result = %#v, want nothing published and the list kept", result)
	}
	published, err := queries.RecommendationCandidates(ctx, testRecommendationSource)
	if err != nil {
		t.Fatalf("RecommendationCandidates() error = %v", err)
	}
	if len(published) != 1 ||
		published[0].MusicbrainzRecordingID != yesterday.MusicBrainzRecordingID {
		t.Fatalf("published = %#v, want yesterday's one record unchanged", published)
	}
}

// listeningAccount stores an enabled ListenBrainz account and points the service
// at a ListenBrainz that offers whatever recordings the returned answer holds,
// with a MusicBrainz that knows every one of them.
func listeningAccount(
	t *testing.T, service *Service, queries *db.Queries,
) (*[]map[string]any, *fakeRecordingProvider) {
	t.Helper()
	if _, err := queries.SaveListenBrainzSettings(context.Background(), db.SaveListenBrainzSettingsParams{
		Username: "listener", SimilarityAlgorithm: "session_based", Enabled: true,
	}); err != nil {
		t.Fatalf("save ListenBrainz settings: %v", err)
	}
	answer := make([]map[string]any, 0, 4)
	provider := &fakeRecordingProvider{recordings: make(map[uuid.UUID]musicbrainz.Recording)}
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/1/cf/recommendation/user/listener/recording":
				_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
					"mbids": answer, "model_id": "sweeping-model",
				}})
			case "/1/stats/user/listener/recordings":
				response.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(response, request)
			}
		}))
	t.Cleanup(server.Close)
	service.WithEndpoints(server.URL, server.URL, server.Client()).WithRecordingProvider(provider)
	return &answer, provider
}

// offerRecordings replaces what ListenBrainz offers with count new recordings
// MusicBrainz can expand, scored downwards so a bounded pass takes them in the
// order they were built.
func offerRecordings(answer *[]map[string]any, provider *fakeRecordingProvider, count int) {
	*answer = make([]map[string]any, 0, count)
	for index := range count {
		recordingID := uuid.New()
		provider.recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Offered recording",
		)
		*answer = append(*answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(count - index),
		})
	}
}

func TestConfiguredSweepPopulatesTheCandidateStoreEndToEnd(t *testing.T) {
	ctx := context.Background()
	service, queries, _, _ := recommendationService(t)
	recordingID, releaseGroupID, artistID := uuid.New(), uuid.New(), uuid.New()
	if _, err := queries.SaveListenBrainzSettings(ctx, db.SaveListenBrainzSettingsParams{
		Username: "listener", SimilarityAlgorithm: "session_based", Enabled: true,
	}); err != nil {
		t.Fatalf("save ListenBrainz settings: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_, _ = response.Write([]byte(`{"payload":{"mbids":[{"recording_mbid":"` +
				recordingID.String() + `","score":0.95}],"model_id":"model-live"}}`))
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	service.WithEndpoints(server.URL, server.URL, server.Client()).
		WithRecordingProvider(&fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
			recordingID: expandedRecording(recordingID, releaseGroupID, artistID, "Roads"),
		}})

	if _, err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	snapshot, err := queries.RecommendationSnapshot(ctx, testRecommendationSource)
	if err != nil {
		t.Fatalf("RecommendationSnapshot() error = %v", err)
	}
	candidates, err := queries.RecommendationCandidates(ctx, testRecommendationSource)
	if err != nil {
		t.Fatalf("RecommendationCandidates() error = %v", err)
	}
	if snapshot.SourceSnapshotID.String != "model-live" || snapshot.Status != "complete" ||
		len(candidates) != 1 || candidates[0].MusicbrainzRecordingID != recordingID ||
		len(candidates[0].ReasonCodes) != 1 || candidates[0].ReasonCodes[0] != "cf_raw" {
		t.Fatalf("snapshot = %#v candidates = %#v", snapshot, candidates)
	}
}
