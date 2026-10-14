package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/sources"
)

// fetchingWant sweeps once so the want records a request for a copy from
// first-peer, with second-peer offering another.
// The fake fetcher leaves the request recorded and never started, which is
// what an interrupted start leaves behind.
func fetchingWant(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Service, uuid.UUID, uuid.UUID) {
	t.Helper()
	wanted := sources.File{Name: "Glory Box.flac", DurationSeconds: 301}
	first, second := wanted, wanted
	first.Path = `Music\A First\Glory Box.flac`
	second.Path = `Music\B Second\Glory Box.flac`
	hunter := &fakeHunter{candidates: []sources.Candidate{
		{Provider: "slskd", Username: "first-peer", Directory: `Music\A First`,
			Files: []sources.File{first}, Score: 0.8},
		{Provider: "slskd", Username: "second-peer", Directory: `Music\B Second`,
			Files: []sources.File{second}, Score: 0.8},
	}}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	return service, target.ID, openRequestID(ctx, t, pool, target.ID)
}

// sweepAgain runs the next sweep as if the last stalled check were long ago.
func sweepAgain(ctx context.Context, t *testing.T, service *Service) {
	t.Helper()
	service.stalledAt = time.Time{}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
}

// assertLookedElsewhere checks that the want went straight on to the other
// peer's copy in the same sweep.
func assertLookedElsewhere(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID, closedID uuid.UUID,
) {
	t.Helper()
	next := openRequestID(ctx, t, pool, targetID)
	if next == closedID {
		t.Fatalf("no new request, want the want to fetch another copy")
	}
	var peer string
	if err := pool.QueryRow(ctx, `
		SELECT source_username FROM download_requests WHERE id = $1
	`, next).Scan(&peer); err != nil {
		t.Fatal(err)
	}
	if peer != "second-peer" {
		t.Errorf("next copy from %q, want the peer not yet tried", peer)
	}
}

func requestStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, requestID uuid.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM download_requests WHERE id = $1
	`, requestID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func requestCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, requestID uuid.UUID,
) (verdict, summary string) {
	t.Helper()
	if err := pool.QueryRow(ctx, `
		SELECT verdict, summary FROM acquisition_target_files WHERE download_request_id = $1
	`, requestID).Scan(&verdict, &summary); err != nil {
		t.Fatal(err)
	}
	return verdict, summary
}

// A start that was interrupted left the request recorded with nothing at the
// provider. The want waited on it for good: its one open request was taken,
// and the queue check only looks at transfers that exist. Live, two wants had
// waited like this since 12 and 13 October 2026.
func TestARequestThatNeverStartedIsClosedAndTheWantLooksAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service, targetID, requestID := fetchingWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET requested_at = now() - interval '1 hour',
		    error_message = 'context canceled'
		WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	sweepAgain(ctx, t, service)

	if status := requestStatus(ctx, t, pool, requestID); status != "cancelled" {
		t.Errorf("request status = %q, want the request that never started withdrawn", status)
	}
	if verdict, summary := requestCopy(ctx, t, pool, requestID); verdict != "undelivered" ||
		summary != neverStartedFileSummary {
		t.Errorf("copy = %s %q, want undelivered %q", verdict, summary, neverStartedFileSummary)
	}
	assertLookedElsewhere(ctx, t, pool, targetID, requestID)
}

// A request recorded a moment ago is a start in progress.
func TestARequestJustRecordedIsLeftAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service, _, requestID := fetchingWant(ctx, t, pool)

	sweepAgain(ctx, t, service)

	if status := requestStatus(ctx, t, pool, requestID); status != "requested" {
		t.Errorf("request status = %q, want a fresh request left alone", status)
	}
	if verdict, _ := requestCopy(ctx, t, pool, requestID); verdict != "fetching" {
		t.Errorf("copy verdict = %s, want still fetching", verdict)
	}
}

// slskd forgets a transfer when it restarts, and the poller leaves a transfer
// it is not told about alone. Live, one such request at 40% was still open five
// weeks later. Past the deadline it is withdrawn whatever its transfer says.
func TestAStartedRequestPastTheDeadlineIsWithdrawn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service, targetID, requestID := fetchingWant(ctx, t, pool)
	seedStartedQueuedTransfer(ctx, t, pool, requestID, time.Now().Add(-deliveryDeadline-time.Minute))
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET status = 'downloading', provider_state = 'InProgress',
		    transferred_bytes = 4302412, size_bytes = 11063443
		WHERE download_request_id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	sweepAgain(ctx, t, service)

	if status := requestStatus(ctx, t, pool, requestID); status != "cancelled" {
		t.Errorf("request status = %q, want the stalled transfer withdrawn", status)
	}
	if verdict, summary := requestCopy(ctx, t, pool, requestID); verdict != "undelivered" ||
		summary != stalledFileSummary {
		t.Errorf("copy = %s %q, want undelivered %q", verdict, summary, stalledFileSummary)
	}
	assertLookedElsewhere(ctx, t, pool, targetID, requestID)
}

// A transfer that is still inside the deadline is a copy on its way.
func TestAStartedRequestInsideTheDeadlineIsLeftAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service, _, requestID := fetchingWant(ctx, t, pool)
	seedStartedQueuedTransfer(ctx, t, pool, requestID, time.Now().Add(-time.Hour))
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET status = 'downloading' WHERE download_request_id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	sweepAgain(ctx, t, service)

	if status := requestStatus(ctx, t, pool, requestID); status != "started" {
		t.Errorf("request status = %q, want a transfer inside the deadline left running", status)
	}
}

// A want that was removed while its copy sat in a peer's queue is never looked
// at again, so the queue check in hunt never withdrew its request. The
// deadline does not depend on the want.
func TestAStalledRequestOfARemovedWantIsWithdrawn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service, targetID, requestID := fetchingWant(ctx, t, pool)
	seedStartedQueuedTransfer(ctx, t, pool, requestID, time.Now().Add(-deliveryDeadline-time.Minute))
	if _, err := service.StopPursuing(ctx, targetID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	sweepAgain(ctx, t, service)

	if status := requestStatus(ctx, t, pool, requestID); status != "cancelled" {
		t.Errorf("request status = %q, want the queued transfer withdrawn", status)
	}
	if verdict, _ := requestCopy(ctx, t, pool, requestID); verdict != "undelivered" {
		t.Errorf("copy verdict = %s, want undelivered", verdict)
	}
	want, err := service.Target(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if want.Status != "not_wanted" {
		t.Errorf("want status = %s, want the person's removal kept", want.Status)
	}
}

// A transfer failed after its want was settled or removed. Only a pass over
// that want freed its copy, and no pass came, so the copy said "being fetched"
// for good: 66 copies on the live instance in October 2026.
func TestACopyWhoseRequestFailedIsFreedWhateverTheWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service, targetID, requestID := fetchingWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'failed' WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StopPursuing(ctx, targetID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	sweepAgain(ctx, t, service)

	if verdict, summary := requestCopy(ctx, t, pool, requestID); verdict != "undelivered" ||
		summary != "Download ended without the file." {
		t.Errorf("copy = %s %q, want undelivered", verdict, summary)
	}
}
