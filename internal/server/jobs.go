package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/jobs"
)

// activeJobLimit is how much of the queue one answer carries. A first scan of a
// large library queues five hundred resolutions at once, so a queue longer than
// this is ordinary and reading all of it would say nothing the count does not:
// what an operator needs from a backlog is its size and the head of it.
const activeJobLimit = 200

// failedJobLimit is how far back the failed list reaches. Failures are rare
// enough that this is history rather than a page, and a queue with more than a
// hundred of them has one cause worth finding rather than a hundred worth
// reading — which is what the list is now grouped by, so the reader sees the
// causes rather than the rows whatever this number is.
const failedJobLimit = 200

// causeRetryLimit is how many failures one press on "Retry all" looks at. It is
// larger than the list itself: the reader was shown the head of the failures and
// asked for every job that one cause stopped, not for the ones that happened to
// fit on the screen.
const causeRetryLimit = 1000

// The five lanes, named for the wire. The kinds behind them are the worker's
// own division and are not restated here: a job kind added later lands on the
// general lane by being absent from the other four, in the queue and in this
// view alike.
const (
	laneAcquisition = "acquisition"
	laneTransfers   = "transfers"
	laneAnchors     = "anchors"
	laneJudging     = "judging"
	laneGeneral     = "general"
)

// JobStore reads the queue and puts one failed job back into it. It is the only
// thing in the API that touches jobs as jobs rather than as the work behind
// them, and nothing here can change what a job does — the queue is read, and
// the one write is the move the recovery pass already makes.
type JobStore interface {
	ListActiveJobs(context.Context, int32) ([]db.ListActiveJobsRow, error)
	ListFailedJobs(context.Context, int32) ([]db.ListFailedJobsRow, error)
	JobSubjects(context.Context, []uuid.UUID) ([]db.JobSubjectsRow, error)
	RecurringJobPulse(context.Context, []string) ([]db.RecurringJobPulseRow, error)
	RequeueFailedJob(context.Context, uuid.UUID) (db.JobRow, bool, error)
	RequeueFailedJobs(context.Context, []uuid.UUID) (int64, error)
	FailedReleaseJobs(context.Context, []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error)
	QueueJudgeCopiesAgain(context.Context, time.Time) error
	CancelQueuedJob(context.Context, uuid.UUID) (db.JobRow, bool, error)
	CancelQueuedJobsByKind(context.Context, string) (int64, error)
}

// judgeCopiesAgain asks for one pass over the copies Schall already decided
// about, and answers as soon as the pass is on the books rather than when it has
// run: it decodes every copy it offers, so it is minutes of work and not a
// request to wait on.
func (api *API) judgeCopiesAgain(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	if err := api.queue.QueueJudgeCopiesAgain(request.Context(), time.Now()); err != nil {
		api.internalError(response, request, err)
		return
	}
	// The review queue is where the result shows up, and a pass that answers a
	// question removes a row somebody may be looking at.
	api.events.Publish(events.TopicAcquisitions)
	response.WriteHeader(http.StatusAccepted)
}

func (api *API) queueAvailable(response http.ResponseWriter) bool {
	if api.queue == nil {
		api.problem(response, http.StatusServiceUnavailable, "the job queue cannot be read", nil)
		return false
	}
	return true
}

// jobResponse is one job as the queue reads: what it is, where it is, and how
// many attempts it has left. Error is what the last attempt failed with, which
// a queued job carries while it waits out its backoff.
//
// Subject is what the job is about — the release, artist, file or list its
// payload names — resolved here because the payload holds identifiers and a
// view cannot turn a UUID into a title without asking once per row. It is
// absent for a job whose payload names nothing and for one whose subject has
// since been deleted, and absent is the whole answer: the row reads as its bare
// kind rather than as a kind and an identifier.
type jobResponse struct {
	ID               uuid.UUID  `json:"id"`
	Kind             string     `json:"kind"`
	Lane             string     `json:"lane"`
	Status           string     `json:"status"`
	Attempts         int32      `json:"attempts"`
	MaxAttempts      int32      `json:"maxAttempts"`
	RunAfter         *time.Time `json:"runAfter"`
	StartedAt        *time.Time `json:"startedAt,omitempty"`
	CreatedAt        *time.Time `json:"createdAt"`
	Error            string     `json:"error,omitempty"`
	Subject          string     `json:"subject,omitempty"`
	SubjectArtist    string     `json:"subjectArtist,omitempty"`
	SubjectFileCount *int32     `json:"subjectFileCount,omitempty"`
}

// jobLaneResponse is one lane and what is on it. The counts are of the lane
// rather than of the list, so a truncated answer still says how much is behind
// it.
type jobLaneResponse struct {
	Lane         string        `json:"lane"`
	Label        string        `json:"label"`
	RunningCount int           `json:"runningCount"`
	QueuedCount  int           `json:"queuedCount"`
	Jobs         []jobResponse `json:"jobs"`
}

type jobQueueResponse struct {
	Lanes []jobLaneResponse `json:"lanes"`
	// Total is every running and queued job there is, listed or not.
	Total int64 `json:"total"`
	// Truncated says the lists are the head of the queue rather than all of it.
	Truncated bool `json:"truncated"`
}

// listJobs answers with what is running and what is waiting, one list per lane.
//
// The lanes are the worker's: searching, downloads, anchors, judging, and
// everything else. A Soulseek search is a fixed twenty-second wait on somebody
// else, and preview anchors and judges can also run for minutes. Reading each
// apart keeps those waits from looking like one stalled queue.
func (api *API) listJobs(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	rows, err := api.queue.ListActiveJobs(request.Context(), activeJobLimit)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	lanes := map[string]*jobLaneResponse{
		laneAcquisition: {Lane: laneAcquisition, Label: "Searching", Jobs: []jobResponse{}},
		laneTransfers:   {Lane: laneTransfers, Label: "Downloads", Jobs: []jobResponse{}},
		laneAnchors:     {Lane: laneAnchors, Label: "Anchors", Jobs: []jobResponse{}},
		laneJudging:     {Lane: laneJudging, Label: "Judging", Jobs: []jobResponse{}},
		laneGeneral:     {Lane: laneGeneral, Label: "Everything else", Jobs: []jobResponse{}},
	}
	subjects := api.jobSubjects(request.Context(), activeJobIDs(rows))
	var total int64
	for _, row := range rows {
		total = row.Total
		lane := lanes[laneOf(row.Kind)]
		if row.Status == "running" {
			lane.RunningCount++
		} else {
			lane.QueuedCount++
		}
		job := jobResponse{
			ID:          row.ID,
			Kind:        row.Kind,
			Lane:        lane.Lane,
			Status:      row.Status,
			Attempts:    row.Attempts,
			MaxAttempts: row.MaxAttempts,
			RunAfter:    nullableTime(row.RunAfter.Valid, row.RunAfter.Time),
			StartedAt:   nullableTime(row.StartedAt.Valid, row.StartedAt.Time),
			CreatedAt:   nullableTime(row.CreatedAt.Valid, row.CreatedAt.Time),
			Error:       row.ErrorMessage.String,
		}
		job.describe(subjects[row.ID])
		lane.Jobs = append(lane.Jobs, job)
	}
	api.writeJSON(response, http.StatusOK, jobQueueResponse{
		Lanes: []jobLaneResponse{
			*lanes[laneAcquisition], *lanes[laneTransfers], *lanes[laneAnchors],
			*lanes[laneJudging], *lanes[laneGeneral],
		},
		Total:     total,
		Truncated: len(rows) == activeJobLimit && total > int64(len(rows)),
	})
}

// failedJobResponse is one job that spent its attempts, with the error that
// ended it. Subject is what the job was about, read the same way it is for a
// running job; Payload is that as it was recorded, and it stays. The subject is
// what somebody reads and the payload is what they quote in a bug report, and
// the payload is also the only thing left when whatever it named has since been
// deleted — a failure a week old that outlived its release group can still say
// which release group it was.
// Cause is which group of failures this job belongs to. It is decided in Go,
// beside the queue, by reading the recorded error: a browser must never be the
// thing that reads a Go error string, and two people looking at the same failure
// must not be able to disagree about what caused it.
type failedJobResponse struct {
	ID               uuid.UUID       `json:"id"`
	Kind             string          `json:"kind"`
	Lane             string          `json:"lane"`
	Attempts         int32           `json:"attempts"`
	MaxAttempts      int32           `json:"maxAttempts"`
	Error            string          `json:"error"`
	Cause            string          `json:"cause"`
	FailedAt         *time.Time      `json:"failedAt"`
	CreatedAt        *time.Time      `json:"createdAt"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	Subject          string          `json:"subject,omitempty"`
	SubjectArtist    string          `json:"subjectArtist,omitempty"`
	SubjectFileCount *int32          `json:"subjectFileCount,omitempty"`
}

// failureCauseResponse is one reason jobs stopped, and how much it stopped.
//
// It is what the failed-jobs screen is a list of. Seventy-one jobs that could
// not resolve the name musicbrainz.org are one fact about one afternoon, and
// seventy-one rows saying it seventy-one times — each ending in a URL-encoded
// query string cut off mid-word — is the screen saying nothing at length.
//
// Title says what happened. Detail says what the named thing is before it says
// what went wrong with it, because the reader is not assumed to know what
// MusicBrainz is or why Schall talks to it. The recorded errors themselves are
// on the jobs, untouched: never the first thing shown, and never shortened.
type failureCauseResponse struct {
	Cause         string     `json:"cause"`
	Title         string     `json:"title"`
	Detail        string     `json:"detail"`
	Service       string     `json:"service,omitempty"`
	JobCount      int        `json:"jobCount"`
	FirstFailedAt *time.Time `json:"firstFailedAt"`
	LastFailedAt  *time.Time `json:"lastFailedAt"`
}

type failedJobListResponse struct {
	// Causes is what the screen reads, and Items is what sits behind each of
	// them: every job carries the cause it belongs to.
	Causes    []failureCauseResponse `json:"causes"`
	Items     []failedJobResponse    `json:"items"`
	Total     int64                  `json:"total"`
	Truncated bool                   `json:"truncated"`
}

// listFailedJobs answers with the jobs that will not be tried again unless
// somebody asks.
//
// This is the half of the queue worth a page. A release group ingest that spent
// its retries leaves a gap a user can only notice as an oddly incomplete
// discography, with the reason sitting unread in a table; most job kinds surface
// nowhere else at all.
func (api *API) listFailedJobs(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	rows, err := api.queue.ListFailedJobs(request.Context(), failedJobLimit)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	subjects := api.jobSubjects(request.Context(), failedJobIDs(rows))
	result := failedJobListResponse{Items: make([]failedJobResponse, 0, len(rows))}
	causes := newCauseIndex()
	for _, row := range rows {
		result.Total = row.Total
		cause := jobs.ClassifyMessage(row.ErrorMessage.String)
		failedAt := nullableTime(row.CompletedAt.Valid, row.CompletedAt.Time)
		causes.add(cause, failedAt)
		failure := failedJobResponse{
			ID:          row.ID,
			Kind:        row.Kind,
			Lane:        laneOf(row.Kind),
			Attempts:    row.Attempts,
			MaxAttempts: row.MaxAttempts,
			Error:       row.ErrorMessage.String,
			Cause:       cause.Key,
			FailedAt:    failedAt,
			CreatedAt:   nullableTime(row.CreatedAt.Valid, row.CreatedAt.Time),
			Payload:     json.RawMessage(row.Payload),
		}
		failure.describe(subjects[row.ID])
		result.Items = append(result.Items, failure)
	}
	result.Causes = causes.listed()
	result.Truncated = len(rows) == failedJobLimit && result.Total > int64(len(rows))
	api.writeJSON(response, http.StatusOK, result)
}

// causeIndex collects the failures into the causes behind them, keeping the span
// of dates each one covers.
type causeIndex struct {
	order []string
	byKey map[string]*failureCauseResponse
}

func newCauseIndex() *causeIndex {
	return &causeIndex{byKey: map[string]*failureCauseResponse{}}
}

func (index *causeIndex) add(cause jobs.Failure, failedAt *time.Time) {
	grouped, seen := index.byKey[cause.Key]
	if !seen {
		grouped = &failureCauseResponse{
			Cause:   cause.Key,
			Title:   cause.Title,
			Detail:  cause.Detail,
			Service: cause.Service,
		}
		index.byKey[cause.Key] = grouped
		index.order = append(index.order, cause.Key)
	}
	grouped.JobCount++
	if failedAt == nil {
		return
	}
	if grouped.FirstFailedAt == nil || failedAt.Before(*grouped.FirstFailedAt) {
		grouped.FirstFailedAt = failedAt
	}
	if grouped.LastFailedAt == nil || failedAt.After(*grouped.LastFailedAt) {
		grouped.LastFailedAt = failedAt
	}
}

// listed puts the biggest problem first, and the most recent of two problems of
// the same size above the older one. What the reader is looking for is which
// thing is wrong, and a cause holding seventy-one jobs is more of an answer to
// that than one holding one.
func (index *causeIndex) listed() []failureCauseResponse {
	listed := make([]failureCauseResponse, 0, len(index.order))
	for _, key := range index.order {
		listed = append(listed, *index.byKey[key])
	}
	slices.SortStableFunc(listed, func(first, second failureCauseResponse) int {
		if first.JobCount != second.JobCount {
			return second.JobCount - first.JobCount
		}
		switch {
		case first.LastFailedAt == nil || second.LastFailedAt == nil:
			return 0
		case first.LastFailedAt.After(*second.LastFailedAt):
			return -1
		case first.LastFailedAt.Before(*second.LastFailedAt):
			return 1
		default:
			return 0
		}
	})
	return listed
}

// jobSubjects asks what a page of jobs is about — one question for the page,
// never one per row. A queue view that spent a round trip per line would spend
// two hundred of them on a first scan, which is exactly the moment somebody is
// looking at it.
//
// A subject that could not be read is nothing rather than an error. This is the
// view an operator opens when they suspect the queue is stuck, and losing the
// whole of it because a join failed would take away the answer they came for;
// the rows still say their kind, which is all they said before there were
// subjects at all.
func (api *API) jobSubjects(
	ctx context.Context, jobIDs []uuid.UUID,
) map[uuid.UUID]db.JobSubjectsRow {
	if len(jobIDs) == 0 {
		return nil
	}
	rows, err := api.queue.JobSubjects(ctx, jobIDs)
	if err != nil {
		api.logger.Warn().Err(err).Int("jobs", len(jobIDs)).
			Msg("read what the queued jobs are about")
		return nil
	}
	subjects := make(map[uuid.UUID]db.JobSubjectsRow, len(rows))
	for _, row := range rows {
		subjects[row.ID] = row
	}
	return subjects
}

func activeJobIDs(rows []db.ListActiveJobsRow) []uuid.UUID {
	jobIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		jobIDs = append(jobIDs, row.ID)
	}
	return jobIDs
}

func failedJobIDs(rows []db.ListFailedJobsRow) []uuid.UUID {
	jobIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		jobIDs = append(jobIDs, row.ID)
	}
	return jobIDs
}

// describe says what the job is about. A job the query answered nothing for is
// left exactly as it was, which is what makes the row degrade to its bare kind
// rather than to a kind and a blank.
func (job *jobResponse) describe(subject db.JobSubjectsRow) {
	job.Subject = subject.Subject
	job.SubjectArtist = subject.SubjectArtist
	job.SubjectFileCount = fileCount(subject)
}

// describe is the same on a job that will not be tried again. A failure keeps
// its payload as well, because the payload is the evidence and outlives what it
// names; the subject is only what a person reads.
func (failure *failedJobResponse) describe(subject db.JobSubjectsRow) {
	failure.Subject = subject.Subject
	failure.SubjectArtist = subject.SubjectArtist
	failure.SubjectFileCount = fileCount(subject)
}

// fileCount keeps a count nothing recorded out of the answer, and keeps one
// that has nothing to qualify out too. Every count here is of files a job
// agreed to take — there is no job about none of them — and a count beside a
// row that cannot say what it is about would be a number attached to nothing.
func fileCount(subject db.JobSubjectsRow) *int32 {
	if subject.Subject == "" || subject.SubjectFileCount <= 0 {
		return nil
	}
	count := subject.SubjectFileCount
	return &count
}

// recurringJob is one of the passes that run on their own schedule, and what to
// say about it when the queue holds nothing for it.
//
// Idle is not a fallback time. Three of these four schedule themselves and the
// fourth is woken by the work it watches, so an installation with no queued row
// has a reason for it rather than a time nobody has written down yet — and
// inventing one would be this view telling the operator something the queue
// never said.
type recurringJob struct {
	kind  string
	label string
	idle  string
}

// recurringJobs are the passes, in the order they are worth reading: the one
// that follows work already in flight, then the passes that find work or keep
// the collection current, and finally the passes that improve its metadata.
//
// The genre sweep is here because its pace is the thing worth reading: it comes
// back every ten minutes while anything is still waiting, and every twelve
// hours once nothing is. It was not listed, so a sweep running six times an
// hour for a row it could never answer was invisible (issue #494).
var recurringJobs = []recurringJob{
	{
		kind:  jobs.PollDownloads,
		label: "Transfer poller",
		idle: "Nothing is being transferred. The poller runs only while a transfer " +
			"is moving, and the next one to start queues it again.",
	},
	{
		kind:  jobs.SweepAcquisitionTargets,
		label: "Acquisition sweep",
		idle: "No wanted recording is due. The sweep is queued again by the next " +
			"wishlist entry, and by a scan that settles one.",
	},
	{
		kind:  jobs.SweepFollowFeed,
		label: "Follow feed sweep",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepCoverArt,
		label: "Cover art sweep",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepLyrics,
		label: "Lyrics sweep",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepAudioSpectrum,
		label: "Audio measurement",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepPreviewAnchors,
		label: "Preview sweep",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.TagLibrary,
		label: "Tag write sweep",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepGenres,
		label: "Genre backfill",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepDuplicates,
		label: "Duplicate sweep",
		idle: "Not scheduled. This sweep queues its own replacement after it runs, " +
			"so nothing waiting means no duplicate pass is in progress.",
	},
	{
		kind:  jobs.SweepLoudness,
		label: "Loudness measurement",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepRecommendations,
		label: "Recommendation sweep",
		idle: "Not scheduled. This sweep runs only with an enabled ListenBrainz " +
			"account and queues its own replacement, so nothing waiting means the " +
			"account is disabled or no pass is running.",
	},
	{
		kind:  jobs.SyncListens,
		label: "Listening history sync",
		idle: "Not scheduled. This sync runs only with an enabled ListenBrainz " +
			"account and queues its own replacement every fifteen minutes, so " +
			"nothing waiting means the account is disabled.",
	},
	{
		kind:  jobs.SweepCopyFingerprints,
		label: "Copy fingerprint sweep",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepLibraryWitnesses,
		label: "Library witness sweep",
		idle:  "Not scheduled. This sweep queues one full-length fingerprint job per library file and asks for its replacement while files remain.",
	},
	{
		kind:  jobs.SweepUpgradeCandidates,
		label: "Upgrade sweep",
		idle: "Not scheduled. This sweep queues its own replacement and one is " +
			"queued at startup, so nothing waiting means no worker has run it yet.",
	},
	{
		kind:  jobs.SweepTranscode,
		label: "Transcode sweep",
		idle: "Not scheduled. This pass starts when a transcode scan is requested " +
			"and queues another pass only while files remain.",
	},
	{
		kind:  jobs.RefreshWeeklyPlaylist,
		label: "Weekly playlist refresh",
		idle: "Not scheduled. The refresh queues its own replacement after each " +
			"run, so nothing waiting means the weekly playlist is disabled or has " +
			"not been enabled.",
	},
	{
		kind:  jobs.RefreshNewReleasesPlaylist,
		label: "New releases refresh",
		idle: "Not scheduled. This refresh is queued after a feed pass or library " +
			"scan changes the list, so nothing waiting means neither has asked for it.",
	},
}

// recurringJobResponse is one pass and its pulse. NextRunAt is the queued row's
// due time and nothing else; where there is no row there is no time, and Detail
// says why rather than leaving the absence to be read as a stopped worker.
type recurringJobResponse struct {
	Kind            string     `json:"kind"`
	Label           string     `json:"label"`
	LastRunAt       *time.Time `json:"lastRunAt"`
	LastCompletedAt *time.Time `json:"lastCompletedAt"`
	Running         bool       `json:"running"`
	RunningSince    *time.Time `json:"runningSince,omitempty"`
	NextRunAt       *time.Time `json:"nextRunAt"`
	Detail          string     `json:"detail,omitempty"`
}

// jobSchedule answers with the recurring machinery's pulse: when each pass last
// ran and when it next intends to.
func (api *API) jobSchedule(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	kinds := make([]string, 0, len(recurringJobs))
	for _, recurring := range recurringJobs {
		kinds = append(kinds, recurring.kind)
	}
	rows, err := api.queue.RecurringJobPulse(request.Context(), kinds)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	pulse := make(map[string]db.RecurringJobPulseRow, len(rows))
	for _, row := range rows {
		pulse[row.Kind] = row
	}

	items := make([]recurringJobResponse, 0, len(recurringJobs))
	for _, recurring := range recurringJobs {
		row := pulse[recurring.kind]
		item := recurringJobResponse{
			Kind:            recurring.kind,
			Label:           recurring.label,
			LastRunAt:       nullableTime(row.LastFinishedAt.Valid, row.LastFinishedAt.Time),
			LastCompletedAt: nullableTime(row.LastCompletedAt.Valid, row.LastCompletedAt.Time),
			Running:         row.RunningSince.Valid,
			RunningSince:    nullableTime(row.RunningSince.Valid, row.RunningSince.Time),
			NextRunAt:       nullableTime(row.NextRunAfter.Valid, row.NextRunAfter.Time),
		}
		switch {
		case item.NextRunAt != nil:
			// The queue answered, and nothing needs explaining.
		case item.Running:
			// A running pass holds the one row the index allows, so there is
			// nothing queued behind it and no next time to report until it has
			// finished and asked for one.
			item.Detail = "Running now."
		default:
			item.Detail = recurring.idle
		}
		items = append(items, item)
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

// retriedJobResponse is what became of a press. Requeued is false when the job
// was already on its way, which is the second press and not a failure; Detail
// is that in a sentence.
type retriedJobResponse struct {
	Job      jobResponse `json:"job"`
	Requeued bool        `json:"requeued"`
	Detail   string      `json:"detail"`
}

// retryJob puts a job that spent its attempts back on the queue.
//
// It is the same move the recovery pass makes on the jobs a shutdown
// interrupted, by the same statement (db.RequeueFailedJob), and it is the whole
// of what this endpoint does: the job's attempts go back to nothing, so a retry
// is new work with the whole ladder ahead of it rather than one more run on a
// spent one, and what the job does when it runs is exactly what it would have
// done.
//
// Pressing twice costs the queue nothing. The second press finds a job that is
// already queued, already running, or already finished, changes none of them,
// and says which — a retry that stacked a second job behind the first, or
// pulled a running one back out of the worker's hands, would be a worse answer
// than the error it was avoiding.
func (api *API) retryJob(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	jobID, err := uuid.Parse(chi.URLParam(request, "jobID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid job id", nil)
		return
	}
	row, requeued, err := api.queue.RequeueFailedJob(request.Context(), jobID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "job not found", nil)
		return
	case errors.Is(err, db.ErrJobWasStopped):
		api.problem(response, http.StatusConflict, "this job was stopped rather than tried",
			[]string{"The work it belonged to was called off, so there is nothing " +
				"here that failed and nothing to run again."})
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	if requeued {
		api.logger.Info().
			Str("job_id", row.ID.String()).
			Str("kind", row.Kind).
			Int32("attempts", row.Attempts).
			Msg("a failed job was put back on the queue")
	}
	job := jobResponse{
		ID:          row.ID,
		Kind:        row.Kind,
		Lane:        laneOf(row.Kind),
		Status:      row.Status,
		Attempts:    row.Attempts,
		MaxAttempts: row.MaxAttempts,
		RunAfter:    nullableTime(row.RunAfter.Valid, row.RunAfter.Time),
		StartedAt:   nullableTime(row.StartedAt.Valid, row.StartedAt.Time),
		CreatedAt:   nullableTime(row.CreatedAt.Valid, row.CreatedAt.Time),
		Error:       row.ErrorMessage.String,
	}
	// The answer to a press is one job, so this is the one place a subject is
	// read a row at a time. It is asked for the same reason the list asks: the
	// row the press came from is redrawn from this, and a job that had a name
	// before it was retried must not lose it by being retried.
	subjects := api.jobSubjects(request.Context(), []uuid.UUID{row.ID})
	job.describe(subjects[row.ID])
	api.writeJSON(response, http.StatusOK, retriedJobResponse{
		Job:      job,
		Requeued: requeued,
		Detail:   retryDetail(requeued, row.Status),
	})
}

// cancelledJobResponse is what became of a press on Cancel. Cancelled is false
// when nothing queued was found under that id — the second press, or a job
// that started running in the moment between the two — which is not a
// failure.
type cancelledJobResponse struct {
	Job       jobResponse `json:"job"`
	Cancelled bool        `json:"cancelled"`
	Detail    string      `json:"detail"`
}

// cancelJob removes one job before a worker ever claims it, by
// db.CancelQueuedJob: the row is deleted rather than marked, because nothing
// about a queued job that never ran is worth keeping.
//
// Only a queued row is touched. A running job is being run by a worker and
// stopping it is a different problem; a completed or failed one is history.
// This can remove a recurring sweep's own scheduled row — the one queued copy
// of itself a sweep keeps between passes — and that is allowed here, because
// a press on one row is a person choosing that row deliberately. The bulk
// control below is the one that must never be able to do that by accident.
func (api *API) cancelJob(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	jobID, err := uuid.Parse(chi.URLParam(request, "jobID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid job id", nil)
		return
	}
	row, cancelled, err := api.queue.CancelQueuedJob(request.Context(), jobID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "job not found", nil)
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	if cancelled {
		api.logger.Info().
			Str("job_id", row.ID.String()).
			Str("kind", row.Kind).
			Msg("a queued job was cancelled")
	}
	job := jobResponse{
		ID:          row.ID,
		Kind:        row.Kind,
		Lane:        laneOf(row.Kind),
		Status:      row.Status,
		Attempts:    row.Attempts,
		MaxAttempts: row.MaxAttempts,
		RunAfter:    nullableTime(row.RunAfter.Valid, row.RunAfter.Time),
		StartedAt:   nullableTime(row.StartedAt.Valid, row.StartedAt.Time),
		CreatedAt:   nullableTime(row.CreatedAt.Valid, row.CreatedAt.Time),
		Error:       row.ErrorMessage.String,
	}
	if !cancelled {
		// The row still exists — a subject is worth reading. A cancelled row is
		// gone, and there is nothing left to join it against.
		subjects := api.jobSubjects(request.Context(), []uuid.UUID{row.ID})
		job.describe(subjects[row.ID])
	}
	api.writeJSON(response, http.StatusOK, cancelledJobResponse{
		Job:       job,
		Cancelled: cancelled,
		Detail:    cancelJobDetail(cancelled, row.Status),
	})
}

// cancelJobDetail says what the press found, in the words the person who
// pressed it would use.
func cancelJobDetail(cancelled bool, status string) string {
	if cancelled {
		return "Cancelled. It will not run."
	}
	if status == "running" {
		return "Already running. Nothing was changed."
	}
	return "Already finished. Nothing was changed."
}

// retriedCauseResponse is what became of a press on "Retry all". Requeued is
// how many jobs moved, which is not always how many were asked for: a job that
// was retried a second ago is already on its way and is left alone.
type retriedCauseResponse struct {
	Cause    string `json:"cause"`
	Asked    int    `json:"asked"`
	Requeued int64  `json:"requeued"`
	Detail   string `json:"detail"`
}

// retryFailedCause puts every job that one cause stopped back on the queue.
//
// It is the one action the failed-jobs screen offers, and it is one press rather
// than seventy-one because the failures are one fact rather than seventy-one.
// Which jobs a cause holds is decided here, by classifying each recorded error
// exactly as the list that was read did, so the button retries what the row it
// sits on says it will.
//
// Each job goes back by the statement a single retry uses: attempts back to
// nothing, the whole ladder ahead of it, and a job that is already queued,
// running or finished left exactly where it is.
func (api *API) retryFailedCause(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	var input struct {
		Cause string `json:"cause"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "the request could not be read",
			[]string{"Name the cause to retry, as the failed list gave it."})
		return
	}
	cause := strings.TrimSpace(input.Cause)
	if cause == "" {
		api.problem(response, http.StatusBadRequest, "no cause was named", nil)
		return
	}
	rows, err := api.queue.ListFailedJobs(request.Context(), causeRetryLimit)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	jobIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if jobs.ClassifyMessage(row.ErrorMessage.String).Key == cause {
			jobIDs = append(jobIDs, row.ID)
		}
	}
	requeued, err := api.queue.RequeueFailedJobs(request.Context(), jobIDs)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if requeued > 0 {
		api.logger.Info().
			Str("cause", cause).
			Int("asked", len(jobIDs)).
			Int64("requeued", requeued).
			Msg("every job one cause had stopped was put back on the queue")
	}
	api.writeJSON(response, http.StatusOK, retriedCauseResponse{
		Cause:    cause,
		Asked:    len(jobIDs),
		Requeued: requeued,
		Detail:   causeRetryDetail(len(jobIDs), requeued),
	})
}

// cancelledKindResponse is what became of a press on Cancel all. Cancelled is
// how many rows moved, which answers zero both when nothing of that kind was
// queued and when the kind named a recurring sweep's own schedule — Refused
// says which.
type cancelledKindResponse struct {
	Kind      string `json:"kind"`
	Cancelled int64  `json:"cancelled"`
	Refused   bool   `json:"refused"`
	Detail    string `json:"detail"`
}

// cancelQueuedJobsByKind removes every queued job of one kind — the shape a
// runaway sweep takes, clearing a backlog with one press where the only
// remedy used to be a DELETE FROM jobs through a port-forward and a wait, job
// by job, at whatever pace the queue drains (#492).
//
// A kind that is one of the recurring sweeps' own schedule is refused by
// db.CancelQueuedJobsByKind itself, which is where the guard has to live: that
// statement is the only thing standing between this endpoint and deleting the
// one queued row a sweep keeps of itself, and a check written only here would
// be a second copy of the rule for anyone calling the query directly to miss.
// This handler reads db.ScheduledSweepKinds itself only to say why, in the
// response, when it was that rule rather than an empty queue that answered
// zero.
func (api *API) cancelQueuedJobsByKind(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	var input struct {
		Kind string `json:"kind"`
	}
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "the request could not be read",
			[]string{"Name the kind to cancel, as the queue gave it."})
		return
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		api.problem(response, http.StatusBadRequest, "no kind was named", nil)
		return
	}
	cancelled, err := api.queue.CancelQueuedJobsByKind(request.Context(), kind)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if cancelled > 0 {
		api.logger.Info().
			Str("kind", kind).
			Int64("cancelled", cancelled).
			Msg("every queued job of one kind was cancelled")
	}
	refused := cancelled == 0 && slices.Contains(db.ScheduledSweepKinds, kind)
	api.writeJSON(response, http.StatusOK, cancelledKindResponse{
		Kind:      kind,
		Cancelled: cancelled,
		Refused:   refused,
		Detail:    cancelKindDetail(cancelled, refused),
	})
}

// cancelKindDetail says what the press found, in the words the person who
// pressed it would use.
func cancelKindDetail(cancelled int64, refused bool) string {
	switch {
	case refused:
		return "That kind runs on its own schedule and cannot be bulk-cancelled. " +
			"Cancel it from its own row instead."
	case cancelled == 0:
		return "Nothing of that kind was queued. Nothing was changed."
	case cancelled == 1:
		return "One job was cancelled."
	default:
		return fmt.Sprintf("%d jobs were cancelled.", cancelled)
	}
}

// causeRetryDetail says what the press did. It counts what moved rather than
// what was asked for, because the difference is the ordinary one: somebody
// pressed twice, or a job of that cause had already been retried on its own.
func causeRetryDetail(asked int, requeued int64) string {
	switch {
	case asked == 0:
		return "Nothing is failing for that reason now. Nothing was changed."
	case requeued == 0:
		return "Those jobs are already on their way. Nothing was changed."
	case requeued == 1:
		return "One job was queued again, with all of its attempts back."
	case int64(asked) == requeued:
		return fmt.Sprintf("%d jobs were queued again, with all of their attempts back.", requeued)
	default:
		return fmt.Sprintf(
			"%d jobs were queued again, with all of their attempts back. The other %d were already on their way.",
			requeued, int64(asked)-requeued)
	}
}

// retryDetail says what the press did, in the words the person who pressed it
// would use.
func retryDetail(requeued bool, status string) string {
	if requeued {
		return "Queued again, with all of its attempts back."
	}
	switch status {
	case "running":
		return "Already running. Nothing was changed."
	case "queued":
		return "Already queued. Nothing was changed."
	case "failed":
		// A job that was running when the press arrived and failed in the moment
		// between the two questions. Nothing was requeued, and saying it had
		// finished would be the one sentence here that is not true.
		return "It has failed since. Nothing was changed; retry it again."
	default:
		return "Already finished. Nothing was changed."
	}
}

// laneOf names the lane a kind is claimed on. It reads the worker's own lists
// rather than keeping one of its own, so a kind added to a lane there does not
// have to be added here as well.
func laneOf(kind string) string {
	switch {
	case slices.Contains(jobs.AcquisitionKinds, kind):
		return laneAcquisition
	case slices.Contains(jobs.TransferKinds, kind):
		return laneTransfers
	case slices.Contains(jobs.AnchorKinds, kind):
		return laneAnchors
	case slices.Contains(jobs.JudgingKinds, kind):
		return laneJudging
	default:
		return laneGeneral
	}
}
