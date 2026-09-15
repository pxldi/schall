package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// A want with no anchor has one thing that can admit a copy: AcoustID naming the
// recording it asks for. When that failed on the first copy it almost always
// fails on the second, so a want holding a question fetches nothing more until
// somebody answers it or an anchor arrives. These tests are that rule.

// heldCopyFor records one copy the loop could not decide about, and leaves the
// request that fetched it waiting for a person.
func heldCopyFor(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID, requestID uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary, evidence,
			download_request_id
		)
		VALUES ($1, 'slskd', 'peer', $2, '03.flac', 9000000, 'held', 'schall',
		        'Nothing could identify the audio.', '{}'::jsonb, $3)
	`, targetID, `Music\Anetha\03.flac`, requestID); err != nil {
		t.Fatalf("hold the copy: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests
		SET import_status = 'needs_review',
		    import_error = 'Nothing could identify the audio.'
		WHERE id = $1
	`, requestID); err != nil {
		t.Fatalf("leave the request waiting for a decision: %v", err)
	}
}

// nextLook reads when a want is due, and what it says about itself.
func nextLook(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) (time.Time, string) {
	t.Helper()
	var (
		due     *time.Time
		summary string
	)
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at, summary FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&due, &summary); err != nil {
		t.Fatalf("read the want: %v", err)
	}
	if due == nil {
		return time.Time{}, summary
	}
	return *due, summary
}

// The rule itself. A want holding a copy nobody has answered, with nothing of
// its own to answer it with, loses its next look.
func TestAWantHoldingAnUnansweredCopyWithNoAnchorStopsFetching(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)

	stopped, err := queries.StopWhileACopyWaits(ctx, targetID, "A copy is waiting for your answer.")
	if err != nil {
		t.Fatalf("StopWhileACopyWaits() error = %v", err)
	}
	if !stopped {
		t.Fatal("the want was not stopped, want no further copies fetched for it")
	}
	due, summary := nextLook(ctx, t, pool, targetID)
	if !due.IsZero() {
		t.Errorf("next attempt = %v, want no time at all rather than a later one", due)
	}
	if summary != "A copy is waiting for your answer." {
		t.Errorf("summary = %q, want the sentence it was stopped with", summary)
	}
}

// A want with an anchor keeps fetching. The anchor can refuse or admit each new
// copy without anybody listening, so a further copy can still settle it.
func TestAnAnchoredWantHoldingAnUnansweredCopyKeepsFetching(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	anchored(ctx, t, queries, targetID)

	stopped, err := queries.StopWhileACopyWaits(ctx, targetID, "A copy is waiting for your answer.")
	if err != nil {
		t.Fatalf("StopWhileACopyWaits() error = %v", err)
	}
	if stopped {
		t.Fatal("an anchored want was stopped, want it looking for a copy the anchor can rule on")
	}
	if due, _ := nextLook(ctx, t, pool, targetID); due.IsZero() {
		t.Error("next attempt = none, want the want's time left where it was")
	}
}

// A want whose copy somebody accepted is not holding a question. The import is
// on its way, and taking its time away would strand it.
func TestAWantWhoseCopyWasAcceptedIsNotStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	if _, err := queries.AcceptHeldCopy(ctx, heldCopyID(ctx, t, pool, targetID),
		"You listened and said this copy is the wanted recording."); err != nil {
		t.Fatalf("AcceptHeldCopy() error = %v", err)
	}

	stopped, err := queries.StopWhileACopyWaits(ctx, targetID, "A copy is waiting for your answer.")
	if err != nil {
		t.Fatalf("StopWhileACopyWaits() error = %v", err)
	}
	if stopped {
		t.Fatal("an answered want was stopped, want the accepted copy left to import")
	}
}

// heldCopyID names the one copy this want is holding.
func heldCopyID(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM acquisition_target_files
		WHERE acquisition_target_id = $1 AND verdict = 'held'
	`, targetID).Scan(&id); err != nil {
		t.Fatalf("read the held copy: %v", err)
	}
	return id
}

// The first way back. A person saying none of these refuses the copies and gives
// the want a time, whether it had one or not.
func TestRefusingTheHeldCopyGivesAStoppedWantItsNextLookBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	if _, err := queries.StopWhileACopyWaits(ctx, targetID, "A copy is waiting for your answer."); err != nil {
		t.Fatalf("StopWhileACopyWaits() error = %v", err)
	}

	if err := queries.RefuseHeldCopies(ctx, targetID,
		"You listened and said this copy is not the wanted recording.",
		"The next copy will be tried.", time.Now()); err != nil {
		t.Fatalf("RefuseHeldCopies() error = %v", err)
	}
	due, summary := nextLook(ctx, t, pool, targetID)
	if due.IsZero() {
		t.Fatal("next attempt = none, want a refused want looking again")
	}
	if summary != "The next copy will be tried." {
		t.Errorf("summary = %q, want what a refusal says", summary)
	}
}

// The second way back. An anchor is the witness the copies were judged without,
// so a want that stopped for want of one looks again as soon as it has one and
// the copies have been put to it.
func TestAnAnchorGivesAStoppedWantItsNextLookBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	if _, err := queries.StopWhileACopyWaits(ctx, targetID, "A copy is waiting for your answer."); err != nil {
		t.Fatalf("StopWhileACopyWaits() error = %v", err)
	}
	anchored(ctx, t, queries, targetID)

	woken, err := queries.RearmWantNowAnchored(ctx, targetID, RearmedByAnAnchorSummary)
	if err != nil {
		t.Fatalf("RearmWantNowAnchored() error = %v", err)
	}
	if !woken {
		t.Fatal("the want was not woken, want an anchored want looking again")
	}
	due, summary := nextLook(ctx, t, pool, targetID)
	if due.IsZero() {
		t.Fatal("next attempt = none, want the want due again")
	}
	if summary != RearmedByAnAnchorSummary {
		t.Errorf("summary = %q, want the sentence an anchor wakes a want with", summary)
	}
	// The sweeper reads times, so a want that is due and nothing scheduled to
	// read it waits for whatever pass happens to come along next.
	if sweeps, _ := queuedSweeps(ctx, t, pool, "sweep_acquisition_targets"); sweeps != 1 {
		t.Errorf("queued sweeps = %d, want one pass to read the woken want", sweeps)
	}
}

// A want that still has no anchor is left where it is. Nothing has changed about
// what could admit its copy, so nothing about it should move.
func TestAWantWithNoAnchorIsNotWokenByTheJudgingPass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	heldCopyFor(ctx, t, pool, targetID, requestID)
	if _, err := queries.StopWhileACopyWaits(ctx, targetID, "A copy is waiting for your answer."); err != nil {
		t.Fatalf("StopWhileACopyWaits() error = %v", err)
	}

	woken, err := queries.RearmWantNowAnchored(ctx, targetID, RearmedByAnAnchorSummary)
	if err != nil {
		t.Fatalf("RearmWantNowAnchored() error = %v", err)
	}
	if woken {
		t.Fatal("a want with no anchor was woken, want it still waiting on a person")
	}
	if due, _ := nextLook(ctx, t, pool, targetID); !due.IsZero() {
		t.Errorf("next attempt = %v, want the want left stopped", due)
	}
}

// A sibling holding a question is left out of the folder a neighbouring want
// found. An open folder must not reach around the rule the sibling's own turn
// would apply to it.
func TestASiblingHoldingAnUnansweredCopyIsNotOfferedAFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	releaseGroupID := uuid.New()
	glory := wantOnRelease(ctx, t, pool, "Glory Box", releaseGroupID)
	mysterons := wantOnRelease(ctx, t, pool, "Mysterons", releaseGroupID)
	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_error
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Portishead', 1, 1, 25000000, 'flac', 0.8,
		        '[]'::jsonb, 'completed', 'needs_review', 'Nothing could identify the audio.')
	`, requestID, mysterons); err != nil {
		t.Fatal(err)
	}
	heldCopyFor(ctx, t, pool, mysterons, requestID)

	siblings, err := New(pool).WantsSharingAReleaseWith(ctx, glory)
	if err != nil {
		t.Fatalf("WantsSharingAReleaseWith() error = %v", err)
	}
	if len(siblings) != 0 {
		t.Fatalf("siblings = %v, want a sibling already holding a question left alone", siblings)
	}
}

// The same sibling with an anchor is offered the folder. The anchor can rule on
// what arrives, so a second copy is worth taking.
func TestAnAnchoredSiblingHoldingACopyIsOfferedAFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queries := New(pool)
	releaseGroupID := uuid.New()
	glory := wantOnRelease(ctx, t, pool, "Glory Box", releaseGroupID)
	mysterons := wantOnRelease(ctx, t, pool, "Mysterons", releaseGroupID)
	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_error
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Portishead', 1, 1, 25000000, 'flac', 0.8,
		        '[]'::jsonb, 'completed', 'needs_review', 'Nothing could identify the audio.')
	`, requestID, mysterons); err != nil {
		t.Fatal(err)
	}
	heldCopyFor(ctx, t, pool, mysterons, requestID)
	anchored(ctx, t, queries, mysterons)

	siblings, err := queries.WantsSharingAReleaseWith(ctx, glory)
	if err != nil {
		t.Fatalf("WantsSharingAReleaseWith() error = %v", err)
	}
	if len(siblings) != 1 || siblings[0].ID != mysterons {
		t.Fatalf("siblings = %v, want the anchored sibling offered the folder", siblings)
	}
}
