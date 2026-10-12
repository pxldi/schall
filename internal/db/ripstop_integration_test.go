package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// An anchor found by name never proves or refuses a copy, so every copy of a
// want that has only that is a question. On 2026-10-12 32 such wants held 4,490
// of 6,349 fetched copies, one of them 153. These tests are where that stops.

// moreHeldCopiesFor records a further held copy of the want for each size given,
// under a path named by its size and its place in the list.
func moreHeldCopiesFor(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	targetID, requestID uuid.UUID, sizes ...int64,
) {
	t.Helper()
	for i, size := range sizes {
		if _, err := pool.Exec(ctx, `
			INSERT INTO acquisition_target_files (
				acquisition_target_id, provider, source_username, remote_path,
				file_name, size_bytes, verdict, decided_by, summary, evidence,
				download_request_id
			)
			VALUES ($1, 'slskd', 'peer', $2, '03.flac', $3, 'held', 'schall',
			        'Nothing could identify the audio.', '{}'::jsonb, $4)
		`, targetID, fmt.Sprintf(`Music\Copy %d-%d\03.flac`, size, i), size, requestID); err != nil {
			t.Fatalf("hold copy %d: %v", i, err)
		}
	}
}

// anchoredBy records an anchor from the named source.
func anchoredBy(
	ctx context.Context, t *testing.T, queries *Queries, targetID uuid.UUID, source string,
) {
	t.Helper()
	if err := queries.RecordAnchor(ctx, AnchorParams{
		TargetID: targetID, Fingerprint: "AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC",
		Source: source, Reference: "dQw4w9WgXcQ", Seconds: 30,
	}); err != nil {
		t.Fatalf("record the %s anchor: %v", source, err)
	}
}

func stopWhileACopyWaits(
	ctx context.Context, t *testing.T, queries *Queries, targetID uuid.UUID,
) bool {
	t.Helper()
	stopped, err := queries.StopWhileACopyWaits(ctx, targetID, "A copy is waiting for your answer.")
	if err != nil {
		t.Fatalf("StopWhileACopyWaits() error = %v", err)
	}
	return stopped
}

// One copy waiting is not yet enough. AcoustID may still name a later copy, so
// a want with a by-name anchor keeps fetching as it did before.
func TestAWantAnchoredByNameWithOneHeldCopyKeepsFetching(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	anchoredBy(ctx, t, queries, targetID, "youtube")

	if stopWhileACopyWaits(ctx, t, queries, targetID) {
		t.Fatal("stopped after one copy, want a by-name anchored want still fetching")
	}
}

// Five copies of one upload are one rip, and still enough copies to stop for.
func TestAWantAnchoredByNameHoldingFiveCopiesStopsFetching(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	moreHeldCopiesFor(ctx, t, pool, targetID, requestID, 9000000, 9000000, 9000000, 9000000)
	anchoredBy(ctx, t, queries, targetID, "youtube")

	if !stopWhileACopyWaits(ctx, t, queries, targetID) {
		t.Fatal("not stopped, want a want holding five unanswered copies left alone")
	}
	if due, _ := nextLook(ctx, t, pool, targetID); !due.IsZero() {
		t.Errorf("next attempt = %v, want none", due)
	}
}

// Three rips of different sizes stop the want before the fifth copy.
func TestAWantAnchoredByNameHoldingThreeRipsStopsFetching(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	moreHeldCopiesFor(ctx, t, pool, targetID, requestID, 9100000)
	anchoredBy(ctx, t, queries, targetID, "youtube")
	if stopWhileACopyWaits(ctx, t, queries, targetID) {
		t.Fatal("stopped at two rips, want the want still fetching")
	}

	moreHeldCopiesFor(ctx, t, pool, targetID, requestID, 9200000)
	if !stopWhileACopyWaits(ctx, t, queries, targetID) {
		t.Fatal("not stopped, want a want holding three unanswered rips left alone")
	}
}

// An anchor that can rule on a copy keeps the want fetching however many wait.
func TestAWantWithAnAnchorThatRulesKeepsFetchingWhateverWaits(t *testing.T) {
	for _, source := range []string{"deezer", "youtube-topic", "source"} {
		t.Run(source, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := dbtest.Setup(t)
			queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
			heldCopyFor(ctx, t, pool, targetID, requestID)
			moreHeldCopiesFor(ctx, t, pool, targetID, requestID,
				9100000, 9200000, 9300000, 9400000, 9500000)
			anchoredBy(ctx, t, queries, targetID, source)

			if stopWhileACopyWaits(ctx, t, queries, targetID) {
				t.Fatalf("a want anchored by %s was stopped, want it fetching", source)
			}
		})
	}
}

// A by-name anchor landing on a stopped want holding enough copies wakes
// nothing. Its next turn would only stop it again.
func TestAByNameAnchorDoesNotWakeAWantHoldingEnoughCopies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	moreHeldCopiesFor(ctx, t, pool, targetID, requestID, 9100000, 9200000)
	if !stopWhileACopyWaits(ctx, t, queries, targetID) {
		t.Fatal("not stopped, want a want with no anchor stopped")
	}
	anchoredBy(ctx, t, queries, targetID, "youtube")

	woken, err := queries.RearmWantNowAnchored(ctx, targetID, RearmedByAnAnchorSummary)
	if err != nil {
		t.Fatalf("RearmWantNowAnchored() error = %v", err)
	}
	if woken {
		t.Fatal("woken, want the want left waiting on a person")
	}
}
