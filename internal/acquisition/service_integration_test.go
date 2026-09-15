package acquisition

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/events"
	"github.com/rs/zerolog"
)

// The want the library turns out to already hold is finished without anybody
// searching for it. A file bought outside Schall, or acquired for a different
// release, satisfies it the moment its identity is decided.
func TestASweepAcquiresAWantTheLibraryAlreadyHolds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	recordingID := uuid.New()
	fileID := seedIdentifiedFile(ctx, t, pool, "/music/glass.flac", recordingID)
	target := createWant(ctx, t, service, recordingID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if settled.Status != "acquired" {
		t.Fatalf("status = %s, want acquired", settled.Status)
	}
	if !settled.AcquiredLibraryFileID.Valid || settled.AcquiredLibraryFileID.UUID != fileID {
		t.Fatalf("acquired file = %v, want %s", settled.AcquiredLibraryFileID, fileID)
	}
}

// The requirement this entity exists for: an attempt that came to nothing
// leaves the target wanted, with a later attempt scheduled, rather than failed.
func TestASweepLeavesAWantNothingSatisfiesScheduledForLater(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	target := createWant(ctx, t, service, uuid.New())
	// Frozen after the want exists, so the sweep is looking back at a schedule
	// the database wrote rather than racing it.
	now := time.Now().Add(time.Minute)
	service.now = func() time.Time { return now }

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if waiting.Status != "pending" {
		t.Fatalf("status = %s, want pending", waiting.Status)
	}
	if waiting.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", waiting.Attempts)
	}
	if !waiting.NextAttemptAt.Valid || waiting.NextAttemptAt.Time.Before(now.Add(14*time.Minute)) {
		t.Fatalf("next attempt = %v, want about a quarter hour after %v", waiting.NextAttemptAt, now)
	}
}

// A sweeper with nothing to sweep stops. Otherwise an installation that wants
// nothing would keep a timer running forever to discover that every few
// seconds.
func TestASweepWithNothingWaitingSchedulesNoFurtherSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(pool)
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	nextDue, err := service.NextDue(ctx)
	if err != nil {
		t.Fatalf("NextDue() error = %v", err)
	}
	if !nextDue.IsZero() {
		t.Fatalf("next sweep = %v, want none", nextDue)
	}
}

// The next sweep is asked for rather than assumed, so it happens when a want is
// actually due instead of on a fixed interval that is either too eager or too
// late.
func TestASweepReportsWhenTheNextWantIsDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	createWant(ctx, t, service, uuid.New())
	now := time.Now().Add(time.Minute)
	service.now = func() time.Time { return now }

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	nextDue, err := service.NextDue(ctx)
	if err != nil {
		t.Fatalf("NextDue() error = %v", err)
	}
	if nextDue.Before(now.Add(14 * time.Minute)) {
		t.Fatalf("next sweep = %v, want about a quarter hour after %v", nextDue, now)
	}
}

// A sweep is bounded, and what it did not reach is due now rather than in an
// hour: a full batch asks to carry straight on instead of quietly capping how
// much can be wanted at once.
func TestAFullSweepLeavesWhatItDidNotReachDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	for range sweepBatch + 1 {
		createWant(ctx, t, service, uuid.New())
	}
	now := time.Now().Add(time.Minute)
	service.now = func() time.Time { return now }

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	nextDue, err := service.NextDue(ctx)
	if err != nil {
		t.Fatalf("NextDue() error = %v", err)
	}
	if nextDue.After(now) {
		t.Fatalf("next sweep = %v, want one due by %v", nextDue, now)
	}
}

// An entry nobody has resolved yet is not something to search for. It waits to
// be resolved, and a sweep leaves it exactly where it is.
func TestAnUnresolvedEntryWaitsToBeResolved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist",
		Artist: "Portishead",
		Title:  "Glory Box",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if target.Status != "unresolved" {
		t.Fatalf("status = %s, want unresolved", target.Status)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	if after.Attempts != 0 {
		t.Fatalf("attempts = %d, want none until it has been resolved", after.Attempts)
	}
}

// A want with nothing to identify it is refused rather than stored as a target
// nothing could ever satisfy.
func TestAnEntryWithNothingToIdentifyItIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, _, err := newTestService(pool).Create(ctx, Entry{Origin: "manual", Artist: "Portishead"})
	if !errors.Is(err, ErrEntryNotIdentifiable) {
		t.Fatalf("Create() error = %v, want %v", err, ErrEntryNotIdentifiable)
	}
}

// Wanting a target again puts it back in the queue, or it would be wanted and
// never looked for.
func TestWantingATargetAgainPutsItBackInTheQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	target := createWant(ctx, t, service, uuid.New())
	if _, err := service.StopPursuing(ctx, target.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	wanted, err := service.PursueAgain(ctx, target.ID)
	if err != nil {
		t.Fatalf("PursueAgain() error = %v", err)
	}
	if wanted.Status != "pending" {
		t.Fatalf("status = %s, want pending", wanted.Status)
	}
	if !wanted.NextAttemptAt.Valid {
		t.Fatal("a target wanted again is not scheduled for an attempt")
	}
}

// A target stopped before anybody resolved it comes back waiting to be
// resolved, and says so. Reporting that it is waiting for a copy would describe
// a search that cannot start, of a recording nothing has named yet.
func TestAnUnresolvedTargetWantedAgainSaysItIsWaitingToBeResolved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist",
		Artist: "Portishead",
		Title:  "Glory Box",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.StopPursuing(ctx, target.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	wanted, err := service.PursueAgain(ctx, target.ID)
	if err != nil {
		t.Fatalf("PursueAgain() error = %v", err)
	}
	if wanted.Status != "unresolved" {
		t.Fatalf("status = %s, want unresolved", wanted.Status)
	}
	if wanted.Summary != UnresolvedSummary {
		t.Fatalf("summary = %q, want %q", wanted.Summary, UnresolvedSummary)
	}
}

// And something goes looking for the answer. A want somebody has just asked for
// again is due now, so the sweeper is asked for now — without this it sat at
// "due" until an unrelated target's turn came round, which is a want nobody is
// working on wearing the word wanted.
func TestAnUnresolvedTargetWantedAgainWakesTheSweeper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Portishead", Title: "Glory Box",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.StopPursuing(ctx, target.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM jobs WHERE kind = 'sweep_acquisition_targets'
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := service.PursueAgain(ctx, target.ID); err != nil {
		t.Fatalf("PursueAgain() error = %v", err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued sweeps = %d, want the one this asked for", queued)
	}
}

func newTestService(pool *pgxpool.Pool) *Service {
	return NewService(db.New(pool), zerolog.Nop())
}

func createWant(
	ctx context.Context, t *testing.T, service *Service, recordingID uuid.UUID,
) db.AcquisitionTargetRow {
	t.Helper()

	target, _, err := service.Create(ctx, Entry{
		Origin:      "manual",
		Artist:      "Portishead",
		Title:       "Glory Box",
		RecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return target
}

// The views that show the wants change on a schedule nobody is watching — hours
// after the person who asked closed the page — so a new want has to reach them
// without anybody polling for it.
func TestAViewWatchingTheWantsHearsAboutANewOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hub := events.NewHub()
	service := newTestService(pool).WithEvents(hub)
	notices, stop := hub.Subscribe()
	defer stop()

	createWant(ctx, t, service, uuid.New())

	select {
	case topic := <-notices:
		if topic != events.TopicAcquisitions {
			t.Fatalf("notice = %q, want %q", topic, events.TopicAcquisitions)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notice arrived, want the views told a want was asked for")
	}
}

// The wants list is what somebody opens to see what is being pursued, so it
// answers with the wants and how many there are past the end of the page.
func TestTheWantsAreListedWithHowManyThereAre(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	for range 2 {
		createWant(ctx, t, service, uuid.New())
	}

	page, err := service.List(ctx, db.ListAcquisitionTargetsParams{Limit: 1})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if page.Total != 2 || len(page.Items) != 1 {
		t.Fatalf("page = %d of %d, want one of the two wants", len(page.Items), page.Total)
	}
}

// A decision is made once. Stopping a want that is already stopped, acquired or
// merged has nothing left to stop, and saying so is what keeps the interface from
// reporting a decision nobody made.
func TestStoppingAWantThatIsAlreadyStoppedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())
	if _, err := service.StopPursuing(ctx, target.ID); err != nil {
		t.Fatalf("StopPursuing() error = %v", err)
	}

	_, err := service.StopPursuing(ctx, target.ID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("StopPursuing() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// Withdrawing a decision nobody made is refused as well. A want that is already
// being pursued has nothing to take back, and quietly rescheduling it would be a
// press that reads as if it did something.
func TestWantingAgainAWantNobodyStoppedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())

	_, err := service.PursueAgain(ctx, target.ID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("PursueAgain() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// Rejecting a resolution nobody made is refused rather than silently re-opening
// an entry that was never resolved: there is nothing to remember as wrong.
func TestRejectingAResolutionNobodyMadeIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)

	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Portishead", Title: "Glory Box",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := service.RejectResolution(ctx, target.ID); !errors.Is(err, db.ErrNothingToReject) {
		t.Fatalf("RejectResolution() error = %v, want ErrNothingToReject", err)
	}
}

// The sweeper wakes when the next want is due, so a schedule that could not be
// read has to be an error rather than the zero time — which means "nothing is
// scheduled" and would stop the sweeper for good.
func TestAScheduleThatCannotBeReadIsNotReadAsNothingScheduled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	createWant(ctx, t, service, uuid.New())

	stopped, cancelRead := context.WithCancel(ctx)
	cancelRead()

	if _, err := service.NextDue(stopped); err == nil {
		t.Fatal("a schedule that could not be read reported no schedule")
	}
}

// heldCopy is the world once a copy has arrived for a want and nothing could
// decide about it: the copy is on record as a question, and the request that
// fetched it is waiting for somebody.
func heldCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) db.AcquisitionTargetFileRow {
	t.Helper()

	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_error
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Portishead/Dummy', 1, 1, 25000000, 'flac', 0.8,
		        '[]'::jsonb, 'completed', 'needs_review', 'Nothing could identify the audio.')
	`, requestID, targetID); err != nil {
		t.Fatalf("seed the request: %v", err)
	}
	if err := db.New(pool).RecordAcquiredFile(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Portishead\Dummy\05 Glory Box.flac`, FileName: "05 Glory Box.flac",
		SizeBytes: 25000000, Verdict: db.AcquiredFileHeld,
		Summary:           "Nothing could identify the audio.",
		Evidence:          &db.AcquiredFileEvidence{Name: "05 Glory Box.flac"},
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatalf("hold the copy: %v", err)
	}
	held, err := db.New(pool).AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 {
		t.Fatalf("copies = %d, want the one just held", len(held))
	}
	return held[0]
}

// How often a copy is proven, refused, or turns out to be a question is the one
// thing nobody can guess at from the outside, so what has been tried for a want
// is readable.
func TestWhatHasBeenTriedForAWantIsReadBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())
	heldCopy(ctx, t, pool, target.ID)

	tried, err := service.Copies(ctx, target.ID)
	if err != nil {
		t.Fatalf("Copies() error = %v", err)
	}
	if len(tried) != 1 || tried[0].Verdict != db.AcquiredFileHeld {
		t.Fatalf("copies = %+v, want the one held for a decision", tried)
	}
}

// The review queue plays a copy before somebody decides about it, so one offer is
// readable on its own.
func TestOneOfferIsReadBackByName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())
	held := heldCopy(ctx, t, pool, target.ID)

	copied, err := service.Copy(ctx, held.ID)
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if copied.ID != held.ID || copied.FileName != "05 Glory Box.flac" {
		t.Fatalf("copy = %+v, want the held offer %s", copied, held.ID)
	}
}

// A want that has stopped is a want that will not move again until somebody says
// something, so it has to be reachable: stored and answerable is not the same as
// visible.
func TestTheReviewQueueOffersAWantHoldingACopyNobodyCouldDecide(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())
	heldCopy(ctx, t, pool, target.ID)

	page, err := service.ReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatalf("ReviewQueue() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Target.ID != target.ID {
		t.Fatalf("queue = %+v, want the want waiting on a person", page.Items)
	}
	if len(page.Items[0].Files) != 1 {
		t.Fatalf("copies = %+v, want the held one offered with it", page.Items[0].Files)
	}
}

// Copying a file into the library is the import job's work — it reads tags,
// writes to disc and queues a scan — so accepting a copy records the decision and
// re-opens the import rather than doing it behind an HTTP timeout.
func TestAcceptingAHeldCopyPutsItsImportBackInTheQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())
	held := heldCopy(ctx, t, pool, target.ID)

	if err := service.AcceptCopy(ctx, held.ID); err != nil {
		t.Fatalf("AcceptCopy() error = %v", err)
	}

	tried, err := service.Copies(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tried[0].Verdict != db.AcquiredFileAccepted || tried[0].DecidedBy != "user" {
		t.Fatalf("copy = %+v, want the person's acceptance recorded", tried[0])
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'import_download' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued imports = %d, want the copy actually brought in", queued)
	}
}

// A copy something already answered is never re-asked, whoever asks.
func TestAcceptingACopyThatWasAlreadyAnsweredIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())
	held := heldCopy(ctx, t, pool, target.ID)
	if err := service.AcceptCopy(ctx, held.ID); err != nil {
		t.Fatal(err)
	}

	err := service.AcceptCopy(ctx, held.ID)
	if !errors.Is(err, db.ErrNotWaitingForADecision) {
		t.Fatalf("AcceptCopy() error = %v, want ErrNotWaitingForADecision", err)
	}
}

// Somebody listening and saying no is a judgement about the audio, so it is
// recorded as one — and the want carries on looking for another copy.
func TestRefusingTheHeldCopiesSendsTheWantLookingAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())
	heldCopy(ctx, t, pool, target.ID)

	if err := service.RefuseCopies(ctx, target.ID); err != nil {
		t.Fatalf("RefuseCopies() error = %v", err)
	}

	tried, err := service.Copies(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tried[0].Verdict != db.AcquiredFileDiscardedAudio || tried[0].DecidedBy != "user" {
		t.Fatalf("copy = %+v, want the refusal recorded as a judgement about the audio", tried[0])
	}
	waiting, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "pending" || !waiting.NextAttemptAt.Valid {
		t.Fatalf("target = %#v, want it still wanted and scheduled", waiting)
	}
}

// There has to be something held to refuse. A refusal is permanent, so it must
// not be recordable against a want nobody is holding a copy for.
func TestRefusingCopiesWhenNothingIsHeldIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target := createWant(ctx, t, service, uuid.New())

	err := service.RefuseCopies(ctx, target.ID)
	if !errors.Is(err, db.ErrNotWaitingForADecision) {
		t.Fatalf("RefuseCopies() error = %v, want ErrNotWaitingForADecision", err)
	}
}

// askedEntry stops one entry with the recordings it could name, which is the
// state askTheUser leaves behind.
func askedEntry(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, service *Service,
) (db.AcquisitionTargetRow, uuid.UUID) {
	t.Helper()

	target, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Portishead", Title: "Glory Box",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wanted := uuid.New()
	if _, err := db.New(pool).SendAcquisitionTargetToReview(
		ctx, db.ReviewAcquisitionTargetParams{
			ID:      target.ID,
			Summary: "2 recordings fit this entry equally well.",
			Detail:  "Two recordings fit.",
			Candidates: []db.AcquisitionTargetCandidateRow{
				{
					MusicBrainzRecordingID: wanted, ArtistName: "Portishead",
					TrackTitle: "Glory Box", Rank: 1,
					Agrees: []string{"artist", "title"}, Differs: []string{},
				},
				{
					MusicBrainzRecordingID: uuid.New(), ArtistName: "Portishead",
					TrackTitle: "Glory Box", Rank: 2,
					Agrees: []string{"artist", "title"}, Differs: []string{},
				},
			},
		}); err != nil {
		t.Fatalf("ask the question: %v", err)
	}
	return target, wanted
}

// The candidates were stored precisely because the evidence did not choose
// between them. A person can, and what they say is written as a manual
// resolution — permanent, and never revisited by the resolver.
func TestChoosingAnOfferedRecordingResolvesTheEntryByHand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target, wanted := askedEntry(ctx, t, pool, service)

	chosen, err := service.ChooseResolution(ctx, target.ID, wanted)
	if err != nil {
		t.Fatalf("ChooseResolution() error = %v", err)
	}
	if chosen.Status != "pending" || chosen.MusicBrainzRecordingID.UUID != wanted {
		t.Fatalf("target = %#v, want it wanted for the recording that was chosen", chosen)
	}
	if chosen.ResolutionMethod.String != "manual" {
		t.Fatalf("method = %q, want manual: nothing proved this", chosen.ResolutionMethod.String)
	}
}

// And something goes looking for it. A want that has just been given its
// recording is due now, so the sweeper is asked for now rather than left to
// notice on somebody else's turn.
func TestChoosingAnOfferedRecordingWakesTheSweeper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target, wanted := askedEntry(ctx, t, pool, service)
	if _, err := pool.Exec(ctx, `
		DELETE FROM jobs WHERE kind = 'sweep_acquisition_targets'
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := service.ChooseResolution(ctx, target.ID, wanted); err != nil {
		t.Fatalf("ChooseResolution() error = %v", err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued sweeps = %d, want the one this asked for", queued)
	}
}

// Only the recordings this want offered can answer it. Anything else is a want
// for music nobody weighed the evidence for.
func TestChoosingARecordingThatWasNotOfferedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target, _ := askedEntry(ctx, t, pool, service)

	_, err := service.ChooseResolution(ctx, target.ID, uuid.New())
	if !errors.Is(err, db.ErrNotOfferedForReview) {
		t.Fatalf("ChooseResolution() error = %v, want ErrNotOfferedForReview", err)
	}
}

// A want the entry merges into is already being pursued, so there is nothing for
// this one to look for and the sweeper is not asked.
func TestChoosingARecordingAnotherWantCarriesMergesTheEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool)
	target, wanted := askedEntry(ctx, t, pool, service)
	survivor := createWant(ctx, t, service, wanted)

	merged, err := service.ChooseResolution(ctx, target.ID, wanted)
	if err != nil {
		t.Fatalf("ChooseResolution() error = %v", err)
	}
	if merged.Status != "superseded" || merged.SupersededByID.UUID != survivor.ID {
		t.Fatalf("target = %#v, want it merged into the want that already carries it", merged)
	}
}

func seedIdentifiedFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, path string, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1024, now())
	`, fileID, path); err != nil {
		t.Fatalf("seed library file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'recording-id', 'proven by identifier')
	`, fileID, recordingID); err != nil {
		t.Fatalf("seed file identity: %v", err)
	}
	return fileID
}
