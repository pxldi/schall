package server

import (
	"net/http"
	"time"

	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/library"
)

// transcodeSweepResponse is a pass over the library's audio, in the shape the
// settings page reads it: whether one is running, what it has done, and how
// much of the library the policy stored right now still asks for.
type transcodeSweepResponse struct {
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Transcoded  int        `json:"transcoded"`
	Skipped     int        `json:"skipped"`
	Failed      int        `json:"failed"`
	// Eligible is every present, proven file the stored policy would still
	// shrink. It moves with the setting: turning it off, changing the bit
	// rate, or turning it on for the first time all change what this counts.
	Eligible int `json:"eligible"`
}

func transcodeSweep(status library.TranscodeStatus) transcodeSweepResponse {
	return transcodeSweepResponse{
		Status:      status.Status,
		Error:       status.Error,
		StartedAt:   status.StartedAt,
		CompletedAt: status.CompletedAt,
		Transcoded:  status.Run.Transcoded,
		Skipped:     status.Run.Skipped,
		Failed:      status.Run.Failed,
		Eligible:    status.Eligible,
	}
}

// getTranscodeSweep answers with the most recent pass over the library's
// audio, or with an idle one where none has ever been asked for.
func (api *API) getTranscodeSweep(response http.ResponseWriter, request *http.Request) {
	if !api.transcodeSweepAvailable(response) {
		return
	}
	status, err := api.shrink.LatestSweep(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, transcodeSweep(status))
}

// queueTranscodeSweep sets a pass going. The work is a job: a library is tens
// of thousands of files and each one shrunk is a whole decode and encode, so
// nobody is holding a browser connection open for it.
func (api *API) queueTranscodeSweep(response http.ResponseWriter, request *http.Request) {
	if !api.transcodeSweepAvailable(response) {
		return
	}
	job, err := api.shrink.QueueSweep(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// The worker may be minutes from claiming this, and an interface that is
	// not the one that pressed the button would otherwise show nothing until it
	// started.
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusAccepted, refreshResponse{
		JobID: job.ID, Status: job.Status, CreatedAt: job.CreatedAt,
	})
}

func (api *API) transcodeSweepAvailable(response http.ResponseWriter) bool {
	if api.shrink == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"shrinking the library is not configured", nil)
		return false
	}
	return true
}
