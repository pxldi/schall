package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

var ownListenedAt = time.Date(2026, time.September, 20, 21, 0, 0, 0, time.UTC)

// ownService is a service with no ListenBrainz account at all. The own engine
// must work without one (ADR 0039 §6).
func ownService(store *fakeStore, provider *fakeRecordingProvider) *Service {
	store.missing = true
	return NewService(store, "schall-test/0.0 (test)", zerolog.Nop()).WithRecordingProvider(provider)
}

func poolRow(recordingID uuid.UUID, plays, shared int64, seeds ...uuid.UUID) db.OwnRecommendationPoolRow {
	if seeds == nil {
		seeds = []uuid.UUID{}
	}
	return db.OwnRecommendationPoolRow{
		RecordingMbid:    recordingID,
		Plays:            plays,
		LatestListenedAt: pgtype.Timestamptz{Time: ownListenedAt, Valid: true},
		SharedSessions:   shared,
		SeedRecordingIds: seeds,
	}
}

// expandable makes every recording MusicBrainz is asked about known to it.
func expandable(provider *fakeRecordingProvider, recordingIDs ...uuid.UUID) {
	if provider.recordings == nil {
		provider.recordings = make(map[uuid.UUID]musicbrainz.Recording, len(recordingIDs))
	}
	for _, recordingID := range recordingIDs {
		provider.recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Roads")
	}
}

// sortedIDs is the pool in MBID order, so a test can name which recording ranks
// first when every score is equal.
func sortedIDs(count int) []uuid.UUID {
	ids := make([]uuid.UUID, count)
	for index := range ids {
		ids[index] = uuid.New()
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left].String() < ids[right].String() })
	return ids
}

func ownPublished(store *fakeStore) []db.InsertRecommendationCandidateParams {
	return store.candidates[OwnSource]
}

func ownContext(t *testing.T, row db.InsertRecommendationCandidateParams) map[string]any {
	t.Helper()
	context := map[string]any{}
	if err := json.Unmarshal(row.ReasonContext, &context); err != nil {
		t.Fatal(err)
	}
	return context
}

func TestOwnScoreWeighsSharedSessionsAndPlays(t *testing.T) {
	for _, test := range []struct {
		name                                string
		shared, mostShared, plays, mostPlay int64
		want                                float64
	}{
		{"the top of both", 4, 4, 10, 10, 1},
		{"half of each", 2, 4, 5, 10, 0.5},
		{"sessions only", 4, 4, 0, 10, 0.7},
		{"plays only", 0, 4, 10, 10, 0.3},
		// Nothing in the pool was ever played beside owned music: that half
		// contributes nothing instead of dividing by zero.
		{"no shared sessions anywhere", 0, 0, 10, 10, 0.3},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ownScore(test.shared, test.mostShared, test.plays, test.mostPlay)
			if math.Abs(got-test.want) > 1e-9 {
				t.Fatalf("ownScore() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestOwnSweepAsksThePoolWithTheADRNumbers(t *testing.T) {
	store := &fakeStore{}
	service := ownService(store, &fakeRecordingProvider{})
	if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
		t.Fatal(err)
	}
	want := db.OwnRecommendationPoolParams{
		SessionGapMicroseconds: (30 * time.Minute).Microseconds(),
		MinimumPlays:           2,
		MaximumSeeds:           5,
	}
	if len(store.poolParams) != 1 || store.poolParams[0] != want {
		t.Fatalf("pool params = %#v, want %#v", store.poolParams, want)
	}
}

func TestOwnSweepWritesListenedAndCoListenedReasons(t *testing.T) {
	store := &fakeStore{}
	provider := &fakeRecordingProvider{}
	beside, alone := uuid.New(), uuid.New()
	seedA, seedB := uuid.New(), uuid.New()
	store.pool = []db.OwnRecommendationPoolRow{
		poolRow(alone, 6, 0),
		poolRow(beside, 3, 2, seedA, seedB),
	}
	expandable(provider, beside, alone)
	service := ownService(store, provider)

	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if result.More || result.Resume || result.Report.Status != "complete" {
		t.Fatalf("result = %#v, want a finished chain", result)
	}
	if store.snapshots[OwnSource].Status != "complete" {
		t.Fatalf("snapshot = %#v, want complete", store.snapshots[OwnSource])
	}
	rows := ownPublished(store)
	if len(rows) != 2 {
		t.Fatalf("stored %d rows, want 2", len(rows))
	}
	// beside: 0.7 × 2/2 + 0.3 × 3/6 = 0.85; alone: 0.3 × 6/6 = 0.3.
	if rows[0].MusicbrainzRecordingID != beside || rows[0].Rank != 1 {
		t.Fatalf("first row = %v rank %d, want %v rank 1", rows[0].MusicbrainzRecordingID, rows[0].Rank, beside)
	}
	if math.Abs(rows[0].SourceScore.Float64-0.85) > 1e-9 || math.Abs(rows[1].SourceScore.Float64-0.3) > 1e-9 {
		t.Fatalf("scores = %v, %v, want 0.85, 0.3", rows[0].SourceScore.Float64, rows[1].SourceScore.Float64)
	}
	if !reflect.DeepEqual(rows[0].ReasonCodes, []string{"co_listened", "listened"}) {
		t.Fatalf("reasons = %v, want co_listened and listened", rows[0].ReasonCodes)
	}
	stored := ownContext(t, rows[0])
	if stored["plays"] != float64(3) || stored["shared_sessions"] != float64(2) ||
		stored["latest_listened_at"] != ownListenedAt.Format(time.RFC3339) {
		t.Fatalf("context = %#v", stored)
	}
	if !reflect.DeepEqual(stored["seed_recording_ids"], []any{seedA.String(), seedB.String()}) {
		t.Fatalf("seed_recording_ids = %#v, want %v then %v", stored["seed_recording_ids"], seedA, seedB)
	}
	if !reflect.DeepEqual(rows[1].ReasonCodes, []string{"listened"}) {
		t.Fatalf("reasons = %v, want listened alone", rows[1].ReasonCodes)
	}
	aloneContext := ownContext(t, rows[1])
	if _, ok := aloneContext["seed_recording_ids"]; ok {
		t.Fatalf("context = %#v, want no seeds without a shared session", aloneContext)
	}
}

func TestOwnSweepLooksUpNothingThePublishedSnapshotHolds(t *testing.T) {
	store := &fakeStore{}
	provider := &fakeRecordingProvider{}
	first, second := uuid.New(), uuid.New()
	store.pool = []db.OwnRecommendationPoolRow{poolRow(first, 4, 1), poolRow(second, 2, 0)}
	expandable(provider, first, second)
	service := ownService(store, provider)

	if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
		t.Fatal(err)
	}
	if len(provider.asked) != 2 {
		t.Fatalf("first chain asked %v, want both recordings", provider.asked)
	}

	// The next day's chain starts from nothing and still asks about nothing.
	provider.asked = nil
	store.pool = []db.OwnRecommendationPoolRow{poolRow(first, 5, 2), poolRow(second, 2, 0)}
	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.asked) != 0 {
		t.Fatalf("second chain asked %v, want nothing", provider.asked)
	}
	if result.Report.Cached != 2 || result.Report.Stored != 2 {
		t.Fatalf("report = %#v, want both carried from the published snapshot", result.Report)
	}
	// The carried rows take this pass's counts, not the counts they were stored with.
	stored := ownContext(t, ownPublished(store)[0])
	if stored["plays"] != float64(5) || stored["shared_sessions"] != float64(2) {
		t.Fatalf("context = %#v, want this pass's counts", stored)
	}
}

func TestOwnSweepWalksThePoolTwentyFiveAtATime(t *testing.T) {
	store := &fakeStore{}
	provider := &fakeRecordingProvider{}
	ids := sortedIDs(30)
	for _, recordingID := range ids {
		store.pool = append(store.pool, poolRow(recordingID, 2, 0))
	}
	expandable(provider, ids...)
	service := ownService(store, provider)

	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.More || !result.Resume || len(provider.asked) != 25 {
		t.Fatalf("result = %#v after %d look-ups, want 25 and a continuation", result, len(provider.asked))
	}
	if !reflect.DeepEqual(provider.asked, ids[:25]) {
		t.Fatalf("asked %v, want the first 25 in rank order", provider.asked)
	}
	snapshot := store.snapshots[OwnSource]
	if snapshot.Status != "partial" || snapshot.Detail != "25 of 30 recordings looked up in MusicBrainz" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(ownPublished(store)) != 25 {
		t.Fatalf("published %d rows during the chain, want 25", len(ownPublished(store)))
	}

	provider.asked = nil
	result, err = service.SweepOwnPage(context.Background(), result.Position)
	if err != nil {
		t.Fatal(err)
	}
	if result.More || result.Resume || !reflect.DeepEqual(provider.asked, ids[25:]) {
		t.Fatalf("second pass = %#v asking %v, want the last five and an end", result, provider.asked)
	}
	if store.snapshots[OwnSource].Status != "complete" || len(ownPublished(store)) != 30 {
		t.Fatalf("snapshot = %#v with %d rows, want 30 complete", store.snapshots[OwnSource], len(ownPublished(store)))
	}
}

func TestOwnSweepNeverAsksTwiceAboutARecordingMusicBrainzCouldNotExpand(t *testing.T) {
	store := &fakeStore{}
	provider := &fakeRecordingProvider{}
	ids := sortedIDs(26)
	for _, recordingID := range ids {
		store.pool = append(store.pool, poolRow(recordingID, 2, 0))
	}
	// MusicBrainz does not know the first-ranked recording.
	expandable(provider, ids[1:]...)
	service := ownService(store, provider)

	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Position.Unexpandable, []uuid.UUID{ids[0]}) {
		t.Fatalf("carried %v, want %v", result.Position.Unexpandable, ids[0])
	}
	provider.asked = nil
	if _, err := service.SweepOwnPage(context.Background(), result.Position); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(provider.asked, []uuid.UUID{ids[25]}) {
		t.Fatalf("second pass asked %v, want only %v", provider.asked, ids[25])
	}
	if store.snapshots[OwnSource].Status != "complete" {
		t.Fatalf("snapshot = %#v, want complete", store.snapshots[OwnSource])
	}
}

func TestOwnSweepWaitsWhenMusicBrainzAnswersNothing(t *testing.T) {
	store := &fakeStore{}
	recordingID := uuid.New()
	store.pool = []db.OwnRecommendationPoolRow{poolRow(recordingID, 3, 0)}
	provider := &fakeRecordingProvider{errors: map[uuid.UUID]error{recordingID: errors.New("503 Service Unavailable")}}
	service := ownService(store, provider)

	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if result.More || !result.Resume || !result.Report.ProviderFailed {
		t.Fatalf("result = %#v, want a wait and the chain kept", result)
	}
	if store.recommendationWrites != 0 {
		t.Fatalf("wrote %d snapshots, want none with nothing to store", store.recommendationWrites)
	}
}

// ADR 0039 §6 waits thirty minutes on a MusicBrainz outage, and an outage that
// starts part way through a pass is still one. The pass keeps what it stored
// and hands its position on.
func TestOwnSweepWaitsWhenMusicBrainzFailsAfterAnswering(t *testing.T) {
	store := &fakeStore{}
	ids := sortedIDs(25)
	for _, recordingID := range ids {
		store.pool = append(store.pool, poolRow(recordingID, 2, 0))
	}
	provider := &fakeRecordingProvider{errors: map[uuid.UUID]error{ids[24]: errors.New("timeout")}}
	expandable(provider, ids[:24]...)
	service := ownService(store, provider)

	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if result.More || !result.Resume || !result.Report.ProviderFailed || result.Position.Passes != 1 {
		t.Fatalf("result = %#v, want a wait with the chain kept", result)
	}
	snapshot := store.snapshots[OwnSource]
	if snapshot.Status != "partial" || len(ownPublished(store)) != 24 ||
		!strings.HasPrefix(snapshot.Detail, "24 of 25 recordings looked up in MusicBrainz; ") {
		t.Fatalf("snapshot = %#v with %d rows, want the 24 answers kept", snapshot, len(ownPublished(store)))
	}
}

func TestOwnSweepStopsAChainThatHasSpentItsPasses(t *testing.T) {
	store := &fakeStore{}
	provider := &fakeRecordingProvider{}
	ids := sortedIDs(26)
	for _, recordingID := range ids {
		store.pool = append(store.pool, poolRow(recordingID, 2, 0))
	}
	expandable(provider, ids...)
	service := ownService(store, provider)

	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{Passes: 127})
	if err != nil {
		t.Fatal(err)
	}
	if result.More || result.Resume {
		t.Fatalf("result = %#v, want the chain to stop at 128 passes", result)
	}
	snapshot := store.snapshots[OwnSource]
	want := "the sweep stopped after 128 passes with 25 of 26 recordings looked up in MusicBrainz"
	if snapshot.Status != "partial" || snapshot.Detail != want {
		t.Fatalf("snapshot = %#v, want %q", snapshot, want)
	}
	// What it looked up stays: the published snapshot is the next chain's cache.
	if len(ownPublished(store)) != 25 {
		t.Fatalf("published %d rows, want 25", len(ownPublished(store)))
	}
}

func TestOwnSweepWritesAnEmptyPoolAsAnEmptyCompleteList(t *testing.T) {
	store := &fakeStore{}
	store.write(db.UpsertRecommendationSnapshotParams{Source: OwnSource, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{storedCandidate(OwnSource, uuid.New(), "Now owned")})
	service := ownService(store, &fakeRecordingProvider{})

	result, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{})
	if err != nil {
		t.Fatal(err)
	}
	if result.More || result.Resume || store.snapshots[OwnSource].Status != "complete" || len(ownPublished(store)) != 0 {
		t.Fatalf("result = %#v, snapshot %#v with %d rows, want an empty complete list",
			result, store.snapshots[OwnSource], len(ownPublished(store)))
	}
}

// mergedPool is a pool whose listens carry two identifiers for one recording:
// MusicBrainz answers about the older one under the recording it was merged
// into.
func mergedPool() (*fakeStore, *fakeRecordingProvider, uuid.UUID, uuid.UUID) {
	store := &fakeStore{}
	listened, canonical := uuid.New(), uuid.New()
	store.pool = []db.OwnRecommendationPoolRow{poolRow(listened, 5, 1), poolRow(canonical, 2, 0)}
	answer := expandedRecording(canonical, uuid.New(), uuid.New(), "Roads")
	provider := &fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		listened: answer, canonical: answer,
	}}
	return store, provider, listened, canonical
}

func TestOwnSweepStoresTwoIdentifiersMusicBrainzMergedAsOneRow(t *testing.T) {
	store, provider, listened, canonical := mergedPool()
	service := ownService(store, provider)

	if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
		t.Fatal(err)
	}
	rows := ownPublished(store)
	if len(rows) != 1 || rows[0].MusicbrainzRecordingID != canonical {
		t.Fatalf("rows = %#v, want one row under %v", rows, canonical)
	}
	stored := ownContext(t, rows[0])
	if stored["plays"] != float64(5) ||
		!reflect.DeepEqual(stored[listenedRecordingsKey], []any{listened.String()}) {
		t.Fatalf("context = %#v, want the higher-ranked listen and its identifier", stored)
	}
}

// Two identifiers the listens carry, both merged into a third the pool does not
// hold. The lower-ranked one gives the row nothing, and it is still named on
// the row so no later pass asks about it.
func TestOwnSweepDoesNotAskAgainAboutAMergedRecordingItDidNotKeep(t *testing.T) {
	store := &fakeStore{}
	kept, folded, canonical := uuid.New(), uuid.New(), uuid.New()
	store.pool = []db.OwnRecommendationPoolRow{poolRow(kept, 5, 1), poolRow(folded, 2, 0)}
	answer := expandedRecording(canonical, uuid.New(), uuid.New(), "Roads")
	provider := &fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		kept: answer, folded: answer,
	}}
	service := ownService(store, provider)
	if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
		t.Fatal(err)
	}

	provider.asked = nil
	if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
		t.Fatal(err)
	}
	if len(provider.asked) != 0 {
		t.Fatalf("second pass asked %v, want nothing", provider.asked)
	}
}

func TestOwnSweepDoesNotAskAgainAboutAMergedRecording(t *testing.T) {
	store, provider, _, _ := mergedPool()
	service := ownService(store, provider)
	if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
		t.Fatal(err)
	}

	provider.asked = nil
	if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
		t.Fatal(err)
	}
	if len(provider.asked) != 0 {
		t.Fatalf("second chain asked %v, want nothing", provider.asked)
	}
}

func TestOwnSweepReadsOnlyItsOwnFeedback(t *testing.T) {
	ids := sortedIDs(3)
	anchor, lower, higher := ids[0], ids[1], ids[2]
	sharedArtist := uuid.New()
	sweep := func(feedbackSource string) []db.InsertRecommendationCandidateParams {
		t.Helper()
		store := &fakeStore{}
		provider := &fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
			anchor: expandedRecording(anchor, uuid.New(), sharedArtist, "Anchor"),
			lower:  expandedRecording(lower, uuid.New(), sharedArtist, "Lower"),
			higher: expandedRecording(higher, uuid.New(), uuid.New(), "Higher"),
		}}
		store.pool = []db.OwnRecommendationPoolRow{
			poolRow(anchor, 2, 0), poolRow(higher, 4, 0), poolRow(lower, 3, 0),
		}
		feedback := feedbackFor(anchor, uuid.New(), []uuid.UUID{sharedArtist}, FeedbackMoreLikeThis)
		feedback.Source = feedbackSource
		store.feedback = []db.RecommendationFeedback{feedback, feedback, feedback}
		service := ownService(store, provider)
		if _, err := service.SweepOwnPage(context.Background(), OwnSweepPosition{}); err != nil {
			t.Fatal(err)
		}
		return ownPublished(store)
	}

	// ListenBrainz feedback beside this list leaves the source order alone.
	rows := sweep(listenbrainz.Name)
	if rows[0].MusicbrainzRecordingID != higher || rows[1].MusicbrainzRecordingID != lower {
		t.Fatalf("with ListenBrainz feedback the order is %v, %v; want %v, %v",
			rows[0].MusicbrainzRecordingID, rows[1].MusicbrainzRecordingID, higher, lower)
	}
	if containsReason(rows[1].ReasonCodes, "feedback_") {
		t.Fatalf("reasons = %v, want no feedback reason", rows[1].ReasonCodes)
	}

	// The same presses under `schall` lift the row that shares the artist.
	rows = sweep(OwnSource)
	if rows[0].MusicbrainzRecordingID != lower || !containsReason(rows[0].ReasonCodes, "feedback_more_like_this") {
		t.Fatalf("with Schall feedback the first row is %v %v, want %v lifted",
			rows[0].MusicbrainzRecordingID, rows[0].ReasonCodes, lower)
	}
}
