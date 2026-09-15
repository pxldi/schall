package server

import (
	"net/http"
	"time"

	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/library"
)

// libraryTagsResponse is a pass over the library's tags as the settings page
// reads it: whether one is running, what it has done, and how many files it has
// to get through.
type libraryTagsResponse struct {
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Written     int        `json:"written"`
	Unchanged   int        `json:"unchanged"`
	Skipped     int        `json:"skipped"`
	Failed      int        `json:"failed"`
	// Why the skipped files were left alone: a file that answers the same
	// recording on more than one release, and a file the catalogue holds too
	// little about to describe. The page names the reasons that happened rather
	// than guessing at one for the total.
	SkippedAmbiguous int `json:"skippedAmbiguous"`
	SkippedUnproved  int `json:"skippedUnproved"`
	// Eligible is every file mapped to a catalogue track, which is what a pass
	// covers. Files mapped to nothing are not counted here because they are not
	// read, not copied and not touched.
	Eligible int `json:"eligible"`
}

func libraryTags(status library.TagStatus) libraryTagsResponse {
	return libraryTagsResponse{
		Status:      status.Status,
		Error:       status.Error,
		StartedAt:   status.StartedAt,
		CompletedAt: status.CompletedAt,
		Written:     status.Run.Written,
		Unchanged:   status.Run.Unchanged,
		Skipped:     status.Run.Skipped,
		Failed:      status.Run.Failed,

		SkippedAmbiguous: status.Run.SkippedAmbiguous,
		SkippedUnproved:  status.Run.SkippedUnproved,
		Eligible:         status.Eligible,
	}
}

// getLibraryTags answers with the most recent pass, or with an idle one where
// none has ever been asked for. Never having written the library's tags is the
// ordinary state of an installation, not an absence worth an error.
func (api *API) getLibraryTags(response http.ResponseWriter, request *http.Request) {
	if !api.taggerAvailable(response) {
		return
	}
	status, err := api.tagger.LatestTagging(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, libraryTags(status))
}

// queueLibraryTags sets a pass going. The work is a job: a library is tens of
// thousands of files and each one is copied to be written, so nobody is holding
// a browser connection open for it.
func (api *API) queueLibraryTags(response http.ResponseWriter, request *http.Request) {
	if !api.taggerAvailable(response) {
		return
	}
	job, err := api.tagger.QueueTagging(request.Context())
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

func (api *API) taggerAvailable(response http.ResponseWriter) bool {
	if api.tagger == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"writing the library's tags is not configured", nil)
		return false
	}
	return true
}
