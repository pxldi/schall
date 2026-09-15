package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/recommendations"
	"github.com/rs/zerolog"
)

// These tests read the route against the real stores, with the real service
// behind it. The five suppression rules themselves are proved one by one in
// internal/recommendations; what is proved here is that the list a reader is
// given is the list those rules produced — no rule applied twice, none skipped
// on the way through the handler.

const testSource = "listenbrainz"

var storedAt = time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)

func recommendationRoute(t *testing.T) (http.Handler, *recommendations.Service, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.Setup(t)
	store := db.New(pool)
	service := recommendations.NewService(store, "schall-test/0.0 (test)", zerolog.Nop())
	handler := NewAPI(store, pool, &fakeArtistSearcher{}, zerolog.Nop(),
		WithRecommendations(service))
	return handler, service, pool
}

// storeSuggestions writes one sweep's answer, the way the background sweep
// writes it.
func storeSuggestions(t *testing.T, service *recommendations.Service, recordingIDs ...uuid.UUID) {
	t.Helper()
	candidates := make([]recommendations.CandidateInput, 0, len(recordingIDs))
	for _, recordingID := range recordingIDs {
		candidates = append(candidates, recommendations.CandidateInput{
			MusicBrainzRecordingID:    recordingID,
			MusicBrainzReleaseGroupID: uuid.New(),
			MusicBrainzArtistIDs:      []uuid.UUID{uuid.New()},
			RecordingTitle:            "A recording",
			ReleaseTitle:              "A release",
			ArtistName:                "An artist",
			ReasonCodes:               []string{"cf_raw"},
		})
	}
	if err := service.ReplaceSnapshot(context.Background(), recommendations.Snapshot{
		Source:           testSource,
		SourceSnapshotID: "model-1",
		Status:           "complete",
		FetchedAt:        storedAt,
		Candidates:       candidates,
	}); err != nil {
		t.Fatalf("ReplaceSnapshot() error = %v", err)
	}
}

func listedRecordings(t *testing.T, handler http.Handler) (map[uuid.UUID]bool, recommendationPage) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var page recommendationPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	listed := make(map[uuid.UUID]bool, len(page.Items))
	for _, item := range page.Items {
		listed[item.RecordingID] = true
	}
	return listed, page
}

// The library already holding the recording is the first suppression rule. The
// file has to be present and identified: a raw tag is not a decision.
func TestARecordingTheLibraryHoldsIsNotOffered(t *testing.T) {
	ctx := context.Background()
	handler, service, pool := recommendationRoute(t)
	held, offered := uuid.New(), uuid.New()
	storeSuggestions(t, service, held, offered)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, musicbrainz_recording_id)
		VALUES ($1, '/music/held.flac', 10, $2, $3)
	`, fileID, storedAt, held); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'recording_mbid', 'identified')
	`, fileID, held); err != nil {
		t.Fatal(err)
	}

	listed, page := listedRecordings(t, handler)
	if listed[held] || !listed[offered] {
		t.Fatalf("listed = %v, want only %s", listed, offered)
	}
	if page.Hidden.Owned != 1 || page.Hidden.Total != 1 {
		t.Fatalf("hidden = %#v, want one owned", page.Hidden)
	}
}

// A dismissal is the reader saying no. It is written through the route the page
// presses, and takes effect on the next read without a sweep.
func TestARecordingTheReaderDismissedIsNotOfferedAgain(t *testing.T) {
	handler, service, _ := recommendationRoute(t)
	turnedDown, offered := uuid.New(), uuid.New()
	storeSuggestions(t, service, turnedDown, offered)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/dismissals",
		strings.NewReader(`{"subject":"recording","musicBrainzId":"`+turnedDown.String()+`"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	listed, page := listedRecordings(t, handler)
	if listed[turnedDown] || !listed[offered] {
		t.Fatalf("listed = %v, want only %s", listed, offered)
	}
	if page.Hidden.Dismissed != 1 {
		t.Fatalf("hidden = %#v, want one dismissed", page.Hidden)
	}
}

// The review queue is where somebody decides whether a fetched file is the
// recording it was fetched for. Rejecting a copy there is a statement about
// audio identity, not about taste, and docs/decisions/0005 keeps the two
// apart: the recording stays on offer.
func TestAReviewQueueRejectionLeavesTheSuggestionOnOffer(t *testing.T) {
	ctx := context.Background()
	handler, service, pool := recommendationRoute(t)
	rejected := uuid.New()
	storeSuggestions(t, service, rejected)

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
		) VALUES ($1, $2, 'wrong recording')
	`, targetID, rejected); err != nil {
		t.Fatal(err)
	}

	listed, page := listedRecordings(t, handler)
	if !listed[rejected] {
		t.Fatalf("listed = %v, want %s still offered", listed, rejected)
	}
	if page.Hidden.Total != 0 {
		t.Fatalf("hidden = %#v, want nothing hidden", page.Hidden)
	}
}

// The fifth rule counts showings: after three, with no decision in between, a
// recording is held back for ninety days. The ninety days and the day between
// showings belong to the service and are proved there. What the route owes is
// the count itself — without it the rule can never fire at all.
func TestShowingASuggestionCountsAgainstIt(t *testing.T) {
	ctx := context.Background()
	handler, service, pool := recommendationRoute(t)
	shown := uuid.New()
	storeSuggestions(t, service, shown)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/impressions",
		strings.NewReader(`{"recordingIds":["`+shown.String()+`"]}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var counted int32
	if err := pool.QueryRow(ctx, `
		SELECT ignored_impressions FROM recommendation_impressions
		WHERE musicbrainz_recording_id = $1
	`, shown).Scan(&counted); err != nil {
		t.Fatalf("read impression: %v", err)
	}
	if counted != 1 {
		t.Fatalf("ignored_impressions = %d, want 1", counted)
	}
}

// The snapshot header is what tells an empty list apart from a list nobody has
// fetched, so it reports the sweep that stored the rows.
func TestTheListReportsTheSweepItWasReadFrom(t *testing.T) {
	handler, service, _ := recommendationRoute(t)
	storeSuggestions(t, service, uuid.New())

	_, page := listedRecordings(t, handler)

	if !page.Snapshot.Fetched || page.Snapshot.Status != "complete" ||
		page.Snapshot.FetchedAt == nil || !page.Snapshot.FetchedAt.Equal(storedAt) {
		t.Fatalf("snapshot = %#v", page.Snapshot)
	}
}

// recordSweepPass writes one finished pass of the background sweep, the way the
// job worker leaves it behind. A pass is a row in the jobs table, and one sweep
// is a chain of them.
func recordSweepPass(t *testing.T, pool *pgxpool.Pool, startedAt time.Time, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO jobs (kind, status, started_at, completed_at)
		VALUES ('sweep_recommendations', $1, $2, $3)
	`, status, startedAt, startedAt.Add(time.Minute)); err != nil {
		t.Fatalf("record sweep pass: %v", err)
	}
}

// queueSweepPass writes the pass waiting to run, which is what says when the
// sweep comes back.
func queueSweepPass(t *testing.T, pool *pgxpool.Pool, runAfter time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO jobs (kind, status, run_after) VALUES ('sweep_recommendations', 'queued', $1)
	`, runAfter); err != nil {
		t.Fatalf("queue sweep pass: %v", err)
	}
}

// The sweep is what keeps the list up to date, and it reports itself through
// the job queue rather than through the list. The three tests below prove the
// query that reads the queue against real rows, because the shapes it has to
// tell apart are written by the job worker and not by anything here.
//
// This one is a sweep that ran after the stored list was written and published
// nothing: ListenBrainz was down, or the account was larger than one sweep may
// walk. The list on screen is the last complete one, and it is getting older.
func TestASweepThatPublishedNothingLeavesTheListUnrefreshed(t *testing.T) {
	handler, service, pool := recommendationRoute(t)
	storeSuggestions(t, service, uuid.New())
	recordSweepPass(t, pool, storedAt.Add(time.Hour), "completed")
	due := time.Now().Add(25 * time.Minute).UTC().Truncate(time.Second)
	queueSweepPass(t, pool, due)

	_, page := listedRecordings(t, handler)

	if !page.Refresh.Attempted || page.Refresh.Succeeded {
		t.Fatalf("refresh = %#v, want a list nobody is refreshing", page.Refresh)
	}
	if page.Refresh.NextAttemptAt == nil || !page.Refresh.NextAttemptAt.Equal(due) {
		t.Fatalf("next attempt = %v, want %v", page.Refresh.NextAttemptAt, due)
	}
}

// A pass writes the list it publishes before the queue marks the pass done, so
// the list a sweep published is written after that pass started.
func TestASweepThatPublishedTheStoredListReadsAsRefreshed(t *testing.T) {
	handler, service, pool := recommendationRoute(t)
	storeSuggestions(t, service, uuid.New())
	recordSweepPass(t, pool, storedAt.Add(-time.Minute), "completed")
	queueSweepPass(t, pool, time.Now().Add(20*time.Hour))

	_, page := listedRecordings(t, handler)

	if !page.Refresh.Attempted || !page.Refresh.Succeeded {
		t.Fatalf("refresh = %#v, want a refreshed list", page.Refresh)
	}
}

// A stored list with an empty job queue is a seeded or restored database, which
// the browser tests read on every run. It is ordinary, and the screen must not
// put a fault on it.
func TestAStoredListWithAnEmptyQueueReportsNoSweep(t *testing.T) {
	handler, service, _ := recommendationRoute(t)
	storeSuggestions(t, service, uuid.New())

	_, page := listedRecordings(t, handler)

	if page.Refresh.Attempted || page.Refresh.LastAttemptAt != nil ||
		page.Refresh.NextAttemptAt != nil {
		t.Fatalf("refresh = %#v, want no sweep attempted", page.Refresh)
	}
}
