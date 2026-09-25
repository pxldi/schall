package recommendations

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/musicbrainz"
)

var ownFirstListen = time.Date(2026, time.September, 1, 20, 0, 0, 0, time.UTC)

// listenTo stores one listen. A nil recording is a listen nobody could identify.
func listenTo(t *testing.T, pool *pgxpool.Pool, at time.Time, recordingID *uuid.UUID) {
	t.Helper()
	var recording any
	if recordingID != nil {
		recording = *recordingID
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO listens (listened_at, artist_name, track_name, recording_mbid)
		VALUES ($1, 'An artist', $2, $3)
	`, at, uuid.NewString(), recording); err != nil {
		t.Fatal(err)
	}
}

// holdInLibrary gives the library a present file whose proven identity is the
// recording, and answers the file's identifier.
func holdInLibrary(t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 10, $3)
	`, fileID, "/music/"+fileID.String()+".flac", testObservedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'recording_mbid', 'identified')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func ownPoolRows(t *testing.T, queries *db.Queries) map[uuid.UUID]db.OwnRecommendationPoolRow {
	t.Helper()
	rows, err := queries.OwnRecommendationPool(context.Background(), db.OwnRecommendationPoolParams{
		SessionGapMicroseconds: ownSessionGap.Microseconds(),
		MinimumPlays:           ownMinimumPlays,
		MaximumSeeds:           ownMaximumSeeds,
	})
	if err != nil {
		t.Fatalf("OwnRecommendationPool() error = %v", err)
	}
	result := make(map[uuid.UUID]db.OwnRecommendationPoolRow, len(rows))
	for _, row := range rows {
		result[row.RecordingMbid] = row
	}
	return result
}

func TestOneListenDoesNotEnterTheOwnPool(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	listenTo(t, pool, ownFirstListen, &recordingID)
	if rows := ownPoolRows(t, queries); len(rows) != 0 {
		t.Fatalf("pool = %#v, want nothing after one listen", rows)
	}

	later := ownFirstListen.Add(48 * time.Hour)
	listenTo(t, pool, later, &recordingID)
	row, ok := ownPoolRows(t, queries)[recordingID]
	if !ok || row.Plays != 2 || !row.LatestListenedAt.Time.Equal(later) {
		t.Fatalf("pool row = %#v, want two plays, latest %s", row, later)
	}
}

func TestAnOwnedRecordingNeverEntersTheOwnPool(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	listenTo(t, pool, ownFirstListen, &recordingID)
	listenTo(t, pool, ownFirstListen.Add(time.Hour), &recordingID)
	holdInLibrary(t, pool, recordingID)

	if rows := ownPoolRows(t, queries); len(rows) != 0 {
		t.Fatalf("pool = %#v, want an owned recording kept out", rows)
	}
}

func TestARecordingOnAMappedTrackNeverEntersTheOwnPool(t *testing.T) {
	ctx := context.Background()
	_, queries, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	listenTo(t, pool, ownFirstListen, &recordingID)
	listenTo(t, pool, ownFirstListen.Add(time.Hour), &recordingID)

	artistID, albumID, trackID, fileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
		  VALUES ($1, $2, 'Local artist', 'Local artist', NULL, 'held by the library')`,
			[]any{artistID, uuid.New()}},
		{`INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		  VALUES ($1, $2, $3, 'A release')`, []any{albumID, artistID, uuid.New()}},
		{`INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title)
		  VALUES ($1, $2, $3, 'The recording')`, []any{trackID, albumID, recordingID}},
		{`INSERT INTO library_files (id, path, size_bytes, modified_at)
		  VALUES ($1, '/music/mapped.flac', 10, $2)`, []any{fileID, testObservedAt}},
		{`INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		  VALUES ($1, $2, 'manual', true)`, []any{trackID, fileID}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	if rows := ownPoolRows(t, queries); len(rows) != 0 {
		t.Fatalf("pool = %#v, want a mapped recording kept out", rows)
	}
}

func TestAMissingFileDoesNotKeepARecordingOutOfTheOwnPool(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	listenTo(t, pool, ownFirstListen, &recordingID)
	listenTo(t, pool, ownFirstListen.Add(time.Hour), &recordingID)
	fileID := holdInLibrary(t, pool, recordingID)
	if _, err := pool.Exec(context.Background(),
		`UPDATE library_files SET missing_at = $2 WHERE id = $1`, fileID, testObservedAt); err != nil {
		t.Fatal(err)
	}

	if _, ok := ownPoolRows(t, queries)[recordingID]; !ok {
		t.Fatal("a recording whose only file is missing is not in the pool")
	}
}

func TestAListenWithNoRecordingNeverEntersTheOwnPool(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	for index := range 3 {
		listenTo(t, pool, ownFirstListen.Add(time.Duration(index)*time.Hour), nil)
	}
	if rows := ownPoolRows(t, queries); len(rows) != 0 {
		t.Fatalf("pool = %#v, want nothing from listens with no recording", rows)
	}
}

// besideOwned plays an owned recording and, after the gap, a recording the
// library does not hold; then plays that recording once more on its own days
// later so it has the two plays the pool asks for.
func besideOwned(t *testing.T, pool *pgxpool.Pool, gap time.Duration) (owned, played uuid.UUID) {
	t.Helper()
	owned, played = uuid.New(), uuid.New()
	holdInLibrary(t, pool, owned)
	listenTo(t, pool, ownFirstListen, &owned)
	listenTo(t, pool, ownFirstListen.Add(gap), &played)
	listenTo(t, pool, ownFirstListen.Add(72*time.Hour), &played)
	return owned, played
}

func TestListensThirtyOneMinutesApartAreTwoSessions(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	_, played := besideOwned(t, pool, 31*time.Minute)

	row := ownPoolRows(t, queries)[played]
	if row.SharedSessions != 0 || len(row.SeedRecordingIds) != 0 {
		t.Fatalf("pool row = %#v, want no shared session", row)
	}
}

func TestListensThirtyMinutesApartShareASession(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	owned, played := besideOwned(t, pool, 30*time.Minute)

	row := ownPoolRows(t, queries)[played]
	if row.SharedSessions != 1 || !reflect.DeepEqual(row.SeedRecordingIds, []uuid.UUID{owned}) {
		t.Fatalf("pool row = %#v, want one shared session seeded by %v", row, owned)
	}
}

func TestAListenWithNoRecordingKeepsASessionOpen(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	owned, played := uuid.New(), uuid.New()
	holdInLibrary(t, pool, owned)
	listenTo(t, pool, ownFirstListen, &owned)
	listenTo(t, pool, ownFirstListen.Add(20*time.Minute), nil)
	listenTo(t, pool, ownFirstListen.Add(40*time.Minute), &played)
	listenTo(t, pool, ownFirstListen.Add(72*time.Hour), &played)

	if row := ownPoolRows(t, queries)[played]; row.SharedSessions != 1 {
		t.Fatalf("pool row = %#v, want the unidentified listen to bridge the gap", row)
	}
}

func TestTheOwnPoolNamesTheFiveOwnedRecordingsPlayedMostBesideIt(t *testing.T) {
	_, queries, pool, _ := recommendationService(t)
	played := uuid.New()
	owned := make([]uuid.UUID, 6)
	for index := range owned {
		owned[index] = uuid.New()
		holdInLibrary(t, pool, owned[index])
	}
	// The first owned recording shares three sessions, the second two, and the
	// other four one each; ties are broken by MBID.
	sessions := [][]uuid.UUID{
		{owned[0], owned[1], owned[2], owned[3]},
		{owned[0], owned[1], owned[4], owned[5]},
		{owned[0]},
	}
	for day, together := range sessions {
		start := ownFirstListen.Add(time.Duration(day) * 24 * time.Hour)
		listenTo(t, pool, start, &played)
		for index, ownedID := range together {
			listenTo(t, pool, start.Add(time.Duration(index+1)*time.Minute), &ownedID)
		}
	}
	ones := append([]uuid.UUID(nil), owned[2:]...)
	sort.Slice(ones, func(left, right int) bool { return ones[left].String() < ones[right].String() })
	want := append([]uuid.UUID{owned[0], owned[1]}, ones[:3]...)

	row := ownPoolRows(t, queries)[played]
	if row.Plays != 3 || row.SharedSessions != 3 || !reflect.DeepEqual(row.SeedRecordingIds, want) {
		t.Fatalf("pool row = %#v, want three plays, three shared sessions and seeds %v", row, want)
	}
}

func TestTheOwnSweepBuildsTheSchallListFromListens(t *testing.T) {
	ctx := context.Background()
	service, queries, pool, _ := recommendationService(t)
	owned, played := besideOwned(t, pool, 10*time.Minute)
	releaseGroupID, artistID := uuid.New(), uuid.New()
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		played: expandedRecording(played, releaseGroupID, artistID, "Roads"),
	}})

	result, err := service.SweepOwnPage(ctx, OwnSweepPosition{})
	if err != nil {
		t.Fatalf("SweepOwnPage() error = %v", err)
	}
	if result.More || result.Resume {
		t.Fatalf("result = %#v, want a finished chain", result)
	}
	listed, err := service.List(ctx, OwnSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].MusicBrainzRecordingID != played ||
		!reflect.DeepEqual(listed[0].ReasonCodes, []string{"co_listened", "listened"}) ||
		listed[0].SnapshotStatus != "complete" {
		t.Fatalf("listed = %#v, want %v from the listens", listed, played)
	}
	if _, err := queries.RecommendationSnapshot(ctx, OwnSource); err != nil {
		t.Fatalf("RecommendationSnapshot(schall) error = %v", err)
	}
	if listed[0].MusicBrainzRecordingID == owned {
		t.Fatal("the owned recording was listed")
	}
}

// ownAndListenBrainzRows stores the same candidate under both sources, so a
// test can prove one read query answers both.
func ownAndListenBrainzRows(t *testing.T, service *Service, candidates ...CandidateInput) {
	t.Helper()
	for _, source := range []string{testRecommendationSource, OwnSource} {
		if err := service.ReplaceSnapshot(context.Background(), Snapshot{
			Source: source, Status: "complete", FetchedAt: testObservedAt, Candidates: candidates,
		}); err != nil {
			t.Fatalf("ReplaceSnapshot(%s) error = %v", source, err)
		}
	}
}

func rulesFor(t *testing.T, service *Service, source string) map[uuid.UUID]SuppressionRule {
	t.Helper()
	rows, err := service.Evaluate(context.Background(), source)
	if err != nil {
		t.Fatalf("Evaluate(%s) error = %v", source, err)
	}
	result := make(map[uuid.UUID]SuppressionRule, len(rows))
	for _, row := range rows {
		result[row.MusicBrainzRecordingID] = row.SuppressedBy
	}
	return result
}

func TestASchallRowIsHiddenByTheSameRulesAsAListenBrainzRow(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	owned, requested, followed, dismissed, fatigued, shown := uuid.New(), uuid.New(), uuid.New(),
		uuid.New(), uuid.New(), uuid.New()
	followedArtist := uuid.New()
	ownAndListenBrainzRows(t, service,
		recommendationCandidate(owned, uuid.New(), uuid.New()),
		recommendationCandidate(requested, uuid.New(), uuid.New()),
		recommendationCandidate(followed, uuid.New(), followedArtist),
		recommendationCandidate(dismissed, uuid.New(), uuid.New()),
		recommendationCandidate(fatigued, uuid.New(), uuid.New()),
		recommendationCandidate(shown, uuid.New(), uuid.New()),
	)

	holdInLibrary(t, pool, owned)
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_title, musicbrainz_recording_id,
			resolution_method, resolved_at, status, summary
		) VALUES ($1, 'manual', 'Wanted', $2, 'manual', $3, 'pending', 'wanted')
	`, uuid.New(), requested, testObservedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name)
		VALUES ($1, 'Followed', 'Followed')
	`, followedArtist); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dismiss(ctx, DismissRecording, dismissed); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO recommendation_impressions (
			musicbrainz_recording_id, ignored_impressions, last_shown_at, suppressed_until
		) VALUES ($1, 3, $2, $2::timestamptz + interval '90 days')
	`, fatigued, testObservedAt); err != nil {
		t.Fatal(err)
	}

	want := map[uuid.UUID]SuppressionRule{
		owned:     SuppressedOwned,
		requested: SuppressedRequested,
		followed:  SuppressedFollowedArtist,
		dismissed: SuppressedDismissed,
		fatigued:  SuppressedImpressionFatigue,
		shown:     "",
	}
	if got := rulesFor(t, service, OwnSource); !reflect.DeepEqual(got, want) {
		t.Fatalf("schall rules = %v, want %v", got, want)
	}
	if got := rulesFor(t, service, testRecommendationSource); !reflect.DeepEqual(got, want) {
		t.Fatalf("listenbrainz rules = %v, want %v", got, want)
	}
}

func TestReviewQueueRejectionDoesNotAffectOwnRecommendations(t *testing.T) {
	ctx := context.Background()
	service, _, pool, _ := recommendationService(t)
	recordingID := uuid.New()
	if err := service.ReplaceSnapshot(ctx, Snapshot{
		Source: OwnSource, Status: "complete", FetchedAt: testObservedAt,
		Candidates: []CandidateInput{recommendationCandidate(recordingID, uuid.New(), uuid.New())},
	}); err != nil {
		t.Fatal(err)
	}
	before := rulesFor(t, service, OwnSource)[recordingID]

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
	after := rulesFor(t, service, OwnSource)[recordingID]
	if before != "" || after != before {
		t.Fatalf("rule before rejection = %q, after = %q", before, after)
	}
}

func TestFeedbackOnASchallRowDoesNotChangeAListenBrainzRank(t *testing.T) {
	ctx := context.Background()
	service, queries, pool, _ := recommendationService(t)
	answer, provider := listeningAccount(t, service, queries)
	higher, lower, anchor := uuid.New(), uuid.New(), uuid.New()
	sharedArtist := uuid.New()
	provider.recordings[higher] = expandedRecording(higher, uuid.New(), uuid.New(), "Higher")
	provider.recordings[lower] = expandedRecording(lower, uuid.New(), sharedArtist, "Lower")
	*answer = []map[string]any{
		{"recording_mbid": higher.String(), "score": 0.9},
		{"recording_mbid": lower.String(), "score": 0.8},
	}
	// A `schall` row shares the lower ListenBrainz row's artist, and is pressed
	// three times: enough to lift that row past the higher one if it were read.
	if err := service.ReplaceSnapshot(ctx, Snapshot{
		Source: OwnSource, Status: "complete", FetchedAt: testObservedAt,
		Candidates: []CandidateInput{recommendationCandidate(anchor, uuid.New(), sharedArtist)},
	}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := service.RecordFeedback(ctx, OwnSource, anchor, FeedbackMoreLikeThis); err != nil {
			t.Fatal(err)
		}
	}
	ranks := func() map[uuid.UUID]int32 {
		t.Helper()
		if _, err := service.Sweep(ctx); err != nil {
			t.Fatalf("Sweep() error = %v", err)
		}
		stored, err := queries.RecommendationCandidates(ctx, testRecommendationSource)
		if err != nil {
			t.Fatal(err)
		}
		result := make(map[uuid.UUID]int32, len(stored))
		for _, row := range stored {
			result[row.MusicbrainzRecordingID] = row.Rank
		}
		return result
	}

	if got := ranks(); got[higher] != 1 || got[lower] != 2 {
		t.Fatalf("ranks with Schall feedback = %v, want the source order", got)
	}

	// The same presses filed under ListenBrainz do lift it, so the order above
	// is the sweep ignoring them and not the neighbourhood missing.
	if _, err := pool.Exec(ctx, `
		INSERT INTO recommendation_feedback (
			source, musicbrainz_recording_id, musicbrainz_release_group_id,
			musicbrainz_artist_ids, signal, reason_codes, reason_context
		)
		SELECT 'listenbrainz', musicbrainz_recording_id, musicbrainz_release_group_id,
		       musicbrainz_artist_ids, signal, reason_codes, reason_context
		FROM recommendation_feedback WHERE source = 'schall'
	`); err != nil {
		t.Fatal(err)
	}
	if got := ranks(); got[lower] != 1 {
		t.Fatalf("ranks with ListenBrainz feedback = %v, want %v lifted", got, lower)
	}
}
