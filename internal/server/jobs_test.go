package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/jobs"
	"github.com/rs/zerolog"
)

// queueStore is the fakeStore with the queue on top, so NewAPI's interface
// assertion finds it.
type queueStore struct {
	*fakeStore
	active []db.ListActiveJobsRow
	failed []db.ListFailedJobsRow
	pulse  []db.RecurringJobPulseRow
	// What each job is about, and how often the page had to ask. A view that
	// asked once per row would answer the same but cost two hundred round
	// trips on a first scan, so the number of questions is part of the
	// contract.
	subjects     []db.JobSubjectsRow
	subjectAsks  int
	subjectIDs   []uuid.UUID
	subjectError error
	// The kinds each read was asked about, so a test can prove the worker's own
	// division reached the query rather than a second one written here.
	pulseKinds []string
	// The failed jobs each release has, for the bulk retry, and the releases it
	// was asked about.
	failedByRelease    map[uuid.UUID][]uuid.UUID
	askedAboutReleases []uuid.UUID
	// Passes over the copies already decided about that were asked for.
	judgeAgainAsked int
	judgeAgainErr   error
	// What the requeue answers, and the job it was asked about.
	requeued    db.JobRow
	requeuedNew bool
	requeueErr  error
	requeuedID  uuid.UUID
	// The jobs a press on "Retry all" asked for, and how many the queue says
	// it moved. Zero means it moved every one it was given.
	requeuedIDs  []uuid.UUID
	requeuedRows int64
	queueErr     error
	// What CancelQueuedJob answers, and the job it was asked about.
	cancelled    db.JobRow
	cancelledNew bool
	cancelErr    error
	cancelledID  uuid.UUID
	// What CancelQueuedJobsByKind answers, and the kind it was asked about.
	cancelledByKind int64
	cancelKindErr   error
	cancelledKind   string
}

func (store *queueStore) ListActiveJobs(
	_ context.Context, _ int32,
) ([]db.ListActiveJobsRow, error) {
	return store.active, store.queueErr
}

func (store *queueStore) ListFailedJobs(
	_ context.Context, _ int32,
) ([]db.ListFailedJobsRow, error) {
	return store.failed, store.queueErr
}

func (store *queueStore) JobSubjects(
	_ context.Context, jobIDs []uuid.UUID,
) ([]db.JobSubjectsRow, error) {
	store.subjectAsks++
	store.subjectIDs = jobIDs
	return store.subjects, store.subjectError
}

func (store *queueStore) RecurringJobPulse(
	_ context.Context, kinds []string,
) ([]db.RecurringJobPulseRow, error) {
	store.pulseKinds = kinds
	return store.pulse, store.queueErr
}

func (store *queueStore) RequeueFailedJob(
	_ context.Context, jobID uuid.UUID,
) (db.JobRow, bool, error) {
	store.requeuedID = jobID
	if store.requeueErr != nil {
		return db.JobRow{}, false, store.requeueErr
	}
	return store.requeued, store.requeuedNew, nil
}

func (store *queueStore) RequeueFailedJobs(
	_ context.Context, jobIDs []uuid.UUID,
) (int64, error) {
	store.requeuedIDs = jobIDs
	if store.requeueErr != nil {
		return 0, store.requeueErr
	}
	if store.requeuedRows > 0 {
		return store.requeuedRows, nil
	}
	return int64(len(jobIDs)), nil
}

// The failed jobs of a selection of releases, keyed the way the query keys
// them. failedByRelease is what the store holds; the handler asks about the
// releases it was given and reads what is there.
func (store *queueStore) FailedReleaseJobs(
	_ context.Context, albumIDs []uuid.UUID,
) (map[uuid.UUID][]uuid.UUID, error) {
	store.askedAboutReleases = albumIDs
	if store.requeueErr != nil {
		return nil, store.requeueErr
	}
	answer := map[uuid.UUID][]uuid.UUID{}
	for _, albumID := range albumIDs {
		if jobIDs := store.failedByRelease[albumID]; len(jobIDs) > 0 {
			answer[albumID] = jobIDs
		}
	}
	return answer, nil
}

func (store *queueStore) CancelQueuedJob(
	_ context.Context, jobID uuid.UUID,
) (db.JobRow, bool, error) {
	store.cancelledID = jobID
	if store.cancelErr != nil {
		return db.JobRow{}, false, store.cancelErr
	}
	return store.cancelled, store.cancelledNew, nil
}

func (store *queueStore) CancelQueuedJobsByKind(
	_ context.Context, kind string,
) (int64, error) {
	store.cancelledKind = kind
	if store.cancelKindErr != nil {
		return 0, store.cancelKindErr
	}
	return store.cancelledByKind, nil
}

// judgeAgainAsked counts the passes over already-decided copies that were asked
// for, because "one press queues one pass" is the whole contract here.
func (store *queueStore) QueueJudgeCopiesAgain(_ context.Context, _ time.Time) error {
	store.judgeAgainAsked++
	return store.judgeAgainErr
}

func queueHandler(store *queueStore) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
}

func stamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func note(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func readQueue[T any](t *testing.T, handler http.Handler, method, path string, status int) T {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body)
	}
	var decoded T
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
	return decoded
}

// The lanes are the worker's own. Nothing here restates which kinds those are
// — the query is asked with the worker's lists, so a kind added later lands on
// the general lane in this view for the same reason it does in the queue.
func TestTheQueueIsReadOneListPerLane(t *testing.T) {
	searchID, scanID := uuid.New(), uuid.New()
	store := &queueStore{fakeStore: &fakeStore{}, active: []db.ListActiveJobsRow{
		{
			ID: searchID, Kind: jobs.SearchAlbumSources, Status: "running",
			Attempts: 1, MaxAttempts: 3, RunAfter: stamp(time.Now()),
			StartedAt: stamp(time.Now()), CreatedAt: stamp(time.Now()),
			Total: 2,
		},
		{
			ID: scanID, Kind: jobs.ScanLibrary, Status: "queued",
			Attempts: 0, MaxAttempts: 1, RunAfter: stamp(time.Now()),
			CreatedAt: stamp(time.Now()), Total: 2,
		},
	}}

	decoded := readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	if len(decoded.Lanes) != 5 {
		t.Fatalf("lanes = %d, want the five the worker runs", len(decoded.Lanes))
	}
	searching, transfers, anchors, judging, everythingElse :=
		decoded.Lanes[0], decoded.Lanes[1], decoded.Lanes[2], decoded.Lanes[3], decoded.Lanes[4]
	if transfers.Lane != laneTransfers || len(transfers.Jobs) != 0 {
		t.Fatalf("transfers lane = %+v, want it named and empty", transfers)
	}
	if anchors.Lane != laneAnchors || len(anchors.Jobs) != 0 {
		t.Fatalf("anchors lane = %+v, want it named and empty", anchors)
	}
	if judging.Lane != laneJudging || len(judging.Jobs) != 0 {
		t.Fatalf("judging lane = %+v, want it named and empty", judging)
	}
	if searching.Lane != laneAcquisition || everythingElse.Lane != laneGeneral {
		t.Fatalf("lanes = %q and %q, want the searching lane first and general lane last",
			searching.Lane, everythingElse.Lane)
	}
	if len(searching.Jobs) != 1 || searching.Jobs[0].ID != searchID {
		t.Fatalf("searching lane = %+v, want the source search", searching.Jobs)
	}
	if searching.RunningCount != 1 || searching.QueuedCount != 0 {
		t.Fatalf("searching counts = %d running, %d queued; want 1 and 0",
			searching.RunningCount, searching.QueuedCount)
	}
	if len(everythingElse.Jobs) != 1 || everythingElse.Jobs[0].ID != scanID {
		t.Fatalf("general lane = %+v, want the library scan", everythingElse.Jobs)
	}
	if everythingElse.Jobs[0].Lane != laneGeneral {
		t.Fatalf("scan lane = %q, want %q", everythingElse.Jobs[0].Lane, laneGeneral)
	}
	if decoded.Total != 2 || decoded.Truncated {
		t.Fatalf("total = %d, truncated = %v; want the whole queue reported as whole",
			decoded.Total, decoded.Truncated)
	}
}

func TestLaneOfNamesAnchorAndJudgingLanes(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{kind: jobs.SweepPreviewAnchors, want: laneAnchors},
		{kind: jobs.JudgeWantCopies, want: laneJudging},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			if got := laneOf(test.kind); got != test.want {
				t.Fatalf("laneOf(%q) = %q, want %q", test.kind, got, test.want)
			}
		})
	}
}

// A queued job carrying an error is between retries rather than new, and the
// backoff it is waiting out is the difference between a queue that is stuck and
// one that is being patient.
func TestAQueuedJobSaysWhatTheLastAttemptFailedWith(t *testing.T) {
	due := time.Now().Add(40 * time.Second).UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, active: []db.ListActiveJobsRow{{
		ID: uuid.New(), Kind: jobs.RefreshArtistMetadata, Status: "queued",
		Attempts: 2, MaxAttempts: 3, RunAfter: stamp(due), CreatedAt: stamp(time.Now()),
		ErrorMessage: note("fetch artist catalogue: 503"), Total: 1,
	}}}

	decoded := readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	job := onlyJob(t, decoded)
	if job.Error != "fetch artist catalogue: 503" {
		t.Fatalf("error = %q, want what the last attempt failed with", job.Error)
	}
	if job.RunAfter == nil || !job.RunAfter.Equal(due) {
		t.Fatalf("runAfter = %v, want the moment the backoff ends, %v", job.RunAfter, due)
	}
	if job.Attempts != 2 || job.MaxAttempts != 3 {
		t.Fatalf("attempts = %d of %d, want 2 of 3", job.Attempts, job.MaxAttempts)
	}
}

// The failed list is the half of the queue worth a page, so the error and what
// the job was about have to survive the journey to it.
func TestFailedJobsCarryTheErrorThatEndedThemAndWhatTheyWereAbout(t *testing.T) {
	failedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, failed: []db.ListFailedJobsRow{{
		ID: uuid.New(), Kind: jobs.IngestReleaseGroup,
		Payload:      []byte(`{"releaseGroupId": "b1a9c0de-0000-4000-8000-000000000001"}`),
		Attempts:     3,
		MaxAttempts:  3,
		ErrorMessage: note("fetch release group: 502"),
		CompletedAt:  stamp(failedAt),
		CreatedAt:    stamp(failedAt.Add(-time.Minute)),
		Total:        1,
	}}}

	decoded := readQueue[failedJobListResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs/failed", http.StatusOK)

	if len(decoded.Items) != 1 {
		t.Fatalf("items = %d, want the one job that spent its attempts", len(decoded.Items))
	}
	item := decoded.Items[0]
	if item.Error != "fetch release group: 502" {
		t.Fatalf("error = %q, want the error that ended it", item.Error)
	}
	if item.Lane != laneGeneral {
		t.Fatalf("lane = %q, want an ingest on %q", item.Lane, laneGeneral)
	}
	if item.FailedAt == nil || !item.FailedAt.Equal(failedAt) {
		t.Fatalf("failedAt = %v, want %v", item.FailedAt, failedAt)
	}
	var payload struct {
		ReleaseGroupID string `json:"releaseGroupId"`
	}
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		t.Fatalf("decode payload %s: %v", item.Payload, err)
	}
	if payload.ReleaseGroupID == "" {
		t.Fatal("payload carried no release group; the error alone says nothing about which")
	}
}

// nameFailure is what a job records when the machine could not turn the name
// musicbrainz.org into an address. Seventy-one jobs recorded one of these on 12
// August 2026, and reading them as seventy-one problems is what this list used
// to do.
const nameFailure = `import download: verify "10-ufo361-gewinn.flac" against recording ` +
	`c75fe756-1e6e-4a5b-9c7c-4b0b2f7e1f10: look up MusicBrainz recording: ` +
	`Get "https://musicbrainz.org/ws/2/recording/c75fe756?fmt=json&inc=artist-credits%2Bisrcs": ` +
	`dial tcp: lookup musicbrainz.org on 10.43.0.10:53: server misbehaving`

// spentJob is one row of the failed list, said in as few words as a test needs.
func spentJob(jobID uuid.UUID, message string, failedAt time.Time, total int64) db.ListFailedJobsRow {
	return db.ListFailedJobsRow{
		ID: jobID, Kind: jobs.ImportDownload, Payload: []byte(`{}`),
		Attempts: 3, MaxAttempts: 3, ErrorMessage: note(message),
		CompletedAt: stamp(failedAt), CreatedAt: stamp(failedAt.Add(-time.Minute)),
		Total: total,
	}
}

// press sends one JSON body and reads the answer, for the two endpoints that
// change something.
func press[T any](t *testing.T, handler http.Handler, path, body string, status int) T {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body)
	}
	var decoded T
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
	return decoded
}

// Two jobs stopped by one outage are one row on the screen and one thing to do
// about it, rather than two lines of the same unreadable sentence.
func TestFailuresWithOneCauseBehindThemAreGroupedIntoOne(t *testing.T) {
	failedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, failed: []db.ListFailedJobsRow{
		spentJob(uuid.New(), nameFailure, failedAt, 2),
		spentJob(uuid.New(), nameFailure, failedAt.Add(-time.Minute), 2),
	}}

	decoded := readQueue[failedJobListResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs/failed", http.StatusOK)

	if len(decoded.Causes) != 1 {
		t.Fatalf("causes = %d, want the one outage behind both", len(decoded.Causes))
	}
	if decoded.Causes[0].JobCount != 2 {
		t.Fatalf("jobCount = %d, want both jobs counted under it", decoded.Causes[0].JobCount)
	}
}

// Two different problems stay two rows, which is what makes the count beside
// Settings information rather than noise.
func TestTwoDifferentProblemsStayTwoCauses(t *testing.T) {
	failedAt := time.Now().UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, failed: []db.ListFailedJobsRow{
		spentJob(uuid.New(), nameFailure, failedAt, 2),
		spentJob(uuid.New(), "fetch release group: MusicBrainz returned HTTP 404", failedAt, 2),
	}}

	decoded := readQueue[failedJobListResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs/failed", http.StatusOK)

	if len(decoded.Causes) != 2 {
		t.Fatalf("causes = %d, want one for each problem", len(decoded.Causes))
	}
}

// The cause a reader is looking for is the one holding most of the failures.
func TestTheCauseHoldingMostOfTheFailuresIsListedFirst(t *testing.T) {
	failedAt := time.Now().UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, failed: []db.ListFailedJobsRow{
		spentJob(uuid.New(), "fetch release group: MusicBrainz returned HTTP 404", failedAt, 3),
		spentJob(uuid.New(), nameFailure, failedAt.Add(-time.Minute), 3),
		spentJob(uuid.New(), nameFailure, failedAt.Add(-2*time.Minute), 3),
	}}

	decoded := readQueue[failedJobListResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs/failed", http.StatusOK)

	if decoded.Causes[0].JobCount != 2 {
		t.Fatalf("the first cause holds %d jobs, want the largest first", decoded.Causes[0].JobCount)
	}
}

// A cause says when it started and when it last happened, because "71 jobs
// stopped between 10 and 12 August" is the sentence a reader needs and neither
// date is on any single row.
func TestACauseCarriesTheSpanOfTimeItStopped(t *testing.T) {
	first := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	last := time.Date(2026, 8, 12, 17, 0, 0, 0, time.UTC)
	store := &queueStore{fakeStore: &fakeStore{}, failed: []db.ListFailedJobsRow{
		spentJob(uuid.New(), nameFailure, last, 2),
		spentJob(uuid.New(), nameFailure, first, 2),
	}}

	decoded := readQueue[failedJobListResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs/failed", http.StatusOK)

	cause := decoded.Causes[0]
	if cause.FirstFailedAt == nil || !cause.FirstFailedAt.Equal(first) {
		t.Fatalf("firstFailedAt = %v, want %v", cause.FirstFailedAt, first)
	}
	if cause.LastFailedAt == nil || !cause.LastFailedAt.Equal(last) {
		t.Fatalf("lastFailedAt = %v, want %v", cause.LastFailedAt, last)
	}
}

// Every job the cause stopped goes back on the queue, and nothing else does.
func TestRetryingACauseQueuesEveryJobItStoppedAndNoOther(t *testing.T) {
	failedAt := time.Now().UTC().Truncate(time.Second)
	stopped, other := uuid.New(), uuid.New()
	store := &queueStore{fakeStore: &fakeStore{}, failed: []db.ListFailedJobsRow{
		spentJob(stopped, nameFailure, failedAt, 2),
		spentJob(other, "fetch release group: MusicBrainz returned HTTP 404", failedAt, 2),
	}}
	handler := queueHandler(store)

	listed := readQueue[failedJobListResponse](
		t, handler, http.MethodGet, "/api/v1/jobs/failed", http.StatusOK)
	body := fmt.Sprintf(`{"cause": %q}`, listed.Causes[0].Cause)
	decoded := press[retriedCauseResponse](
		t, handler, "/api/v1/jobs/failed/retry", body, http.StatusOK)

	if decoded.Requeued != 1 {
		t.Fatalf("requeued = %d, want the one job that cause stopped", decoded.Requeued)
	}
	if len(store.requeuedIDs) != 1 || store.requeuedIDs[0] != stopped {
		t.Fatalf("requeued %v, want only %s", store.requeuedIDs, stopped)
	}
}

// A cause nothing is failing for any more is the second press, and the second
// press costs the queue nothing and says so.
func TestRetryingACauseNothingIsFailingForChangesNothing(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}}

	decoded := press[retriedCauseResponse](t, queueHandler(store),
		"/api/v1/jobs/failed/retry", `{"cause": "unknown_host:musicbrainz"}`, http.StatusOK)

	if decoded.Requeued != 0 {
		t.Fatalf("requeued = %d, want nothing changed", decoded.Requeued)
	}
	if decoded.Detail == "" {
		t.Fatal("the press said nothing about what it found")
	}
}

// activeJob is one row of the queue in whatever state a test needs, with the
// fields every row carries filled in so that each test says only what it is
// about.
func activeJob(jobID uuid.UUID, kind string) db.ListActiveJobsRow {
	return db.ListActiveJobsRow{
		ID: jobID, Kind: kind, Status: "running", Attempts: 1, MaxAttempts: 3,
		RunAfter: stamp(time.Now()), StartedAt: stamp(time.Now()),
		CreatedAt: stamp(time.Now()), Total: 1,
	}
}

// onlyJob is the single job an answer holds, whichever lane it landed on. Which
// lane that is belongs to the tests about lanes, and a test about what a row
// says should not have to know.
func onlyJob(t *testing.T, decoded jobQueueResponse) jobResponse {
	t.Helper()
	for _, lane := range decoded.Lanes {
		if len(lane.Jobs) == 1 {
			return lane.Jobs[0]
		}
	}
	t.Fatalf("lanes = %+v, want one job on one of them", decoded.Lanes)
	return jobResponse{}
}

// A job kind is what Schall is doing and never what it is doing it to. The
// payload holds identifiers, so the queue is the only thing that can turn one
// into a title, and a row that could not would send the reader to the log to
// find out which release a search is about.
func TestAJobSaysWhatItIsAbout(t *testing.T) {
	searchID := uuid.New()
	store := &queueStore{
		fakeStore: &fakeStore{},
		active:    []db.ListActiveJobsRow{activeJob(searchID, jobs.SearchAlbumSources)},
		subjects: []db.JobSubjectsRow{
			{ID: searchID, Subject: "Spirit of Eden", SubjectArtist: "Talk Talk"},
		},
	}

	decoded := readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	job := onlyJob(t, decoded)
	if job.Subject != "Spirit of Eden" {
		t.Fatalf("subject = %q, want the release the search is for", job.Subject)
	}
	if job.SubjectArtist != "Talk Talk" {
		t.Fatalf("subject artist = %q, want who the release is by", job.SubjectArtist)
	}
}

// The whole reason this is resolved in the API rather than in the browser. A
// first scan queues five hundred resolutions, and a view that asked what each
// one was about would ask five hundred times to draw one screen.
func TestAPageOfJobsIsNamedInOneQuestion(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	store := &queueStore{fakeStore: &fakeStore{}, active: []db.ListActiveJobsRow{
		activeJob(first, jobs.ResolveLibraryFile),
		activeJob(second, jobs.ResolveLibraryFile),
	}}

	readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	if store.subjectAsks != 1 {
		t.Fatalf("asked %d times, want the page named in one question", store.subjectAsks)
	}
	if len(store.subjectIDs) != 2 {
		t.Fatalf("asked about %v, want every job on the page", store.subjectIDs)
	}
}

// A sweep is about the whole installation and an ingest may name a release
// group the catalogue does not hold yet. Both read as their bare kind: an
// identifier in the subject would say only that Schall cannot name what it is
// doing, which is worse than saying nothing.
func TestAJobThatNamesNothingReadsAsItsBareKind(t *testing.T) {
	sweepID := uuid.New()
	store := &queueStore{
		fakeStore: &fakeStore{},
		active:    []db.ListActiveJobsRow{activeJob(sweepID, jobs.SweepCoverArt)},
	}

	decoded := readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	job := onlyJob(t, decoded)
	if job.Subject != "" || job.SubjectArtist != "" || job.SubjectFileCount != nil {
		t.Fatalf("job = %+v, want a sweep left with nothing but its kind", job)
	}
}

// How much of a release is being fetched is the difference between a transfer
// worth waiting for and one worth looking into, and it is a fact the request
// recorded when somebody chose it.
func TestATransferSaysHowManyFilesItIsFor(t *testing.T) {
	transferID := uuid.New()
	store := &queueStore{
		fakeStore: &fakeStore{},
		active:    []db.ListActiveJobsRow{activeJob(transferID, jobs.ImportDownload)},
		subjects: []db.JobSubjectsRow{{
			ID: transferID, Subject: "Kid A", SubjectArtist: "Radiohead",
			SubjectFileCount: 10,
		}},
	}

	decoded := readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	job := onlyJob(t, decoded)
	if job.SubjectFileCount == nil || *job.SubjectFileCount != 10 {
		t.Fatalf("file count = %v, want the ten files the request agreed to take",
			job.SubjectFileCount)
	}
}

// A count is a qualifier of a name. A request whose release has been deleted
// under it still knows how many files it asked for, and reporting that beside a
// row with nothing to call itself would be a number attached to nothing.
func TestACountWithNothingToQualifyIsNotReported(t *testing.T) {
	transferID := uuid.New()
	store := &queueStore{
		fakeStore: &fakeStore{},
		active:    []db.ListActiveJobsRow{activeJob(transferID, jobs.ImportDownload)},
		subjects:  []db.JobSubjectsRow{{ID: transferID, SubjectFileCount: 10}},
	}

	decoded := readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	if count := onlyJob(t, decoded).SubjectFileCount; count != nil {
		t.Fatalf("file count = %d, want nothing beside a row that cannot name itself", *count)
	}
}

// The subject is what somebody reads and the payload is what they quote. A
// failure that outlived what it was about has only the payload left, so the two
// are carried together rather than one replacing the other.
func TestAFailedJobCarriesItsSubjectBesideItsPayload(t *testing.T) {
	failedID := uuid.New()
	store := &queueStore{fakeStore: &fakeStore{}, failed: []db.ListFailedJobsRow{{
		ID: failedID, Kind: jobs.IngestReleaseGroup,
		Payload:      []byte(`{"releaseGroupId": "b1a9c0de-0000-4000-8000-000000000001"}`),
		Attempts:     3,
		MaxAttempts:  3,
		ErrorMessage: note("fetch release group: 502"),
		Total:        1,
	}}, subjects: []db.JobSubjectsRow{
		{ID: failedID, Subject: "Hail To The Thief", SubjectArtist: "Radiohead"},
	}}

	decoded := readQueue[failedJobListResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs/failed", http.StatusOK)

	item := decoded.Items[0]
	if item.Subject != "Hail To The Thief" {
		t.Fatalf("subject = %q, want the release the ingest failed on", item.Subject)
	}
	if len(item.Payload) == 0 {
		t.Fatal("the payload was dropped; it is the evidence and it stays")
	}
}

// The queue view is what somebody opens when they suspect the worker is stuck,
// so a subject that could not be read costs the rows their names and nothing
// else. Losing the page would take away the answer they came for.
func TestSubjectsThatCouldNotBeReadLeaveTheQueueReadable(t *testing.T) {
	store := &queueStore{
		fakeStore:    &fakeStore{},
		active:       []db.ListActiveJobsRow{activeJob(uuid.New(), jobs.ScanLibrary)},
		subjectError: errors.New("the catalogue could not be reached"),
	}

	decoded := readQueue[jobQueueResponse](
		t, queueHandler(store), http.MethodGet, "/api/v1/jobs", http.StatusOK)

	job := onlyJob(t, decoded)
	if job.Kind != jobs.ScanLibrary || job.Subject != "" {
		t.Fatalf("job = %+v, want the row still readable as its kind", job)
	}
}

// Every recurring pass is reported, including the ones the queue is holding
// nothing for. A pass missing from the answer would read as a pass that does
// not exist, and a next time invented for it would be this view saying
// something the queue never said.
func TestEveryRecurringPassIsReportedWhetherOrNotOneIsQueued(t *testing.T) {
	lastRun := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	due := time.Now().Add(4 * time.Hour).UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, pulse: []db.RecurringJobPulseRow{{
		Kind:            jobs.SweepCoverArt,
		LastFinishedAt:  stamp(lastRun),
		LastCompletedAt: stamp(lastRun),
		NextRunAfter:    stamp(due),
	}}}

	decoded := readQueue[struct {
		Items []recurringJobResponse `json:"items"`
	}](t, queueHandler(store), http.MethodGet, "/api/v1/jobs/schedule", http.StatusOK)

	if len(decoded.Items) != len(recurringJobs) {
		t.Fatalf("items = %d, want all %d recurring passes",
			len(decoded.Items), len(recurringJobs))
	}
	byKind := make(map[string]recurringJobResponse, len(decoded.Items))
	for _, item := range decoded.Items {
		byKind[item.Kind] = item
	}
	covers := byKind[jobs.SweepCoverArt]
	if covers.NextRunAt == nil || !covers.NextRunAt.Equal(due) {
		t.Fatalf("cover art nextRunAt = %v, want the queued row's own time %v",
			covers.NextRunAt, due)
	}
	if covers.Detail != "" {
		t.Fatalf("cover art detail = %q, want nothing to explain when there is a time",
			covers.Detail)
	}
	if covers.LastRunAt == nil || !covers.LastRunAt.Equal(lastRun) {
		t.Fatalf("cover art lastRunAt = %v, want %v", covers.LastRunAt, lastRun)
	}
	poller := byKind[jobs.PollDownloads]
	if poller.NextRunAt != nil {
		t.Fatalf("poller nextRunAt = %v, want nothing rather than a guessed time",
			poller.NextRunAt)
	}
	if poller.Detail == "" {
		t.Fatal("poller said nothing about why it has no next time")
	}
	if poller.LastRunAt != nil {
		t.Fatalf("poller lastRunAt = %v, want a pass nothing is recorded for", poller.LastRunAt)
	}
}

// The whole-library tagging pass is recurring now, not only a press away, so
// it belongs in the schedule view with a label of its own rather than the
// queue's bare kind.
func TestTagLibraryIsOneOfTheRecurringPasses(t *testing.T) {
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, pulse: []db.RecurringJobPulseRow{{
		Kind:         jobs.TagLibrary,
		NextRunAfter: stamp(due),
	}}}

	decoded := readQueue[struct {
		Items []recurringJobResponse `json:"items"`
	}](t, queueHandler(store), http.MethodGet, "/api/v1/jobs/schedule", http.StatusOK)

	byKind := make(map[string]recurringJobResponse, len(decoded.Items))
	for _, item := range decoded.Items {
		byKind[item.Kind] = item
	}
	tagging, ok := byKind[jobs.TagLibrary]
	if !ok {
		t.Fatal("tag_library is not among the recurring passes")
	}
	if tagging.Label == "" {
		t.Fatal("tag_library has no label")
	}
	if tagging.NextRunAt == nil || !tagging.NextRunAt.Equal(due) {
		t.Fatalf("tag_library nextRunAt = %v, want the queued row's own time %v", tagging.NextRunAt, due)
	}
}

var recurringJobExclusions = map[string]struct{}{
	// scan_library is queued by library changes, so it is not a schedule.
	jobs.ScanLibrary: {},
	// poll_downloads is queued when a transfer starts, so it is not a schedule.
	jobs.PollDownloads: {},
	// notify_player is queued by library changes, so it is not a schedule.
	jobs.NotifyPlayer: {},
	// answer_peer_challenges is queued when a transfer is asked for or settles,
	// so it is not a schedule.
	jobs.AnswerPeerChallenges: {},
}

// Every kind held to one live row is listed, except kinds that react to an
// event. The database list is the source of the rule; this test keeps the view
// from silently losing a pass when a new unique index is added.
func TestEveryScheduledSweepIsListed(t *testing.T) {
	want := make(map[string]struct{}, len(db.ScheduledSweepKinds))
	for _, kind := range db.ScheduledSweepKinds {
		if _, excluded := recurringJobExclusions[kind]; !excluded {
			want[kind] = struct{}{}
		}
	}

	got := make(map[string]struct{}, len(recurringJobs))
	for _, recurring := range recurringJobs {
		if recurring.label == "" || recurring.idle == "" {
			t.Errorf("%s has no label and idle text", recurring.kind)
		}
		if _, excluded := recurringJobExclusions[recurring.kind]; excluded {
			continue
		}
		if _, duplicate := got[recurring.kind]; duplicate {
			t.Errorf("recurringJobs lists %s more than once", recurring.kind)
		}
		got[recurring.kind] = struct{}{}
	}

	for kind := range want {
		if _, listed := got[kind]; !listed {
			t.Errorf("%s is in ScheduledSweepKinds but not recurringJobs", kind)
		}
	}
	for kind := range got {
		if _, expected := want[kind]; !expected {
			t.Errorf("%s is in recurringJobs but not ScheduledSweepKinds", kind)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("recurringJobs has %d unique kinds, want %d", len(got), len(want))
	}
}

// A pass that is running holds the one row the index allows, so there is
// nothing queued behind it. That is not the same absence as a pass nobody has
// scheduled, and it must not read as one.
func TestARunningPassSaysItIsRunningRatherThanUnscheduled(t *testing.T) {
	since := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	store := &queueStore{fakeStore: &fakeStore{}, pulse: []db.RecurringJobPulseRow{{
		Kind: jobs.SweepFollowFeed, RunningSince: stamp(since),
	}}}

	decoded := readQueue[struct {
		Items []recurringJobResponse `json:"items"`
	}](t, queueHandler(store), http.MethodGet, "/api/v1/jobs/schedule", http.StatusOK)

	for _, item := range decoded.Items {
		if item.Kind != jobs.SweepFollowFeed {
			continue
		}
		if !item.Running || item.RunningSince == nil {
			t.Fatalf("follow feed = %+v, want it reported as running", item)
		}
		if item.Detail != "Running now." {
			t.Fatalf("follow feed detail = %q, want it told apart from unscheduled",
				item.Detail)
		}
		return
	}
	t.Fatal("the follow feed sweep was missing from the schedule")
}

// The press that puts a job back. It answers with the job as it now stands, so
// that the view showing it can stop showing it as failed without asking again.
func TestRetryReportsAJobPutBackOnTheQueue(t *testing.T) {
	jobID := uuid.New()
	store := &queueStore{fakeStore: &fakeStore{}, requeuedNew: true, requeued: db.JobRow{
		ID: jobID, Kind: jobs.IngestReleaseGroup, Status: "queued",
		Attempts: 0, MaxAttempts: 3, RunAfter: stamp(time.Now()),
	}}

	decoded := readQueue[retriedJobResponse](t, queueHandler(store),
		http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/retry", http.StatusOK)

	if !decoded.Requeued {
		t.Fatal("requeued = false, want the press reported as having done something")
	}
	if decoded.Job.Status != "queued" {
		t.Fatalf("status = %q, want the job waiting again", decoded.Job.Status)
	}
	if decoded.Job.Attempts != 0 {
		t.Fatalf("attempts = %d, want the whole ladder ahead of it again",
			decoded.Job.Attempts)
	}
	if store.requeuedID != jobID {
		t.Fatalf("asked about %s, want %s", store.requeuedID, jobID)
	}
}

// The row the press came from is redrawn from the answer to it, so a job that
// had a name before it was retried must not lose it by being retried.
func TestARetriedJobKeepsWhatItIsAbout(t *testing.T) {
	jobID := uuid.New()
	store := &queueStore{
		fakeStore: &fakeStore{}, requeuedNew: true,
		requeued: db.JobRow{
			ID: jobID, Kind: jobs.IngestReleaseGroup, Status: "queued",
			Attempts: 3, MaxAttempts: 3, RunAfter: stamp(time.Now()),
		},
		subjects: []db.JobSubjectsRow{
			{ID: jobID, Subject: "Hail To The Thief", SubjectArtist: "Radiohead"},
		},
	}

	decoded := readQueue[retriedJobResponse](t, queueHandler(store),
		http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/retry", http.StatusOK)

	if decoded.Job.Subject != "Hail To The Thief" {
		t.Fatalf("subject = %q, want the release the retried job is about",
			decoded.Job.Subject)
	}
}

// The second press. A job already on its way is not an error to report: it is
// the outcome the person pressing wanted, arrived at by the first press.
func TestASecondRetryAnswersPlainlyThatNothingChanged(t *testing.T) {
	for status, detail := range map[string]string{
		"queued":    "Already queued. Nothing was changed.",
		"running":   "Already running. Nothing was changed.",
		"completed": "Already finished. Nothing was changed.",
		// The job was running when the press arrived and failed between the
		// requeue and the read that followed it. Nothing was queued, and the
		// answer has to say so rather than call it finished.
		"failed": "It has failed since. Nothing was changed; retry it again.",
	} {
		jobID := uuid.New()
		store := &queueStore{fakeStore: &fakeStore{}, requeuedNew: false, requeued: db.JobRow{
			ID: jobID, Kind: jobs.IngestReleaseGroup, Status: status, Attempts: 4, MaxAttempts: 3,
		}}

		decoded := readQueue[retriedJobResponse](t, queueHandler(store),
			http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/retry", http.StatusOK)

		if decoded.Requeued {
			t.Fatalf("%s: requeued = true, want the second press to have added nothing", status)
		}
		if decoded.Detail != detail {
			t.Fatalf("%s: detail = %q, want %q", status, decoded.Detail, detail)
		}
		if decoded.Job.Status != status {
			t.Fatalf("%s: status = %q, want the job left as it was", status, decoded.Job.Status)
		}
	}
}

// A cancelled job was stopped because the work it belonged to was called off.
// Retrying it would search a release nobody is waiting for, so it is refused in
// words rather than quietly requeued.
func TestRetryRefusesAJobThatWasStopped(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}, requeueErr: db.ErrJobWasStopped}

	response := httptest.NewRecorder()
	queueHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/jobs/"+uuid.New().String()+"/retry", nil))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s",
			response.Code, http.StatusConflict, response.Body)
	}
	if !strings.Contains(response.Body.String(), "stopped") {
		t.Fatalf("body = %s, want it to say the job was stopped", response.Body)
	}
}

// A store that cannot answer for the queue says so, rather than reporting an
// empty queue — an installation whose jobs cannot be read is not an
// installation with nothing to do.
func TestTheQueueRoutesSayWhenTheQueueCannotBeRead(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodGet, "/api/v1/jobs/failed"},
		{http.MethodGet, "/api/v1/jobs/schedule"},
		{http.MethodPost, "/api/v1/jobs/" + uuid.New().String() + "/retry"},
		{http.MethodPost, "/api/v1/jobs/" + uuid.New().String() + "/cancel"},
		{http.MethodPost, "/api/v1/jobs/queued/cancel"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(probe.method, probe.path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: status = %d, want %d",
				probe.method, probe.path, response.Code, http.StatusServiceUnavailable)
		}
	}
}

// An identifier the queue has never held is a missing job, not a failure of the
// queue.
func TestRetryAnswersNotFoundForAJobTheQueueNeverHeld(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}, requeueErr: pgx.ErrNoRows}

	response := httptest.NewRecorder()
	queueHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/jobs/"+uuid.New().String()+"/retry", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s",
			response.Code, http.StatusNotFound, response.Body)
	}
}

// Cancelling one queued job from its row removes it, and says so.
func TestCancelReportsAJobRemovedFromTheQueue(t *testing.T) {
	jobID := uuid.New()
	store := &queueStore{fakeStore: &fakeStore{}, cancelledNew: true, cancelled: db.JobRow{
		ID: jobID, Kind: jobs.RefreshAlbumMetadata, Status: "queued",
		Attempts: 0, MaxAttempts: 3,
	}}

	decoded := readQueue[cancelledJobResponse](t, queueHandler(store),
		http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/cancel", http.StatusOK)

	if !decoded.Cancelled {
		t.Fatal("cancelled = false, want the press reported as having done something")
	}
	if decoded.Detail != "Cancelled. It will not run." {
		t.Fatalf("detail = %q, want the press to say what it did", decoded.Detail)
	}
	if store.cancelledID != jobID {
		t.Fatalf("asked about %s, want %s", store.cancelledID, jobID)
	}
}

// A running or already-finished job is left alone, and the press says which —
// the same second-press shape retry answers with.
func TestASecondCancelAnswersPlainlyThatNothingChanged(t *testing.T) {
	for status, detail := range map[string]string{
		"running":   "Already running. Nothing was changed.",
		"completed": "Already finished. Nothing was changed.",
		"failed":    "Already finished. Nothing was changed.",
	} {
		jobID := uuid.New()
		store := &queueStore{fakeStore: &fakeStore{}, cancelledNew: false, cancelled: db.JobRow{
			ID: jobID, Kind: jobs.IngestReleaseGroup, Status: status, Attempts: 1, MaxAttempts: 3,
		}}

		decoded := readQueue[cancelledJobResponse](t, queueHandler(store),
			http.MethodPost, "/api/v1/jobs/"+jobID.String()+"/cancel", http.StatusOK)

		if decoded.Cancelled {
			t.Fatalf("%s: cancelled = true, want the second press to have added nothing", status)
		}
		if decoded.Detail != detail {
			t.Fatalf("%s: detail = %q, want %q", status, decoded.Detail, detail)
		}
	}
}

// An identifier the queue has never held is a missing job, not a failure of
// the queue.
func TestCancelAnswersNotFoundForAJobTheQueueNeverHeld(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}, cancelErr: pgx.ErrNoRows}

	response := httptest.NewRecorder()
	queueHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/jobs/"+uuid.New().String()+"/cancel", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s",
			response.Code, http.StatusNotFound, response.Body)
	}
}

// Cancelling every queued job of one kind reports how many moved, and asks
// the queue about the kind it was named.
func TestCancelKindReportsHowManyWereRemoved(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}, cancelledByKind: 3}

	decoded := press[cancelledKindResponse](t, queueHandler(store),
		"/api/v1/jobs/queued/cancel", `{"kind": "refresh_album_metadata"}`, http.StatusOK)

	if decoded.Cancelled != 3 {
		t.Fatalf("cancelled = %d, want the three the queue reported", decoded.Cancelled)
	}
	if decoded.Refused {
		t.Fatal("refused = true, want an ordinary kind answered plainly")
	}
	if store.cancelledKind != "refresh_album_metadata" {
		t.Fatalf("asked about %q, want %q", store.cancelledKind, "refresh_album_metadata")
	}
}

// A kind that is one of the recurring sweeps' own schedule answers zero
// cancelled, and the response says why rather than leaving the press reading
// as though nothing had been queued.
func TestCancelKindSaysWhyAScheduledSweepWasRefused(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}, cancelledByKind: 0}

	decoded := press[cancelledKindResponse](t, queueHandler(store),
		"/api/v1/jobs/queued/cancel", `{"kind": "sweep_genres"}`, http.StatusOK)

	if decoded.Cancelled != 0 {
		t.Fatalf("cancelled = %d, want the schedule left standing", decoded.Cancelled)
	}
	if !decoded.Refused {
		t.Fatal("refused = false, want the response to say this kind runs on its own schedule")
	}
	if decoded.Detail == "" {
		t.Fatal("the press said nothing about why nothing was cancelled")
	}
}

// No kind named is a request nobody can act on.
func TestCancelKindRefusesAnEmptyKind(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}}

	response := httptest.NewRecorder()
	queueHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/jobs/queued/cancel", strings.NewReader(`{"kind": ""}`)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s",
			response.Code, http.StatusBadRequest, response.Body)
	}
}

// Asking for a pass over the copies already decided about queues one and
// answers at once. The pass decodes every copy it offers, so it is minutes of
// work: waiting for it would be waiting for the collection rather than for a
// request.
func TestAskingToJudgeCopiesAgainQueuesOnePass(t *testing.T) {
	store := &queueStore{fakeStore: &fakeStore{}}
	response := httptest.NewRecorder()

	queueHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/jobs/judge-copies-again", nil))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s",
			response.Code, http.StatusAccepted, response.Body)
	}
	if store.judgeAgainAsked != 1 {
		t.Errorf("passes asked for = %d, want one", store.judgeAgainAsked)
	}
}

// A queue that cannot be written to says so rather than answering as though a
// pass were on its way.
func TestAPassThatCouldNotBeQueuedSaysSo(t *testing.T) {
	store := &queueStore{
		fakeStore:     &fakeStore{},
		judgeAgainErr: errors.New("the jobs table is unreachable"),
	}
	response := httptest.NewRecorder()

	queueHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/jobs/judge-copies-again", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}
