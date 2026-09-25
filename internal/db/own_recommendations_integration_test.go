package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

func storeListen(ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID *uuid.UUID) {
	t.Helper()
	var recording any
	if recordingID != nil {
		recording = *recordingID
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO listens (listened_at, artist_name, track_name, recording_mbid)
		VALUES (now(), 'An artist', $1, $2)
	`, uuid.NewString(), recording); err != nil {
		t.Fatal(err)
	}
}

// An installation whose listens name no recording has nothing for the own
// engine to read, so startup queues nothing (ADR 0039 §6).
func TestStartupQueuesNoOwnSweepWithoutAListenNamingARecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	storeListen(ctx, t, pool, nil)

	if err := queries.EnsureOwnRecommendationSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureOwnRecommendationSweepQueued() error = %v", err)
	}
	var sweeps int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE kind = 'sweep_own_recommendations'`).Scan(&sweeps); err != nil {
		t.Fatal(err)
	}
	if sweeps != 0 {
		t.Fatalf("own sweeps = %d, want none", sweeps)
	}
}

func TestStartupQueuesOneOwnSweepOnceAListenNamesARecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	recordingID := uuid.New()
	storeListen(ctx, t, pool, &recordingID)

	first := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	if err := queries.EnsureOwnRecommendationSweepQueued(ctx, first); err != nil {
		t.Fatalf("EnsureOwnRecommendationSweepQueued() error = %v", err)
	}
	// A restart inside the wait does not move the pass already waiting.
	if err := queries.EnsureOwnRecommendationSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("a second EnsureOwnRecommendationSweepQueued() error = %v", err)
	}
	if sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_own_recommendations"); sweeps != 1 || !runAfter.Equal(first) {
		t.Fatalf("queued own sweeps = %d at %v, want one at %v", sweeps, runAfter, first)
	}
}

// A listens sync starts the own sweep only when it stored a listen naming a
// recording, so the count must leave out listens with none and listens
// already held.
func TestInsertingListensCountsTheNewOnesThatNameARecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))
	at := time.Date(2026, time.September, 5, 13, 0, 0, 0, time.UTC)
	page := []ListenInsert{
		{ListenedAt: at, ArtistName: "A", TrackName: "Mapped", RecordingMBID: uuid.NewString()},
		{ListenedAt: at.Add(-time.Minute), ArtistName: "A", TrackName: "Unmapped"},
	}

	first, err := queries.InsertListens(ctx, page)
	if err != nil || first != (ListensInserted{Stored: 2, Recordings: 1}) {
		t.Fatalf("first insert = %+v, %v; want two stored, one naming a recording", first, err)
	}
	again, err := queries.InsertListens(ctx, page)
	if err != nil || again != (ListensInserted{}) {
		t.Fatalf("second insert = %+v, %v; want nothing", again, err)
	}
}

func TestOneOwnSweepIsQueuedOrRunningHoweverOftenItIsAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	sooner := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueOwnRecommendationSweep(ctx, later, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("QueueOwnRecommendationSweep() error = %v", err)
	}
	if err := queries.QueueOwnRecommendationSweep(ctx, sooner, json.RawMessage(`{"passes":2}`)); err != nil {
		t.Fatalf("queueing an earlier own sweep: %v", err)
	}
	if sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_own_recommendations"); sweeps != 1 || !runAfter.Equal(sooner) {
		t.Fatalf("queued own sweeps = %d at %v, want one at %v", sweeps, runAfter, sooner)
	}
	var passes int
	if err := pool.QueryRow(ctx, `
		SELECT (payload->>'passes')::int FROM jobs
		WHERE kind = 'sweep_own_recommendations' AND status = 'queued'
	`).Scan(&passes); err != nil || passes != 2 {
		t.Fatalf("queued position passes = %d, %v; want the newer position", passes, err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running'
		WHERE kind = 'sweep_own_recommendations' AND status = 'queued'
	`); err != nil {
		t.Fatal(err)
	}
	if err := queries.QueueOwnRecommendationSweep(ctx, time.Now(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("queueing while the own sweep is running: %v", err)
	}
	var live int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_own_recommendations' AND status IN ('queued', 'running')
	`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live own sweeps = %d, want exactly one", live)
	}
}

// Each source's list reads its own sweep's rows. A ListenBrainz pass running
// says nothing about whether the `schall` list is being kept up to date.
func TestTheSweepStateReadsOnlyTheKindItIsAskedAbout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	if err := queries.QueueRecommendationSweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running' WHERE kind = 'sweep_recommendations'
	`); err != nil {
		t.Fatal(err)
	}

	own, err := queries.RecommendationSweepState(ctx, "sweep_own_recommendations")
	if err != nil {
		t.Fatal(err)
	}
	listenBrainz, err := queries.RecommendationSweepState(ctx, "sweep_recommendations")
	if err != nil {
		t.Fatal(err)
	}
	if own.Running || own.Attempted || !listenBrainz.Running {
		t.Fatalf("own = %#v, listenbrainz = %#v; want only the ListenBrainz sweep running", own, listenBrainz)
	}
}
