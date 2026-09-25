package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/acoustid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/listens"
	"github.com/pxldi/schall/internal/lyrics"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/navidrome"
	"github.com/pxldi/schall/internal/newreleases"
	"github.com/pxldi/schall/internal/playlists"
	"github.com/pxldi/schall/internal/recommendations"
	"github.com/pxldi/schall/internal/sources"
	"github.com/pxldi/schall/internal/uploads"
	"github.com/pxldi/schall/internal/weekly"
	"github.com/rs/zerolog"
)

type fakeQueue struct {
	artistGenresAsked []uuid.UUID
	albumGenresAsked  []uuid.UUID

	musicBrainzID uuid.UUID
	catalogue     musicbrainz.ArtistCatalogue
	release       musicbrainz.ReleaseCatalogue
	completed     uuid.UUID
	// wokenWhileRunning is what CompleteAcquisitionSweep reports: something
	// asked for a sweep while this one was still running.
	wokenWhileRunning   bool
	failed              uuid.UUID
	cancelled           uuid.UUID
	retried             uuid.UUID
	message             string
	runAfter            time.Time
	pollQueuedFor       time.Time
	challengesQueuedFor time.Time
	sweepQueuedFor      time.Time
	// coverSweepQueuedFor is when the picture sweep asked to run next.
	coverSweepQueuedFor time.Time
	// loudnessSweepQueuedFor is when the loudness pass asked to run next.
	loudnessSweepQueuedFor time.Time
	// feedSweepQueuedFor is when the follow feed asked to run next.
	playerSweepQueuedFor          time.Time
	feedSweepQueuedFor            time.Time
	recommendationSweepQueuedFor  time.Time
	lyricsSweepQueuedFor          time.Time
	anchorSweepQueuedFor          time.Time
	genreSweepQueuedFor           time.Time
	genreBackfill                 db.BackfillGenresRow
	genreBackfills                int
	backfillGenresErr             error
	copyFingerprintSweepQueuedFor time.Time
	spectrumSweepQueuedFor        time.Time
	upgradeSweepQueuedFor         time.Time
	upgradeSweepPayload           json.RawMessage
	lyricsSweepPayload            json.RawMessage
	weeklyRefreshQueuedFor        time.Time
	newReleasesRefreshQueuedFor   time.Time
	newReleasesRefreshQueuedCalls int
	recommendationSweepPayload    json.RawMessage
	ownSweepQueuedFor             time.Time
	ownSweepPayload               json.RawMessage
	ownSweepEnsuredFor            time.Time
	ownSweepEnsured               int
	// err is how the calls that read a catalogue identity answer. Everything
	// else has a field of its own, so a test can break exactly the call it is
	// about rather than every call the job makes.
	err error
	// saveErr breaks writing a catalogue back, completeErr breaks recording a
	// job as done, cancelErr breaks stopping one, scheduleErr breaks queueing a
	// successor, and resolutionsErr breaks queueing the files a scan found.
	saveErr              error
	completeErr          error
	cancelErr            error
	scheduleErr          error
	listensSyncQueuedFor time.Time
	resolutionsErr       error

	resolutionBatch   int32
	resolutionsQueued int64
	markedUpload      uuid.UUID
	markedPaths       []string

	// notifyPlayerQueued counts how many times a caller asked for the player
	// to be told the library changed, and notifyPlayerErr is how the queue
	// answers when it could not take the request.
	notifyPlayerQueued int
	notifyPlayerErr    error

	ingested   musicbrainz.ReleaseGroupDetail
	ingestedID uuid.UUID

	// runCancelled is what the queue says when the worker asks whether a bulk
	// source search was stopped; runCancelledErr is a lookup that cannot answer.
	runCancelled    bool
	runCancelledErr error

	// recovered counts the requeues of jobs a shutdown interrupted, which must
	// happen once however many lanes start; recoverErr is a requeue that failed.
	recovered  int
	recoverErr error
	// reaped counts the sweeps for jobs left running with nobody running them,
	// reapedFor records how long a row had to have been running to be swept up,
	// reapedRows is how many the queue says it put back, and reapErr is a sweep
	// that could not be made.
	reaped       int
	reapedFor    time.Duration
	reapedRows   int64
	reapErr      error
	reapedSignal chan struct{}
	// spent is what the queue answers when the pass over the failed list asks
	// which jobs stopped for good, spentBefore is the moment it asked about,
	// and revived and forgotten are what that pass then did with the answer.
	spent           []SpentJob
	spentBefore     time.Time
	spentLimit      int32
	spentErr        error
	revived         []uuid.UUID
	revivedRows     int64
	skippedRows     int64
	reviveErr       error
	forgottenBefore time.Time
	forgottenRows   int64
	forgetErr       error
	heartbeats      int
	heartbeat       chan struct{}
	owned           bool
	heartbeatErr    error
	// claims is what Claim answers with, in order, and lanes records what each
	// Claim asked for. Once the script is spent the queue cancels stop and says
	// the queue is empty, so a lane under test ends instead of looping forever.
	claims []claim
	lanes  []Lane
	stop   context.CancelFunc
}

// claim is one answer the queue gives the lane loop. stop ends the run before
// the answer is given, for the shutdown that arrives mid-loop.
type claim struct {
	job  Job
	err  error
	stop bool
}

type blockingReapQueue struct {
	*fakeQueue
	started chan struct{}
	calls   int
}

func (queue *blockingReapQueue) Claim(context.Context, Lane) (Job, error) {
	return Job{}, pgx.ErrNoRows
}

func (queue *blockingReapQueue) ReapStalled(ctx context.Context, _ time.Duration) (int64, error) {
	queue.calls++
	close(queue.started)
	<-ctx.Done()
	return 0, ctx.Err()
}

func (queue *fakeQueue) RecoverInterrupted(context.Context) error {
	queue.recovered++
	return queue.recoverErr
}

func (queue *fakeQueue) ReapStalled(_ context.Context, runningFor time.Duration) (int64, error) {
	queue.reaped++
	queue.reapedFor = runningFor
	if queue.reapedSignal != nil {
		select {
		case queue.reapedSignal <- struct{}{}:
		default:
		}
	}
	return queue.reapedRows, queue.reapErr
}

func (queue *fakeQueue) SpentJobs(
	_ context.Context, failedBefore time.Time, limit int32,
) ([]SpentJob, error) {
	queue.spentBefore, queue.spentLimit = failedBefore, limit
	return queue.spent, queue.spentErr
}

func (queue *fakeQueue) ReviveSpentJobs(_ context.Context, jobIDs []uuid.UUID) (int64, int64, error) {
	if queue.reviveErr != nil {
		return 0, 0, queue.reviveErr
	}
	queue.revived = append(queue.revived, jobIDs...)
	return queue.revivedRows, queue.skippedRows, nil
}

func (queue *fakeQueue) ForgetSpentJobs(_ context.Context, failedBefore time.Time) (int64, error) {
	if queue.forgetErr != nil {
		return 0, queue.forgetErr
	}
	queue.forgottenBefore = failedBefore
	return queue.forgottenRows, nil
}

func (queue *fakeQueue) Heartbeat(_ context.Context, _ Job) (bool, error) {
	queue.heartbeats++
	if queue.heartbeat != nil {
		select {
		case queue.heartbeat <- struct{}{}:
		default:
		}
	}
	return queue.owned, queue.heartbeatErr
}

func (queue *fakeQueue) Claim(_ context.Context, lane Lane) (Job, error) {
	queue.lanes = append(queue.lanes, lane)
	if len(queue.claims) == 0 {
		if queue.stop != nil {
			queue.stop()
		}
		return Job{}, pgx.ErrNoRows
	}
	next := queue.claims[0]
	queue.claims = queue.claims[1:]
	if next.stop && queue.stop != nil {
		queue.stop()
	}
	return next.job, next.err
}
func (queue *fakeQueue) ArtistMusicBrainzID(context.Context, uuid.UUID) (uuid.UUID, error) {
	return queue.musicBrainzID, queue.err
}
func (queue *fakeQueue) AlbumMusicBrainzIDs(context.Context, uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	return queue.musicBrainzID, uuid.Nil, queue.err
}
func (queue *fakeQueue) SaveArtistCatalogue(_ context.Context, _ uuid.UUID, catalogue musicbrainz.ArtistCatalogue) error {
	queue.catalogue = catalogue
	return queue.saveErr
}
func (queue *fakeQueue) SaveReleaseCatalogue(_ context.Context, _ uuid.UUID, catalogue musicbrainz.ReleaseCatalogue) error {
	queue.release = catalogue
	return queue.saveErr
}
func (queue *fakeQueue) MarkArtistGenresAsked(_ context.Context, artistID uuid.UUID) error {
	queue.artistGenresAsked = append(queue.artistGenresAsked, artistID)
	return nil
}
func (queue *fakeQueue) MarkAlbumGenresAsked(_ context.Context, albumID uuid.UUID) error {
	queue.albumGenresAsked = append(queue.albumGenresAsked, albumID)
	return nil
}
func (queue *fakeQueue) JobPayload(context.Context, uuid.UUID) ([]byte, error) {
	return nil, nil
}
func (queue *fakeQueue) Complete(_ context.Context, job Job) error {
	if queue.completeErr != nil {
		return queue.completeErr
	}
	queue.completed = job.ID
	return nil
}

// The real Repository queues the follow-up sweep itself, in the same
// transaction that retires the row, so this stand-in does the same thing
// the worker would otherwise see: a queued sweep when a wake landed.
func (queue *fakeQueue) CompleteAcquisitionSweep(_ context.Context, job Job) (bool, error) {
	if queue.completeErr != nil {
		return false, queue.completeErr
	}
	queue.completed = job.ID
	if queue.wokenWhileRunning {
		queue.sweepQueuedFor = time.Now()
	}
	return queue.wokenWhileRunning, nil
}
func (queue *fakeQueue) CompleteLibraryWitnessSweep(_ context.Context, job Job) (bool, error) {
	if queue.completeErr != nil {
		return false, queue.completeErr
	}
	queue.completed = job.ID
	if queue.wokenWhileRunning {
		queue.sweepQueuedFor = time.Now()
	}
	return queue.wokenWhileRunning, nil
}
func (queue *fakeQueue) Retry(_ context.Context, job Job, message string, runAfter time.Time) error {
	queue.retried, queue.message, queue.runAfter = job.ID, message, runAfter
	return nil
}
func (queue *fakeQueue) QueueDownloadPoll(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.pollQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueuePeerChallenges(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.challengesQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueAcquisitionSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.sweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueCoverArtSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.coverSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueLoudnessSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.loudnessSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueFollowFeedSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.feedSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueWeeklyPlaylistRefresh(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.weeklyRefreshQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueNewReleasesPlaylistRefresh(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.newReleasesRefreshQueuedFor = runAfter
	queue.newReleasesRefreshQueuedCalls++
	return nil
}
func (queue *fakeQueue) QueueRecommendationSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.recommendationSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueRecommendationSweepContinuation(
	_ context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.recommendationSweepQueuedFor = runAfter
	queue.recommendationSweepPayload = append(json.RawMessage(nil), payload...)
	return nil
}
func (queue *fakeQueue) QueueOwnRecommendationSweep(
	_ context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.ownSweepQueuedFor = runAfter
	queue.ownSweepPayload = append(json.RawMessage(nil), payload...)
	return nil
}
func (queue *fakeQueue) EnsureOwnRecommendationSweepQueued(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.ownSweepEnsuredFor = runAfter
	queue.ownSweepEnsured++
	return nil
}
func (queue *fakeQueue) QueueListensSync(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.listensSyncQueuedFor = runAfter
	return nil
}

func (queue *fakeQueue) QueueLyricsSweep(
	_ context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.lyricsSweepQueuedFor = runAfter
	queue.lyricsSweepPayload = append(json.RawMessage(nil), payload...)
	return nil
}
func (queue *fakeQueue) QueuePreviewAnchorSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.anchorSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueWantCopyJudging(_ context.Context, _ uuid.UUID, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.anchorSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueGenreSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.genreSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) BackfillGenres(_ context.Context, _ int32) (db.BackfillGenresRow, error) {
	if queue.backfillGenresErr != nil {
		return db.BackfillGenresRow{}, queue.backfillGenresErr
	}
	queue.genreBackfills++
	return queue.genreBackfill, nil
}
func (queue *fakeQueue) QueueCopyFingerprintSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.copyFingerprintSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueSpectrumSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.spectrumSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueUpgradeSweep(_ context.Context, runAfter time.Time, payload json.RawMessage) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.upgradeSweepQueuedFor = runAfter
	queue.upgradeSweepPayload = payload
	return nil
}
func (queue *fakeQueue) QueueNavidromePlaylistSweep(_ context.Context, runAfter time.Time) error {
	if queue.scheduleErr != nil {
		return queue.scheduleErr
	}
	queue.playerSweepQueuedFor = runAfter
	return nil
}
func (queue *fakeQueue) QueueFileResolutions(_ context.Context, limit int32) (int64, error) {
	if queue.resolutionsErr != nil {
		return 0, queue.resolutionsErr
	}
	queue.resolutionBatch = limit
	return queue.resolutionsQueued, nil
}

func (queue *fakeQueue) QueueLibraryWitnessFingerprintSweep(_ context.Context, runAfter time.Time) error {
	queue.sweepQueuedFor = runAfter
	return nil
}

func (queue *fakeQueue) QueueLibraryWitnessFingerprintJobs(_ context.Context, _ int32) (int64, error) {
	return 0, nil
}

func (queue *fakeQueue) MarkUploadedFiles(
	_ context.Context, uploadID uuid.UUID, paths []string,
) error {
	queue.markedUpload = uploadID
	queue.markedPaths = append([]string(nil), paths...)
	return nil
}

func (queue *fakeQueue) QueueNotifyPlayer(context.Context) error {
	queue.notifyPlayerQueued++
	return queue.notifyPlayerErr
}

func (queue *fakeQueue) SaveCatalogueReleaseGroup(
	_ context.Context, detail musicbrainz.ReleaseGroupDetail,
) (uuid.UUID, error) {
	queue.ingested = detail
	return queue.ingestedID, queue.err
}

func (queue *fakeQueue) Fail(_ context.Context, job Job, message string) error {
	queue.failed, queue.message = job.ID, message
	return nil
}

func (queue *fakeQueue) Cancel(_ context.Context, job Job, message string) error {
	if queue.cancelErr != nil {
		return queue.cancelErr
	}
	queue.cancelled, queue.message = job.ID, message
	return nil
}

func (queue *fakeQueue) SourceSearchRunCancelled(context.Context, uuid.UUID) (bool, error) {
	return queue.runCancelled, queue.runCancelledErr
}

type fakeProvider struct {
	catalogue    musicbrainz.ArtistCatalogue
	release      musicbrainz.ReleaseCatalogue
	releaseGroup musicbrainz.ReleaseGroupDetail
	err          error
	// releaseGroupErr is separate so an ingest can fail while the other
	// provider calls succeed.
	releaseGroupErr error
}

type fakeScanner struct {
	result library.ScanResult
	err    error
}

type blockingScanner struct {
	started chan struct{}
}

type fakeImporter struct {
	imported uuid.UUID
	failed   uuid.UUID
	detail   string
	err      error
	// failErr is an exhaustion that could not be written against the request.
	failErr error
	// judgedAgain counts the passes over copies already decided about,
	// judgedWants names the wants judged one at a time, and judgeAgainErr is a
	// pass that could not run.
	judgedWants   []uuid.UUID
	judgedAgain   int
	judgeAgainErr error
	// The same two for the pass over the copies refused on the artist tag.
	judgedCreditRefusals   int
	judgeCreditRefusalsErr error
	// fingerprinted counts the backfill passes over held copies, moreToMeasure
	// is a pass that filled its batch, and fingerprintErr is a pass that could
	// not run at all.
	fingerprinted  int
	moreToMeasure  bool
	fingerprintErr error
}

func (importer *fakeImporter) Import(_ context.Context, requestID uuid.UUID) error {
	importer.imported = requestID
	return importer.err
}

func (importer *fakeImporter) JudgeAgain(_ context.Context) error {
	importer.judgedAgain++
	return importer.judgeAgainErr
}

func (importer *fakeImporter) JudgeCreditRefusalsAgain(_ context.Context) error {
	importer.judgedCreditRefusals++
	return importer.judgeCreditRefusalsErr
}

func (importer *fakeImporter) JudgeWantCopies(_ context.Context, targetID uuid.UUID) error {
	importer.judgedWants = append(importer.judgedWants, targetID)
	return importer.judgeAgainErr
}
func (importer *fakeImporter) FingerprintHeldCopies(_ context.Context) (bool, error) {
	importer.fingerprinted++
	return importer.moreToMeasure, importer.fingerprintErr
}
func (importer *fakeImporter) Fail(_ context.Context, requestID uuid.UUID, detail string) error {
	if importer.failErr != nil {
		return importer.failErr
	}
	importer.failed, importer.detail = requestID, detail
	return nil
}

func (scanner fakeScanner) Scan(context.Context) (library.ScanResult, error) {
	return scanner.result, scanner.err
}

func (scanner blockingScanner) Scan(ctx context.Context) (library.ScanResult, error) {
	close(scanner.started)
	<-ctx.Done()
	return library.ScanResult{}, ctx.Err()
}

func (provider fakeProvider) ReleaseCatalogue(context.Context, uuid.UUID, uuid.UUID) (musicbrainz.ReleaseCatalogue, error) {
	return provider.release, provider.err
}

func (provider fakeProvider) ArtistCatalogue(context.Context, uuid.UUID) (musicbrainz.ArtistCatalogue, error) {
	return provider.catalogue, provider.err
}

func (provider fakeProvider) ReleaseGroup(context.Context, uuid.UUID) (musicbrainz.ReleaseGroupDetail, error) {
	if provider.releaseGroupErr != nil {
		return musicbrainz.ReleaseGroupDetail{}, provider.releaseGroupErr
	}
	return provider.releaseGroup, provider.err
}

func TestWorkerProcessesAlbumRefresh(t *testing.T) {
	jobID, albumID, releaseGroupID, releaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"albumId": albumID})
	release := musicbrainz.ReleaseCatalogue{
		Edition: musicbrainz.ReleaseEdition{ID: releaseID, Title: "Dummy"},
		Tracks:  []musicbrainz.Track{{RecordingID: uuid.New(), Title: "Mysterons"}},
	}
	queue := &fakeQueue{musicBrainzID: releaseGroupID}
	worker := NewWorker(queue, fakeProvider{release: release}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshAlbumMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || queue.release.Edition.ID != releaseID {
		t.Errorf("completed = %s, release = %#v", queue.completed, queue.release)
	}
}

func TestWorkerProcessesAndExhaustsDownloadImports(t *testing.T) {
	jobID, requestID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"requestId": requestID})
	queue := &fakeQueue{}
	importer := &fakeImporter{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)
	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportDownload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})
	if importer.imported != requestID || queue.completed != jobID {
		t.Fatalf("imported=%s completed=%s", importer.imported, queue.completed)
	}
	if queue.notifyPlayerQueued != 1 {
		t.Fatalf("notify queued = %d, want the player told once", queue.notifyPlayerQueued)
	}

	queue = &fakeQueue{}
	importer = &fakeImporter{err: errors.New("inbox is unavailable")}
	worker = NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)
	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportDownload, Payload: payload, Attempts: 3, MaxAttempts: 3,
	})
	if importer.failed != requestID || queue.failed != jobID ||
		!strings.Contains(importer.detail, "inbox") {
		t.Fatalf("failed import=%s job=%s detail=%q", importer.failed, queue.failed, importer.detail)
	}
}

func TestWorkerProcessesArtistRefresh(t *testing.T) {
	jobID, artistID, musicBrainzID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": artistID})
	catalogue := musicbrainz.ArtistCatalogue{
		Artist: musicbrainz.Artist{ID: musicBrainzID, Name: "Portishead"},
	}
	queue := &fakeQueue{musicBrainzID: musicBrainzID}
	worker := NewWorker(queue, fakeProvider{catalogue: catalogue}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID {
		t.Errorf("completed job = %s, want %s", queue.completed, jobID)
	}
	if queue.catalogue.Artist.ID != musicBrainzID {
		t.Errorf("saved catalogue = %#v", queue.catalogue)
	}
	if queue.failed != uuid.Nil || queue.retried != uuid.Nil {
		t.Error("successful job was marked failed or retried")
	}
}

func TestWorkerProcessesLibraryScan(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 3, Updated: 2, Unchanged: 1}},
	)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.completed != jobID || queue.failed != uuid.Nil {
		t.Fatalf("completed = %s, failed = %s", queue.completed, queue.failed)
	}
	// A scan finds music but does not find out what it is, so the files nobody
	// has asked about are queued once the scan itself is done.
	if queue.resolutionBatch != resolutionBatch {
		t.Errorf("resolution batch = %d, want %d", queue.resolutionBatch, resolutionBatch)
	}
}

// announced drains the notices a worker published. Publishing is synchronous
// into a buffered channel, so everything a finished job had to say is already
// waiting by the time process returns.
func announced(notices <-chan events.Topic) []events.Topic {
	var topics []events.Topic
	for {
		select {
		case topic := <-notices:
			topics = append(topics, topic)
		default:
			return topics
		}
	}
}

// A view that is not already asking has no other way to learn that a scan
// started, so both ends of one are announced.
func TestWorkerAnnouncesBothEndsOfALibraryScan(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop(), fakeScanner{}).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if got := announced(notices); len(got) != 2 ||
		got[0] != events.TopicLibrary || got[1] != events.TopicLibrary {
		t.Fatalf("notices = %v, want the library topic at both ends", got)
	}
}

// A scan that failed has to stop reading as one still running, so the settling
// notice does not depend on the scan having worked.
func TestWorkerAnnouncesAScanThatFailed(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(
		&fakeQueue{}, fakeProvider{}, zerolog.Nop(),
		fakeScanner{err: errors.New("no enabled music folders are configured")},
	).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if got := announced(notices); len(got) != 2 {
		t.Fatalf("notices = %v, want the library topic at both ends", got)
	}
}

// One scan queues hundreds of resolutions, so a file says only that it settled.
// Announcing the claim as well would double what the library view refetches for
// no state a person could read in between.
func TestWorkerAnnouncesAResolvedFileOnce(t *testing.T) {
	fileID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": fileID})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).
		WithFileResolver(&fakeResolver{}).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ResolveLibraryFile, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if got := announced(notices); len(got) != 1 || got[0] != events.TopicLibrary {
		t.Fatalf("notices = %v, want one library notice", got)
	}
}

// A completed scan queues one of these per unresolved file, and a file the
// library can already answer for settles at once. The library view is told at a
// pace it can be redrawn at rather than once per file.
func TestWorkerRationsNoticesFromABatchOfResolutions(t *testing.T) {
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": uuid.New()})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).
		WithFileResolver(&fakeResolver{}).WithEvents(hub)

	for range 20 {
		worker.process(context.Background(), Job{
			ID: uuid.New(), Kind: ResolveLibraryFile, Payload: payload, Attempts: 1, MaxAttempts: 3,
		})
	}

	if got := announced(notices); len(got) != 1 || got[0] != events.TopicLibrary {
		t.Fatalf("notices = %v, want one library notice for the batch so far", got)
	}
}

// The artists list shows a refresh as running and then as finished, and neither
// transition is something it can work out for itself.
func TestWorkerAnnouncesAnArtistRefresh(t *testing.T) {
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 1,
	})

	if got := announced(notices); len(got) != 2 ||
		got[0] != events.TopicCatalogue || got[1] != events.TopicCatalogue {
		t.Fatalf("notices = %v, want the catalogue topic at both ends", got)
	}
}

// Until the release group has been saved there is no release for a view to have
// got wrong, so an ingest announces itself only once it has settled.
func TestWorkerAnnouncesAnIngestedReleaseGroupOnce(t *testing.T) {
	releaseGroupID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"releaseGroupId": releaseGroupID})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(&fakeQueue{ingestedID: uuid.New()}, fakeProvider{
		releaseGroup: musicbrainz.ReleaseGroupDetail{
			ReleaseGroup: musicbrainz.ReleaseGroup{ID: releaseGroupID},
		},
	}, zerolog.Nop()).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: IngestReleaseGroup, Payload: payload, Attempts: 1, MaxAttempts: 1,
	})

	if got := announced(notices); len(got) != 1 || got[0] != events.TopicCatalogue {
		t.Fatalf("notices = %v, want one catalogue notice", got)
	}
}

type fakeResolver struct {
	resolved uuid.UUID
	failed   uuid.UUID
	detail   string
	err      error
	// failErr is an exhaustion that could not be written against the file.
	failErr error
}

func (resolver *fakeResolver) Resolve(_ context.Context, fileID uuid.UUID) error {
	resolver.resolved = fileID
	return resolver.err
}

func (resolver *fakeResolver) Fail(_ context.Context, fileID uuid.UUID, detail string) error {
	if resolver.failErr != nil {
		return resolver.failErr
	}
	resolver.failed, resolver.detail = fileID, detail
	return nil
}

func TestWorkerResolvesLibraryFile(t *testing.T) {
	jobID, fileID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": fileID})
	queue := &fakeQueue{}
	resolver := &fakeResolver{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithFileResolver(resolver)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if resolver.resolved != fileID || queue.completed != jobID {
		t.Fatalf("resolved = %s, completed = %s", resolver.resolved, queue.completed)
	}
}

// A provider that cannot be reached is worth retrying, but a file must not be
// left looking as though nobody had got to it. Once the retries are spent, the
// failure is recorded against the file itself.
func TestWorkerRecordsExhaustedResolution(t *testing.T) {
	jobID, fileID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": fileID})
	queue := &fakeQueue{}
	resolver := &fakeResolver{err: errors.New("provider unavailable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithFileResolver(resolver)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})
	if resolver.failed != uuid.Nil || queue.retried != jobID {
		t.Fatalf("failed = %s retried = %s, want a retry first", resolver.failed, queue.retried)
	}

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: payload, Attempts: 3, MaxAttempts: 3,
	})
	if resolver.failed != fileID || !strings.Contains(resolver.detail, "provider unavailable") {
		t.Fatalf("failed = %s detail = %q", resolver.failed, resolver.detail)
	}
	if queue.failed != jobID {
		t.Errorf("job failed = %s, want %s", queue.failed, jobID)
	}
}

func TestWorkerRefusesResolutionWithoutAFile(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithFileResolver(&fakeResolver{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})
	if queue.failed != jobID || queue.retried == jobID {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

func TestWorkerRetriesProviderFailure(t *testing.T) {
	jobID, artistID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": artistID})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{err: errors.New("provider unavailable")}, zerolog.Nop())
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 2, MaxAttempts: 3,
	})

	if queue.retried != jobID || !queue.runAfter.Equal(now.Add(10*time.Second)) {
		t.Errorf("retry = (%s, %s)", queue.retried, queue.runAfter)
	}
	if queue.message == "" {
		t.Error("retry error message is empty")
	}
}

func TestWorkerFailsInvalidPayloadWithoutRetry(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: json.RawMessage(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Errorf("failed = %s, retried = %s", queue.failed, queue.retried)
	}
}

func TestRepositoryReleaseValues(t *testing.T) {
	if value := exactReleaseDate("1994-08-22"); value == nil || value.Format("2006-01-02") != "1994-08-22" {
		t.Errorf("exactReleaseDate() = %v", value)
	}
	if value := exactReleaseDate("1994"); value != nil {
		t.Errorf("partial date = %v, want nil", value)
	}
	if value := albumType(""); value != "album" {
		t.Errorf("albumType(\"\") = %q", value)
	}
}

func TestCreditRowsNumberTheListInProviderOrder(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	rows := creditRows([]musicbrainz.Credit{
		{Name: "Porter Robinson", ArtistID: first, JoinPhrase: ", "},
		{Name: "Ninajirachi", ArtistID: second},
	})
	if len(rows) != 2 {
		t.Fatalf("creditRows() = %d rows, want 2", len(rows))
	}
	if rows[0] != (creditRow{position: 1, name: "Porter Robinson", artistID: first, joinPhrase: ", "}) {
		t.Errorf("first row = %#v", rows[0])
	}
	if rows[1] != (creditRow{position: 2, name: "Ninajirachi", artistID: second}) {
		t.Errorf("second row = %#v", rows[1])
	}
}

// An artist MusicBrainz holds no entity for is silence about the artist, not a
// reason to lose the name — and the place has to stay taken, or the next
// artist's identifier stands against this one's name.
func TestCreditRowsKeepTheirPlaceWithoutAnArtist(t *testing.T) {
	last := uuid.New()
	rows := creditRows([]musicbrainz.Credit{
		{Name: "A choir nobody catalogued", JoinPhrase: " feat. "},
		{Name: "Ninajirachi", ArtistID: last},
	})
	if len(rows) != 2 || rows[0].artistID != nil {
		t.Fatalf("creditRows() = %#v", rows)
	}
	if rows[1].position != 2 || rows[1].artistID != last {
		t.Errorf("second row = %#v", rows[1])
	}
}

// An entry printed with no name at all is still an entry: dropping it would
// take its join phrase with it, and the list would stop reproducing the credit.
func TestCreditRowsKeepAnEntryWithNoPrintedName(t *testing.T) {
	rows := creditRows([]musicbrainz.Credit{
		{JoinPhrase: " & "},
		{Name: "Ninajirachi"},
	})
	if len(rows) != 2 {
		t.Fatalf("creditRows() = %d rows, want 2", len(rows))
	}
	if rows[0].position != 1 || rows[0].name != "" || rows[0].joinPhrase != " & " {
		t.Errorf("blank row = %#v", rows[0])
	}
}

type fakeTracker struct {
	active bool
	calls  int
	err    error
}

func (tracker *fakeTracker) Poll(context.Context) (bool, error) {
	tracker.calls++
	return tracker.active, tracker.err
}

func (tracker *fakeTracker) PollInterval() time.Duration { return 5 * time.Second }

type fakeAnswerer struct {
	waiting bool
	calls   int
	err     error
}

func (answerer *fakeAnswerer) Answer(context.Context) (bool, error) {
	answerer.calls++
	return answerer.waiting, answerer.err
}

func (answerer *fakeAnswerer) Interval() time.Duration { return time.Minute }

// fakeSweeper stands in for the component that attempts wanted recordings. It
// reports when the next one is due, which is the whole contract.
type fakeSweeper struct {
	nextDue time.Time
	calls   int
	err     error
	// nextDueErr is a schedule that cannot be read, which is separate from a
	// sweep that failed: the sweeping worked, the answer about the next one did
	// not.
	nextDueErr error
}

func (sweeper *fakeSweeper) Sweep(context.Context) error {
	sweeper.calls++
	return sweeper.err
}

func (sweeper *fakeSweeper) NextDue(context.Context) (time.Time, error) {
	return sweeper.nextDue, sweeper.nextDueErr
}

// A want is due at a time it recorded for itself, so the next sweep is asked
// for rather than assumed.
func TestWorkerSweepsAgainWhenTheNextWantIsDue(t *testing.T) {
	jobID := uuid.New()
	due := time.Now().Add(time.Hour)
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{nextDue: due})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if queue.completed != jobID {
		t.Fatalf("completed = %s, want %s", queue.completed, jobID)
	}
	if !queue.sweepQueuedFor.Equal(due) {
		t.Fatalf("next sweep = %v, want %v", queue.sweepQueuedFor, due)
	}
}

// A sweeper with nothing to sweep stops, rather than keeping a timer running to
// discover that nothing is wanted.
func TestWorkerStopsSweepingWhenNothingIsScheduled(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if !queue.sweepQueuedFor.IsZero() {
		t.Fatalf("a sweep was rescheduled with nothing waiting: %v", queue.sweepQueuedFor)
	}
}

// A wake that lands mid-pass cannot queue a second sweep — the partial index
// allows only one live sweeper — so QueueAcquisitionSweep marks the running
// row instead of dropping it silently. The pass reads that mark when it
// finishes and asks for another sweep right away, on top of whatever NextDue
// found, because not every wake changes a want's own schedule (the source
// coming back online is one that does not).
func TestWorkerSweepsAgainWhenSomethingWokeItWhileRunning(t *testing.T) {
	queue := &fakeQueue{wokenWhileRunning: true}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if queue.sweepQueuedFor.IsZero() {
		t.Fatal("nothing was due, but a wake during the pass should have queued another sweep")
	}
	if queue.sweepQueuedFor.After(time.Now()) {
		t.Fatalf("sweep queued for %v, want now", queue.sweepQueuedFor)
	}
}

// A sweep that could not read the database says nothing about the wants, so it
// is worth trying again rather than settling them.
func TestWorkerRetriesASweepItCouldNotFinish(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{err: errors.New("database is unreachable")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

// The sweeper is its own scheduler, so a sweep that failed for good and queued
// nothing would be the last one this installation ever ran.
func TestWorkerKeepsSweepingAfterASweepFailsForGood(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{err: errors.New("the database is unreachable")})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if queue.sweepQueuedFor.IsZero() {
		t.Fatal("a sweep that failed for good left nothing to sweep again")
	}
}

// The replacement is dated forward. A sweep is scheduled for the moment a target
// became due, so that moment is already past by the time it runs — asking for the
// next one at the same time would retry as fast as the worker can loop for as
// long as whatever broke stays broken.
func TestWorkerWaitsBeforeSweepingAfterAFailure(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{
			nextDue: time.Now().Add(-time.Hour),
			err:     errors.New("the database is unreachable"),
		})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if !queue.sweepQueuedFor.After(time.Now()) {
		t.Fatalf("next sweep = %v, want it dated forward rather than at a time already past",
			queue.sweepQueuedFor)
	}
}

// A sweep that will be retried already is the next sweep, and it carries its own
// backoff. Asking for another would move that backoff to whenever a target
// became due, which is what the backoff exists to avoid.
func TestWorkerDoesNotQueueASweepOverTheRetryOfOne(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{
			nextDue: time.Now().Add(-time.Hour),
			err:     errors.New("the database is unreachable"),
		})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
	if !queue.sweepQueuedFor.IsZero() {
		t.Fatalf("a second sweep was queued over the retry of one: %v", queue.sweepQueuedFor)
	}
}

func TestWorkerKeepsPollingWhileTransfersMove(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	tracker := &fakeTracker{active: true}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithTransferTracker(tracker)

	worker.process(context.Background(), Job{ID: jobID, Kind: PollDownloads, Attempts: 1, MaxAttempts: 1})

	if tracker.calls != 1 || queue.completed != jobID {
		t.Fatalf("calls = %d, completed = %s", tracker.calls, queue.completed)
	}
	// Rescheduled only after completing, because one poll job may be queued at
	// a time, and dated forward rather than immediately.
	if queue.pollQueuedFor.IsZero() || !queue.pollQueuedFor.After(time.Now()) {
		t.Fatalf("next poll = %v, want a future time", queue.pollQueuedFor)
	}
}

// A peer holding a transfer is read again in a minute, and only after this run
// has been completed: one read of a conversation may be queued at a time, and a
// second would answer the same challenge twice.
func TestWorkerKeepsReadingPeerChatWhileATransferWaits(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	answerer := &fakeAnswerer{waiting: true}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithPeerChallengeAnswerer(answerer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: AnswerPeerChallenges, Attempts: 1, MaxAttempts: 3,
	})

	if answerer.calls != 1 || queue.completed != jobID {
		t.Fatalf("calls = %d, completed = %s", answerer.calls, queue.completed)
	}
	if queue.challengesQueuedFor.IsZero() || !queue.challengesQueuedFor.After(time.Now()) {
		t.Fatalf("next read = %v, want a future time", queue.challengesQueuedFor)
	}
}

func TestWorkerStopsReadingPeerChatOnceNothingIsWaiting(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPeerChallengeAnswerer(&fakeAnswerer{waiting: false})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: AnswerPeerChallenges, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.challengesQueuedFor.IsZero() {
		t.Fatalf("peer chat was rescheduled with nothing waiting: %v", queue.challengesQueuedFor)
	}
}

func TestWorkerStopsPollingOnceNothingIsMoving(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithTransferTracker(&fakeTracker{active: false})

	worker.process(context.Background(), Job{ID: uuid.New(), Kind: PollDownloads, Attempts: 1, MaxAttempts: 1})

	if !queue.pollQueuedFor.IsZero() {
		t.Fatalf("polling was rescheduled with nothing left to follow: %v", queue.pollQueuedFor)
	}
}

// A provider that cannot be read is worth retrying: its transfers carry on
// whether or not Schall is watching.
func TestWorkerRetriesAnUnreadableProvider(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	tracker := &fakeTracker{err: errors.New("slskd did not respond in time")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithTransferTracker(tracker)

	worker.process(context.Background(), Job{ID: jobID, Kind: PollDownloads, Attempts: 1, MaxAttempts: 3})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

func TestWorkerFailsAPollWithoutATracker(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{ID: jobID, Kind: PollDownloads, Attempts: 1, MaxAttempts: 1})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want %s", queue.failed, jobID)
	}
}

func TestWorkerIngestsReleaseGroup(t *testing.T) {
	jobID, releaseGroupID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"releaseGroupId": releaseGroupID})
	detail := musicbrainz.ReleaseGroupDetail{
		ReleaseGroup: musicbrainz.ReleaseGroup{ID: releaseGroupID, Title: "Dummy"},
		Artist:       musicbrainz.Artist{ID: uuid.New(), Name: "Portishead", SortName: "Portishead"},
		Credit:       "Portishead",
	}
	queue := &fakeQueue{ingestedID: albumID}
	worker := NewWorker(queue, fakeProvider{releaseGroup: detail}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: IngestReleaseGroup, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.ingested.ReleaseGroup.ID != releaseGroupID || queue.completed != jobID {
		t.Fatalf("ingested = %s, completed = %s", queue.ingested.ReleaseGroup.ID, queue.completed)
	}
}

// A release group MusicBrainz no longer knows is an answer, not an outage.
// Retrying it would ask the same question until the attempts ran out.
func TestWorkerDoesNotRetryUnknownReleaseGroup(t *testing.T) {
	jobID, releaseGroupID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"releaseGroupId": releaseGroupID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{releaseGroupErr: musicbrainz.ErrNotFound}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: IngestReleaseGroup, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried == jobID {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
	if queue.ingested.ReleaseGroup.ID != uuid.Nil {
		t.Errorf("ingested %s, want nothing stored", queue.ingested.ReleaseGroup.ID)
	}
}

// A provider that cannot be reached says nothing about the release group, so
// the ingest is worth trying again.
func TestWorkerRetriesUnreachableProviderDuringIngest(t *testing.T) {
	jobID, releaseGroupID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"releaseGroupId": releaseGroupID})
	queue := &fakeQueue{}
	worker := NewWorker(
		queue, fakeProvider{releaseGroupErr: errors.New("provider unavailable")}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: IngestReleaseGroup, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

func TestWorkerRefusesIngestWithoutAReleaseGroup(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: IngestReleaseGroup, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})
	if queue.failed != jobID || queue.retried == jobID {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

type fakeSearcher struct {
	searchedRun   uuid.UUID
	searchedAlbum uuid.UUID
	failedAlbum   uuid.UUID
	detail        string
	err           error
	// failErr is an exhaustion that could not be written against the release.
	failErr error
}

func (searcher *fakeSearcher) Search(_ context.Context, runID, albumID uuid.UUID) error {
	searcher.searchedRun, searcher.searchedAlbum = runID, albumID
	return searcher.err
}

func (searcher *fakeSearcher) Fail(_ context.Context, _, albumID uuid.UUID, detail string) error {
	if searcher.failErr != nil {
		return searcher.failErr
	}
	searcher.failedAlbum, searcher.detail = albumID, detail
	return nil
}

func TestWorkerSearchesOneReleaseOfARun(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{}
	searcher := &fakeSearcher{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})

	if searcher.searchedRun != runID || searcher.searchedAlbum != albumID {
		t.Fatalf("searched run = %s album = %s", searcher.searchedRun, searcher.searchedAlbum)
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want %s", queue.completed, jobID)
	}
}

// A release whose search never ran must not sit in a run looking as though
// nobody had got to it. Once the retries are spent the failure is recorded
// against that release, and the rest of the run is untouched.
func TestWorkerRecordsAnExhaustedSourceSearch(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{}
	searcher := &fakeSearcher{err: errors.New("slskd is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})
	if searcher.failedAlbum != uuid.Nil || queue.retried != jobID {
		t.Fatalf("failed = %s retried = %s, want a retry first", searcher.failedAlbum, queue.retried)
	}

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 2, MaxAttempts: 2,
	})
	if searcher.failedAlbum != albumID || !strings.Contains(searcher.detail, "slskd is unreachable") {
		t.Fatalf("failed = %s detail = %q", searcher.failedAlbum, searcher.detail)
	}
	if queue.failed != jobID {
		t.Errorf("job failed = %s, want %s", queue.failed, jobID)
	}
}

// Concurrent searches are enough to trip slskd's rate limit. The ordinary
// backoff starts at five seconds, which is soon enough that the remaining
// attempts would be spent collecting the same refusal.
func TestWorkerBacksOffLongerWhenTheSourceIsRateLimiting(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	queue := &fakeQueue{}
	searcher := &fakeSearcher{
		err: fmt.Errorf("%w: slskd is refusing further searches", sources.ErrRateLimited),
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want the job queued again", queue.retried)
	}
	if waited := queue.runAfter.Sub(now); waited != rateLimitBackoff {
		t.Fatalf("backed off %s, want %s", waited, rateLimitBackoff)
	}
}

// A source that cannot search has told the run nothing about the release, and
// coming back in five seconds would spend the remaining attempts on a provider
// that has not had time to become able to answer.
func TestWorkerBacksOffLongerWhenTheSourceCannotSearch(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	queue := &fakeQueue{}
	searcher := &fakeSearcher{
		err: fmt.Errorf("%w: slskd is running but is not connected to the Soulseek network",
			sources.ErrProviderUnavailable),
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want the job queued again", queue.retried)
	}
	if waited := queue.runAfter.Sub(now); waited != rateLimitBackoff {
		t.Fatalf("backed off %s, want %s", waited, rateLimitBackoff)
	}
}

// Rate limiting still has to settle the release once the attempts are spent,
// or it sits in the run forever looking as though nobody had got to it.
func TestWorkerRecordsARateLimitedSearchOnceAttemptsAreSpent(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{}
	searcher := &fakeSearcher{
		err: fmt.Errorf("%w: slskd is refusing further searches", sources.ErrRateLimited),
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 3, MaxAttempts: 3,
	})

	if searcher.failedAlbum != albumID {
		t.Fatalf("failed album = %s, want %s recorded rather than left pending",
			searcher.failedAlbum, albumID)
	}
	if !strings.Contains(searcher.detail, "rate limiting") {
		t.Errorf("detail = %q, want it to say why the search never ran", searcher.detail)
	}
}

func TestWorkerRefusesASourceSearchWithoutARun(t *testing.T) {
	jobID, albumID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"albumId": albumID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(&fakeSearcher{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})
	if queue.failed != jobID || queue.retried == jobID {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

// A release being searched when its run was cancelled was left to finish, and
// finishing badly must not put it back in the queue: the retry would search a
// release nobody is waiting for, after the run it belongs to was called off.
func TestWorkerStopsAFailedSearchWhoseRunWasCancelled(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	queue := &fakeQueue{runCancelled: true}
	searcher := &fakeSearcher{err: errors.New("slskd is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithSourceSearcher(searcher).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})

	if queue.retried == jobID {
		t.Fatalf("retried = %s, want the job left stopped", queue.retried)
	}
	if queue.cancelled != jobID {
		t.Fatalf("cancelled = %s, want %s", queue.cancelled, jobID)
	}
	if got := announced(notices); len(got) != 1 || got[0] != events.TopicSourceSearches {
		t.Errorf("notices = %v, want the source-searches topic", got)
	}
}

// What cancelling took from a release with attempts left is the attempts, so
// stopping is the whole of why it has no sources. Recording a failure would say
// the release had been searched as often as it was going to be and come up
// empty, which is not what happened.
func TestWorkerLeavesAReleaseUnansweredWhenCancellingTookItsRemainingAttempts(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{runCancelled: true}
	searcher := &fakeSearcher{err: errors.New("slskd is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})

	if searcher.failedAlbum != uuid.Nil {
		t.Errorf("recorded a failure against %s for a release that was stopped, not searched out",
			searcher.failedAlbum)
	}
}

// The same on the last attempt: the job is still stopped rather than failed,
// because a cancelled run has nothing left to queue and a retry would search a
// release nobody is waiting for.
func TestWorkerStopsASpentSearchWhoseRunWasCancelled(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{runCancelled: true}
	searcher := &fakeSearcher{err: errors.New("slskd is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 2, MaxAttempts: 2,
	})

	if queue.cancelled != jobID || queue.failed == jobID || queue.retried == jobID {
		t.Fatalf("cancelled = %s failed = %s retried = %s, want the job stopped",
			queue.cancelled, queue.failed, queue.retried)
	}
}

// A release on its last attempt had no attempts left for the cancellation to
// take, so what it has to show for itself is the provider's refusal. Somebody
// asking why this release found nothing is owed that and not the run's
// cancellation, which they can read off the rest of the run.
func TestWorkerRecordsWhyTheLastAttemptOfACancelledRunsSearchFailed(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{runCancelled: true}
	searcher := &fakeSearcher{
		err: fmt.Errorf("%w: slskd is running but is not connected to the Soulseek network",
			sources.ErrProviderUnavailable),
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 2, MaxAttempts: 2,
	})

	if searcher.failedAlbum != albumID {
		t.Fatalf("failed album = %s, want %s recorded rather than left saying only that the run stopped",
			searcher.failedAlbum, albumID)
	}
	if !strings.Contains(searcher.detail, "not connected to the Soulseek network") {
		t.Errorf("detail = %q, want the provider's own words for why the search never ran",
			searcher.detail)
	}
}

// Cancelling a run is not a reason to withdraw an answer somebody may act on.
// A release claimed before the run was stopped is left to finish, and what it
// finds is recorded and kept.
func TestWorkerKeepsTheResultOfASearchThatOutlivedItsCancelledRun(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{runCancelled: true}
	searcher := &fakeSearcher{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})

	if searcher.searchedAlbum != albumID {
		t.Fatalf("searched = %s, want the claimed release searched anyway", searcher.searchedAlbum)
	}
	if queue.completed != jobID || queue.cancelled == jobID {
		t.Fatalf("completed = %s cancelled = %s, want the search kept",
			queue.completed, queue.cancelled)
	}
}

// Whether the run was cancelled is one more thing that can fail to be read, and
// a release must not lose its remaining attempts because of it.
func TestWorkerRetriesWhenItCannotTellWhetherTheRunWasCancelled(t *testing.T) {
	jobID, runID, albumID := uuid.New(), uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID, "albumId": albumID})
	queue := &fakeQueue{runCancelledErr: errors.New("the database is unreachable")}
	searcher := &fakeSearcher{err: errors.New("slskd is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithSourceSearcher(searcher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})

	if queue.retried != jobID || queue.cancelled == jobID {
		t.Fatalf("retried = %s cancelled = %s, want the ordinary retry",
			queue.retried, queue.cancelled)
	}
}

type fakePlayer struct {
	told int
	err  error
}

func (player *fakePlayer) NoticeLibraryChange(context.Context) error {
	player.told++
	return player.err
}

// The player reads the same directory Schall writes, but only when it is told
// to or when its own schedule comes round. Queueing the notify_player job is
// what makes an import playable soon rather than whenever the player's own
// schedule comes round.
func TestWorkerQueuesToTellThePlayerAboutMusicAScanFound(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 43, Updated: 3, Unchanged: 40}},
	)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.notifyPlayerQueued != 1 {
		t.Fatalf("notify queued = %d, want the player told once", queue.notifyPlayerQueued)
	}
}

// A file that left the library is as much a change as one that arrived: the
// player is still showing it.
func TestWorkerQueuesToTellThePlayerAboutMusicAScanNoLongerFinds(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 40, Missing: 1, Unchanged: 40}},
	)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.notifyPlayerQueued != 1 {
		t.Fatalf("notify queued = %d, want the player told once", queue.notifyPlayerQueued)
	}
}

// A scan that found the library exactly as it left it has nothing to say, and
// saying it anyway would have the player rescanning on every timer. Discovered
// counts every file the walk saw, unchanged ones included, which is why it
// cannot be part of the question.
func TestWorkerQueuesNothingAfterAScanThatChangedNothing(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 40, Unchanged: 40}},
	)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.notifyPlayerQueued != 0 {
		t.Fatalf("notify queued = %d, want the player left alone", queue.notifyPlayerQueued)
	}
}

// The music is in the library either way, and the player finds it on its own
// schedule. A scan that succeeded must not be undone by a courtesy that failed
// to queue.
func TestWorkerKeepsAScanThatTheNotifyJobCouldNotBeQueuedFor(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{notifyPlayerErr: errors.New("the database is unreachable")}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 3, Updated: 3}},
	)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.completed != jobID || queue.failed != uuid.Nil || queue.retried != uuid.Nil {
		t.Fatalf("completed = %s failed = %s retried = %s, want the scan kept",
			queue.completed, queue.failed, queue.retried)
	}
}

// processNotifyPlayer is the one place the player is actually told, and its
// failure is a courtesy that never fails the job.
func TestNotifyPlayerJobTellsThePlayer(t *testing.T) {
	jobID := uuid.New()
	player := &fakePlayer{}
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithPlaybackNotifier(player)

	worker.process(context.Background(), Job{ID: jobID, Kind: NotifyPlayer, Attempts: 1, MaxAttempts: 1})

	if player.told != 1 {
		t.Fatalf("told = %d, want the player told once", player.told)
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want %s", queue.completed, jobID)
	}
}

// The music is on the disc and in the catalogue whether or not the player
// answered, so a telling that failed still completes rather than retrying a
// courtesy.
func TestNotifyPlayerJobCompletesWhenThePlayerCouldNotBeToldAbout(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaybackNotifier(&fakePlayer{err: errors.New("navidrome did not respond in time")})

	worker.process(context.Background(), Job{ID: jobID, Kind: NotifyPlayer, Attempts: 1, MaxAttempts: 1})

	if queue.completed != jobID || queue.failed != uuid.Nil || queue.retried != uuid.Nil {
		t.Fatalf("completed = %s failed = %s retried = %s, want the job completed",
			queue.completed, queue.failed, queue.retried)
	}
}

// fakeFeed is a follow feed that reports what it was told to report.
type fakeFeed struct {
	more  bool
	err   error
	swept int
}

func (feed *fakeFeed) Sweep(context.Context) (bool, error) {
	feed.swept++
	return feed.more, feed.err
}

// A pass that filled its batch has more new music waiting, and waiting six hours
// for the rest of an album somebody released this morning is the one thing the
// feed exists to stop.
func TestAFollowFeedPassWithMoreWaitingComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithFollowFeedSweeper(&fakeFeed{more: true})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepFollowFeed, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.feedSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s, want one due now",
			queue.completed, queue.feedSweepQueuedFor)
	}
}

// A pass that found nothing new schedules the next one rather than nothing at
// all: the feed's whole promise is that following an artist keeps meaning
// something without anybody pressing anything.
func TestAFollowFeedPassWithNothingLeftStillSchedulesTheNextOne(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithFollowFeedSweeper(&fakeFeed{})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepFollowFeed, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.feedSweepQueuedFor.Equal(now.Add(followFeedIdle)) {
		t.Fatalf("next pass = %s, want one in %s", queue.feedSweepQueuedFor, followFeedIdle)
	}
}

// A sweep that failed its last attempt must still leave a successor behind. The
// feed schedules itself, so a pass that gave up without queueing one would stop
// the feed for good.
func TestAnExhaustedFollowFeedPassStillLeavesASuccessor(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithFollowFeedSweeper(&fakeFeed{err: errors.New("musicbrainz did not respond")})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepFollowFeed, Attempts: 3, MaxAttempts: 3,
	})

	if queue.failed != jobID || !queue.feedSweepQueuedFor.Equal(now.Add(followFeedIdle)) {
		t.Fatalf("failed = %s, next pass = %s, want the feed still scheduled",
			queue.failed, queue.feedSweepQueuedFor)
	}
}

type fakeRecommendationSweeper struct {
	result   recommendations.SweepResult
	err      error
	swept    int
	position recommendations.SweepPosition
}

func (sweeper *fakeRecommendationSweeper) SweepPage(
	_ context.Context, position recommendations.SweepPosition,
) (recommendations.SweepResult, error) {
	sweeper.swept++
	sweeper.position = position
	return sweeper.result, sweeper.err
}

type fakeOwnRecommendationSweeper struct {
	result   recommendations.OwnSweepResult
	err      error
	position recommendations.OwnSweepPosition
}

func (sweeper *fakeOwnRecommendationSweeper) SweepOwnPage(
	_ context.Context, position recommendations.OwnSweepPosition,
) (recommendations.OwnSweepResult, error) {
	sweeper.position = position
	return sweeper.result, sweeper.err
}

// runOwnSweep processes one own-recommendation job carrying position and says
// what the fake queue was left holding.
func runOwnSweep(
	t *testing.T, sweeper *fakeOwnRecommendationSweeper, position recommendations.OwnSweepPosition, attempts int,
) (*fakeQueue, uuid.UUID, time.Time) {
	t.Helper()
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithOwnRecommendationSweeper(sweeper)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }
	payload, _ := json.Marshal(position)
	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepOwnRecommendations, Payload: payload, Attempts: attempts, MaxAttempts: 3,
	})
	return queue, jobID, now
}

func queuedOwnPosition(t *testing.T, queue *fakeQueue) recommendations.OwnSweepPosition {
	t.Helper()
	var queued recommendations.OwnSweepPosition
	if err := json.Unmarshal(queue.ownSweepPayload, &queued); err != nil {
		t.Fatalf("queued payload %s: %v", queue.ownSweepPayload, err)
	}
	return queued
}

func TestAnOwnRecommendationPassWithRecordingsWaitingComesStraightBack(t *testing.T) {
	next := recommendations.OwnSweepPosition{Passes: 1, Unexpandable: []uuid.UUID{uuid.New()}}
	queue, jobID, now := runOwnSweep(t, &fakeOwnRecommendationSweeper{result: recommendations.OwnSweepResult{
		More: true, Resume: true, Position: next,
	}}, recommendations.OwnSweepPosition{}, 1)

	if queue.completed != jobID || !queue.ownSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s, want one due now", queue.completed, queue.ownSweepQueuedFor)
	}
	if queued := queuedOwnPosition(t, queue); !reflect.DeepEqual(queued, next) {
		t.Fatalf("queued position = %#v, want %#v", queued, next)
	}
}

func TestAnOwnRecommendationPassReadsItsPositionFromThePayload(t *testing.T) {
	position := recommendations.OwnSweepPosition{Passes: 4, Unexpandable: []uuid.UUID{uuid.New()}}
	sweeper := &fakeOwnRecommendationSweeper{}
	runOwnSweep(t, sweeper, position, 1)

	if !reflect.DeepEqual(sweeper.position, position) {
		t.Fatalf("sweeper position = %#v, want %#v", sweeper.position, position)
	}
}

func TestAFinishedOwnRecommendationChainComesBackADayLater(t *testing.T) {
	queue, jobID, now := runOwnSweep(t, &fakeOwnRecommendationSweeper{result: recommendations.OwnSweepResult{
		Position: recommendations.OwnSweepPosition{Passes: 3},
	}}, recommendations.OwnSweepPosition{Passes: 2}, 1)

	if queue.completed != jobID || !queue.ownSweepQueuedFor.Equal(now.Add(recommendationIdle)) {
		t.Fatalf("completed = %s, next pass = %s, want one in %s",
			queue.completed, queue.ownSweepQueuedFor, recommendationIdle)
	}
	if queued := queuedOwnPosition(t, queue); !reflect.DeepEqual(queued, recommendations.OwnSweepPosition{}) {
		t.Fatalf("queued position = %#v, want a new chain", queued)
	}
}

func TestAnOwnRecommendationPassMusicBrainzDidNotAnswerWaitsHalfAnHour(t *testing.T) {
	next := recommendations.OwnSweepPosition{Passes: 1}
	queue, _, now := runOwnSweep(t, &fakeOwnRecommendationSweeper{result: recommendations.OwnSweepResult{
		Resume: true, Position: next, Report: recommendations.OwnSweepReport{ProviderFailed: true},
	}}, recommendations.OwnSweepPosition{}, 1)

	if !queue.ownSweepQueuedFor.Equal(now.Add(recommendationUnreachable)) {
		t.Fatalf("next pass = %s, want one in %s", queue.ownSweepQueuedFor, recommendationUnreachable)
	}
	if queued := queuedOwnPosition(t, queue); !reflect.DeepEqual(queued, next) {
		t.Fatalf("queued position = %#v, want the chain carried on", queued)
	}
}

// Twenty-four answers and then a failure is still an outage (ADR 0039 §6). The
// result says there is more to do, and the failure wins over it.
func TestAnOwnRecommendationPassThatFailedAfterAnswersWaitsHalfAnHour(t *testing.T) {
	next := recommendations.OwnSweepPosition{Passes: 1, Unexpandable: []uuid.UUID{uuid.New()}}
	queue, jobID, now := runOwnSweep(t, &fakeOwnRecommendationSweeper{result: recommendations.OwnSweepResult{
		More: true, Resume: true, Position: next,
		Report: recommendations.OwnSweepReport{Asked: 24, Stored: 24, ProviderFailed: true},
	}}, recommendations.OwnSweepPosition{}, 1)

	if queue.completed != jobID || !queue.ownSweepQueuedFor.Equal(now.Add(recommendationUnreachable)) {
		t.Fatalf("completed = %s, next pass = %s, want one in %s",
			queue.completed, queue.ownSweepQueuedFor, recommendationUnreachable)
	}
	if queued := queuedOwnPosition(t, queue); !reflect.DeepEqual(queued, next) {
		t.Fatalf("queued position = %#v, want the chain carried on", queued)
	}
}

type fakeListensSyncer struct {
	result listens.Result
	err    error
}

func (syncer fakeListensSyncer) Sync(context.Context) (listens.Result, error) {
	return syncer.result, syncer.err
}

// runListensSync processes one listens sync job against a syncer that answers
// with result and err, and says what the fake queue was left holding.
func runListensSync(t *testing.T, result listens.Result, err error) (*fakeQueue, time.Time) {
	t.Helper()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithListens(fakeListensSyncer{result: result, err: err})
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }
	worker.process(context.Background(), Job{ID: uuid.New(), Kind: SyncListens, Attempts: 1, MaxAttempts: 3})
	return queue, now
}

// The own engine reads nothing but the copied listens, so the first ones that
// name a recording start it instead of waiting for a restart (ADR 0039 §6).
func TestAListensSyncThatStoredARecordingAsksForTheOwnSweep(t *testing.T) {
	queue, now := runListensSync(t, listens.Result{Fetched: 3, Stored: 3, StoredRecordings: 2}, nil)

	if queue.ownSweepEnsured != 1 || !queue.ownSweepEnsuredFor.Equal(now) {
		t.Fatalf("own sweep asked for %d times at %s, want once now", queue.ownSweepEnsured, queue.ownSweepEnsuredFor)
	}
}

func TestAListensSyncThatStoredNoRecordingDoesNotAskForTheOwnSweep(t *testing.T) {
	queue, _ := runListensSync(t, listens.Result{Fetched: 4, Stored: 4}, nil)

	if queue.ownSweepEnsured != 0 {
		t.Fatalf("own sweep asked for %d times, want none", queue.ownSweepEnsured)
	}
}

func TestAListensSyncThatFailedAfterStoringARecordingAsksForTheOwnSweep(t *testing.T) {
	queue, _ := runListensSync(t, listens.Result{Fetched: 500, Stored: 500, StoredRecordings: 480},
		errors.New("read listens after 2026-09-25T11:00:00Z: timeout"))

	if queue.ownSweepEnsured != 1 {
		t.Fatalf("own sweep asked for %d times, want once", queue.ownSweepEnsured)
	}
}

func TestAnOwnRecommendationPassThatSpentItsRetriesQueuesTheNextDay(t *testing.T) {
	queue, jobID, now := runOwnSweep(t, &fakeOwnRecommendationSweeper{err: errors.New("database went away")},
		recommendations.OwnSweepPosition{}, 3)

	if queue.failed != jobID || !queue.ownSweepQueuedFor.Equal(now.Add(recommendationIdle)) {
		t.Fatalf("failed = %s, next pass = %s, want one in %s", queue.failed, queue.ownSweepQueuedFor, recommendationIdle)
	}
}

func TestARecommendationPassWithMoreWaitingComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	next := recommendations.SweepPosition{
		Candidates: []recommendations.SweepCandidate{
			{RecordingID: uuid.New(), Reasons: []string{"cf_raw"}}},
		SnapshotID: "a-model", HasCandidates: true, Passes: 1, Offered: 26,
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{result: recommendations.SweepResult{
			More: true, Resume: true, Position: next,
		}})
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.recommendationSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s, want one due now",
			queue.completed, queue.recommendationSweepQueuedFor)
	}
	var queued recommendations.SweepPosition
	if err := json.Unmarshal(queue.recommendationSweepPayload, &queued); err != nil ||
		!reflect.DeepEqual(queued, next) {
		t.Fatalf("queued position = %#v, %v, want %#v", queued, err, next)
	}
}

func TestACompletedRecommendationPassKeepsTheDailySchedule(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	position := recommendations.SweepPosition{
		Candidates: []recommendations.SweepCandidate{
			{RecordingID: uuid.New(), Reasons: []string{"cf_raw"}}},
		SnapshotID: "a-model", HasCandidates: true, Passes: 1, Offered: 26,
	}
	payload, _ := json.Marshal(position)
	sweeper := &fakeRecommendationSweeper{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(sweeper)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID ||
		!queue.recommendationSweepQueuedFor.Equal(now.Add(recommendationIdle)) {
		t.Fatalf("completed = %s, next pass = %s, want one in %s",
			queue.completed, queue.recommendationSweepQueuedFor, recommendationIdle)
	}
	if !reflect.DeepEqual(sweeper.position, position) {
		t.Fatalf("sweeper position = %#v, want %#v from the job payload", sweeper.position, position)
	}
}

// A pass that got nothing at all out of a service walked less of the list than
// it meant to. Waiting a full day on that costs the list every record the pass
// never got to ask about, so it comes back once an outage has plausibly passed
// instead.
func TestARecommendationPassThatReachedNobodyComesBackSooner(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{
			result: recommendations.SweepResult{
				Resume: true,
				Report: recommendations.SweepReport{
					Status: "partial", ProviderFailed: true,
					Detail: "MusicBrainz expansion stopped at " +
						"c294757b-0430-4eb4-ae7a-6abcffb87405: context deadline exceeded",
				},
			},
		})
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID ||
		!queue.recommendationSweepQueuedFor.Equal(now.Add(recommendationUnreachable)) {
		t.Fatalf("completed = %s, next pass = %s, want one in %s",
			queue.completed, queue.recommendationSweepQueuedFor, recommendationUnreachable)
	}
}

// The pass in between the two above reports More and ProviderFailed together: it
// stored records and then one look-up failed. This is where the worker reads that
// pair, and More is what it goes by, because a service that answered several
// times over is up and merely slow. Which passes report the pair is decided in
// internal/recommendations, and issue #310 is about that; this test holds the
// wait such a pass is given once it says so.
func TestARecommendationPassThatFailedAfterStoringComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{
			result: recommendations.SweepResult{
				More: true, Resume: true,
				Position: recommendations.SweepPosition{
					Candidates: []recommendations.SweepCandidate{
						{RecordingID: uuid.New(), Reasons: []string{"cf_raw"}}},
					SnapshotID: "a-model", HasCandidates: true, Passes: 1, Offered: 1298,
				},
				Report: recommendations.SweepReport{
					Status: "partial", Stored: 3, ProviderFailed: true,
					Detail: "MusicBrainz expansion stopped at " +
						"c294757b-0430-4eb4-ae7a-6abcffb87405: context deadline exceeded",
				},
			},
		})
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.recommendationSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s, want one due now rather than in %s",
			queue.completed, queue.recommendationSweepQueuedFor, recommendationUnreachable)
	}
}

// ListenBrainz answers with no records at all both when an account has nothing
// to suggest and when its two services are quiet. A pass told that, with a list
// already published, keeps the published list and publishes nothing (issue
// #317). It cannot then wait a day, because a day is the wait for an answer
// nobody doubts; it asks again once an outage has plausibly passed.
func TestARecommendationPassThatKeptThePublishedListComesBackSooner(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{
			result: recommendations.SweepResult{
				Report: recommendations.SweepReport{
					Status:        "partial",
					Detail:        "ListenBrainz named no recordings, so the 1294 already published are kept",
					KeptPublished: true,
				},
			},
		})
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID ||
		!queue.recommendationSweepQueuedFor.Equal(now.Add(recommendationUnreachable)) {
		t.Fatalf("completed = %s, next pass = %s, want one in %s",
			queue.completed, queue.recommendationSweepQueuedFor, recommendationUnreachable)
	}
}

// A chain interrupted by an outage keeps the records it stored, unpublished, so
// the pass after it must carry the chain's position rather than begin a new
// chain with an empty one. A new chain reads nothing back and walks the same
// records again: one slow MusicBrainz request threw away 1006 of 1298 records
// this way (issue #305).
func TestAnInterruptedRecommendationChainCarriesItsPositionOn(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	position := recommendations.SweepPosition{
		Candidates: []recommendations.SweepCandidate{
			{RecordingID: uuid.New(), Reasons: []string{"cf_raw"}}},
		SnapshotID: "a-model", HasCandidates: true, Passes: 41, Offered: 1298,
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{
			result: recommendations.SweepResult{
				Resume: true, Position: position,
				Report: recommendations.SweepReport{
					Status: "partial", Detail: "MusicBrainz expansion stopped", ProviderFailed: true,
				},
			},
		})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	var queued recommendations.SweepPosition
	if err := json.Unmarshal(queue.recommendationSweepPayload, &queued); err != nil ||
		!reflect.DeepEqual(queued, position) {
		t.Fatalf("queued position = %#v, %v, want %#v", queued, err, position)
	}
}

// The other interrupted pass. A chain that walked the whole list with a
// degraded answer is finished: it published what it has, and the next sweep is
// a new chain with nothing to carry.
func TestAFinishedRecommendationChainStartsTheNextOneFresh(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{
			result: recommendations.SweepResult{Report: recommendations.SweepReport{
				Status: "partial", Detail: "top-recording seeds unavailable",
				ProviderFailed: true, Published: true,
			}},
		})
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.recommendationSweepQueuedFor.Equal(now.Add(recommendationUnreachable)) {
		t.Fatalf("next sweep = %s, want one in %s",
			queue.recommendationSweepQueuedFor, recommendationUnreachable)
	}
	if len(queue.recommendationSweepPayload) != 0 {
		t.Fatalf("queued payload = %s, want a fresh chain", queue.recommendationSweepPayload)
	}
}

// The other partial. A pass that walked as far as its own bound allows is
// working exactly as intended, and the pass after it is already queued to run
// now, so nothing about it justifies a shorter daily wait.
func TestARecommendationPassThatOnlyReachedItsBoundKeepsTheDailySchedule(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{
			result: recommendations.SweepResult{Report: recommendations.SweepReport{
				Status: "partial", Detail: "MusicBrainz expansion stopped after 64 passes",
			}},
		})
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.recommendationSweepQueuedFor.Equal(now.Add(recommendationIdle)) {
		t.Fatalf("next pass = %s, want one in %s",
			queue.recommendationSweepQueuedFor, recommendationIdle)
	}
}

func TestAnUnreachableRecommendationSourceRetriesVisibly(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{err: errors.New("listenbrainz did not respond")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID || queue.failed != uuid.Nil ||
		!strings.Contains(queue.message, "listenbrainz did not respond") {
		t.Fatalf("retried = %s failed = %s message = %q, want a visible retry",
			queue.retried, queue.failed, queue.message)
	}
	if !queue.recommendationSweepQueuedFor.IsZero() {
		t.Fatalf("successor = %s, want the retrying row to remain the one live sweep",
			queue.recommendationSweepQueuedFor)
	}
}

func TestAnExhaustedRecommendationPassStillLeavesASuccessor(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithRecommendationSweeper(&fakeRecommendationSweeper{err: errors.New("listenbrainz did not respond")})
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 3, MaxAttempts: 3,
	})

	if queue.failed != jobID || !strings.Contains(queue.message, "listenbrainz did not respond") ||
		!queue.recommendationSweepQueuedFor.Equal(now.Add(recommendationIdle)) {
		t.Fatalf("failed = %s message = %q next pass = %s, want a visible failure and successor",
			queue.failed, queue.message, queue.recommendationSweepQueuedFor)
	}
}

func TestADisabledOrUnconfiguredRecommendationSourceStopsItsQueuedSweep(t *testing.T) {
	for name, sweepErr := range map[string]error{
		"disabled": recommendations.ErrDisabled, "not configured": recommendations.ErrNotConfigured,
	} {
		t.Run(name, func(t *testing.T) {
			jobID := uuid.New()
			queue := &fakeQueue{}
			worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
				WithRecommendationSweeper(&fakeRecommendationSweeper{err: sweepErr})

			worker.process(context.Background(), Job{
				ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
			})

			if queue.failed != jobID || queue.retried != uuid.Nil ||
				!queue.recommendationSweepQueuedFor.IsZero() {
				t.Fatalf("failed = %s retried = %s successor = %s, want one permanent failure",
					queue.failed, queue.retried, queue.recommendationSweepQueuedFor)
			}
		})
	}
}

type fakeUploadImporter struct {
	imported uuid.UUID
	landed   []string
	err      error
}

func (importer *fakeUploadImporter) Import(_ context.Context, uploadID uuid.UUID) ([]string, error) {
	importer.imported = uploadID
	return importer.landed, importer.err
}

func TestWorkerImportsAStagedUpload(t *testing.T) {
	jobID, uploadID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"uploadId": uploadID})
	queue := &fakeQueue{}
	importer := &fakeUploadImporter{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithUploadImporter(importer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if importer.imported != uploadID || queue.completed != jobID {
		t.Fatalf("imported = %s, completed = %s", importer.imported, queue.completed)
	}
	if queue.notifyPlayerQueued != 1 {
		t.Fatalf("notify queued = %d, want the player told once", queue.notifyPlayerQueued)
	}
}

func TestWorkerScansAndMarksEveryImportedUpload(t *testing.T) {
	jobID, uploadID := uuid.New(), uuid.New()
	landed := []string{"/music/Artist/Album/song.flac"}
	payload, _ := json.Marshal(map[string]uuid.UUID{"uploadId": uploadID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop(), fakeScanner{}).
		WithUploadImporter(&fakeUploadImporter{landed: landed})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.markedUpload != uploadID || !reflect.DeepEqual(queue.markedPaths, landed) {
		t.Fatalf("marked upload = %s paths = %v, want %s and %v",
			queue.markedUpload, queue.markedPaths, uploadID, landed)
	}
}

// A file that is not audio will not become audio on a second attempt, and
// spending the attempts on it only delays the reason reaching whoever uploaded
// it.
func TestWorkerDoesNotRetryWhatAnUploadItselfAnswered(t *testing.T) {
	jobID, uploadID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"uploadId": uploadID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithUploadImporter(&fakeUploadImporter{
			err: fmt.Errorf(`"02 Sour Times.flac" cannot be read as audio: %w`, uploads.ErrUnreadable),
		})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s, retried = %s, want a durable refusal", queue.failed, queue.retried)
	}
	if !strings.Contains(queue.message, "02 Sour Times.flac") {
		t.Fatalf("message = %q, want it to name the file", queue.message)
	}
}

func TestWorkerRetriesAnUploadTheFilesystemRefused(t *testing.T) {
	jobID, uploadID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"uploadId": uploadID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithUploadImporter(&fakeUploadImporter{err: errors.New("input/output error")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want the job asked again", queue.retried)
	}
}

func TestWorkerRefusesAnUploadJobItCannotRead(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithUploadImporter(&fakeUploadImporter{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportUpload, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s, retried = %s", queue.failed, queue.retried)
	}
}

func TestWorkerRefusesAnUploadJobWithNothingConfiguredToImportIt(t *testing.T) {
	jobID, uploadID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"uploadId": uploadID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportUpload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the job refused", queue.failed)
	}
}

// A job kind nothing knows how to run is the queue and the worker disagreeing
// about what exists, which no number of attempts settles.
func TestWorkerRefusesAJobKindNothingRuns(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: "polish_the_brass", Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

// fakeCovers stands in for the component that fills the picture cache. It
// reports whether releases were left over, which is how the job knows whether
// to come straight back.
type fakeCovers struct {
	more  bool
	err   error
	swept int
}

func (covers *fakeCovers) Sweep(context.Context) (bool, error) {
	covers.swept++
	return covers.more, covers.err
}

// A pass that filled its batch has more releases waiting, and the pace is
// already set inside the sweep.
func TestACoverArtPassWithMoreWaitingComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithCoverArtSweeper(&fakeCovers{more: true})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepCoverArt, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.coverSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s, want one due now",
			queue.completed, queue.coverSweepQueuedFor)
	}
}

// A pass that ran out of releases comes back later to picture whatever the
// catalogue has gained since, rather than stopping for good.
func TestACoverArtPassWithNothingLeftStillSchedulesTheNextOne(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithCoverArtSweeper(&fakeCovers{})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepCoverArt, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.coverSweepQueuedFor.Equal(now.Add(coverArtIdle)) {
		t.Fatalf("next pass = %s, want one in %s", queue.coverSweepQueuedFor, coverArtIdle)
	}
}

// The picture sweep schedules itself, so a pass that gave up on its last
// attempt without queueing one would be the last this installation ever ran.
func TestAnExhaustedCoverArtPassStillLeavesASuccessor(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithCoverArtSweeper(&fakeCovers{err: errors.New("the archive did not respond")})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepCoverArt, Attempts: 3, MaxAttempts: 3,
	})

	// Sooner than coverArtIdle: this pass gave up because an archive would not
	// answer, not because there was nothing left to picture, and six hours is
	// how long a fully pictured catalogue waits rather than how long an outage
	// lasts.
	if queue.failed != jobID || !queue.coverSweepQueuedFor.Equal(now.Add(coverArtUnreachable)) {
		t.Fatalf("failed = %s, next pass = %s, want the sweep still scheduled in %s",
			queue.failed, queue.coverSweepQueuedFor, coverArtUnreachable)
	}
}

// A pass that will be retried already is the next pass. Queueing another would
// leave two of them where the schedule allows one.
func TestARetriedCoverArtPassDoesNotQueueASecondOne(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithCoverArtSweeper(&fakeCovers{err: errors.New("the archive did not respond")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepCoverArt, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
	if !queue.coverSweepQueuedFor.IsZero() {
		t.Fatalf("a second pass was queued over the retry of one: %v", queue.coverSweepQueuedFor)
	}
}

func TestACoverArtSweepIsRefusedWithNothingConfiguredToFillTheCache(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepCoverArt, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// A pass whose completion was not recorded is still running as far as the queue
// is concerned, and will be requeued by the recovery at startup. Queueing a
// successor as well would leave two.
func TestACoverArtPassThatCouldNotBeCompletedQueuesNoSuccessor(t *testing.T) {
	queue := &fakeQueue{completeErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithCoverArtSweeper(&fakeCovers{more: true})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepCoverArt, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.coverSweepQueuedFor.IsZero() {
		t.Fatalf("next pass = %v, want none over a job still running", queue.coverSweepQueuedFor)
	}
}

// The covers this pass did fill are worth showing whether or not the next pass
// could be queued, so a schedule that failed is reported rather than fatal.
func TestACoverArtPassAnnouncesEvenWhenItsSuccessorCouldNotBeQueued(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	queue := &fakeQueue{scheduleErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithCoverArtSweeper(&fakeCovers{}).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepCoverArt, Attempts: 1, MaxAttempts: 3,
	})

	if got := announced(notices); len(got) != 1 || got[0] != events.TopicCatalogue {
		t.Fatalf("notices = %v, want the catalogue topic", got)
	}
}

func TestAFollowFeedSweepIsRefusedWithNothingConfiguredToWatchTheFollows(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepFollowFeed, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

func TestARecommendationSweepIsRefusedWithNoSourceConfigured(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepRecommendations, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil ||
		!strings.Contains(queue.message, "not configured") {
		t.Fatalf("failed = %s retried = %s message = %q, want a permanent refusal",
			queue.failed, queue.retried, queue.message)
	}
}

// As with the picture sweep: a pass still running has a successor already, in
// the recovery that will requeue it.
func TestAFollowFeedPassThatCouldNotBeCompletedQueuesNoSuccessor(t *testing.T) {
	queue := &fakeQueue{completeErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithFollowFeedSweeper(&fakeFeed{more: true})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepFollowFeed, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.feedSweepQueuedFor.IsZero() {
		t.Fatalf("next pass = %v, want none over a job still running", queue.feedSweepQueuedFor)
	}
}

// The wants this pass created are worth showing whether or not the next pass
// could be queued.
func TestAFollowFeedPassAnnouncesEvenWhenItsSuccessorCouldNotBeQueued(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	queue := &fakeQueue{scheduleErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithFollowFeedSweeper(&fakeFeed{}).WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepFollowFeed, Attempts: 1, MaxAttempts: 3,
	})

	if got := announced(notices); len(got) != 1 || got[0] != events.TopicAcquisitions {
		t.Fatalf("notices = %v, want the acquisitions topic", got)
	}
}

// fakePlaylists stands in for the component that reconciles one followed
// playlist against its source.
type fakePlaylists struct {
	imported uuid.UUID
	err      error
}

func (importer *fakePlaylists) Import(_ context.Context, playlistID uuid.UUID) error {
	importer.imported = playlistID
	return importer.err
}

func TestWorkerImportsAFollowedPlaylist(t *testing.T) {
	jobID, playlistID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": playlistID})
	queue := &fakeQueue{}
	importer := &fakePlaylists{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithPlaylistImporter(importer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if importer.imported != playlistID || queue.completed != jobID {
		t.Fatalf("imported = %s, completed = %s", importer.imported, queue.completed)
	}
}

// The playlists view shows the outcome of an import, so it is told when the job
// settles whichever way it settled.
func TestWorkerAnnouncesAPlaylistImportThatFailed(t *testing.T) {
	playlistID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": playlistID})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).
		WithPlaylistImporter(&fakePlaylists{err: errors.New("spotify did not respond")}).
		WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if got := announced(notices); len(got) != 1 || got[0] != events.TopicPlaylists {
		t.Fatalf("notices = %v, want the playlists topic", got)
	}
}

func TestAPlaylistImportIsRefusedWithNothingConfiguredToRunIt(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

func TestAPlaylistImportIsRefusedWithoutAPlaylist(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistImporter(&fakePlaylists{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportPlaylist, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

// A playlist nobody follows any more will not reappear on a second attempt.
func TestAPlaylistThatIsNoLongerFollowedIsNotAskedAboutAgain(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistImporter(&fakePlaylists{err: pgx.ErrNoRows})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a durable refusal", queue.failed, queue.retried)
	}
}

// Credentials nobody has entered cannot be retried into existence.
func TestAPlaylistImportWithoutCredentialsIsNotAskedAboutAgain(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistImporter(&fakePlaylists{err: playlists.ErrNotConfigured})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a durable refusal", queue.failed, queue.retried)
	}
}

// Nor can an account nobody has connected.
func TestAPlaylistImportWithoutAConnectedAccountIsNotAskedAboutAgain(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistImporter(&fakePlaylists{err: playlists.ErrNotConnected})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a durable refusal", queue.failed, queue.retried)
	}
}

// A provider that stumbled is a different thing from one that has nothing to
// say: asking it again is exactly what fixes it.
func TestAPlaylistProviderThatStumbledIsAskedAgain(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistImporter(&fakePlaylists{err: errors.New("spotify did not respond in time")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want the playlist asked about again", queue.retried)
	}
}

func TestADownloadImportIsRefusedWithNothingConfiguredToRunIt(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"requestId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportDownload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

func TestADownloadImportIsRefusedWithoutARequest(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(&fakeImporter{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportDownload, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

func TestAFileResolutionIsRefusedWithNothingConfiguredToRunIt(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

func TestASourceSearchIsRefusedWithNothingConfiguredToRunIt(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": uuid.New(), "albumId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// A cancellation nobody could record leaves the job running for the recovery to
// requeue, so nothing may be said about a run that has not in fact stopped.
func TestASearchWhoseCancellationCouldNotBeRecordedAnnouncesNothing(t *testing.T) {
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": uuid.New(), "albumId": uuid.New()})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	queue := &fakeQueue{runCancelled: true, cancelErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithSourceSearcher(&fakeSearcher{err: errors.New("slskd is unreachable")}).
		WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SearchAlbumSources, Payload: payload, Attempts: 1, MaxAttempts: 2,
	})

	if got := announced(notices); len(got) != 0 {
		t.Fatalf("notices = %v, want nothing said about a run that did not stop", got)
	}
}

func TestALibraryScanIsRefusedWithNothingConfiguredToWalkTheDisc(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ScanLibrary, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// A scan whose completion was not recorded is still running and will be
// requeued. Queueing its files for resolution now would ask about them twice.
func TestAScanThatCouldNotBeCompletedQueuesNoResolutions(t *testing.T) {
	queue := &fakeQueue{completeErr: errors.New("the database is unreachable")}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 3, Updated: 3}},
	)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.resolutionBatch != 0 {
		t.Fatalf("resolution batch = %d, want none over a scan still running", queue.resolutionBatch)
	}
}

// The music a scan found is on the disc whether or not the providers could be
// queued to say what it is, so the scan itself stands and the player is told.
func TestAScanStandsWhenItsFilesCouldNotBeQueuedForResolution(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{resolutionsErr: errors.New("the database is unreachable")}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 3, Updated: 3}},
	)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.completed != jobID || queue.notifyPlayerQueued != 1 {
		t.Fatalf("completed = %s notify queued = %d, want the scan kept",
			queue.completed, queue.notifyPlayerQueued)
	}
}

// A poll whose completion was not recorded is still running, and the recovery
// requeues it. A second one queued here would poll twice over.
func TestAPollThatCouldNotBeCompletedIsNotRescheduled(t *testing.T) {
	queue := &fakeQueue{completeErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithTransferTracker(&fakeTracker{active: true})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: PollDownloads, Attempts: 1, MaxAttempts: 1,
	})

	if !queue.pollQueuedFor.IsZero() {
		t.Fatalf("next poll = %v, want none over a job still running", queue.pollQueuedFor)
	}
}

// The transfers this poll read were read. A schedule that could not be written
// is worth saying and not worth undoing a poll that worked.
func TestAPollStandsWhenItsSuccessorCouldNotBeQueued(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{scheduleErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithTransferTracker(&fakeTracker{active: true})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: PollDownloads, Attempts: 1, MaxAttempts: 1,
	})

	if queue.completed != jobID || queue.failed != uuid.Nil {
		t.Fatalf("completed = %s failed = %s, want the poll kept", queue.completed, queue.failed)
	}
}

func TestAnAcquisitionSweepIsRefusedWithNothingConfiguredToAttemptTheWants(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// The sweep the index allows is this one, still running as far as the queue can
// tell. Asking for the next one before it settles would collide with it.
func TestASweepThatCouldNotBeCompletedQueuesNoSuccessor(t *testing.T) {
	queue := &fakeQueue{completeErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{nextDue: time.Now().Add(time.Hour)})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if !queue.sweepQueuedFor.IsZero() {
		t.Fatalf("next sweep = %v, want none over a job still running", queue.sweepQueuedFor)
	}
}

// A schedule that could not be read says nothing about when the next want is
// due, and guessing would either sweep too early or bury the answer.
func TestASweepWhoseScheduleCouldNotBeReadQueuesNothing(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{nextDueErr: errors.New("the database is unreachable")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the sweep itself kept", queue.completed)
	}
	if !queue.sweepQueuedFor.IsZero() {
		t.Fatalf("next sweep = %v, want none guessed", queue.sweepQueuedFor)
	}
}

// The artist's own row is what names the catalogue to fetch. A database that
// could not answer says nothing about the artist, so it is asked again.
func TestAnArtistRefreshIsAskedAgainWhenTheArtistCannotBeRead(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	queue := &fakeQueue{err: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

// A catalogue fetched but not written is a refresh that did not happen, and the
// provider will answer the same way next time.
func TestAnArtistRefreshIsAskedAgainWhenTheCatalogueCannotBeSaved(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: uuid.New(), saveErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

func TestAnAlbumRefreshIsRefusedWithoutAnAlbum(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshAlbumMetadata, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

func TestAnAlbumRefreshIsAskedAgainWhenTheAlbumCannotBeRead(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"albumId": uuid.New()})
	queue := &fakeQueue{err: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshAlbumMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

func TestAnAlbumRefreshIsAskedAgainWhenTheProviderCannotBeReached(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"albumId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(
		queue, fakeProvider{err: errors.New("musicbrainz did not respond")}, zerolog.Nop(),
	)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshAlbumMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

func TestAnAlbumRefreshIsAskedAgainWhenTheCatalogueCannotBeSaved(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"albumId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: uuid.New(), saveErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshAlbumMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

// An ingest whose release group could not be written is worth another attempt:
// the provider answered, only the database did not take it.
func TestAnIngestIsAskedAgainWhenTheReleaseGroupCannotBeSaved(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"releaseGroupId": uuid.New()})
	queue := &fakeQueue{err: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: IngestReleaseGroup, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s", queue.retried, jobID)
	}
}

// A worker shutting down cancels the context under whatever it was running.
// Recording that as a failure would spend an attempt on a job nobody stopped
// for a reason of its own: the row stays running, and the recovery pass at the
// next boot is what puts it back.
func TestAJobTheShutdownInterruptedIsNeitherFailedNorRetried(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{err: context.Canceled}, zerolog.Nop())

	stopped, stop := context.WithCancel(context.Background())
	stop()
	worker.process(stopped, Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != uuid.Nil || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want the job left running", queue.failed, queue.retried)
	}
}

// The other half of that, and the bug it was written for. A single request
// inside a healthy job running out of time raises exactly the error a shutdown
// does, and reading the error rather than the worker left the row running for
// good — never completed, never failed, never retried, and holding the partial
// unique index against every replacement for its album. The worker is fine, so
// this is an ordinary retryable failure.
func TestAJobWhoseOwnWorkTimedOutIsRetried(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{err: context.DeadlineExceeded}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want %s settled and queued again", queue.retried, jobID)
	}
	if !strings.Contains(queue.message, context.DeadlineExceeded.Error()) {
		t.Fatalf("message = %q, want the timeout recorded against the job", queue.message)
	}
}

// The last attempt of the same job. Nothing is left to retry it with, so it
// fails and is readable in the failed list rather than sitting running.
func TestAJobWhoseOwnWorkTimedOutOnItsLastAttemptFails(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{err: context.DeadlineExceeded}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 3, MaxAttempts: 3,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want %s settled rather than left running", queue.failed, jobID)
	}
}

// The first attempt waits the shortest delay. A job claimed before its attempt
// counter was written would otherwise compute a negative one.
func TestTheFirstAttemptWaitsTheShortestDelay(t *testing.T) {
	if delay := retryDelay(Failure{}, 0); delay != 5*time.Second {
		t.Fatalf("retryDelay(0) = %s, want 5s", delay)
	}
}

// The backoff doubles but stops. A job that keeps failing must not end up
// scheduled beyond any interval anybody would wait for.
func TestTheBackoffStopsDoublingAtFiveMinutes(t *testing.T) {
	if delay := retryDelay(Failure{}, 20); delay != 5*time.Minute {
		t.Fatalf("retryDelay(20) = %s, want the ceiling of 5m", delay)
	}
}

// A failure that reached nobody waits on a different ladder. Five seconds is
// not a wait for an outage, it is a repetition: three attempts inside twenty
// seconds spend the whole allowance before a name-resolution failure has had
// any chance to end.
func TestAFailureThatReachedNobodyWaitsMinutesBeforeTheNextAttempt(t *testing.T) {
	failure := ClassifyMessage(`Get "https://musicbrainz.org/ws/2/recording/x": ` +
		`dial tcp: lookup musicbrainz.org on 10.43.0.10:53: server misbehaving`)
	if delay := retryDelay(failure, 1); delay != 2*time.Minute {
		t.Fatalf("retryDelay = %s, want the first rung of the unreachable ladder", delay)
	}
}

// The same ladder, still doubling and still bounded.
func TestTheUnreachableBackoffStopsDoublingAtHalfAnHour(t *testing.T) {
	failure := ClassifyMessage("dial tcp 10.43.0.7:5030: connect: connection refused")
	if delay := retryDelay(failure, 20); delay != 30*time.Minute {
		t.Fatalf("retryDelay = %s, want the ceiling of 30m", delay)
	}
}

// An answer that will be the same next time spends one attempt, not three. A
// release group MusicBrainz answers 404 for is not there, and asking twice more
// only delays the reason reaching the person who can do something about it.
func TestARefusalThatWillNotChangeDoesNotSpendTheRemainingAttempts(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"releaseGroupId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{
		releaseGroupErr: errors.New("MusicBrainz returned HTTP 403"),
	}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: IngestReleaseGroup, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried == jobID {
		t.Fatal("the job was queued again; the same answer is waiting for it")
	}
	if queue.failed != jobID {
		t.Fatalf("failed = %s, want %s settled on its first attempt", queue.failed, jobID)
	}
}

// What used to wait for the last attempt now happens on the attempt that ends
// the job. Without this a transfer stopped by a permanent refusal would sit in
// the downloads view reading as still importing, with nothing left to import it.
func TestATransferStoppedForGoodIsRecordedAsFailedAtOnce(t *testing.T) {
	requestID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"requestId": requestID})
	queue := &fakeQueue{}
	importer := &fakeImporter{err: errors.New("slskd returned HTTP 404")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ImportDownload, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if importer.failed != requestID {
		t.Fatalf("failed = %s, want the transfer told it will not be imported", importer.failed)
	}
}

// The pass over the failed list presses Retry for the reader, once, on the
// failures an outage caused. This is what the seventy-one jobs of 12 August
// needed and nothing offered them.
func TestTheJobsAnOutageStoppedAreQueuedAgainByThemselves(t *testing.T) {
	stopped := uuid.New()
	queue := &fakeQueue{
		spent: []SpentJob{{ID: stopped, Kind: ImportDownload, Error: `look up MusicBrainz recording: ` +
			`dial tcp: lookup musicbrainz.org on 10.43.0.10:53: server misbehaving`}},
		revivedRows: 1,
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.reviveSpent(context.Background())

	if len(queue.revived) != 1 || queue.revived[0] != stopped {
		t.Fatalf("revived = %v, want the job the outage stopped", queue.revived)
	}
}

// A failure nobody has classified is left where it is. The bucket that holds a
// wrong setting and an unreadable file is not one to run again on a timer: those
// are for a person to read, and no different for having been run twice.
func TestAFailureSchallCannotNameIsLeftForAPersonToRead(t *testing.T) {
	queue := &fakeQueue{
		spent: []SpentJob{{
			ID: uuid.New(), Kind: ImportUpload, Error: "import upload: the folder is no longer staged",
		}},
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.reviveSpent(context.Background())

	if len(queue.revived) != 0 {
		t.Fatalf("revived = %v, want nothing queued again", queue.revived)
	}
}

// Only failures old enough for the outage behind them to have passed. A job that
// failed a minute ago is answered by its own ladder, not by this pass.
func TestOnlyFailuresOldEnoughToBeWorthRepeatingAreLookedAt(t *testing.T) {
	now := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	worker.now = func() time.Time { return now }

	worker.reviveSpent(context.Background())

	if want := now.Add(-reviveSpentAfter); !queue.spentBefore.Equal(want) {
		t.Fatalf("asked about failures before %s, want %s", queue.spentBefore, want)
	}
}

// Something clears a spent job. Nothing did before, so the failed list only ever
// grew and the count beside Settings could not return to nothing on its own.
func TestFailuresOlderThanTheWindowAreForgotten(t *testing.T) {
	now := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	queue := &fakeQueue{forgottenRows: 3}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	worker.now = func() time.Time { return now }

	worker.forgetSpent(context.Background())

	if want := now.Add(-spentJobRetention); !queue.forgottenBefore.Equal(want) {
		t.Fatalf("forgot failures before %s, want %s", queue.forgottenBefore, want)
	}
}

// The error column is bounded, so a provider that answered with a megabyte of
// HTML does not become a row nothing can read.
func TestALongFailureIsTruncatedToWhatTheColumnHolds(t *testing.T) {
	message := truncateError(errors.New(strings.Repeat("a", 3000)))
	if len(message) != 2000 {
		t.Fatalf("length = %d, want 2000", len(message))
	}
}

// laneWorker builds a worker whose queue answers the given script and then ends
// the run, so a lane loop under test finishes instead of polling forever.
func laneWorker(t *testing.T, claims ...claim) (*fakeQueue, *Worker, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	queue := &fakeQueue{claims: claims, stop: cancel}
	return queue, NewWorker(queue, fakeProvider{}, zerolog.Nop(), fakeScanner{}), ctx
}

// runLane runs a lane to completion, failing rather than hanging if it does not
// stop when its context ends.
func runLane(t *testing.T, worker *Worker, ctx context.Context, lane Lane) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.RunLane(ctx, lane)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the lane did not stop when its context ended")
	}
}

// A lane asks the queue for its own kinds and nothing else; that restriction is
// the whole of what a lane is.
func TestALaneAsksTheQueueForItsOwnKinds(t *testing.T) {
	queue, worker, ctx := laneWorker(t)
	worker.pollInterval = time.Hour

	runLane(t, worker, ctx, Acquisition())

	if len(queue.lanes) != 1 || len(queue.lanes[0].Only) != len(AcquisitionKinds) {
		t.Fatalf("lanes asked for = %+v, want the acquisition kinds", queue.lanes)
	}
}

// A worker with no lane is the one worker there was before there were lanes: it
// claims anything.
func TestAWorkerWithNoLaneAsksForEveryKind(t *testing.T) {
	queue, worker, ctx := laneWorker(t)
	worker.pollInterval = time.Hour

	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not stop when its context ended")
	}

	if len(queue.lanes) != 1 || len(queue.lanes[0].Only) != 0 || len(queue.lanes[0].Except) != 0 {
		t.Fatalf("lanes asked for = %+v, want no restriction", queue.lanes)
	}
}

// An empty queue is not the end of the work; the lane waits its interval and
// asks again.
func TestAnEmptyQueueIsAskedAgainAfterTheInterval(t *testing.T) {
	queue, worker, ctx := laneWorker(t, claim{err: pgx.ErrNoRows})
	worker.pollInterval = time.Millisecond

	runLane(t, worker, ctx, Lane{})

	if len(queue.lanes) < 2 {
		t.Fatalf("claims made = %d, want the queue asked again", len(queue.lanes))
	}
}

// A queue that could not be read is a database that will probably answer next
// time, so the lane keeps asking rather than ending.
func TestAQueueThatCouldNotBeReadIsAskedAgain(t *testing.T) {
	queue, worker, ctx := laneWorker(t, claim{err: errors.New("the database is unreachable")})
	worker.pollInterval = time.Millisecond

	runLane(t, worker, ctx, Lane{})

	if len(queue.lanes) < 2 {
		t.Fatalf("claims made = %d, want the queue asked again", len(queue.lanes))
	}
}

// A cancelled claim is the shutdown arriving, and the lane ends there rather
// than waiting out an interval nobody is left to care about.
func TestALaneEndsWhenTheClaimIsCancelled(t *testing.T) {
	queue, worker, ctx := laneWorker(t, claim{err: context.Canceled})
	worker.pollInterval = time.Hour

	runLane(t, worker, ctx, Lane{})

	if len(queue.lanes) != 1 {
		t.Fatalf("claims made = %d, want the lane to stop at the cancelled one", len(queue.lanes))
	}
}

func TestALaneRunsTheJobItClaimed(t *testing.T) {
	jobID := uuid.New()
	queue, worker, ctx := laneWorker(t, claim{job: Job{ID: jobID, Kind: ScanLibrary, MaxAttempts: 1}})
	worker.pollInterval = time.Hour

	runLane(t, worker, ctx, Lane{})

	if queue.completed != jobID {
		t.Fatalf("completed = %s, want %s", queue.completed, jobID)
	}
}

// A shutdown leaves jobs marked running that nobody is running. They are put
// back once, however many lanes start: a second pass would requeue a job the
// other lane had just claimed.
func TestInterruptedJobsAreRecoveredOnceHoweverManyLanesStart(t *testing.T) {
	queue, worker, ctx := laneWorker(t)
	worker.pollInterval = time.Hour

	runLane(t, worker, ctx, General())
	runLane(t, worker, ctx, Acquisition())

	if queue.recovered != 1 {
		t.Fatalf("recoveries = %d, want exactly one across both lanes", queue.recovered)
	}
}

// Jobs that could not be put back are worth saying and never worth refusing to
// work: what is queued is still queued.
func TestALaneWorksEvenWhenInterruptedJobsCouldNotBeRecovered(t *testing.T) {
	queue, worker, ctx := laneWorker(t)
	queue.recoverErr = errors.New("the database is unreachable")
	worker.pollInterval = time.Hour

	runLane(t, worker, ctx, Lane{})

	if len(queue.lanes) == 0 {
		t.Fatal("the lane claimed nothing after a recovery that failed")
	}
}

// The recovery pass only catches what a shutdown left behind. A row left
// running by a worker that stopped without one is put back by the same pass on
// a clock, so that a leak heals without waiting for somebody to restart the
// application.
func TestJobsLeftRunningWithNobodyOnThemArePutBack(t *testing.T) {
	queue := &fakeQueue{}
	queue.reapedRows = 2
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	worker.reapStalled(context.Background())

	if queue.reaped != 1 {
		t.Fatalf("sweeps = %d, want one", queue.reaped)
	}
	if queue.reapedFor != leaseExpiredAfter {
		t.Fatalf("expired lease = %s, want %s", queue.reapedFor, leaseExpiredAfter)
	}
}

// Reaping has its own loop. A lane occupied by one long scan must not postpone
// recovery until that scan returns.
func TestTheReaperRunsWhileALaneIsBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	jobID := uuid.New()
	queue := &fakeQueue{
		claims:       []claim{{job: Job{ID: jobID, Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1}}},
		stop:         cancel,
		reapedSignal: make(chan struct{}, 1),
	}
	started := make(chan struct{})
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop(), blockingScanner{started: started})
	worker.reapEvery = time.Millisecond
	worker.heartbeatEvery = time.Hour
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.RunLane(ctx, Lane{})
	}()

	<-started
	select {
	case <-queue.reapedSignal:
	case <-time.After(10 * time.Second):
		t.Fatal("the reaper did not run while the scan was still working")
	}
	cancel()
	<-done
}

// Duration is not evidence that ownership ended. A live scan renews the claim
// while it runs, however long the walk over the library takes.
func TestALongRunningJobRenewsItsLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	jobID := uuid.New()
	queue := &fakeQueue{
		claims:    []claim{{job: Job{ID: jobID, Kind: ScanLibrary, Attempts: 3, MaxAttempts: 3}}},
		stop:      cancel,
		heartbeat: make(chan struct{}, 1),
		owned:     true,
	}
	started := make(chan struct{})
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop(), blockingScanner{started: started})
	worker.heartbeatEvery = time.Millisecond
	worker.reapEvery = time.Hour
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.RunLane(ctx, Lane{})
	}()

	<-started
	select {
	case <-queue.heartbeat:
	case <-time.After(10 * time.Second):
		t.Fatal("the live scan did not renew its lease")
	}
	cancel()
	<-done
}

// A worker that can no longer renew its exact claim stops doing that work. The
// reaper or a later claim owns what happens next; this worker settles nothing.
func TestAWorkerStopsWhenItsClaimLeaseIsLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobID := uuid.New()
	queue := &fakeQueue{
		claims: []claim{{job: Job{ID: jobID, Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1}}},
		stop:   cancel,
		owned:  false,
	}
	started := make(chan struct{})
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop(), blockingScanner{started: started})
	worker.heartbeatEvery = time.Millisecond
	worker.reapEvery = time.Hour

	runLane(t, worker, ctx, Lane{})
	if queue.completed != uuid.Nil || queue.failed != uuid.Nil || queue.retried != uuid.Nil {
		t.Fatalf(
			"completed = %s failed = %s retried = %s, want the old claim to settle nothing",
			queue.completed, queue.failed, queue.retried,
		)
	}
}

// Both lanes start together in production, but one reaper serves them. Running
// this under -race also exercises the sync.Once boundary between them.
func TestOneReaperServesConcurrentLanes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &blockingReapQueue{fakeQueue: &fakeQueue{}, started: make(chan struct{})}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	worker.pollInterval = time.Millisecond
	worker.reapEvery = time.Millisecond

	var lanes sync.WaitGroup
	for _, lane := range []Lane{General(), Acquisition()} {
		lanes.Add(1)
		go func() {
			defer lanes.Done()
			worker.RunLane(ctx, lane)
		}()
	}
	select {
	case <-queue.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the shared reaper did not start")
	}
	cancel()
	lanes.Wait()
	if queue.calls != 1 {
		t.Fatalf("concurrent reapers = %d, want one", queue.calls)
	}
}

// A sweep that could not be made is worth saying and never worth refusing to
// work over: what is queued is still queued.
func TestALaneWorksEvenWhenStalledJobsCouldNotBePutBack(t *testing.T) {
	queue := &fakeQueue{}
	queue.reapErr = errors.New("the database is unreachable")
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())
	worker.reapStalled(context.Background())
	if queue.reaped != 1 {
		t.Fatalf("sweeps = %d, want the failed pass attempted once", queue.reaped)
	}
}

// A worker shutting down while backing off from a queue it could not read stops
// there, rather than holding the shutdown open for the rest of the interval.
func TestALaneStopsWithoutWaitingOutTheBackoff(t *testing.T) {
	queue, worker, ctx := laneWorker(t, claim{err: errors.New("the database is unreachable"), stop: true})
	worker.pollInterval = time.Hour

	runLane(t, worker, ctx, Lane{})

	if len(queue.lanes) != 1 {
		t.Fatalf("claims made = %d, want the lane to stop rather than ask again", len(queue.lanes))
	}
}

// The sweep itself ran; a schedule that could not be written is worth saying and
// not worth undoing it.
func TestASweepStandsWhenItsSuccessorCouldNotBeQueued(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{scheduleErr: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAcquisitionSweeper(&fakeSweeper{nextDue: time.Now().Add(time.Hour)})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAcquisitionTargets, Attempts: 1, MaxAttempts: 1,
	})

	if queue.completed != jobID || queue.failed != uuid.Nil {
		t.Fatalf("completed = %s failed = %s, want the sweep kept", queue.completed, queue.failed)
	}
}

// Recording the exhaustion against the request is a courtesy to whoever is
// watching it; the job itself still has to settle either way.
func TestADownloadImportSettlesEvenWhenItsExhaustionCannotBeRecorded(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"requestId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithDownloadImporter(&fakeImporter{
			err:     errors.New("the inbox is unavailable"),
			failErr: errors.New("the database is unreachable"),
		})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportDownload, Payload: payload, Attempts: 3, MaxAttempts: 3,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the job settled anyway", queue.failed)
	}
}

func TestAFileResolutionSettlesEvenWhenItsExhaustionCannotBeRecorded(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithFileResolver(&fakeResolver{
			err:     errors.New("the provider is unavailable"),
			failErr: errors.New("the database is unreachable"),
		})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: payload, Attempts: 3, MaxAttempts: 3,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the job settled anyway", queue.failed)
	}
}

func TestASourceSearchSettlesEvenWhenItsExhaustionCannotBeRecorded(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": uuid.New(), "albumId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithSourceSearcher(&fakeSearcher{
			err:     errors.New("slskd is unreachable"),
			failErr: errors.New("the database is unreachable"),
		})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SearchAlbumSources, Payload: payload, Attempts: 2, MaxAttempts: 2,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the job settled anyway", queue.failed)
	}
}

// fakePlayerSync answers for the playlist sync pass. It records what it was
// asked so a test can tell a pass over one list from a look at the whole
// player, and answers with a result rather than nothing, because everything a
// pass could not do is an answer.
type fakePlayerSync struct {
	synced   uuid.UUID
	swept    bool
	result   navidrome.SyncResult
	sweep    navidrome.SweepResult
	err      error
	sweepErr error
}

func (fake *fakePlayerSync) SyncPlaylist(_ context.Context, playlistID uuid.UUID) (navidrome.SyncResult, error) {
	fake.synced = playlistID
	return fake.result, fake.err
}

func (fake *fakePlayerSync) Sweep(context.Context) (navidrome.SweepResult, error) {
	fake.swept = true
	return fake.sweep, fake.sweepErr
}

// The sweep is the only pass over the player that nothing else asks for, so it
// has to leave a successor behind it. Without one it runs once at startup and
// the player never hears about anything acquired afterwards.
func TestASweepOverThePlayerSchedulesTheNextOne(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistSyncer(&fakePlayerSync{})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SyncNavidromePlaylists, Payload: []byte(`{}`),
		Attempts: 1, MaxAttempts: 3,
	})

	if !queue.playerSweepQueuedFor.Equal(now.Add(playerSyncIdle)) {
		t.Fatalf("next sweep = %s, want one in %s", queue.playerSweepQueuedFor, playerSyncIdle)
	}
}

// A sweep that ran out of attempts must still leave a successor, or a player
// that was down when Schall started is never spoken to again until a restart.
func TestAnExhaustedSweepOverThePlayerStillLeavesASuccessor(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistSyncer(&fakePlayerSync{sweepErr: errors.New("navidrome did not answer")})
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SyncNavidromePlaylists, Payload: []byte(`{}`),
		Attempts: 3, MaxAttempts: 3,
	})

	if !queue.playerSweepQueuedFor.Equal(now.Add(playerSyncIdle)) {
		t.Fatalf("next sweep = %s, want the player still scheduled", queue.playerSweepQueuedFor)
	}
}

// A pass over one list is asked for by an import or by a press, and both ask
// again when there is reason to. Scheduling one would be a second timer.
func TestAPassOverOneListSchedulesNothing(t *testing.T) {
	playlistID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": playlistID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistSyncer(&fakePlayerSync{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SyncNavidromePlaylists, Payload: payload,
		Attempts: 1, MaxAttempts: 3,
	})

	if !queue.playerSweepQueuedFor.IsZero() {
		t.Fatalf("next sweep = %s, want a pass over one list to schedule nothing",
			queue.playerSweepQueuedFor)
	}
}

func TestWorkerSyncsOnePlaylistWithThePlayer(t *testing.T) {
	jobID, playlistID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": playlistID})
	queue := &fakeQueue{}
	syncer := &fakePlayerSync{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithPlaylistSyncer(syncer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SyncNavidromePlaylists, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if syncer.synced != playlistID || syncer.swept {
		t.Fatalf("synced = %s swept = %v, want the named list", syncer.synced, syncer.swept)
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the job settled", queue.completed)
	}
}

// No playlist named is the sweep: adopt what the player holds, then ask for a
// pass over every list.
func TestASyncJobWithNoPlaylistLooksAtTheWholePlayer(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	syncer := &fakePlayerSync{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithPlaylistSyncer(syncer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SyncNavidromePlaylists, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if !syncer.swept || syncer.synced != uuid.Nil {
		t.Fatalf("swept = %v synced = %s, want the sweep", syncer.swept, syncer.synced)
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the job settled", queue.completed)
	}
}

// A player that stumbled is worth asking again; the want list and the library
// are untouched either way.
func TestASyncThatFailedIsAskedAgain(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistSyncer(&fakePlayerSync{err: errors.New("navidrome is not ready")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SyncNavidromePlaylists, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want the pass asked again", queue.retried)
	}
}

// A pass that recognised nothing is refused rather than retried: a setting, a
// scan or a path map is what fixes it, and none of them happens in five seconds.
func TestASyncThatPairedNothingIsNotAskedAgain(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistSyncer(&fakePlayerSync{err: navidrome.ErrNothingPaired})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SyncNavidromePlaylists, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a refusal", queue.failed, queue.retried)
	}
}

// A sync that failed is its own business: nothing else in the queue is settled
// differently because of it.
func TestASyncThatFailedSettlesNothingElse(t *testing.T) {
	jobID, otherID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithPlaylistSyncer(&fakePlayerSync{err: errors.New("navidrome is not ready")}).
		WithPlaylistImporter(&fakePlaylists{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SyncNavidromePlaylists, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})
	worker.process(context.Background(), Job{
		ID: otherID, Kind: ImportPlaylist, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != otherID {
		t.Fatalf("completed = %s, want the next job unaffected", queue.completed)
	}
}

func TestASyncIsRefusedWithNothingConfiguredToRunIt(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SyncNavidromePlaylists, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// The playlist view shows what a pass changed, so it is told when one settles.
func TestWorkerAnnouncesAFinishedSync(t *testing.T) {
	payload, _ := json.Marshal(map[string]uuid.UUID{"playlistId": uuid.New()})
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).
		WithPlaylistSyncer(&fakePlayerSync{}).
		WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SyncNavidromePlaylists, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if got := announced(notices); len(got) != 1 || got[0] != events.TopicPlaylists {
		t.Fatalf("notices = %v, want the playlists topic", got)
	}
}

// Syncing is not searching, so it belongs on the lane that does the work rather
// than the one that waits on peers.
func TestSyncingBelongsOnTheGeneralLane(t *testing.T) {
	for _, kind := range AcquisitionKinds {
		if kind == SyncNavidromePlaylists {
			t.Fatal("playlist sync was put on the acquisition lane")
		}
	}
}

// fakeTagger stands in for the pass that writes the catalogue's account into
// the files the library already holds. Tag reports whether files are still
// waiting, which is the whole contract: it is what makes a library's worth of
// file copies a sequence of batches rather than one job holding the lane.
type fakeTagger struct {
	more    bool
	written bool
	err     error
	tagged  []uuid.UUID
	queued  []uuid.UUID
	files   []uuid.UUID
	// swept is when QueueLibrarySweep was asked for the next weekly pass.
	swept []time.Time
}

func (tagger *fakeTagger) Tag(_ context.Context, jobID uuid.UUID) (bool, error) {
	tagger.tagged = append(tagger.tagged, jobID)
	return tagger.more, tagger.err
}

func (tagger *fakeTagger) QueueNextTagging(_ context.Context, jobID uuid.UUID) error {
	tagger.queued = append(tagger.queued, jobID)
	return nil
}

func (tagger *fakeTagger) TagFile(_ context.Context, fileID uuid.UUID) (bool, error) {
	tagger.files = append(tagger.files, fileID)
	return tagger.written, tagger.err
}

func (tagger *fakeTagger) QueueLibrarySweep(_ context.Context, at time.Time) error {
	tagger.swept = append(tagger.swept, at)
	return nil
}

// A batch that leaves files waiting comes straight back for them, carrying the
// pass on from the job that reached that far.
func TestWorkerQueuesTheRestOfAPassOverTheTags(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	tagger := &fakeTagger{more: true}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithTagger(tagger)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: TagLibrary, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 5,
	})

	if queue.completed != jobID {
		t.Fatalf("completed = %s, want %s", queue.completed, jobID)
	}
	if len(tagger.queued) != 1 || tagger.queued[0] != jobID {
		t.Fatalf("queued = %v, want the pass carried on", tagger.queued)
	}
	if len(tagger.swept) != 0 {
		t.Fatalf("swept = %v, want the weekly sweep left alone while the run continues", tagger.swept)
	}
}

// A pass that reached the end of the library stops, rather than queueing a job
// to discover that there is nothing left.
func TestWorkerStopsWhenAPassOverTheTagsIsDone(t *testing.T) {
	tagger := &fakeTagger{}
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).WithTagger(tagger)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: TagLibrary, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 5,
	})

	if len(tagger.queued) != 0 {
		t.Fatalf("queued = %v, want nothing", tagger.queued)
	}
}

// A run that reaches the end of the library asks for the next one a week out,
// which is what makes this recurring rather than a one-off the button starts.
func TestWorkerSchedulesTheNextWeeklyPassWhenThisOneFinishes(t *testing.T) {
	tagger := &fakeTagger{}
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).WithTagger(tagger)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: TagLibrary, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 5,
	})

	if len(tagger.swept) != 1 || !tagger.swept[0].Equal(now.Add(tagLibraryIdle)) {
		t.Fatalf("swept = %v, want one pass a week from %s", tagger.swept, now)
	}
}

// The files say something else now, and a finished pass over the whole library
// is worth telling the player about once rather than file by file.
func TestWorkerTellsThePlayerWhenAPassOverTheTagsFinishes(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithTagger(&fakeTagger{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: TagLibrary, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 5,
	})

	if queue.notifyPlayerQueued != 1 {
		t.Fatalf("notify queued = %d, want the player told once", queue.notifyPlayerQueued)
	}
}

// A batch that could not be worked at all is retried: the pass records where it
// reached as it goes, so the retry costs nothing already done.
func TestWorkerRetriesAPassOverTheTagsItCouldNotWork(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithTagger(&fakeTagger{err: errors.New("the database went away")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: TagLibrary, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 5,
	})

	if queue.retried != jobID || queue.failed != uuid.Nil {
		t.Fatalf("retried = %s failed = %s, want a retry", queue.retried, queue.failed)
	}
}

// A pass nothing is configured to work is refused rather than left looking as
// though it were under way.
func TestAPassOverTheTagsIsRefusedWithNoTagger(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: TagLibrary, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 5,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// A file matched between two passes is written where it is, without anybody
// pressing the button that covers the library.
func TestWorkerWritesTheTagsOfOneFile(t *testing.T) {
	fileID := uuid.New()
	tagger := &fakeTagger{}
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).WithTagger(tagger)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: TagLibraryFile, Attempts: 1, MaxAttempts: 3,
		Payload: []byte(`{"fileId":"` + fileID.String() + `"}`),
	})

	if len(tagger.files) != 1 || tagger.files[0] != fileID {
		t.Fatalf("files = %v, want the file the job names", tagger.files)
	}
}

// One file's write is not a pass: nothing waits behind it, so a write that
// failed is worth attempting again rather than moved past.
func TestWorkerRetriesAFileWhoseTagsItCouldNotWrite(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithTagger(&fakeTagger{err: errors.New("the database went away")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: TagLibraryFile, Attempts: 1, MaxAttempts: 3,
		Payload: []byte(`{"fileId":"` + uuid.New().String() + `"}`),
	})

	if queue.retried != jobID || queue.failed != uuid.Nil {
		t.Fatalf("retried = %s failed = %s, want a retry", queue.retried, queue.failed)
	}
}

// A job naming no file names nothing a retry could find, so it is refused for
// good rather than asked again three times.
func TestWorkerRefusesAFileTaggingJobNamingNoFile(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithTagger(&fakeTagger{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: TagLibraryFile, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// The player groups by what the files say, so a file that now says something
// else is worth telling it about.
func TestWorkerQueuesToTellThePlayerAboutAFileItRewrote(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithTagger(&fakeTagger{written: true})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: TagLibraryFile, Attempts: 1, MaxAttempts: 3,
		Payload: []byte(`{"fileId":"` + uuid.New().String() + `"}`),
	})

	if queue.notifyPlayerQueued != 1 {
		t.Fatalf("notify queued = %d, want the player told once", queue.notifyPlayerQueued)
	}
}

// A file that already said what the catalogue says is not a change, and telling
// the player about every match that wrote nothing would be a rescan a minute.
func TestWorkerLeavesThePlayerAloneOverAFileItDidNotRewrite(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithTagger(&fakeTagger{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: TagLibraryFile, Attempts: 1, MaxAttempts: 3,
		Payload: []byte(`{"fileId":"` + uuid.New().String() + `"}`),
	})

	if queue.notifyPlayerQueued != 0 {
		t.Fatalf("notify queued = %d, want the player left alone", queue.notifyPlayerQueued)
	}
}

// Writing tags is work Schall does rather than a wait on somebody else, so it
// belongs on the lane that does the work.
func TestAPassOverTheTagsBelongsOnTheGeneralLane(t *testing.T) {
	for _, kind := range AcquisitionKinds {
		if kind == TagLibrary {
			t.Fatal("writing the library's tags was put on the acquisition lane")
		}
	}
}

// fakeMover stands in for the component that renames the library. Apply reports
// whether the run still has files waiting, which is the whole contract: it is
// what makes a migration of a large library a sequence of batches rather than
// one job holding the lane.
type fakeMover struct {
	more    bool
	err     error
	applied []uuid.UUID
	queued  []uuid.UUID
}

func (mover *fakeMover) Apply(_ context.Context, runID uuid.UUID) (bool, error) {
	mover.applied = append(mover.applied, runID)
	return mover.more, mover.err
}

func (mover *fakeMover) QueueApply(_ context.Context, runID uuid.UUID) error {
	mover.queued = append(mover.queued, runID)
	return nil
}

// A batch that leaves files waiting comes straight back for them, so the run
// continues at full speed without holding the lane for tens of thousands of
// renames.
func TestWorkerQueuesTheRestOfALayoutMigration(t *testing.T) {
	jobID, runID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID})
	queue := &fakeQueue{}
	mover := &fakeMover{more: true}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithLayoutMover(mover)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ApplyLibraryLayout, Payload: payload, Attempts: 1, MaxAttempts: 5,
	})

	if queue.completed != jobID {
		t.Fatalf("completed = %s, want %s", queue.completed, jobID)
	}
	if len(mover.queued) != 1 || mover.queued[0] != runID {
		t.Fatalf("queued = %v, want the same run again", mover.queued)
	}
}

// A run with nothing left stops, rather than queueing a job to discover that
// there is nothing left.
func TestWorkerStopsWhenALayoutMigrationIsDone(t *testing.T) {
	runID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID})
	mover := &fakeMover{}
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).WithLayoutMover(mover)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ApplyLibraryLayout, Payload: payload, Attempts: 1, MaxAttempts: 5,
	})

	if len(mover.queued) != 0 {
		t.Fatalf("queued = %v, want nothing", mover.queued)
	}
}

// The files a finished migration moved are somewhere else now, so the player
// is told once the whole run is done rather than after each batch.
func TestWorkerTellsThePlayerWhenALayoutMigrationFinishes(t *testing.T) {
	runID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": runID})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithLayoutMover(&fakeMover{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ApplyLibraryLayout, Payload: payload, Attempts: 1, MaxAttempts: 5,
	})

	if queue.notifyPlayerQueued != 1 {
		t.Fatalf("notify queued = %d, want the player told once", queue.notifyPlayerQueued)
	}
}

// A batch that could not be worked at all is retried: the journal says which
// files it did not reach, so the retry costs nothing already done.
func TestWorkerRetriesALayoutMigrationItCouldNotWork(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithLayoutMover(&fakeMover{err: errors.New("the database went away")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ApplyLibraryLayout, Payload: payload, Attempts: 1, MaxAttempts: 5,
	})

	if queue.retried != jobID || queue.failed != uuid.Nil {
		t.Fatalf("retried = %s failed = %s, want a retry", queue.retried, queue.failed)
	}
}

// A migration named by nothing is not a migration, and no amount of retrying
// makes a payload readable.
func TestWorkerRefusesALayoutMigrationWithoutARun(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithLayoutMover(&fakeMover{})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ApplyLibraryLayout, Payload: []byte(`{}`), Attempts: 1, MaxAttempts: 5,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// Renaming is work Schall does rather than a wait on somebody else, so it
// belongs on the lane that does the work.
func TestALayoutMigrationBelongsOnTheGeneralLane(t *testing.T) {
	for _, kind := range AcquisitionKinds {
		if kind == ApplyLibraryLayout {
			t.Fatal("the layout migration was put on the acquisition lane")
		}
	}
}

// The general lane is claimed by exclusion (General() excepts AcquisitionKinds
// and TransferKinds), so a job kind left off both lists is claimable there
// without anybody having to remember to add it. Telling the player is not a wait on somebody else, and a
// notify_player row stuck on neither lane would sit queued forever.
func TestNotifyPlayerBelongsOnTheGeneralLane(t *testing.T) {
	for _, kind := range AcquisitionKinds {
		if kind == NotifyPlayer {
			t.Fatal("telling the player was put on the acquisition lane")
		}
	}
}

// Every kind on a named lane has to be excluded from the general one, or it is
// claimable on both and two workers run the same work at once.
func TestTheGeneralLaneExcludesEveryNamedLanesKinds(t *testing.T) {
	general := General()
	for _, kind := range slices.Concat(AcquisitionKinds, TransferKinds, AnchorKinds, JudgingKinds) {
		if !slices.Contains(general.Except, kind) {
			t.Errorf("%q is on a named lane and still claimable on the general one", kind)
		}
	}
}

func TestNamedLaneKindsArePairwiseDisjoint(t *testing.T) {
	lanes := []struct {
		name  string
		kinds []string
	}{
		{name: "acquisition", kinds: AcquisitionKinds},
		{name: "transfers", kinds: TransferKinds},
		{name: "anchors", kinds: AnchorKinds},
		{name: "judging", kinds: JudgingKinds},
	}
	for i, lane := range lanes {
		for _, other := range lanes[i+1:] {
			for _, kind := range lane.kinds {
				if slices.Contains(other.kinds, kind) {
					t.Errorf("%q is in both %s and %s", kind, lane.name, other.name)
				}
			}
		}
	}
}

// A run nothing is configured to work is refused rather than left looking as
// though it were under way.
func TestALayoutMigrationIsRefusedWithNoMover(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"runId": uuid.New()})
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ApplyLibraryLayout, Payload: payload, Attempts: 1, MaxAttempts: 5,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent refusal", queue.failed, queue.retried)
	}
}

// fakeWeeklyRefresher stands in for the weekly playlist.
type fakeWeeklyRefresher struct {
	result weekly.RunResult
	err    error
	ran    int
}

func (refresher *fakeWeeklyRefresher) Refresh(context.Context) (weekly.RunResult, error) {
	refresher.ran++
	return refresher.result, refresher.err
}

func TestAWeeklyPlaylistRefreshAsksForTheNextOneAWeekOut(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	refresher := &fakeWeeklyRefresher{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithWeeklyPlaylistRefresher(refresher)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshWeeklyPlaylist, Attempts: 1, MaxAttempts: 1,
	})

	if refresher.ran != 1 {
		t.Fatalf("the refresh ran %d times, want 1", refresher.ran)
	}
	if queue.completed != jobID || !queue.weeklyRefreshQueuedFor.Equal(now.Add(weeklyIdle)) {
		t.Fatalf("completed = %s, next refresh = %s, want one in %s",
			queue.completed, queue.weeklyRefreshQueuedFor, weeklyIdle)
	}
}

// A refresh that could not be accounted for still leaves a successor. A feature
// that stops asking is a feature that has silently switched itself off, and
// nobody would see that until a month of music had piled up.
func TestAWeeklyPlaylistRefreshThatFailedStillAsksForTheNextOne(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	refresher := &fakeWeeklyRefresher{err: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithWeeklyPlaylistRefresher(refresher)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshWeeklyPlaylist, Attempts: 1, MaxAttempts: 1,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the job to say it did not run", queue.failed)
	}
	if !queue.weeklyRefreshQueuedFor.Equal(now.Add(weeklyIdle)) {
		t.Fatalf("next refresh = %s, want one in %s", queue.weeklyRefreshQueuedFor, weeklyIdle)
	}
}

func TestAWeeklyPlaylistRefreshIsRefusedWithNothingConfiguredToRunIt(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshWeeklyPlaylist, Attempts: 1, MaxAttempts: 1,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil ||
		!strings.Contains(queue.message, "not configured") {
		t.Fatalf("failed = %s retried = %s message = %q, want a permanent refusal",
			queue.failed, queue.retried, queue.message)
	}
}

// fakeNewReleasesRefresher stands in for the new-releases playlist.
type fakeNewReleasesRefresher struct {
	result newreleases.RefreshResult
	err    error
	ran    int
}

func (refresher *fakeNewReleasesRefresher) Refresh(context.Context) (newreleases.RefreshResult, error) {
	refresher.ran++
	return refresher.result, refresher.err
}

func TestANewReleasesPlaylistRefreshCompletesTheJobAndAnnouncesTheChange(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	refresher := &fakeNewReleasesRefresher{result: newreleases.RefreshResult{Added: 2}}
	hub := events.NewHub()
	sub, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithNewReleasesPlaylistRefresher(refresher).
		WithEvents(hub)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshNewReleasesPlaylist, Attempts: 1, MaxAttempts: 1,
	})

	if refresher.ran != 1 {
		t.Fatalf("the refresh ran %d times, want 1", refresher.ran)
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the job completed", queue.completed)
	}
	select {
	case topic := <-sub:
		if topic != events.TopicPlaylists {
			t.Fatalf("announced %q, want %q", topic, events.TopicPlaylists)
		}
	default:
		t.Fatal("a refresh that changed the list announced nothing")
	}
}

// A refresh that failed is not retried: nothing it removes cannot be worked
// out again, and the next feed sweep or scan asks for another one within
// hours — a retry loop here would just be a second clock for the same wait.
func TestANewReleasesPlaylistRefreshThatFailedIsNotRetried(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	refresher := &fakeNewReleasesRefresher{err: errors.New("the database is unreachable")}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithNewReleasesPlaylistRefresher(refresher)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshNewReleasesPlaylist, Attempts: 1, MaxAttempts: 1,
	})

	if queue.failed != jobID {
		t.Fatalf("failed = %s, want the job to say it did not run", queue.failed)
	}
	if queue.retried != uuid.Nil {
		t.Fatalf("retried = %s, want no retry", queue.retried)
	}
}

func TestANewReleasesPlaylistRefreshIsRefusedWithNothingConfiguredToRunIt(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshNewReleasesPlaylist, Attempts: 1, MaxAttempts: 1,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil ||
		!strings.Contains(queue.message, "not configured") {
		t.Fatalf("failed = %s retried = %s message = %q, want a permanent refusal",
			queue.failed, queue.retried, queue.message)
	}
}

// A follow feed pass is the moment the window a refresh draws can have moved:
// it just decided which releases are new. Every completed pass asks for a
// refresh, whether or not it found anything, because a pass that found nothing
// still means candidates already on the list may have aged out.
func TestAFollowFeedPassAsksForANewReleasesRefresh(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithFollowFeedSweeper(&fakeFeed{}).
		WithNewReleasesPlaylistRefresher(&fakeNewReleasesRefresher{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepFollowFeed, Attempts: 1, MaxAttempts: 3,
	})

	if queue.newReleasesRefreshQueuedCalls != 1 {
		t.Fatalf("new-releases refresh queued %d times, want 1", queue.newReleasesRefreshQueuedCalls)
	}
}

// A scan is how a copy fetched for a want becomes a library file, which is the
// only thing the new-releases playlist can add: a refresh is asked for exactly
// when a scan found something to say.
func TestAScanThatFoundNewFilesAsksForANewReleasesRefresh(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 43, Updated: 3, Unchanged: 40}},
	).WithNewReleasesPlaylistRefresher(&fakeNewReleasesRefresher{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.newReleasesRefreshQueuedCalls != 1 {
		t.Fatalf("new-releases refresh queued %d times, want 1", queue.newReleasesRefreshQueuedCalls)
	}
}

// A scan that changed nothing has nothing the new-releases playlist would
// ever show, and asking for a refresh over it would be a refresh on a timer in
// disguise.
func TestAScanThatChangedNothingAsksForNoNewReleasesRefresh(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(
		queue, fakeProvider{}, zerolog.Nop(),
		fakeScanner{result: library.ScanResult{Discovered: 40, Unchanged: 40}},
	).WithNewReleasesPlaylistRefresher(&fakeNewReleasesRefresher{})

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: ScanLibrary, Attempts: 1, MaxAttempts: 1,
	})

	if queue.newReleasesRefreshQueuedCalls != 0 {
		t.Fatalf("new-releases refresh queued %d times, want 0", queue.newReleasesRefreshQueuedCalls)
	}
}

// The lyrics sweep, which walks the library writing the words of each song
// beside it. Nothing in the database records which files have been asked about,
// so the pass carries its own position — and these pin that it does.

type fakeLyricsSweeper struct {
	result lyrics.Result
	err    error
	from   lyrics.Position
	worded []uuid.UUID
}

func (sweeper *fakeLyricsSweeper) Sweep(
	_ context.Context, from lyrics.Position,
) (lyrics.Result, error) {
	sweeper.from = from
	return sweeper.result, sweeper.err
}

func (sweeper *fakeLyricsSweeper) ForFile(_ context.Context, fileID uuid.UUID) {
	sweeper.worded = append(sweeper.worded, fileID)
}

func TestALyricsPassStartsWhereTheLastOneStopped(t *testing.T) {
	stopped := uuid.New()
	payload, _ := json.Marshal(lyrics.Position{AfterFileID: stopped})
	sweeper := &fakeLyricsSweeper{}
	worker := NewWorker(&fakeQueue{}, fakeProvider{}, zerolog.Nop()).WithLyrics(sweeper)

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepLyrics, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if sweeper.from.AfterFileID != stopped {
		t.Fatalf("started at %v; want the file the last pass stopped on", sweeper.from.AfterFileID)
	}
}

func TestALyricsPassWithMoreWaitingComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	next := lyrics.Position{AfterFileID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithLyrics(&fakeLyricsSweeper{result: lyrics.Result{More: true, Next: next}})
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepLyrics, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.lyricsSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s; want one due now",
			queue.completed, queue.lyricsSweepQueuedFor)
	}
	var queued lyrics.Position
	if err := json.Unmarshal(queue.lyricsSweepPayload, &queued); err != nil || queued != next {
		t.Fatalf("queued position = %#v, %v; want %#v", queued, err, next)
	}
}

func TestALyricsPassThatWalkedTheWholeLibraryWaits(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithLyrics(&fakeLyricsSweeper{result: lyrics.Result{}})
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepLyrics, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.lyricsSweepQueuedFor.Equal(now.Add(lyricsIdle)) {
		t.Fatalf("next pass = %s; want one in %s", queue.lyricsSweepQueuedFor, lyricsIdle)
	}
}

type fakeSpectrumSweeper struct {
	more bool
	err  error
}

func (sweeper *fakeSpectrumSweeper) Sweep(context.Context) (bool, error) {
	return sweeper.more, sweeper.err
}

// The pass that measures what the library's audio is. It walks the whole
// collection once and then only what a scan has re-read, so a batch that filled
// itself comes straight back and one that did not waits.
func TestASpectrumPassWithMoreWaitingComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithSpectrumSweeper(&fakeSpectrumSweeper{more: true})
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAudioSpectrum, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.spectrumSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s; want one due now",
			queue.completed, queue.spectrumSweepQueuedFor)
	}
}

func TestASpectrumPassWithNothingLeftWaits(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithSpectrumSweeper(&fakeSpectrumSweeper{})
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepAudioSpectrum, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.spectrumSweepQueuedFor.Equal(now.Add(spectrumIdle)) {
		t.Fatalf("next pass = %s; want one in %s", queue.spectrumSweepQueuedFor, spectrumIdle)
	}
}

// A machine with no decoder is not a broken installation. spectrum.Service
// absorbs "ffmpeg is missing" itself and reports it as a clean pass with
// nothing done — Sweep returns (false, nil), never an error — so the worker
// never sees a failure to retry here. The job completes on its last attempt
// exactly as it would have on its first, and the next pass is scheduled the
// same as one that simply found nothing left to measure.
func TestASpectrumPassThatFindsNoDecoderCompletesCleanly(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithSpectrumSweeper(&fakeSpectrumSweeper{})
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepAudioSpectrum, Attempts: 3, MaxAttempts: 3,
	})

	if queue.failed == jobID {
		t.Fatal("a pass with no decoder was recorded as failed")
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the pass to finish clean", queue.completed)
	}
	if !queue.spectrumSweepQueuedFor.Equal(now.Add(spectrumIdle)) {
		t.Fatalf("next pass = %s; want one in %s", queue.spectrumSweepQueuedFor, spectrumIdle)
	}
}

type fakeAnchorSweeper struct {
	more bool
	err  error
	runs int
}

func (sweeper *fakeAnchorSweeper) Sweep(context.Context) (bool, error) {
	sweeper.runs++
	return sweeper.more, sweeper.err
}

func TestAnAnchorPassWithMoreWaitingComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAnchorSweeper(&fakeAnchorSweeper{more: true})
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepPreviewAnchors, Attempts: 1, MaxAttempts: 3,
	})

	if queue.completed != jobID || !queue.anchorSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s; want one due now",
			queue.completed, queue.anchorSweepQueuedFor)
	}
}

func TestAnAnchorPassWithNothingLeftWaits(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAnchorSweeper(&fakeAnchorSweeper{})
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepPreviewAnchors, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.anchorSweepQueuedFor.Equal(now.Add(anchorIdle)) {
		t.Fatalf("next pass = %s; want one in %s", queue.anchorSweepQueuedFor, anchorIdle)
	}
}

// A pass that spent its last attempt still queues its replacement, far enough
// out that a service in difficulty is not asked again in a few minutes. Without
// this the sweep would silently switch itself off after one bad afternoon.
func TestAnAnchorPassThatSpentItsAttemptsStillComesBack(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithAnchorSweeper(&fakeAnchorSweeper{err: errors.New("could not reach Deezer")})
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepPreviewAnchors, Attempts: 3, MaxAttempts: 3,
	})

	if !queue.anchorSweepQueuedFor.Equal(now.Add(anchorUnreachable)) {
		t.Fatalf("next pass = %s; want one in %s", queue.anchorSweepQueuedFor, anchorUnreachable)
	}
}

// An installation with no anchor sweeper wired is not a broken one — it is what
// every installation was before previews existed — so the job says so and stops
// rather than being retried into the failed list.
func TestAnAnchorPassWithNothingWiredDoesNotRetry(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepPreviewAnchors, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed == uuid.Nil {
		t.Fatal("a pass with nothing to sweep with was not recorded as failed")
	}
	if !queue.anchorSweepQueuedFor.IsZero() {
		t.Fatal("a pass with nothing to sweep with queued another one")
	}
}

// A pass that gave up because LRCLIB stopped answering keeps the position it
// reached, so nothing is walked twice, and waits before asking again.
func TestALyricsPassThatGaveUpKeepsItsPlace(t *testing.T) {
	queue := &fakeQueue{}
	reached := lyrics.Position{AfterFileID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithLyrics(&fakeLyricsSweeper{
			result: lyrics.Result{Next: reached},
			err:    errors.New("lrclib is down"),
		})
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepLyrics, Attempts: 3, MaxAttempts: 3,
	})

	if !queue.lyricsSweepQueuedFor.Equal(now.Add(lyricsUnreachable)) {
		t.Fatalf("next pass = %s; want one in %s", queue.lyricsSweepQueuedFor, lyricsUnreachable)
	}
	var queued lyrics.Position
	if err := json.Unmarshal(queue.lyricsSweepPayload, &queued); err != nil || queued != reached {
		t.Fatalf("queued position = %#v, %v; want %#v", queued, err, reached)
	}
}

// AcoustID is the service that identifies audio by fingerprint. Asked too fast
// it answers 429, which means "ask again later" and says nothing at all about
// the file. The ordinary retry ladder starts at five seconds, which is inside
// the window AcoustID is throttling, so every try collected the same refusal.
func TestWorkerBacksOffWhenAcoustIDAsksForLess(t *testing.T) {
	jobID, fileID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": fileID})
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	queue := &fakeQueue{}
	resolver := &fakeResolver{
		err: fmt.Errorf("identify %s: %w", fileID, acoustid.ErrRateLimited),
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithFileResolver(resolver)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID {
		t.Fatalf("retried = %s, want the job queued again", queue.retried)
	}
	if waited := queue.runAfter.Sub(now); waited != rateLimitBackoff {
		t.Fatalf("backed off %s, want %s", waited, rateLimitBackoff)
	}
}

// And once the attempts are spent on the same throttle, nothing is written
// against the file. What was learned about this audio is nothing, and the
// identity panel would otherwise show a fact about the afternoon as the file's
// own answer, to a person who can do nothing about it. The file stays unasked,
// which is what the next scan picks up.
func TestWorkerRecordsNoThrottleAgainstTheFile(t *testing.T) {
	jobID, fileID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"fileId": fileID})
	queue := &fakeQueue{}
	resolver := &fakeResolver{
		err: fmt.Errorf("identify %s: %w", fileID, acoustid.ErrRateLimited),
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithFileResolver(resolver)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ResolveLibraryFile, Payload: payload, Attempts: 3, MaxAttempts: 3,
	})

	if resolver.failed != uuid.Nil {
		t.Fatalf("the throttle was recorded against file %s", resolver.failed)
	}
	// The job itself still says what stopped it, for anybody reading the queue.
	if queue.failed != jobID || !strings.Contains(queue.message, "rate limiting") {
		t.Fatalf("job failed = %s, message = %q", queue.failed, queue.message)
	}
}

// A pass over the copies already decided about finishes and asks for nothing
// more. Every other pass here queues its own replacement; this one is asked for
// by a person, because the rules it re-runs change rarely and a pass on a timer
// would keep re-deciding copies nothing had changed for.
func TestAPassOverDecidedCopiesQueuesNoReplacement(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	importer := &fakeImporter{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: JudgeCopiesAgain, Attempts: 1, MaxAttempts: 1,
	})

	if importer.judgedAgain != 1 {
		t.Fatalf("passes = %d, want the one that was asked for", importer.judgedAgain)
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the pass settled", queue.completed)
	}
	if !queue.anchorSweepQueuedFor.IsZero() {
		t.Errorf("a replacement was queued for %s, want none", queue.anchorSweepQueuedFor)
	}
}

// A pass that could not run is retried rather than recorded as done. It touches
// the decisions Schall has already made, so half a pass is a state worth coming
// back from rather than one to write down as finished.
func TestAPassOverDecidedCopiesThatFailedIsRetried(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithDownloadImporter(&fakeImporter{judgeAgainErr: errors.New("the inbox is unavailable")})

	worker.process(context.Background(), Job{
		ID: jobID, Kind: JudgeCopiesAgain, Attempts: 1, MaxAttempts: 3,
	})

	if queue.retried != jobID || queue.completed == jobID {
		t.Fatalf("retried = %s completed = %s, want the pass retried", queue.retried, queue.completed)
	}
}

// An import job takes one finished transfer into the library. It begins by
// claiming the download request, and the claim only takes a request whose
// import is still waiting to be done. A request whose import was paused for
// review, or one that has been deleted, has nothing left for the job to take.
//
// That is not a fault of the job. The request already carries the reason it
// stopped, and the person is offered the action that belongs to it, so the job
// finishes quietly rather than failing — and it must not write a new reason
// over the one already there.
func TestAnImportOfSomethingAlreadyDecidedFinishesInsteadOfFailing(t *testing.T) {
	payload, _ := json.Marshal(map[string]uuid.UUID{"requestId": uuid.New()})

	for _, settled := range []error{db.ErrImportAlreadyDecided, db.ErrDownloadRequestGone} {
		jobID := uuid.New()
		queue := &fakeQueue{}
		importer := &fakeImporter{err: fmt.Errorf("import download: %w", settled)}
		worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)

		worker.process(context.Background(), Job{
			ID: jobID, Kind: ImportDownload, Payload: payload, Attempts: 3, MaxAttempts: 3,
		})

		if queue.completed != jobID || queue.failed != uuid.Nil {
			t.Errorf("%v: completed = %s, failed = %s, want the job finished",
				settled, queue.completed, queue.failed)
		}
		if importer.failed != uuid.Nil {
			t.Errorf("%v: the reason on the request was written over with %q",
				settled, importer.detail)
		}
	}
}

// A transfer that has not finished is not a decision about the import. The
// poller writes what it finds a second later, so the request can become
// importable without anybody touching it, and the job that waits and asks again
// is the one that imports it. Finishing the job here would leave the music
// downloaded and never filed.
func TestAnImportOfSomethingNotReadyYetKeepsItsAttempts(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"requestId": uuid.New()})
	queue := &fakeQueue{}
	importer := &fakeImporter{
		err: fmt.Errorf("import download: %w", db.ErrImportNotReadyYet),
	}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: ImportDownload, Payload: payload, Attempts: 1, MaxAttempts: 6,
	})

	if queue.retried != jobID || queue.completed != uuid.Nil {
		t.Fatalf("retried = %s, completed = %s, want the job asked again",
			queue.retried, queue.completed)
	}
}

// The backfill measures the audio of the held copies that have none on record,
// so the review queue can show the copies that are one piece of audio as one
// row. A pass that filled its batch comes straight back, because there are more
// copies waiting and nothing about the pace is set here.
func TestAFingerprintPassWithMoreWaitingComesStraightBack(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	importer := &fakeImporter{moreToMeasure: true}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: jobID, Kind: SweepCopyFingerprints, Attempts: 1, MaxAttempts: 3,
	})

	if importer.fingerprinted != 1 {
		t.Fatalf("passes = %d, want one", importer.fingerprinted)
	}
	if queue.completed != jobID || !queue.copyFingerprintSweepQueuedFor.Equal(now) {
		t.Fatalf("completed = %s, next pass = %s; want one due now",
			queue.completed, queue.copyFingerprintSweepQueuedFor)
	}
}

// A pass with nothing left to measure comes back in a while, for whatever has
// been held since. Nothing about it is urgent: a queue that is not grouped yet
// reads exactly as it read last month.
func TestAFingerprintPassWithNothingLeftWaits(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithDownloadImporter(&fakeImporter{})
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepCopyFingerprints, Attempts: 1, MaxAttempts: 3,
	})

	if !queue.copyFingerprintSweepQueuedFor.Equal(now.Add(copyFingerprintIdle)) {
		t.Fatalf("next pass = %s; want one in %s",
			queue.copyFingerprintSweepQueuedFor, copyFingerprintIdle)
	}
}

// A pass that spent its last attempt still queues its replacement, far enough
// out that a machine with no fingerprinting program is not asked every hour.
// Without this the backfill would switch itself off for good.
func TestAFingerprintPassThatSpentItsAttemptsStillComesBack(t *testing.T) {
	queue := &fakeQueue{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).
		WithDownloadImporter(&fakeImporter{fingerprintErr: errors.New("fpcalc is not installed")})
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return now }

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: SweepCopyFingerprints, Attempts: 3, MaxAttempts: 3,
	})

	if !queue.copyFingerprintSweepQueuedFor.Equal(now.Add(copyFingerprintUnreadable)) {
		t.Fatalf("next pass = %s; want one in %s",
			queue.copyFingerprintSweepQueuedFor, copyFingerprintUnreadable)
	}
}

// MusicBrainz files compilations under a placeholder artist, and a placeholder
// has no catalogue. Asking again would spend a request a day on the same
// answer, so the refresh is told once and left alone.
func TestAnArtistRefreshIsNotAskedAgainForAPlaceholder(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: musicbrainz.VariousArtistsID}
	worker := NewWorker(queue, fakeProvider{err: musicbrainz.ErrNotAnArtist}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

// A refusal that will be the same tomorrow has to be recorded as an answer, or
// the genre backfill reads the NULL genres as "nobody has asked", queues the
// refusal again a day later, and counts the artist as waiting in between —
// which held the sweep at ten minutes forever for one placeholder row.
func TestAnArtistRefreshRecordsThatAPlaceholderHasNoGenres(t *testing.T) {
	artistID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": artistID})
	queue := &fakeQueue{musicBrainzID: musicbrainz.VariousArtistsID}
	worker := NewWorker(queue, fakeProvider{err: musicbrainz.ErrNotAnArtist}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if len(queue.artistGenresAsked) != 1 || queue.artistGenresAsked[0] != artistID {
		t.Fatalf("artists recorded as asked = %v, want %s once", queue.artistGenresAsked, artistID)
	}
}

// A release group nobody can read a track list off is the same answer every
// time, for the same reason.
func TestAReleaseRefreshIsNotAskedAgainWhenNoEditionCanBeRead(t *testing.T) {
	jobID, albumID := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"albumId": albumID})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{err: musicbrainz.ErrNoUsableEditions}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshAlbumMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

// The same release group is recorded as answered, so the backfill stops
// counting it.
func TestAReleaseRefreshRecordsThatAnUnreadableGroupHasNoGenres(t *testing.T) {
	albumID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"albumId": albumID})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{err: musicbrainz.ErrNoUsableEditions}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: uuid.New(), Kind: RefreshAlbumMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if len(queue.albumGenresAsked) != 1 || queue.albumGenresAsked[0] != albumID {
		t.Fatalf("releases recorded as asked = %v, want %s once", queue.albumGenresAsked, albumID)
	}
}

// An artist with more releases than one pass reads still has them next time.
// Retrying held the one worker that scans, tags and moves files for the better
// part of an hour and then queued a refresh for every release it had read.
func TestAnArtistRefreshIsNotAskedAgainForACatalogueTooLargeToRead(t *testing.T) {
	jobID := uuid.New()
	payload, _ := json.Marshal(map[string]uuid.UUID{"artistId": uuid.New()})
	queue := &fakeQueue{musicBrainzID: uuid.New()}
	worker := NewWorker(queue, fakeProvider{err: musicbrainz.ErrCatalogueTooLarge}, zerolog.Nop())

	worker.process(context.Background(), Job{
		ID: jobID, Kind: RefreshArtistMetadata, Payload: payload, Attempts: 1, MaxAttempts: 3,
	})

	if queue.failed != jobID || queue.retried != uuid.Nil {
		t.Fatalf("failed = %s retried = %s, want a permanent failure", queue.failed, queue.retried)
	}
}

// The pass over the copies refused on the artist tag is its own kind. It is
// queued once by migration 00087 and queues no replacement.
func TestThePassOverCreditRefusalsQueuesNoReplacement(t *testing.T) {
	jobID := uuid.New()
	queue := &fakeQueue{}
	importer := &fakeImporter{}
	worker := NewWorker(queue, fakeProvider{}, zerolog.Nop()).WithDownloadImporter(importer)

	worker.process(context.Background(), Job{
		ID: jobID, Kind: JudgeCreditRefusals, Attempts: 1, MaxAttempts: 1,
	})

	if importer.judgedCreditRefusals != 1 || importer.judgedAgain != 0 {
		t.Fatalf("credit passes = %d other passes = %d, want only the one asked for",
			importer.judgedCreditRefusals, importer.judgedAgain)
	}
	if queue.completed != jobID {
		t.Fatalf("completed = %s, want the pass settled", queue.completed)
	}
}
