package acquisition

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/matching"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// fakeHunter offers whatever a test says peers are sharing. It is locked because
// one pass attempts several wants at once, which is the point of the pass.
type fakeHunter struct {
	mutex            sync.Mutex
	candidates       []sources.Candidate
	waivedCandidates []sources.Candidate
	// refused is what the user's stored format preferences turned away. A want
	// with candidates and no refusals is the ordinary case.
	refused []sources.Refused
	err     error
	texts   [][]string
	queries []sources.Query
	// preferences are what the search applied, handed back so the want's own
	// choice of file can be held to the same bitrate floor the folders were.
	preferences sources.Preferences
	// during is whatever the rest of the world does while this want is out at a
	// provider. A real search takes minutes and nothing stands still for it, so
	// this is where a test puts the decision that lands mid-search.
	during func()
}

func (hunter *fakeHunter) SearchFor(
	_ context.Context, query sources.Query, rungs []sources.Rung,
) ([]sources.Candidate, []sources.Refused, sources.Preferences, string, error) {
	hunter.mutex.Lock()
	defer hunter.mutex.Unlock()
	texts := sources.QueryTexts(rungs)
	hunter.texts = append(hunter.texts, texts)
	hunter.queries = append(hunter.queries, query)
	if hunter.during != nil {
		hunter.during()
	}
	if hunter.err != nil {
		return nil, hunter.refused, hunter.preferences, texts[0], hunter.err
	}
	return hunter.candidates, hunter.refused, hunter.preferences, texts[0], nil
}

func (hunter *fakeHunter) SearchForWithFloorWaived(
	_ context.Context, query sources.Query, rungs []sources.Rung,
) ([]sources.Candidate, []sources.Refused, sources.Preferences, string, error) {
	hunter.mutex.Lock()
	defer hunter.mutex.Unlock()
	texts := sources.QueryTexts(rungs)
	hunter.texts = append(hunter.texts, texts)
	hunter.queries = append(hunter.queries, query)
	if hunter.during != nil {
		hunter.during()
	}
	preferences := hunter.preferences
	preferences.MinimumBitRate = 0
	preferences.FormatMinimumBitRate = nil
	candidates := hunter.waivedCandidates
	if candidates == nil {
		candidates = hunter.candidates
	}
	if hunter.err != nil {
		return nil, hunter.refused, preferences, texts[0], hunter.err
	}
	return candidates, hunter.refused, preferences, texts[0], nil
}
func (hunter *fakeHunter) StoredPreferences(context.Context) sources.Preferences {
	hunter.mutex.Lock()
	defer hunter.mutex.Unlock()
	return hunter.preferences
}

// fakeFetcher stands in for the transfer path. started records what was asked
// for and batches records how it was asked — one entry per call, which is what
// says whether one peer was asked once or several times. err makes the provider
// refuse it.
//
// Cancel has no provider to stop a transfer at, so it reaches straight into the
// real store — exactly what is left of the production Cancel once slskd has
// been told, and pool is set by hunting so every test gets one without asking.
type fakeFetcher struct {
	mutex     sync.Mutex
	started   []uuid.UUID
	batches   [][]uuid.UUID
	cancelled []uuid.UUID
	err       error
	pool      *pgxpool.Pool
}

func (fetcher *fakeFetcher) StartTogether(_ context.Context, requestIDs []uuid.UUID) error {
	fetcher.mutex.Lock()
	defer fetcher.mutex.Unlock()
	fetcher.batches = append(fetcher.batches, append([]uuid.UUID{}, requestIDs...))
	fetcher.started = append(fetcher.started, requestIDs...)
	return fetcher.err
}

func (fetcher *fakeFetcher) Cancel(
	ctx context.Context, requestID uuid.UUID,
) (db.DownloadRequestRow, error) {
	fetcher.mutex.Lock()
	fetcher.cancelled = append(fetcher.cancelled, requestID)
	fetcher.mutex.Unlock()
	return db.New(fetcher.pool).CancelDownloadRequest(ctx, requestID)
}

type fakeListener struct{ configured bool }

func (listener *fakeListener) Configured(context.Context) bool { return listener.configured }

func offering(files ...sources.File) []sources.Candidate {
	return []sources.Candidate{{
		Provider: "slskd", Username: "peer", Directory: `Music\Portishead\Dummy`,
		Files: files, Score: 0.8,
	}}
}

func hunting(
	pool *pgxpool.Pool, hunter *fakeHunter, fetcher *fakeFetcher, listening bool,
) *Service {
	fetcher.pool = pool
	return newTestService(pool).
		WithHunter(hunter).
		WithFetcher(fetcher).
		WithListener(&fakeListener{configured: listening})
}

func offers(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) []db.AcquisitionTargetFileRow {
	t.Helper()
	found, err := db.New(pool).AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatalf("AcquisitionTargetFiles() error = %v", err)
	}
	return found
}

// The whole point of the loop: a want nothing in the library satisfies has one
// copy fetched for it, chosen by what the peer disclosed and recorded as being
// fetched so nothing fetches it twice.
func TestASweepFetchesOneCopyOfAWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
			Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want one copy at a time", len(fetcher.started))
	}
	recorded := offers(ctx, t, pool, target.ID)
	if len(recorded) != 1 || recorded[0].Verdict != db.AcquiredFileFetching {
		t.Fatalf("offers = %+v, want the chosen copy recorded as being fetched", recorded)
	}
	if recorded[0].FileName != "05 Glory Box.flac" {
		t.Errorf("chose %q, want the file named and timed like the want",
			recorded[0].FileName)
	}

	// The words it was asked for in are what a want has to search with, and the
	// length is what a peer can be corroborated against.
	if len(hunter.texts) != 1 || hunter.texts[0][0] != "Portishead Glory Box" {
		t.Errorf("searched %v, want the entry's own words", hunter.texts)
	}
	if len(hunter.queries[0].Tracks) != 1 || hunter.queries[0].Tracks[0].DurationSeconds != 301 {
		t.Errorf("query = %+v, want the wanted length carried into the search",
			hunter.queries[0])
	}

	// The want keeps its turn rather than spending it: the copy in flight is what
	// settles it, and the attempt was not a failure to find anything.
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" || waiting.Attempts != 0 {
		t.Errorf("target = %s attempts = %d, want the fetch not counted as a look",
			waiting.Status, waiting.Attempts)
	}
	// And it is not due again immediately. A want that stayed permanently due
	// would have the sweeper queueing itself over and over, and the one worker
	// would never reach the poller that settles the transfer it is waiting for.
	if !waiting.NextAttemptAt.Valid || !waiting.NextAttemptAt.Time.After(time.Now()) {
		t.Errorf("next attempt = %v, want the want waiting for the copy",
			waiting.NextAttemptAt)
	}
}

// A copy already in flight is this want's turn taken. Fetching a second one
// would be two transfers of the same music, which is what the one open request
// per want exists to prevent.
func TestASweepDoesNotFetchASecondCopyWhileOneIsInFlight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	// Due again immediately, as it would be after a restart.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}

	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want the one already in flight left alone",
			len(fetcher.started))
	}
	if len(hunter.texts) != 1 {
		t.Errorf("searched %d times, want a want with a copy in flight not searched again",
			len(hunter.texts))
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !waiting.NextAttemptAt.Valid || !waiting.NextAttemptAt.Time.After(time.Now()) {
		t.Fatalf("next attempt = %v, want a time to come back at rather than a spin",
			waiting.NextAttemptAt)
	}
}

// openRequestID reads the request a sweep opened for this want's own copy.
func openRequestID(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM download_requests
		WHERE acquisition_target_id = $1
		ORDER BY requested_at DESC LIMIT 1
	`, targetID).Scan(&id); err != nil {
		t.Fatalf("read the open request for want %s: %v", targetID, err)
	}
	return id
}

// seedStartedQueuedTransfer moves a request the fake fetcher only recorded
// into the state slskd itself leaves behind for a peer that accepted a file
// and has said nothing since: the request started, and its one transfer
// sitting at 'queued' since queuedAt. There is no poller under test here, so
// the state it would have written is written directly.
func seedStartedQueuedTransfer(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, requestID uuid.UUID, queuedAt time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'started', started_at = now() WHERE id = $1
	`, requestID); err != nil {
		t.Fatalf("start the request %s: %v", requestID, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (
			download_request_id, provider, remote_path, source_username,
			size_bytes, status, queued_at
		)
		SELECT id, provider, files->0->>'path', source_username,
		       coalesce((files->0->>'sizeBytes')::bigint, 0), 'queued', $2
		FROM download_requests WHERE id = $1
	`, requestID, queuedAt); err != nil {
		t.Fatalf("queue the transfer for request %s: %v", requestID, err)
	}
}

// A peer that took a download into its queue and then said nothing for two
// hours — no position, no byte — is not a copy on its way, whatever the
// request row still claims. Schall withdraws it, spends the want's turn on
// what that silence came to, and says so.
func TestAQueuedCopyPastPatienceIsCancelledAndTheWantIsRequeued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	requestID := openRequestID(ctx, t, pool, target.ID)
	seedStartedQueuedTransfer(ctx, t, pool, requestID, time.Now().Add(-queuePatience-time.Minute))
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}

	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM download_requests WHERE id = $1
	`, requestID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Errorf("request status = %q, want the stalled transfer withdrawn", status)
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" {
		t.Fatalf("status = %s, want the want still wanted", waiting.Status)
	}
	if waiting.Attempts != 1 {
		t.Errorf("attempts = %d, want the peer's silence to spend one", waiting.Attempts)
	}
	if !waiting.NextAttemptAt.Valid || !waiting.NextAttemptAt.Time.After(time.Now()) {
		t.Errorf("next attempt = %v, want a time to look again", waiting.NextAttemptAt)
	}
	if waiting.Summary != queueExpiredSummary {
		t.Errorf("summary = %q, want %q", waiting.Summary, queueExpiredSummary)
	}
}

// A transfer that has only just gone quiet is not yet a peer that will never
// deliver. queuePatience has to actually pass before Schall gives up on it.
func TestAQueuedCopyUnderPatienceIsLeftAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	requestID := openRequestID(ctx, t, pool, target.ID)
	seedStartedQueuedTransfer(ctx, t, pool, requestID, time.Now().Add(-queuePatience+time.Minute))
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}

	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM download_requests WHERE id = $1
	`, requestID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "started" {
		t.Errorf("request status = %q, want the transfer still inside patience left running", status)
	}
	if len(hunter.texts) != 1 {
		t.Errorf("searched %d times, want a copy still inside patience left alone", len(hunter.texts))
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Attempts != 0 {
		t.Errorf("attempts = %d, want a copy still on its way to cost nothing", waiting.Attempts)
	}
}

// Cancelling a stalled transfer and asking the same peer for the same file on
// the very next look would be the churn repeated requests are — and the kind
// of asking that gets a Soulseek account banned. The peer that just went quiet
// is passed over for one that has not been tried.
func TestALookAfterAQueuedCopyExpiresDoesNotAskTheSamePeerAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	// Offers tie on rank and score, so reading order comes down to the file
	// path — "A ..." is put first deliberately, to pin which peer is chosen
	// first rather than leaving it to chance.
	wanted := sources.File{Name: "Glory Box.flac", DurationSeconds: 301}
	stalling := wanted
	stalling.Path = `Music\A Stalling\Glory Box.flac`
	other := wanted
	other.Path = `Music\B Other\Glory Box.flac`
	candidates := []sources.Candidate{
		{Provider: "slskd", Username: "stalling-peer", Directory: `Music\A Stalling`,
			Files: []sources.File{stalling}, Score: 0.8},
		{Provider: "slskd", Username: "other-peer", Directory: `Music\B Other`,
			Files: []sources.File{other}, Score: 0.8},
	}
	hunter := &fakeHunter{candidates: candidates}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	firstRequest := openRequestID(ctx, t, pool, target.ID)
	var firstPeer string
	if err := pool.QueryRow(ctx, `
		SELECT source_username FROM download_requests WHERE id = $1
	`, firstRequest).Scan(&firstPeer); err != nil {
		t.Fatal(err)
	}
	if firstPeer != "stalling-peer" {
		t.Fatalf("first offer chosen from %q, want the reading order's first peer", firstPeer)
	}
	seedStartedQueuedTransfer(ctx, t, pool, firstRequest, time.Now().Add(-queuePatience-time.Minute))

	// Cancelling only settles this attempt; the want looks again once its own
	// turn comes round, exactly as any other failed attempt does.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("third Sweep() error = %v", err)
	}

	if len(fetcher.started) != 2 {
		t.Fatalf("started %d transfers, want a second copy fetched from a different peer",
			len(fetcher.started))
	}
	secondRequest := fetcher.started[len(fetcher.started)-1]
	var secondPeer string
	if err := pool.QueryRow(ctx, `
		SELECT source_username FROM download_requests WHERE id = $1
	`, secondRequest).Scan(&secondPeer); err != nil {
		t.Fatal(err)
	}
	if secondPeer != "other-peer" {
		t.Errorf("second offer chosen from %q, want the peer that never delivered skipped", secondPeer)
	}
}

// A want's turn is minutes long, and what settles it can land inside them: a
// sibling's harvest reaching it, a person withdrawing it, its own copy arriving.
// The pass comes back holding a row that was true when it left, and a request
// written from that row would be a second copy of music the library already has.
func TestASweepDoesNotFetchForAWantSettledWhileItWasSearched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	recordingID := uuid.New()
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)

	// While the provider is being asked, the library turns out to hold the
	// recording and something else settles the want against the file that has it
	// — which is exactly what a sibling's harvest does.
	queries := db.New(pool)
	settled := uuid.UUID{}
	hunter.during = func() {
		settled = seedIdentifiedFile(ctx, t, pool, "/music/glory-box.flac", recordingID)
		if err := queries.SettleAcquiredTarget(
			ctx, target.ID, settled, "already in the library", "settled elsewhere",
		); err != nil {
			t.Errorf("settle the want mid-search: %v", err)
		}
	}

	// The refusal is the pass working, not the pass failing.
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 0 {
		t.Errorf("started %d transfers, want none for a want nobody is waiting on",
			len(fetcher.started))
	}
	var requests int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM download_requests WHERE acquisition_target_id = $1
	`, target.ID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Errorf("%d requests written for a want that had moved on, want none", requests)
	}
	// The offer is written in the same transaction as the request, so a refusal
	// leaves neither behind.
	if recorded := offers(ctx, t, pool, target.ID); len(recorded) != 0 {
		t.Errorf("offers = %+v, want nothing recorded against a settled want", recorded)
	}
	// And what settled it stands: the pass wrote nothing over it.
	answered, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != "acquired" || answered.AcquiredLibraryFileID.UUID != settled {
		t.Errorf("target = %s against %v, want it still settled against the file the library holds",
			answered.Status, answered.AcquiredLibraryFileID)
	}
}

// A want is asked about before its search and answered after it, and a sibling's
// harvest can open its one request in between. The second copy is not started:
// one open request per want is what stops two transfers of the same music, and
// the want is told that rather than handed the index that would have refused it.
func TestASweepDoesNotFetchASecondCopyForAWantGivenOneMidSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	// While the provider is being asked, another pass chooses a copy for this
	// want out of a folder it already had open.
	queries := db.New(pool)
	elsewhere := uuid.UUID{}
	hunter.during = func() {
		id, err := queries.FetchAcquiredFile(ctx, db.FetchAcquiredFileParams{
			AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "other",
			SourceDirectory: `Music\Dummy`, RemotePath: `Music\Dummy\05 Glory Box.flac`,
			FileName: "05 Glory Box.flac", Extension: "flac", SizeBytes: 100, Score: 0.9,
			Summary: "fetching",
		})
		if err != nil {
			t.Errorf("open a request for the want mid-search: %v", err)
		}
		elsewhere = id
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 0 {
		t.Errorf("started %d transfers, want the copy the want already follows left alone",
			len(fetcher.started))
	}
	var requests []uuid.UUID
	rows, err := pool.Query(ctx,
		`SELECT id FROM download_requests WHERE acquisition_target_id = $1`, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, id)
	}
	if len(requests) != 1 || requests[0] != elsewhere {
		t.Errorf("requests = %v, want only the one opened while the search was running",
			requests)
	}
}

// A copy that has already been judged is never fetched again. A copy that merely
// failed to arrive has not been judged at all, and may be tried another day.
func TestASweepSkipsCopiesItHasAlreadyJudged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	refused := sources.File{
		Path: `Music\refused.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	undelivered := sources.File{
		Path: `Music\undelivered.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	hunter := &fakeHunter{candidates: offering(refused, undelivered)}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	queries := db.New(pool)
	for _, judged := range []struct {
		file    sources.File
		verdict string
	}{{refused, db.AcquiredFileDiscardedAudio}, {undelivered, db.AcquiredFileUndelivered}} {
		if err := queries.RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
			AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: judged.file.Path, FileName: judged.file.Name,
			Verdict: judged.verdict, Summary: "recorded by an earlier pass",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The failed transfer is old news: its rest has passed, so the copy is
	// worth asking for again, while the refusal stays struck off for good.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET decided_at = now() - interval '25 hours'
		WHERE acquisition_target_id = $1 AND remote_path = $2
	`, target.ID, undelivered.Path); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want the copy that never arrived tried again",
			len(fetcher.started))
	}
	recorded := offers(ctx, t, pool, target.ID)
	for _, offer := range recorded {
		if offer.RemotePath == undelivered.Path && offer.Verdict != db.AcquiredFileFetching {
			t.Errorf("offer = %+v, want the undelivered copy being fetched", offer)
		}
		if offer.RemotePath == refused.Path && offer.Verdict != db.AcquiredFileDiscardedAudio {
			t.Errorf("offer = %+v, want the refusal untouched", offer)
		}
	}
}

// A copy that failed to arrive is never struck off — a transfer says nothing
// about the music — but it no longer outranks everything else forever: one
// broken copy was fetched twenty-five times in a day while untried copies sat
// below it. Every copy nobody has tried goes first.
func TestAnUntriedCopyGoesBeforeOneThatFailedToArrive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	failed := sources.File{
		Path: `Music\failed.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	untried := sources.File{
		Path: `Music\untried.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	hunter := &fakeHunter{candidates: offering(failed, untried)}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: failed.Path, FileName: failed.Name,
		Verdict: db.AcquiredFileUndelivered, Summary: "the bytes did not arrive",
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want exactly one", len(fetcher.started))
	}
	for _, offer := range offers(ctx, t, pool, target.ID) {
		if offer.RemotePath == untried.Path && offer.Verdict != db.AcquiredFileFetching {
			t.Errorf("offer = %+v, want the untried copy being fetched", offer)
		}
		if offer.RemotePath == failed.Path && offer.Verdict != db.AcquiredFileUndelivered {
			t.Errorf("offer = %+v, want the failed copy left resting", offer)
		}
	}
}

// With nothing untried on offer, a copy that just failed to arrive is left to
// rest rather than fetched again on the spot. The want waits out the ladder —
// asking the same peer for the same file every few minutes hammered one peer
// all day and never produced a different answer.
func TestACopyThatFailedToArriveRestsBeforeBeingAskedForAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	failed := sources.File{
		Path: `Music\failed.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, &fakeHunter{candidates: offering(failed)}, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: failed.Path, FileName: failed.Name,
		Verdict: db.AcquiredFileUndelivered, Summary: "the bytes did not arrive",
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 0 {
		t.Fatalf("started %d transfers, want the resting copy left alone", len(fetcher.started))
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" || !waiting.NextAttemptAt.Valid {
		t.Fatalf("target = %#v, want it wanted still, with a later attempt", waiting)
	}
}

// Every copy found for this want disagreed on the artist credit alone. The want
// stops and puts the copies in the review queue instead of searching again.
func TestAWantWithCreditOnlyRefusalsStopsForReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	offered := sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	hunter := &fakeHunter{candidates: offering(offered)}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: offered.Path, FileName: offered.Name,
		Verdict: db.AcquiredFileDiscardedTags, Summary: "the artist credit differs",
		Evidence: &db.AcquiredFileEvidence{
			Name:   offered.Name,
			Agrees: []string{"title", "ISRC", "audio"}, Differs: []string{"artist"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" || waiting.NextAttemptAt.Valid {
		t.Fatalf("target = %#v, want it pending without another attempt", waiting)
	}
	if len(hunter.queries) != 0 {
		t.Fatalf("searches = %d, want no search after the last copy was refused", len(hunter.queries))
	}
	page, err := service.ReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("ReviewQueue() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || len(page.Items[0].Files) != 1 {
		t.Fatalf("review queue = %+v, want the refused copy offered", page)
	}
	if page.Items[0].Files[0].Evidence == nil ||
		len(page.Items[0].Files[0].Evidence.Differs) != 1 ||
		page.Items[0].Files[0].Evidence.Differs[0] != "artist" {
		t.Fatalf("evidence = %+v, want the artist contradiction preserved", page.Items[0].Files[0].Evidence)
	}
}

// A recording-ID contradiction is a real refusal. It keeps the want in the
// search loop because another copy may still prove the wanted recording.
func TestAWantWithRecordingIDRefusalStillRequeues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	offered := sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	hunter := &fakeHunter{candidates: offering(offered)}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: offered.Path, FileName: offered.Name,
		Verdict: db.AcquiredFileDiscardedTags, Summary: "the recording ID differs",
		Evidence: &db.AcquiredFileEvidence{
			Name: offered.Name, Differs: []string{"recording ID"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" || waiting.Attempts != 1 || !waiting.NextAttemptAt.Valid {
		t.Fatalf("target = %#v, want it wanted still, with a later attempt", waiting)
	}
}

// Only the audio can admit a copy a stranger sent, so an installation that
// cannot listen fetches nothing at all. Downloading files it could never accept
// would spend the user's bandwidth and a peer's upload slot for nothing.
func TestNothingIsFetchedWhenNoCopyCouldEverBeVerified(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, false)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 0 || len(fetcher.started) != 0 {
		t.Fatalf("searched %v and started %v, want nothing fetched",
			hunter.texts, fetcher.started)
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" || !waiting.NextAttemptAt.Valid {
		t.Fatalf("target = %#v, want it still wanted", waiting)
	}
	if waiting.Summary != unlistenableSummary {
		t.Errorf("summary = %q, want it to say why nothing was fetched", waiting.Summary)
	}
}

// An anchor gives the acquisition loop a verification path when AcoustID is not configured.
func TestAnAnchoredWantIsHuntedWithoutAnAcoustIDListener(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, false)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET anchor_fingerprint = 'anchor' WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(hunter.texts) != 1 || len(fetcher.started) != 1 {
		t.Fatalf("searched %v and started %v, want one fetched copy", hunter.texts, fetcher.started)
	}
}

// A provider that could not be reached has said nothing about the music. It is
// recorded as a failure to ask, kept apart from nobody sharing it, and the want
// is as wanted as it was.
func TestASearchThatFailedIsNotAnAnswerAboutTheMusic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool,
		&fakeHunter{err: errors.New("dial slskd: connection refused")}, &fakeFetcher{}, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" || !waiting.LastError.Valid {
		t.Fatalf("target = %#v, want the failure recorded against Schall", waiting)
	}
	var outcome string
	if err := pool.QueryRow(ctx, `
		SELECT outcome FROM acquisition_target_attempts
		WHERE acquisition_target_id = $1 ORDER BY recorded_at DESC LIMIT 1
	`, target.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" {
		t.Errorf("outcome = %q, want it kept apart from nobody having it", outcome)
	}
}

// One refusal is heard by the whole pass. The provider saying "no more
// searches" to one want is saying it to all of them, and a pass that asked
// anyway burned its whole round learning the same fact once per want —
// seventeen of nineteen, one evening. The wants that were never asked stand
// back for the same short while, spending nothing.
func TestOneRefusalStandsTheWholePassBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{err: sources.ErrRateLimited}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	titles := []string{
		"Mysterons", "Sour Times", "Strangers", "It Could Be Sweet",
		"Wandering Star", "Numb", "Roads", "Glory Box",
	}
	targets := make([]db.AcquisitionTargetRow, 0, len(titles))
	for _, title := range titles {
		target, _, err := service.Create(ctx, Entry{
			Origin: "manual", Artist: "Portishead", Title: title,
			RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		targets = append(targets, target)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	// At most one search per worker slot can be in flight before the first
	// refusal lands; every want after that stands back unasked.
	if len(hunter.texts) > huntWorkers {
		t.Errorf("searched %d wants of %d, want the refusal heard by the rest of the pass",
			len(hunter.texts), len(titles))
	}
	for _, target := range targets {
		waiting, err := service.Target(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waiting.Attempts != 0 {
			t.Errorf("%s: attempts = %d, want a throttled pass to spend none",
				target.EntryTitle, waiting.Attempts)
		}
		if waiting.Summary != throttledSummary {
			t.Errorf("%s: summary = %q, want %q", target.EntryTitle, waiting.Summary,
				throttledSummary)
		}
	}
}

// A source that cannot search at all is the other refusal, and it costs the want
// exactly as little: slskd logged out of Soulseek for two hours took 156 wants
// down the ladder with it, none of which had been looked for.
func TestASourceThatCannotSearchSpendsNoAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool,
		&fakeHunter{err: sources.ErrProviderUnavailable}, &fakeFetcher{}, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Attempts != target.Attempts {
		t.Errorf("attempts = %d, want the want to have spent none of them (%d)",
			waiting.Attempts, target.Attempts)
	}
	var recorded int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_target_attempts
		WHERE acquisition_target_id = $1
	`, target.ID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("attempt rows = %d, want none written for a question nobody asked", recorded)
	}
}

// The two refusals mean different things to whoever has to fix them, so a want
// stood down by a source that cannot search must not be told the source is busy.
func TestAPassStoodDownByAnUnavailableSourceDoesNotCallItBusy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool,
		&fakeHunter{err: sources.ErrProviderUnavailable}, &fakeFetcher{}, true)

	titles := []string{
		"Mysterons", "Sour Times", "Strangers", "It Could Be Sweet",
		"Wandering Star", "Numb", "Roads", "Glory Box",
	}
	targets := make([]db.AcquisitionTargetRow, 0, len(titles))
	for _, title := range titles {
		target, _, err := service.Create(ctx, Entry{
			Origin: "manual", Artist: "Portishead", Title: title,
			RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		targets = append(targets, target)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	for _, target := range targets {
		waiting, err := service.Target(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waiting.Summary != unavailableSummary {
			t.Errorf("%s: summary = %q, want %q", target.EntryTitle, waiting.Summary,
				unavailableSummary)
		}
	}
}

// Being refused for asking too often is the provider's rate limiter talking, not
// an answer about the music. It must not spend one of the six attempts that
// carry a want out to a daily cadence, or a press big enough to be throttled
// would bury the very wants it was throttled for.
func TestBeingRefusedForAskingTooOftenSpendsNoAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool,
		&fakeHunter{err: sources.ErrRateLimited}, &fakeFetcher{}, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	before := service.now()
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Attempts != target.Attempts {
		t.Errorf("attempts = %d, want the want to have spent none of them (%d)",
			waiting.Attempts, target.Attempts)
	}
	var recorded int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_target_attempts
		WHERE acquisition_target_id = $1
	`, target.ID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("attempt rows = %d, want none written for a question nobody asked", recorded)
	}
	if !waiting.LastError.Valid {
		t.Error("no reason recorded, want the refusal readable on the want")
	}
	if !waiting.NextAttemptAt.Valid ||
		waiting.NextAttemptAt.Time.After(before.Add(throttleDelay+time.Minute)) {
		t.Errorf("next attempt = %v, want it back within the throttle delay of %v",
			waiting.NextAttemptAt.Time, throttleDelay)
	}
}

// The music has arrived and only the scan that gives it a library row is still
// to come. Nothing may go looking in that window: the copy it fetched would be a
// second copy of exactly the music sitting in the import folder.
func TestAWantWhoseCopyIsImportedDoesNotLookForAnother(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	// The shape the importer leaves behind: the copy is accepted, and the scan
	// that would make a library file of it has not run.
	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID,
		Provider:            "slskd",
		SourceUsername:      "peer",
		RemotePath:          `Music\Glory Box.flac`,
		FileName:            "Glory Box.flac",
		SizeBytes:           9_000_000,
		Verdict:             db.AcquiredFileAccepted,
		Summary:             "Proven by its audio.",
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 0 || len(fetcher.started) != 0 {
		t.Fatalf("searched %v and started %v, want nothing looked for", hunter.texts, fetcher.started)
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != importedSummary {
		t.Errorf("summary = %q, want it to say the copy is being taken in", waiting.Summary)
	}
}

// A peer that has withdrawn the file between the search and the transfer refuses
// the start. Nothing was learned about the music, so the copy is not struck off
// and the want carries straight on.
func TestACopyTheProviderWouldNotStartIsNotAJudgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{err: errors.New("peer is not sharing that any more")}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	recorded := offers(ctx, t, pool, target.ID)
	if len(recorded) != 1 || recorded[0].Verdict != db.AcquiredFileUndelivered {
		t.Fatalf("offers = %+v, want a transport fact rather than a verdict", recorded)
	}
	var open int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM download_requests
		WHERE acquisition_target_id = $1 AND status IN ('requested', 'started')
	`, target.ID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("open requests = %d, want the request withdrawn", open)
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" {
		t.Fatalf("status = %s, want it still wanted", waiting.Status)
	}
}

func createWantWithDuration(
	ctx context.Context, t *testing.T, service *Service, recordingID uuid.UUID, durationMS int32,
) db.AcquisitionTargetRow {
	t.Helper()
	target, _, err := service.Create(ctx, Entry{
		Origin: "manual", Artist: "Portishead", Title: "Glory Box",
		DurationMS:  &durationMS,
		RecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return target
}

// The copy was proven and imported; the scan has since made a library file of it.
// This is where the loop closes: the file is given the identity verification
// proved, the release behind it is asked for, and the want is finished.
func TestASweepFinishesACopyTheScanHasSinceLinked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{}, &fakeFetcher{}, true)

	recordingID, releaseGroupID := uuid.New(), uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_release_group_id = $2 WHERE id = $1
	`, target.ID, releaseGroupID); err != nil {
		t.Fatal(err)
	}
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "acquired" || settled.AcquiredLibraryFileID.UUID != fileID {
		t.Fatalf("target = %#v, want it acquired against the imported file", settled)
	}

	var (
		method     string
		confidence float32
		manual     bool
		ingests    int
	)
	if err := pool.QueryRow(ctx, `
		SELECT method, confidence, is_manual,
		       (SELECT count(*) FROM jobs WHERE kind = 'ingest_release_group')
		FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&method, &confidence, &manual, &ingests); err != nil {
		t.Fatal(err)
	}
	if method != "fingerprint" || confidence != 1 || manual {
		t.Errorf("identity = %s/%v/%v, want what proved the copy", method, confidence, manual)
	}
	if ingests != 1 {
		t.Errorf("queued ingests = %d, want the release asked for once", ingests)
	}

	// The file is answered, so the ordinary resolution job never runs on it. Its
	// first act is to delete every identity nobody decided by hand, which would
	// throw away the one proof that admitted the copy and leave a stranger's tags
	// deciding what the file is.
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT resolution_status FROM library_files WHERE id = $1
	`, fileID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" {
		t.Errorf("resolution status = %q, want the proof out of reach of the tag-only path",
			status)
	}
}

// The library already says that file is something else. Somebody's decision, or
// an earlier resolution, is never overwritten by this one — the want is left in
// the queue with the disagreement as its reason.
func TestACopyTheLibraryCallsSomethingElseIsNotSettledOver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{}, &fakeFetcher{}, true)

	recordingID := uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)

	somethingElse := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, is_manual, summary
		)
		VALUES ($1, 'external', $2, 'manual', true, 'decided by hand')
	`, fileID, somethingElse); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status == "acquired" {
		t.Fatal("a want must not settle against a file the library calls something else")
	}
	if waiting.Summary != disagreedSummary {
		t.Errorf("summary = %q, want the disagreement said out loud", waiting.Summary)
	}
	var stored uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT musicbrainz_recording_id FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != somethingElse {
		t.Errorf("identity = %s, want the decision already recorded kept", stored)
	}
}

// A want whose copy is on the disc has its music, whatever the library calls
// that file. It stops rather than looking again: nothing may say the two
// recordings are one, and every further look fetches the same song. One want
// fetched twelve copies of it in sixty-four minutes before this (#368).
func TestAWantWhoseCopyTheLibraryCallsSomethingElseStopsLooking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	recordingID := uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)
	// What the ordinary file-resolution job wrote first, from the file's own tag:
	// MusicBrainz's other row for the same performance.
	seedIdentityOf(ctx, t, pool, fileID, uuid.New())

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("target = %s / next attempt %v, want it still wanted and no longer due",
			stopped.Status, stopped.NextAttemptAt)
	}
	if stopped.Summary != disagreedSummary {
		t.Errorf("summary = %q, want %q", stopped.Summary, disagreedSummary)
	}
	if len(fetcher.started) != 0 {
		t.Errorf("started %d transfers, want the copy already on the disc left alone",
			len(fetcher.started))
	}
	if got := requestsFor(ctx, t, pool, target.ID); got != 1 {
		t.Errorf("requests = %d, want no copy fetched beyond the one already here", got)
	}
}

// Wanting it again is a person's decision and it is honoured, but it is not a
// second copy: a want that comes round with its copy still filed under another
// recording is stopped again rather than searched for.
func TestAWantThatComesRoundHoldingAnAcceptedCopyIsNotSearchedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	recordingID := uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)
	seedIdentityOf(ctx, t, pool, fileID, uuid.New())
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}

	if len(hunter.texts) != 0 {
		t.Errorf("searched %v, want no search for music already on the disc", hunter.texts)
	}
	if got := requestsFor(ctx, t, pool, target.ID); got != 1 {
		t.Errorf("requests = %d, want no second copy fetched", got)
	}
	stopped, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.NextAttemptAt.Valid {
		t.Errorf("next attempt = %v, want the want stopped again", stopped.NextAttemptAt)
	}
}

// The way back. A person says by hand which recording that file is; the want is
// then settled against the file already on the disc, and nothing is fetched.
func TestAStoppedWantIsSettledOnceTheFileIsSaidToBeTheRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	fetcher := &fakeFetcher{}
	service := hunting(pool, &fakeHunter{}, fetcher, true)

	recordingID := uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)
	seedIdentityOf(ctx, t, pool, fileID, uuid.New())
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	// The person says what that file is, through the path the review queue takes.
	// Nothing else is touched: no want is rescheduled by hand here, because the
	// decision itself has to be what gives the want its next look back.
	offerCandidate(ctx, t, pool, fileID, recordingID)
	if err := identity.NewService(pool, nil, zerolog.Nop()).Accept(ctx, fileID, recordingID); err != nil {
		t.Fatalf("Accept() error = %v", err)
	}

	rearmed, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rearmed.NextAttemptAt.Valid {
		t.Fatal("the want has no next look, so nothing will ever read the decision")
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}

	settled, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "acquired" || settled.AcquiredLibraryFileID.UUID != fileID {
		t.Fatalf("target = %s / file %v, want it settled against the file on the disc",
			settled.Status, settled.AcquiredLibraryFileID)
	}
	if len(fetcher.started) != 0 {
		t.Errorf("started %d transfers, want nothing fetched on the way back",
			len(fetcher.started))
	}
}

// offerCandidate puts one recording among the answers a file's resolution found,
// which is the only thing a person is allowed to accept for it.
func offerCandidate(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID, recordingID uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_candidates (
			library_file_id, musicbrainz_recording_id, artist_name, track_title,
			rank, agrees, differs, summary
		)
		VALUES ($1, $2, 'Portishead', 'Glory Box', 1, '{}', '{}', 'one of the answers')
	`, fileID, recordingID); err != nil {
		t.Fatalf("offer a candidate for file %s: %v", fileID, err)
	}
}

// A stopped want has its music, which is the only reason it stopped. Deleting
// that file — what somebody does with a surplus copy, and what the duplicate
// screens are for — takes the music away again, so the want goes back to being
// one nobody has a copy of: due, searched for, and fetched for like any other.
func TestDeletingTheCopyPutsAStoppedWantBackInTheHunt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	// Somebody else's copy of the same song. The one the want already took is
	// struck off for good, as every copy that has been judged is, so a want that
	// looks again looks at what it has not been offered before.
	hunter := &fakeHunter{candidates: []sources.Candidate{{
		Provider: "slskd", Username: "another-peer", Directory: `Shared\Portishead`,
		Score: 0.8,
		Files: []sources.File{{
			Path: `Shared\Portishead\Glory Box.flac`, Name: "Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		}},
	}}}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	recordingID := uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)
	seedIdentityOf(ctx, t, pool, fileID, uuid.New())
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(hunter.texts) != 0 {
		t.Fatalf("searched %v before the copy was deleted", hunter.texts)
	}

	// The file goes the way the library deletes one, which is the only way a
	// library file ever goes.
	giveTheFileARoot(ctx, t, pool, fileID)
	if _, err := library.NewRemover(pool, zerolog.Nop()).Remove(ctx, fileID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep() error = %v", err)
	}
	if len(hunter.texts) != 1 {
		t.Fatalf("searched %v, want the recording looked for again once", hunter.texts)
	}
	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want a copy fetched for music nobody has",
			len(fetcher.started))
	}
}

// giveTheFileARoot puts the file inside a library root, with something on disk
// where it says it is. Deleting a file reads both.
func giveTheFileARoot(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID,
) {
	t.Helper()
	rootPath := t.TempDir()
	path := filepath.Join(rootPath, "05 Glory Box.flac")
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_roots (id, path) VALUES ($1, $2)
	`, rootID, rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET library_root_id = $2, path = $3 WHERE id = $1
	`, fileID, rootID, path); err != nil {
		t.Fatal(err)
	}
}

// The other door into a fetch. A folder found for one want is read for the wants
// sharing its release, and a sibling that stopped because its copy is in the
// library under another recording must not be fetched for out of it either.
//
// The door is shut by the list of siblings itself, which leaves out any want
// with a copy in flight, being validated or imported. This pins that it stays
// shut for the want this change created, which is a state that list was written
// before.
func TestAWantHoldingAnAcceptedCopyTakesNothingFromAFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{candidates: dummyFolder()}, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	fileID := acceptedCopy(ctx, t, pool, mysterons.ID, mysterons.MusicBrainzRecordingID.UUID)
	seedIdentityOf(ctx, t, pool, fileID, uuid.New())

	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	if got := requestsFor(ctx, t, pool, mysterons.ID); got != 1 {
		t.Fatalf("requests = %d, want a want that already holds a copy left alone", got)
	}
}

// requestsFor counts the transfers ever asked for on behalf of one want, which
// is what says whether the same music was fetched more than once.
func requestsFor(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM download_requests WHERE acquisition_target_id = $1
	`, targetID).Scan(&count); err != nil {
		t.Fatalf("count the requests of want %s: %v", targetID, err)
	}
	return count
}

// seedIdentityOf is the library's own answer about a file: what the ordinary
// file-resolution job wrote from the file's tags, before the want's copy was
// ever accounted for.
func seedIdentityOf(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID, recordingID uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'recording-id', 'the file said so itself')
	`, fileID, recordingID); err != nil {
		t.Fatalf("seed the identity of file %s: %v", fileID, err)
	}
}

// acceptedCopy is the world as the importer leaves it once a copy has been proven
// and the scan that followed has created its library row.
func acceptedCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	requestID, fileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/Portishead/Dummy/05 Glory Box.flac', 25000000, now())
	`, fileID); err != nil {
		t.Fatalf("seed the imported file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_path, imported_at
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Portishead/Dummy', 1, 1, 25000000,
		        'flac', 0.8, '[]'::jsonb, 'completed', 'imported', '/music', now())
	`, requestID, targetID); err != nil {
		t.Fatalf("seed the request: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (
			download_request_id, provider, remote_path, source_username,
			status, local_path, library_file_id
		)
		VALUES ($1, 'slskd', 'Music\Portishead\Dummy\05 Glory Box.flac', 'peer',
		        'completed', '/music/Portishead/Dummy/05 Glory Box.flac', $2)
	`, requestID, fileID); err != nil {
		t.Fatalf("seed the transfer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path, file_name,
			verdict, summary, evidence, download_request_id
		)
		VALUES ($1, 'slskd', 'peer', 'Music\Portishead\Dummy\05 Glory Box.flac',
		        '05 Glory Box.flac', 'accepted', 'Proven by its audio.', $2::jsonb, $3)
	`, targetID, `{"method":"fingerprint","confidence":1,"agrees":["audio"],
	   "wanted":{"artist":"Portishead","album":"Dummy","title":"Glory Box","durationMs":301000}}`,
		requestID); err != nil {
		t.Fatalf("seed the accepted copy: %v", err)
	}
	return fileID
}

// The moment a proven copy is accounted for, it is offered to the catalogue.
// The scanner reconciled the file before its identity existed, and a peer's
// tags routinely match nothing, so without this the release page keeps calling
// music that is sitting in the library missing.
func TestAnAccountedForCopyIsMappedToItsCatalogueTrack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool).WithMatcher(matching.NewService(pool, zerolog.Nop()))

	recordingID := uuid.New()
	target := createWant(ctx, t, service, recordingID)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)
	trackID := seedCatalogueTrack(ctx, t, pool, recordingID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "acquired" {
		t.Fatalf("status = %s, want acquired", settled.Status)
	}
	var mappedFile uuid.UUID
	var method string
	if err := pool.QueryRow(ctx, `
		SELECT library_file_id, method FROM track_mappings WHERE track_id = $1
	`, trackID).Scan(&mappedFile, &method); err != nil {
		t.Fatalf("read the mapping: %v", err)
	}
	if mappedFile != fileID || method != "resolved_identity" {
		t.Errorf("mapped %s by %q, want %s by resolved_identity", mappedFile, method, fileID)
	}
}

// seedCatalogueTrack is the release page the acquired copy should appear on.
func seedCatalogueTrack(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
	`, artistID); err != nil {
		t.Fatalf("seed the artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatalf("seed the album: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, title, track_number, musicbrainz_recording_id)
		VALUES ($1, $2, 'Glory Box', 5, $3)
	`, trackID, albumID, recordingID); err != nil {
		t.Fatalf("seed the track: %v", err)
	}
	return trackID
}

// A pass looks for every want whose turn it is, not the first few. Searching used
// to be capped per pass because it ran on the worker that also polled transfers
// and imported them; acquisition has its own lane now, and what is left to protect
// is slskd, which is bounded where its 429 is understood.
func TestAPassLooksForEveryWantWhoseTurnItIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	titles := []string{
		"Mysterons", "Sour Times", "Strangers", "It Could Be Sweet",
		"Wandering Star", "Numb", "Roads", "Glory Box",
	}
	for _, title := range titles {
		if _, _, err := service.Create(ctx, Entry{
			Origin: "manual", Artist: "Portishead", Title: title,
			RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(hunter.texts) != len(titles) {
		t.Fatalf("searched %d wants of %d, want every want whose turn it was looked for",
			len(hunter.texts), len(titles))
	}
}

// A want is looked for by its title alone once nothing else has found it, so a
// track by a band Soulseek will not say the name of can still be acquired.
func TestAWantIsLookedForByItsTitleAloneAsWell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	if _, _, err := service.Create(ctx, Entry{
		Origin: "manual", Artist: "Gorillaz", Title: "Feel Good Inc",
		RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 1 {
		t.Fatalf("searched %d times, want the one want looked for", len(hunter.texts))
	}
	ladder := hunter.texts[0]
	if ladder[0] != "Gorillaz Feel Good Inc" || ladder[len(ladder)-1] != "Feel Good Inc" {
		t.Fatalf("ladder = %v, want the artist first and the title alone last", ladder)
	}
}

// wantOnARelease records one want for a named track on a shared release.
func wantOnARelease(
	ctx context.Context, t *testing.T, service *Service, pool *pgxpool.Pool,
	title string, durationMS int32, releaseGroupID uuid.UUID,
) db.AcquisitionTargetRow {
	t.Helper()
	target, _, err := service.Create(ctx, Entry{
		Origin: "manual", Artist: "Portishead", Title: title, Album: "Dummy",
		DurationMS:  &durationMS,
		RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_release_group_id = $2 WHERE id = $1
	`, target.ID, releaseGroupID); err != nil {
		t.Fatal(err)
	}
	target.MusicBrainzReleaseGroupID = uuid.NullUUID{UUID: releaseGroupID, Valid: true}
	return target
}

// A peer shares a folder, not a track. The folder found for one want holds its
// siblings' music too, and asking each of them to find it again on its own turn
// asks a network that answers the same question differently every time.
func TestAFolderFoundForOneWantIsReadForTheWantsSharingItsRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
			Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	taken := offers(ctx, t, pool, mysterons.ID)
	if len(taken) != 1 || taken[0].FileName != "01 Mysterons.flac" {
		t.Fatalf("sibling offers = %+v, want its own file taken from the same folder", taken)
	}
	if got := offers(ctx, t, pool, glory.ID); len(got) != 1 {
		t.Fatalf("offers = %+v, want the want that searched to keep its own copy", got)
	}
	if len(hunter.texts) != 1 {
		t.Errorf("searched %d times, want the sibling to cost no search of its own",
			len(hunter.texts))
	}
}

// One folder, one person, one question. Ten wants that all chose the same peer
// and asked it ten times over is what "too many files" was the peer's answer to.
func TestTwoWantsSharingAFolderAreAskedForInOneRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
			Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	if len(fetcher.batches) != 1 {
		t.Fatalf("asked the peer %d times, want one question for the whole folder",
			len(fetcher.batches))
	}
	if len(fetcher.batches[0]) != 2 {
		t.Fatalf("batch = %v, want both wants' copies in it", fetcher.batches[0])
	}
}

// Batching is about how the peer is asked and never about what a want is. Each
// keeps its own request, because one open request per want is what stops the
// same music being fetched twice.
func TestWantsAskedForTogetherKeepARequestEach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
			Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	queries := db.New(pool)
	for _, want := range []uuid.UUID{glory.ID, mysterons.ID} {
		open, err := queries.OpenTargetRequest(ctx, want)
		if err != nil {
			t.Fatal(err)
		}
		if !open.Valid {
			t.Fatalf("want %s has no open request of its own", want)
		}
	}
	var requests int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM download_requests WHERE acquisition_target_id IS NOT NULL
	`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("recorded %d requests for two wants, want one each", requests)
	}
}

// The bound is on the peer rather than on the pass, so it has to hold against
// what earlier passes already asked that peer for. This is the production
// failure in miniature: every want found the same person, and nothing counted.
func TestAPeerAlreadyBeingAskedForEnoughIsNotAskedForMore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	offered := sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, &fakeHunter{candidates: offering(offered)}, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	// What earlier passes left open at that peer, recorded as the requests they
	// are rather than counted in memory: a restart forgets a counter and the loop
	// would go straight back to over-asking.
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			album_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, reasons, files
		)
		VALUES ($1, 'slskd', 'peer', 'Music\Elsewhere', $2, $2, 0, 'flac', 0.5, '{}', '[]'::jsonb)
	`, albumID, peerFileBudget); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 0 {
		t.Fatalf("started %d transfers, want a peer at its budget left alone",
			len(fetcher.started))
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Waiting one's turn is not a search that came to nothing, so no attempt is
	// spent and the want never claims nobody is sharing the music.
	if waiting.Attempts != 0 {
		t.Errorf("attempts = %d, want a busy peer to cost the want nothing", waiting.Attempts)
	}
	if waiting.Summary != peerBusySummary {
		t.Errorf("summary = %q, want it to say the peers are busy", waiting.Summary)
	}
}

// A busy peer only explains a want that had something to fetch. Where every copy
// on offer has already been answered, the queue in front of them is beside the
// point and the want is on the ladder as it always was.
func TestAWantWithNothingLeftToTryIsNotBlamedOnABusyPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	offered := sources.File{
		Path: `Music\Glory Box.flac`, Name: "Glory Box.flac", DurationSeconds: 301,
	}
	service := hunting(pool, &fakeHunter{candidates: offering(offered)}, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: offered.Path, FileName: offered.Name,
		Verdict: db.AcquiredFileDiscardedTags, Summary: "its own tags contradicted",
	}); err != nil {
		t.Fatal(err)
	}
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			album_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, reasons, files
		)
		VALUES ($1, 'slskd', 'peer', 'Music\Elsewhere', $2, $2, 0, 'flac', 0.5, '{}', '[]'::jsonb)
	`, albumID, peerFileBudget); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary == peerBusySummary {
		t.Errorf("summary = %q, want a want with nothing to try on the ladder", waiting.Summary)
	}
	if waiting.Attempts != 1 {
		t.Errorf("attempts = %d, want the look it actually made counted", waiting.Attempts)
	}
}

// A peer declining for load has said nothing about the music, so the copy it did
// not send is worth asking about again in minutes rather than tomorrow.
func TestACopyAPeerWasTooBusyToSendIsAskedForAgainShortly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	busy := sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, &fakeHunter{candidates: offering(busy)}, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: busy.Path, FileName: busy.Name,
		Verdict: db.AcquiredFileUndelivered, Summary: db.DeferredByPeerSummary,
	}); err != nil {
		t.Fatal(err)
	}
	// Long enough for the peer's queue to have drained, and nowhere near the day
	// a copy that actually failed to arrive would rest.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET decided_at = now() - $2::interval
		WHERE acquisition_target_id = $1
	`, target.ID, (deferredRest + time.Minute).String()); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want the peer asked again once it is free",
			len(fetcher.started))
	}
}

// The shorter rest belongs to backpressure alone. A transfer that went wrong may
// well be a copy that is wrong, and coming straight back to it is how one broken
// file gets fetched twenty-five times in a day.
func TestACopyThatFailedToArriveStillRestsForTheDay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	failed := sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, &fakeHunter{candidates: offering(failed)}, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: target.ID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: failed.Path, FileName: failed.Name,
		Verdict: db.AcquiredFileUndelivered, Summary: "the bytes did not arrive",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET decided_at = now() - $2::interval
		WHERE acquisition_target_id = $1
	`, target.ID, (deferredRest + time.Minute).String()); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 0 {
		t.Fatalf("started %d transfers, want a copy that failed left resting",
			len(fetcher.started))
	}
}

// A sibling whose copy is already imported and merely waiting for the scan to
// settle it has its answer. Its own hunt stands back in that window; the
// harvest must not reach around it and fetch a second copy of music that just
// arrived.
func TestAWantWhoseCopyIsImportedTakesNothingFromAFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
			Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_path, imported_at
		)
		VALUES ($1, 'slskd', 'other-peer', 'Music/Portishead', 1, 1, 30000000,
		        'flac', 0.8, '[]'::jsonb, 'completed', 'imported', '/music', now())
	`, mysterons.ID); err != nil {
		t.Fatal(err)
	}

	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	if got := offers(ctx, t, pool, mysterons.ID); len(got) != 0 {
		t.Fatalf("offers = %+v, want an answered want left alone", got)
	}
}

// The folder a search for one want turns up: the sibling's music is in it too,
// which is the whole reason the harvest reads it.
func dummyFolder() []sources.Candidate {
	return offering(
		sources.File{
			Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
			Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)
}

// The recording is already on the disc, under whatever release it arrived on.
// A folder standing open in front of the loop must not be where a second copy of
// it is taken — that is how one recording ends up in the library twice, under
// two albums, a day apart.
func TestAWantTheLibraryHoldsTakesNothingFromAFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{candidates: dummyFolder()}, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	seedIdentifiedFile(ctx, t, pool,
		"/music/Portishead/Dummy/01 Mysterons.flac", mysterons.MusicBrainzRecordingID.UUID)

	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	if got := offers(ctx, t, pool, mysterons.ID); len(got) != 0 {
		t.Fatalf("offers = %+v, want a recording the library already holds left alone", got)
	}
}

// A want the library answers is finished, not quietly passed over: a want that
// stops being looked for while still reading as wanted is a worse queue than
// either alternative. It says which file answered it.
func TestAHarvestFinishesAWantTheLibraryAlreadyHolds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{candidates: dummyFolder()}, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	fileID := seedIdentifiedFile(ctx, t, pool,
		"/music/Portishead/Dummy/01 Mysterons.flac", mysterons.MusicBrainzRecordingID.UUID)

	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	settled, err := service.Target(ctx, mysterons.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "acquired" || settled.Summary != acquiredSummary {
		t.Fatalf("want = %s / %q, want it acquired and reading as %q",
			settled.Status, settled.Summary, acquiredSummary)
	}
	if !settled.AcquiredLibraryFileID.Valid || settled.AcquiredLibraryFileID.UUID != fileID {
		t.Fatalf("acquired file = %v, want the file that answered it %s",
			settled.AcquiredLibraryFileID, fileID)
	}
}

// A file the scanner has marked gone is not music the library has. The want it
// used to answer is wanted again, and the folder in front of the loop is where
// it is answered.
func TestAWantHeldOnlyByAMissingFileStillTakesItsCopyFromAFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{candidates: dummyFolder()}, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	fileID := seedIdentifiedFile(ctx, t, pool,
		"/music/Portishead/Dummy/01 Mysterons.flac", mysterons.MusicBrainzRecordingID.UUID)
	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET missing_at = now() WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}

	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	taken := offers(ctx, t, pool, mysterons.ID)
	if len(taken) != 1 || taken[0].FileName != "01 Mysterons.flac" {
		t.Fatalf("sibling offers = %+v, want a want the library no longer has looked for", taken)
	}
}

// What a file says about itself in its own tags is not a decision anybody made,
// and it is not proof the library has the recording. A want is only answered by
// an identity that was decided or a mapping onto a track carrying it.
func TestAWantOnlyATagClaimsStillTakesItsCopyFromAFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{candidates: dummyFolder()}, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	mysterons := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, releaseGroupID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, musicbrainz_recording_id)
		VALUES ($1, '/music/tagged.flac', 1024, now(), $2)
	`, uuid.New(), mysterons.MusicBrainzRecordingID.UUID); err != nil {
		t.Fatal(err)
	}

	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	taken := offers(ctx, t, pool, mysterons.ID)
	if len(taken) != 1 || taken[0].FileName != "01 Mysterons.flac" {
		t.Fatalf("sibling offers = %+v, want a want no decision answers looked for", taken)
	}
}

// The filename is the one thing a peer discloses about a track rather than about
// the folder around it. Without it there is nothing to say which file is the
// sibling's, and taking one anyway is downloading a folder on a guess.
func TestAWantTakesNothingFromAFolderThatNamesNoFileAfterIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	})}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	releaseGroupID := uuid.New()
	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, releaseGroupID)
	roads := wantOnARelease(ctx, t, service, pool, "Roads", 302_000, releaseGroupID)
	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	if got := offers(ctx, t, pool, roads.ID); len(got) != 0 {
		t.Fatalf("offers = %+v, want nothing fetched for a want no file is named after", got)
	}
}

// A want on another release is not in that folder, whatever its files are called.
func TestAWantOnAnotherReleaseTakesNothingFromTheFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
			Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	glory := wantOnARelease(ctx, t, service, pool, "Glory Box", 301_000, uuid.New())
	elsewhere := wantOnARelease(ctx, t, service, pool, "Mysterons", 305_000, uuid.New())
	if err := service.hunt(ctx, glory, nil); err != nil {
		t.Fatalf("hunt() error = %v", err)
	}

	if got := offers(ctx, t, pool, elsewhere.ID); len(got) != 0 {
		t.Fatalf("offers = %+v, want a want on another release left alone", got)
	}
}

// A file of a type no scan indexes is passed over, whatever else it looks like.
// Fetching it spends a peer's upload slot on music that would sit in the library
// directory unseen — which is what happened to an AIFF that was proven, imported,
// and then waited all night for a library row nothing would ever create.
func TestASweepPassesOverACopyTheLibraryCouldNotHold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.ape`, Name: "05 Glory Box.ape",
			Extension: "ape", SizeBytes: 28_000_000, DurationSeconds: 301,
		},
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
			Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
		},
	)}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	recorded := offers(ctx, t, pool, target.ID)
	if len(recorded) != 1 {
		t.Fatalf("recorded %d offers, want the one copy fetched", len(recorded))
	}
	if recorded[0].FileName != "05 Glory Box.flac" {
		t.Fatalf("fetched %q, want the copy a scan would index", recorded[0].FileName)
	}
}

// Nothing on offer that the library could hold is nothing on offer. The want
// says so and waits rather than fetching what it could never keep.
func TestAWantFetchesNothingWhenEveryCopyIsOfATypeTheLibraryIgnores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(
		sources.File{
			Path: `Music\Portishead\Dummy\05 Glory Box.ape`, Name: "05 Glory Box.ape",
			Extension: "ape", SizeBytes: 28_000_000, DurationSeconds: 301,
		},
	)}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 0 {
		t.Fatalf("started %d transfers, want none", len(fetcher.started))
	}
	if recorded := offers(ctx, t, pool, target.ID); len(recorded) != 0 {
		t.Fatalf("recorded %+v, want nothing tried", recorded)
	}
}

// A copy that was imported and never became a library file has waited out
// everything a scan could take. The want stops saying it is being taken in,
// because that reads as progress and there is none.
func TestAWantStopsWaitingForALibraryRowThatIsNotComing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{}, &fakeFetcher{}, true)

	recordingID := uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)
	// The scan never reached it: no library row was ever linked to the transfer.
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET library_file_id = NULL WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}

	// Long enough that a scan would have run, twice over.
	service.now = func() time.Time { return time.Now().Add(importedPatience + time.Minute) }
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stranded, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stranded.Summary != strandedSummary {
		t.Fatalf("summary = %q, want it saying the copy never appeared", stranded.Summary)
	}
	if stranded.Status != "pending" {
		t.Fatalf("status = %q, want the want still open", stranded.Status)
	}
}

// Until then it waits, quietly and briefly. A scan takes minutes, and going
// looking in the meantime would fetch a second copy of music already on the disc.
func TestAWantWaitsWhileTheScanIsStillCatchingUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	recordingID := uuid.New()
	target := createWantWithDuration(ctx, t, service, recordingID, 301_000)
	fileID := acceptedCopy(ctx, t, pool, target.ID, recordingID)
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET library_file_id = NULL WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != importedSummary {
		t.Fatalf("summary = %q, want it still being taken in", waiting.Summary)
	}
	if len(fetcher.started) != 0 {
		t.Fatalf("started %d transfers, want none while a copy is already in", len(fetcher.started))
	}
}

// A peer that discloses a path and no name is still disclosing a file the library
// could hold. Reading the name alone would pass over a copy the scan would index
// perfectly well.
func TestACopyThePeerGaveNoNameForIsJudgedByItsPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Extension: "flac",
		SizeBytes: 25_000_000, DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want the unnamed copy fetched", len(fetcher.started))
	}
	recorded := offers(ctx, t, pool, target.ID)
	if len(recorded) != 1 || recorded[0].RemotePath != `Music\Portishead\Dummy\05 Glory Box.flac` {
		t.Fatalf("offers = %+v, want the copy chosen by its path", recorded)
	}
}

// An attempt that finishes after somebody has decided something about the want is
// dropped rather than applied over their decision — and it must not take the rest
// of the sweep down with it.
func TestAnAttemptOnAWantSomebodyStoppedMeanwhileIsDropped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	target := createWant(ctx, t, service, uuid.New())
	if _, err := service.StopPursuing(ctx, target.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	// The row the pass is holding is the one it read before the decision.
	if err := service.hunt(ctx, target, nil); err != nil {
		t.Fatalf("hunt() error = %v, want the attempt quietly dropped", err)
	}

	stopped, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != "not_wanted" || stopped.Attempts != 0 {
		t.Fatalf("target = %s on attempt %d, want the decision to stop left standing",
			stopped.Status, stopped.Attempts)
	}
}

// The same at the other end of a pass: the library turns out to hold the
// recording, but somebody stopped the want while it was being looked at. Their
// decision is the later fact.
func TestSettlingAWantSomebodyStoppedMeanwhileIsDropped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	recordingID := uuid.New()
	seedIdentifiedFile(ctx, t, pool, "/music/glory-box.flac", recordingID)
	target := createWant(ctx, t, service, recordingID)
	if _, err := service.StopPursuing(ctx, target.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	if _, err := service.attempt(ctx, target, nil); err != nil {
		t.Fatalf("attempt() error = %v, want the settlement quietly dropped", err)
	}

	stopped, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != "not_wanted" {
		t.Fatalf("status = %s, want the decision to stop left standing", stopped.Status)
	}
}

// An entry nothing has resolved to a recording has nothing to look for. Guessing
// what it wants from the rest of the row is exactly what must not happen.
func TestAnEntryWithNoRecordingIsNotAttempted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Portishead", Title: "Glory Box",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	looked, err := service.attempt(ctx, target, nil)
	if err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	if looked {
		t.Fatal("an entry with no recording went looking for a copy of it")
	}
	if len(hunter.texts) != 0 {
		t.Fatalf("searched %v, want nothing asked about an entry nobody has resolved",
			hunter.texts)
	}
}

// A want asked for by an identifier alone has no words to search a peer with.
// Soulseek has never heard of an ISRC, and asking it for an empty phrase is a
// question with no subject, so the want says so and waits.
func TestAWantWithNoWordsToSearchWithSaysSo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)

	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", ISRC: "GBAAA9400123",
		RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(hunter.texts) != 0 || len(fetcher.started) != 0 {
		t.Fatalf("searched %v and started %v, want nothing asked", hunter.texts, fetcher.started)
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != unsearchableSummary {
		t.Fatalf("summary = %q, want %q", waiting.Summary, unsearchableSummary)
	}
	if waiting.Status != "pending" || waiting.Attempts != 1 {
		t.Fatalf("target = %s on attempt %d, want it still wanted having spent one",
			waiting.Status, waiting.Attempts)
	}
}

// A sweep is the scheduler for everything wanted, so a pass that could not even
// read the queue has to say so rather than report a quiet round in which nothing
// was due.
func TestASweepThatCannotReadTheQueueReportsIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	createWant(ctx, t, service, uuid.New())

	stopped, cancelSweep := context.WithCancel(ctx)
	cancelSweep()

	if err := service.Sweep(stopped); err == nil {
		t.Fatal("a sweep that could not read the queue reported success")
	}
}

// A want nobody can account for is refused. Provenance is not decoration: a want
// that cannot say where it came from is one nobody can answer for later.
func TestAWantFromNowhereIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, _, err := newTestService(pool).Create(ctx, Entry{
		Origin: "somebody's script", Artist: "Portishead", Title: "Glory Box",
	})
	if !errors.Is(err, ErrUnknownOrigin) {
		t.Fatalf("Create() error = %v, want %v", err, ErrUnknownOrigin)
	}
}

// The Recommended view suggests music the library does not hold. A reader who
// presses "Want it" there creates an ordinary want, and it has to be able to
// say that a suggestion is where it came from. Nothing in the loop reads the
// origin; a want nobody can account for later is what it exists to prevent.
func TestAWantFromASuggestionSaysItCameFromOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	target, created, err := newTestService(pool).Create(ctx, Entry{
		Origin: "recommendation", Artist: "Portishead", Title: "Glory Box",
		RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !created {
		t.Fatal("the first want for a suggested recording was not a new one")
	}
	if target.Origin != "recommendation" {
		t.Fatalf("origin = %s, want recommendation", target.Origin)
	}
}

// The Recommended view holds back a recording that is already wanted, so
// wanting a suggestion has to leave the want in a state that rule holds back.
// A want handed a recording is resolved by whoever handed it over, which is why
// it starts pending rather than unresolved.
func TestAWantFromASuggestionIsOneTheSuggestionListHoldsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	target, _, err := newTestService(pool).Create(ctx, Entry{
		Origin: "recommendation", Artist: "Portishead", Title: "Glory Box",
		RecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// The statuses the suggestion list reads as "already requested", in
	// queries/recommendations.sql.
	held := map[string]bool{
		"unresolved": true, "pending": true, "searching": true, "awaiting_review": true,
	}
	if !held[target.Status] {
		t.Fatalf("status = %s, want one the suggestion list holds back", target.Status)
	}
}

// A search that was made and never went out is not "nobody is sharing this".
// slskd hands back the same empty list either way, so the state it finished in
// is the only thing that tells them apart, and reading it wrong spent an attempt
// and climbed a want's ladder 195 times in seventy-two hours. Nothing is
// recorded, and the want stands back half an hour rather than coming back in
// five minutes to have the same search queued behind it again.
func TestASearchThatNeverWentOutSpendsNoAttemptAndStandsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool,
		&fakeHunter{err: sources.ErrNobodyWasAsked}, &fakeFetcher{}, true)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	before := service.now()
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Attempts != target.Attempts {
		t.Errorf("attempts = %d, want the want to have spent none of them (%d)",
			waiting.Attempts, target.Attempts)
	}
	var recorded int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_target_attempts
		WHERE acquisition_target_id = $1
	`, target.ID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("attempt rows = %d, want none written for a search nobody received", recorded)
	}
	if waiting.Summary != nobodyAskedSummary {
		t.Errorf("summary = %q, want %q", waiting.Summary, nobodyAskedSummary)
	}
	if !waiting.LastError.Valid {
		t.Error("no reason recorded, want the refusal readable on the want")
	}
	if !waiting.NextAttemptAt.Valid ||
		waiting.NextAttemptAt.Time.Before(before.Add(bannedRest)) {
		t.Errorf("next attempt = %v, want it held back at least the %v a ban lasts",
			waiting.NextAttemptAt.Time, bannedRest)
	}
}

// A search slskd's own queue held back is a fact about that one search, not
// about the account: the account can be logged in and searching everything else
// fine. Reading it as a refusal of the whole pass is what stood 592 of 848
// pending wants back on 2026-08-29 without ever asking for them, because one
// want elsewhere in the same pass had a search still queued when its window
// closed. Only that one want stands back; the rest of the pass is asked.
func TestASearchThatNeverWentOutStandsBackOnlyItsOwnWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	held := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	sibling := createWantWithDuration(ctx, t, service, uuid.New(), 244_000)

	var refused passRefusal
	hunter.err = sources.ErrNobodyWasAsked
	looked, err := service.attempt(ctx, held, &refused)
	if err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	if !looked {
		t.Error("the want whose search never went out did not go looking")
	}
	hunter.err = nil
	looked, err = service.attempt(ctx, sibling, &refused)
	if err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	if !looked {
		t.Fatal("the sibling stood back, want a search nobody received to leave the pass alone")
	}
	if len(hunter.texts) != 2 {
		t.Fatalf("searches = %d, want both wants asked", len(hunter.texts))
	}

	standing, err := service.Target(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if standing.Attempts != held.Attempts {
		t.Errorf("held want attempts = %d, want a refusal to spend none of them (%d)",
			standing.Attempts, held.Attempts)
	}
	if standing.Summary != nobodyAskedSummary {
		t.Errorf("held want summary = %q, want %q", standing.Summary, nobodyAskedSummary)
	}

	asked, err := service.Target(ctx, sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if asked.Attempts != sibling.Attempts+1 {
		t.Errorf("sibling attempts = %d, want the want that was asked to have spent one (%d)",
			asked.Attempts, sibling.Attempts+1)
	}
	if asked.Summary != nothingOfferedSummary {
		t.Errorf("sibling summary = %q, want the answer it was actually given (%q)",
			asked.Summary, nothingOfferedSummary)
	}
}

// Being signed out of Soulseek is the opposite case: it is a fact about the
// account, so every search running behind the one that noticed fails the same
// way, and standing the whole pass back is correct rather than the bug above.
func TestBeingLoggedOutStandsTheRestOfThePassBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{err: sources.ErrNotLoggedIn}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	first := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	sibling := createWantWithDuration(ctx, t, service, uuid.New(), 244_000)

	var refused passRefusal
	if _, err := service.attempt(ctx, first, &refused); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	looked, err := service.attempt(ctx, sibling, &refused)
	if err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	if looked {
		t.Error("the sibling was asked, want a signed-out source to be heard once")
	}
	if len(hunter.texts) != 1 {
		t.Errorf("searches = %d, want only the want that heard the refusal to have asked",
			len(hunter.texts))
	}
	standing, err := service.Target(ctx, sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if standing.Attempts != sibling.Attempts {
		t.Errorf("sibling attempts = %d, want a refusal passed to it to spend none of them (%d)",
			standing.Attempts, sibling.Attempts)
	}
	if standing.Summary != loggedOutSummary {
		t.Errorf("sibling summary = %q, want %q", standing.Summary, loggedOutSummary)
	}
}

// The #495 outage never reached confirmAsked: slskd refused every search at
// creation, before any of them ran, which is ErrSignedOut rather than
// ErrNotLoggedIn (see internal/slskd's confirmConnected). Acquisition still
// has to record it, or the dashboard stays silent through exactly the outage
// it exists to surface.
func TestASignedOutSourceRecordsWhenTheOutageStarted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	if _, err := queries.SaveSlskdSettings(ctx, db.SaveSlskdSettingsParams{
		BaseURL: "http://slskd:5030", APIKey: "key", Enabled: true, SearchTimeoutSeconds: 20,
	}); err != nil {
		t.Fatal(err)
	}

	hunter := &fakeHunter{err: sources.ErrSignedOut}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	want := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	before := time.Now()
	var refused passRefusal
	if _, err := service.attempt(ctx, want, &refused); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}

	standing, err := service.Target(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	// ErrSignedOut is the same account-wide fact as ErrNotLoggedIn, learned
	// earlier by a cheaper check, and refusalFor gives it the same case and the
	// same half-hour wait rather than falling through to the generic
	// ErrProviderUnavailable case's five minutes.
	if standing.Summary != loggedOutSummary {
		t.Errorf("summary = %q, want %q", standing.Summary, loggedOutSummary)
	}
	if !standing.NextAttemptAt.Valid ||
		standing.NextAttemptAt.Time.Before(before.Add(bannedRest)) {
		t.Errorf("next attempt = %v, want it held back the %v an account-wide outage gets, "+
			"not the five minutes ErrProviderUnavailable gets",
			standing.NextAttemptAt.Time, bannedRest)
	}

	settings, err := queries.SlskdSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.LoggedOutSince.Valid {
		t.Fatal("logged_out_since was not recorded")
	}
	if settings.LoggedOutSince.Time.Before(before) {
		t.Errorf("logged_out_since = %v, want it no earlier than the attempt (%v)",
			settings.LoggedOutSince.Time, before)
	}
}

// A search that goes through only happens logged in, so it is what has to
// clear the clock a refusal started — otherwise the banner outlives the
// outage whenever the want queue empties before the next refusal would have.
//
// It also has to wake the sweeper: every other want the outage stood back is
// sitting on its own bannedRest clock, some of them close to thirty minutes
// out, and none of that is worth waiting for once the source can answer again.
func TestASuccessfulSearchClearsTheSignedOutClock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	if _, err := queries.SaveSlskdSettings(ctx, db.SaveSlskdSettingsParams{
		BaseURL: "http://slskd:5030", APIKey: "key", Enabled: true, SearchTimeoutSeconds: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.MarkSourceLoggedOut(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	want := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if _, err := pool.Exec(ctx, `
		DELETE FROM jobs WHERE kind = 'sweep_acquisition_targets'
	`); err != nil {
		t.Fatal(err)
	}

	var refused passRefusal
	if _, err := service.attempt(ctx, want, &refused); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}

	settings, err := queries.SlskdSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.LoggedOutSince.Valid {
		t.Errorf("logged_out_since = %v, want a search that went through to clear it",
			settings.LoggedOutSince.Time)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued sweeps = %d, want the source coming back online to wake the sweeper", queued)
	}
}

// One phrase the source will not repeat is a fact about that want's own words,
// not about the source, so only that want stands back. Reading it as a refusal
// of the whole pass took ten wants of one release five passes and twenty-five
// minutes to be asked at all, 167 times in seventy-two hours. Nothing is asked
// any sooner for this: the source still refuses the phrase, and only the wants
// that were never going to say it carry on.
func TestAPhraseTheSourceWillNotRepeatStandsBackOnlyItsOwnWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	held := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	sibling := createWantWithDuration(ctx, t, service, uuid.New(), 244_000)

	// Both wants are taken through one pass in order, which is what the sweep
	// does with the workers it has. The first hears the refusal; what the second
	// then does is the whole question.
	var refused passRefusal
	hunter.err = sources.ErrPhraseHeldBack
	looked, err := service.attempt(ctx, held, &refused)
	if err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	if !looked {
		t.Error("the want whose phrase was refused did not go looking")
	}
	hunter.err = nil
	looked, err = service.attempt(ctx, sibling, &refused)
	if err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	if !looked {
		t.Fatal("the sibling stood back, want a refusal about one phrase to leave the pass alone")
	}
	if len(hunter.texts) != 2 {
		t.Fatalf("searches = %d, want both wants asked", len(hunter.texts))
	}

	standing, err := service.Target(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if standing.Attempts != held.Attempts {
		t.Errorf("held want attempts = %d, want a refusal to spend none of them (%d)",
			standing.Attempts, held.Attempts)
	}
	if standing.Summary != phraseHeldSummary {
		t.Errorf("held want summary = %q, want %q", standing.Summary, phraseHeldSummary)
	}

	asked, err := service.Target(ctx, sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if asked.Attempts != sibling.Attempts+1 {
		t.Errorf("sibling attempts = %d, want the want that was asked to have spent one (%d)",
			asked.Attempts, sibling.Attempts+1)
	}
	if asked.Summary != nothingOfferedSummary {
		t.Errorf("sibling summary = %q, want the answer it was actually given (%q)",
			asked.Summary, nothingOfferedSummary)
	}
}

// The other refusals still stand the whole pass down, which is what makes the
// one above a narrowing rather than a hole: a source that will not search at all
// has said so about every want there is.
func TestASourceThatWillNotSearchStillStandsTheRestOfThePassBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{err: sources.ErrProviderUnavailable}
	service := hunting(pool, hunter, &fakeFetcher{}, true)

	first := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	sibling := createWantWithDuration(ctx, t, service, uuid.New(), 244_000)

	var refused passRefusal
	if _, err := service.attempt(ctx, first, &refused); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	looked, err := service.attempt(ctx, sibling, &refused)
	if err != nil {
		t.Fatalf("attempt() error = %v", err)
	}
	if looked {
		t.Error("the sibling was asked, want a source that cannot search to be heard once")
	}
	if len(hunter.texts) != 1 {
		t.Errorf("searches = %d, want only the want that heard the refusal to have asked",
			len(hunter.texts))
	}
	standing, err := service.Target(ctx, sibling.ID)
	if err != nil {
		t.Fatal(err)
	}
	if standing.Summary != unavailableSummary {
		t.Errorf("sibling summary = %q, want %q", standing.Summary, unavailableSummary)
	}
}

// A want offered nothing but copies under the user's bitrate floor is told so.
//
// The search refuses whole folders by their average bitrate, and it reports what
// it refused. Saying "no peer is sharing a copy yet" would send the reader to
// look at the network, when the floor is the one thing that can change the
// answer. The want keeps waiting on the ladder either way: it never lowers the
// bar by itself.
func TestAWantOfferedOnlyCopiesBelowTheFloorSaysSo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{
		preferences: sources.Preferences{
			FormatMinimumBitRate: map[string]int{"mp3": 320},
		},
		refused: []sources.Refused{{
			Username: "peer", Directory: `Music\Portishead\Dummy`, Format: "mp3",
			Reason: "MP3 at 128 kbps is below the 320 kbps you asked for",
			Kind:   sources.RefusedBitRate,
		}},
	}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != belowBitRateFloorSummary {
		t.Errorf("summary = %q, want %q", waiting.Summary, belowBitRateFloorSummary)
	}
	if waiting.Status != "pending" || !waiting.NextAttemptAt.Valid {
		t.Errorf("status = %q, want the want still waiting on the ladder", waiting.Status)
	}
}

// A want offered only files whose disclosed length contradicts the catalogue's
// is told nothing is on offer, whatever their bitrate. On 2026-09-11 a want for
// a 160-second track was offered twenty 243-second files named for it, some of
// them under the floor, and once the length ruled them all out the summary
// blamed the floor: the only refusals left standing were the bitrate ones. The
// floor is held against files this pass would otherwise fetch, and these were
// never that.
func TestAWantOfferedOnlyContradictedCopiesIsNotToldAboutTheFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{
		preferences: sources.Preferences{
			FormatMinimumBitRate: map[string]int{"mp3": 320},
		},
		candidates: offering(
			sources.File{
				Path: `Music\Portishead\Dummy\01 Mysterons.mp3`, Name: "01 Mysterons.mp3",
				Extension: "mp3", SizeBytes: 5_000_000, DurationSeconds: 243, BitRate: 195,
			},
			sources.File{
				Path: `Music\Portishead\Dummy\01 Mysterons.flac`, Name: "01 Mysterons.flac",
				Extension: "flac", SizeBytes: 28_000_000, DurationSeconds: 243,
			},
		),
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 160_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != nothingOfferedSummary {
		t.Errorf("summary = %q, want %q", waiting.Summary, nothingOfferedSummary)
	}
	if len(fetcher.started) != 0 {
		t.Errorf("fetched %d copies, want none: every file's disclosed length contradicts the want", len(fetcher.started))
	}
}

// Three consecutive below-floor answers park a want on the weekly rung, and a
// person's waiver lets the normal transfer path take the best available copy.
func TestABelowFloorWantParksAfterThreeAnswersAndWaiverTakesCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{
		preferences: sources.Preferences{
			FormatMinimumBitRate: map[string]int{"mp3": 320},
		},
		refused: []sources.Refused{{
			Username: "peer", Directory: "Music\\Portishead\\Dummy", Format: "mp3",
			Reason: "MP3 at 128 kbps is below the 320 kbps you asked for",
			Kind:   sources.RefusedBitRate,
		}},
		waivedCandidates: offering(sources.File{
			Path: "Music\\Portishead\\Dummy\\01 Mysterons.mp3", Name: "01 Mysterons.mp3",
			Extension: "mp3", SizeBytes: 5_000_000, DurationSeconds: 301, BitRate: 128,
		}),
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	for pass := int16(1); pass <= 3; pass++ {
		if _, err := pool.Exec(ctx, "UPDATE acquisition_targets SET next_attempt_at = now() WHERE id = $1", target.ID); err != nil {
			t.Fatalf("wake below-floor pass %d: %v", pass, err)
		}
		if err := service.Sweep(ctx); err != nil {
			t.Fatalf("Sweep() pass %d: %v", pass, err)
		}
		waiting, err := service.Target(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waiting.BelowFloorStreak != pass {
			t.Errorf("pass %d streak = %d, want %d", pass, waiting.BelowFloorStreak, pass)
		}
	}

	parked, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !parked.BelowFloorSince.Valid {
		t.Fatal("below_floor_since is not set after the third below-floor answer")
	}
	if !strings.Contains(parked.Summary, "320 kbps") ||
		!strings.Contains(parked.Summary, "Looked at again weekly") {
		t.Errorf("summary = %q, want the floor and weekly re-check", parked.Summary)
	}
	if parked.NextAttemptAt.Time.Before(time.Now().Add(6 * 24 * time.Hour)) {
		t.Errorf("next attempt = %v, want about a week out", parked.NextAttemptAt.Time)
	}

	if _, err := service.TakeBestAvailable(ctx, target.ID); err != nil {
		t.Fatalf("TakeBestAvailable() error = %v", err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("waived Sweep() error = %v", err)
	}
	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers after waiver, want one sub-floor copy", len(fetcher.started))
	}
	waived, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !waived.FloorWaivedAt.Valid || waived.BelowFloorSince.Valid || waived.BelowFloorStreak != 0 {
		t.Errorf("waived target = %+v, want waiver set and below-floor state cleared", waived)
	}

}

// Waiving one want's bitrate floor does not lower the floor for a sibling
// harvested from the same folder.
func TestAFloorWaiverDoesNotLowerASiblingFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{
		preferences: sources.Preferences{
			FormatMinimumBitRate: map[string]int{"mp3": 320},
		},
		waivedCandidates: offering(
			sources.File{
				Path: `Music\Portishead\Dummy\01 Glory Box.mp3`, Name: "01 Glory Box.mp3",
				Extension: "mp3", SizeBytes: 5_000_000, DurationSeconds: 301, BitRate: 128,
			},
			sources.File{
				Path: `Music\Portishead\Dummy\02 Glory Box.mp3`, Name: "02 Glory Box.mp3",
				Extension: "mp3", SizeBytes: 5_000_000, DurationSeconds: 301, BitRate: 128,
			},
		),
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	leading := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	sibling := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	releaseGroupID := uuid.New()
	for _, target := range []db.AcquisitionTargetRow{leading, sibling} {
		if _, err := pool.Exec(ctx, `
			UPDATE acquisition_targets
			SET musicbrainz_release_group_id = $2
			WHERE id = $1
		`, target.ID, releaseGroupID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET floor_waived_at = now() WHERE id = $1
	`, leading.ID); err != nil {
		t.Fatal(err)
	}
	leading, err := service.Target(ctx, leading.ID)
	if err != nil {
		t.Fatal(err)
	}

	if looked, err := service.attempt(ctx, leading, nil); err != nil {
		t.Fatalf("attempt() error = %v", err)
	} else if !looked {
		t.Fatal("waived want did not search")
	}
	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want only the waived want's copy", len(fetcher.started))
	}
	if got := offers(ctx, t, pool, sibling.ID); len(got) != 0 {
		t.Fatalf("sibling offers = %+v, want its below-floor copy left untouched", got)
	}
}

// A parked want treats candidates that disclose nothing usable like an empty
// search, even though the provider returned a candidate folder.
func TestAParkedWantTreatsUndisclosedCandidatesAsNothingOffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Portishead\Dummy\unrelated.mp3`, Name: "unrelated.mp3",
		Extension: "mp3", SizeBytes: 5_000_000, DurationSeconds: 100, BitRate: 320,
	})}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET below_floor_streak = 3, below_floor_since = now(), next_attempt_at = now()
		WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}
	parked, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.attempt(ctx, parked, nil); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.BelowFloorStreak != 3 || !waiting.BelowFloorSince.Valid {
		t.Errorf("parked state = streak %d, since %v; want it preserved",
			waiting.BelowFloorStreak, waiting.BelowFloorSince)
	}
	if waiting.NextAttemptAt.Time.Before(time.Now().Add(6 * 24 * time.Hour)) {
		t.Errorf("next attempt = %v, want the weekly rung", waiting.NextAttemptAt.Time)
	}
}

// The floor is answered only when it is the whole reason. A want also offered
// a copy in a format the user refuses outright is told about the format list
// instead, because lowering the bitrate floor alone would not be enough to
// leave it with a copy.
func TestAWantRefusedByBothTheFloorAndAFormatIsToldAboutTheFormat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{
		preferences: sources.Preferences{
			Unacceptable:         []string{"flac"},
			FormatMinimumBitRate: map[string]int{"mp3": 320},
		},
		refused: []sources.Refused{
			{
				Username: "peer", Directory: `Music\Portishead\Dummy`, Format: "mp3",
				Reason: "MP3 at 128 kbps is below the 320 kbps you asked for",
				Kind:   sources.RefusedBitRate,
			},
			{
				Username: "other", Directory: `Music\Portishead\Dummy (FLAC)`, Format: "flac",
				Reason: "FLAC is a format you do not accept",
				Kind:   sources.RefusedFormat,
			},
		},
	}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != refusedByPreferenceSummary {
		t.Errorf("summary = %q, want %q", waiting.Summary, refusedByPreferenceSummary)
	}
}

// And the sentence it is left with has to be one somebody can act on. The want
// is stuck for as long as the installation has no key, so a row that only says
// "waiting" is a dead end: it names the key and the place the key is entered.
func TestAWantStuckWithoutAKeySaysWhichKeyAndWhere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := hunting(pool, &fakeHunter{}, &fakeFetcher{}, false)

	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"AcoustID", "Settings"} {
		if !strings.Contains(waiting.Summary, word) {
			t.Errorf("summary = %q, want it to name %s", waiting.Summary, word)
		}
	}
}

// One file under the floor inside a folder that passed it is passed over too.
// A folder's bitrate is an average, and an average is not the file the want
// asks for.
func TestAWantPassesOverTheFileUnderTheFloorInsideAPassingFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{
		preferences: sources.Preferences{
			FormatMinimumBitRate: map[string]int{"mp3": 320},
		},
		candidates: offering(
			sources.File{
				Path: `Music\Portishead\Dummy\05 Glory Box.mp3`, Name: "05 Glory Box.mp3",
				Extension: "mp3", SizeBytes: 5_000_000, DurationSeconds: 301, BitRate: 128,
			},
			sources.File{
				Path:      `Music\Portishead\Dummy\05 Glory Box (alt).mp3`,
				Name:      "05 Glory Box (alt).mp3",
				Extension: "mp3", SizeBytes: 12_000_000, DurationSeconds: 301, BitRate: 320,
			},
		),
	}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	recorded := offers(ctx, t, pool, target.ID)
	if len(recorded) != 1 {
		t.Fatalf("offers = %+v, want one copy chosen", recorded)
	}
	if recorded[0].FileName != "05 Glory Box (alt).mp3" {
		t.Errorf("chose %q, want the file that meets the floor", recorded[0].FileName)
	}
}

// A want offered nothing at all still hears that, and not a sentence about a
// floor that turned nothing away.
func TestAWantOfferedNothingIsNotToldAboutTheFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{preferences: sources.Preferences{
		FormatMinimumBitRate: map[string]int{"mp3": 320},
	}}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	target := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != nothingOfferedSummary {
		t.Errorf("summary = %q, want %q", waiting.Summary, nothingOfferedSummary)
	}
}

// The refusal this is about is the one nobody said out loud: Soulseek stopped
// distributing this account's searches while leaving the connection up, so the
// provider stopped asking. Read as an answer it would settle the want with an
// absence and climb the ladder, which is what 72 searches did in the half hour
// of 2026-09-04 before anything noticed.
func TestASilencedSourceDefersAWantWithoutSpendingAnAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	hunter := &fakeHunter{err: sources.ErrSearchSilenced}
	service := hunting(pool, hunter, &fakeFetcher{}, true)
	want := createWantWithDuration(ctx, t, service, uuid.New(), 301_000)

	var refused passRefusal
	if _, err := service.attempt(ctx, want, &refused); err != nil {
		t.Fatalf("attempt() error = %v", err)
	}

	standing, err := service.Target(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if standing.Attempts != want.Attempts {
		t.Errorf("attempts = %d, want a search nobody heard to spend none of them (%d)",
			standing.Attempts, want.Attempts)
	}
	if standing.Summary != silencedSummary {
		t.Errorf("summary = %q, want %q", standing.Summary, silencedSummary)
	}
	// The whole pass stands back with it: every other want would be asked over
	// the same connection Soulseek is ignoring.
	standingRefusal, heard := refused.refused()
	if !heard {
		t.Fatal("the pass heard nothing, want a source-wide refusal recorded on it")
	}
	if standingRefusal.delay != bannedRest {
		t.Errorf("pass stand-back = %v, want %v", standingRefusal.delay, bannedRest)
	}
}
