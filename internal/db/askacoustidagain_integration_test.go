package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// heardBy gives the want's copy a fingerprint and a decision time some days ago.
func heardBy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID, daysAgo int,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET audio_fingerprint = 'AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC',
		    decided_at = now() - make_interval(days => $2)
		WHERE acquisition_target_id = $1
	`, targetID, daysAgo); err != nil {
		t.Fatalf("fingerprint the copy: %v", err)
	}
}

func askedAgain(ctx context.Context, t *testing.T, queries *Queries) []JudgeAgainRow {
	t.Helper()
	offered, err := queries.CopiesAcoustIDDidNotKnow(ctx)
	if err != nil {
		t.Fatalf("CopiesAcoustIDDidNotKnow() error = %v", err)
	}
	return offered
}

// A held copy AcoustID did not know 31 days ago is asked about again.
func TestAHeldCopyAcoustIDDidNotKnowAMonthAgoIsAskedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld,
		`{"acoustic": {"recordingIds": []}}`)
	heardBy(ctx, t, pool, targetID, 31)

	offered := askedAgain(ctx, t, queries)
	if len(offered) != 1 || offered[0].RequestID != requestID {
		t.Fatalf("offered = %+v, want the copy back", offered)
	}
}

// A copy decided 10 days ago waits for its 30 days.
func TestARecentlyJudgedCopyIsNotAskedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld,
		`{"acoustic": {"recordingIds": []}}`)
	heardBy(ctx, t, pool, targetID, 10)

	if offered := askedAgain(ctx, t, queries); len(offered) != 0 {
		t.Fatalf("offered = %+v, want none", offered)
	}
}

// A copy AcoustID named already has its answer; asking again changes nothing.
func TestACopyAcoustIDNamedIsNotAskedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld,
		`{"acoustic": {"recordingIds": ["8f3471b5-7e6a-48da-86a9-c1c07a0f47ae"]}}`)
	heardBy(ctx, t, pool, targetID, 31)

	if offered := askedAgain(ctx, t, queries); len(offered) != 0 {
		t.Fatalf("offered = %+v, want none", offered)
	}
}

// A copy a person decided about stays decided.
func TestACopyDecidedByHandIsNotAskedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld,
		`{"acoustic": {"unavailable": "rate limited"}}`)
	heardBy(ctx, t, pool, targetID, 31)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET decided_by = 'user' WHERE acquisition_target_id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	if offered := askedAgain(ctx, t, queries); len(offered) != 0 {
		t.Fatalf("offered = %+v, want none", offered)
	}
}
