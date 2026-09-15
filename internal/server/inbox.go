package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
)

// howManyCleanups is how many passes the settings screen is shown. It reads the
// most recent one and keeps the rest as history.
const howManyCleanups = 10

// inboxCleanupResponse is one pass over the download inbox, in the shape the
// settings screen reads it: what state it is in, and what each class came to.
type inboxCleanupResponse struct {
	ID          uuid.UUID  `json:"id"`
	RequestedBy string     `json:"requestedBy,omitempty"`
	RequestedAt time.Time  `json:"requestedAt"`
	DryRun      bool       `json:"dryRun"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
	// Classes is every class in a fixed order, the four that delete first, so
	// the table on screen does not reorder itself between two passes.
	Classes []inboxCleanupClass `json:"classes"`
	// The two totals the buttons are labelled from.
	DeleteFiles int   `json:"deleteFiles"`
	DeleteBytes int64 `json:"deleteBytes"`
	KeptFiles   int   `json:"keptFiles"`
	KeptBytes   int64 `json:"keptBytes"`
	// FailedFiles is what the pass chose and could not remove. They are still
	// in the inbox, and Error is the sentence saying so.
	FailedFiles int `json:"failedFiles"`
}

type inboxCleanupClass struct {
	Class   string `json:"class"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Failed  int    `json:"failed"`
	Deletes bool   `json:"deletes"`
}

func inboxCleanup(row db.InboxCleanupRow) inboxCleanupResponse {
	response := inboxCleanupResponse{
		ID:          row.ID,
		RequestedBy: row.RequestedBy,
		RequestedAt: row.RequestedAt,
		DryRun:      row.DryRun,
		Status:      "queued",
		StartedAt:   row.StartedAt,
		FinishedAt:  row.FinishedAt,
		Error:       row.Error,
		Classes:     make([]inboxCleanupClass, 0, len(db.InboxDeleteClasses)+len(db.InboxKeepClasses)),
	}
	switch {
	case row.Error != "":
		response.Status = "failed"
	case row.FinishedAt != nil:
		response.Status = "finished"
	case row.StartedAt != nil:
		response.Status = "running"
	}
	for _, class := range db.InboxDeleteClasses {
		count := row.Counts[class]
		response.Classes = append(response.Classes, inboxCleanupClass{
			Class: class, Files: count.Files, Bytes: count.Bytes,
			Failed: count.Failed, Deletes: true,
		})
		response.DeleteFiles += count.Files
		response.DeleteBytes += count.Bytes
		response.FailedFiles += count.Failed
	}
	for _, class := range db.InboxKeepClasses {
		count := row.Counts[class]
		response.Classes = append(response.Classes, inboxCleanupClass{
			Class: class, Files: count.Files, Bytes: count.Bytes,
		})
		response.KeptFiles += count.Files
		response.KeptBytes += count.Bytes
	}
	return response
}

type inboxCleanupRequest struct {
	// DryRun classifies every file in the inbox and deletes none of them.
	DryRun bool `json:"dryRun"`
}

// createInboxCleanup asks for one pass over the download inbox. The work is a
// job: the inbox holds tens of thousands of files across a thousand folders, so
// nobody is holding a browser connection open for it.
func (api *API) createInboxCleanup(response http.ResponseWriter, request *http.Request) {
	if !api.inboxCleanupsAvailable(response) {
		return
	}
	var input inboxCleanupRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	// Deleting somebody's music is a person's decision, and the row says whose.
	actor := ActorOf(request.Context()).Name
	row, err := api.cleanups.QueueInboxCleanup(request.Context(), actor, input.DryRun, time.Now())
	switch {
	case errors.Is(err, db.ErrInboxCleanupRunning):
		api.problem(response, http.StatusConflict, "a pass over the inbox is already running",
			[]string{err.Error(), "Wait for it to finish, then ask again."})
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("cleanup_id", row.ID.String()).
		Bool("dry_run", row.DryRun).Str("requested_by", actor).
		Msg("a pass over the download inbox was asked for")
	// The worker may be a moment from claiming this, and an interface that is
	// not the one that pressed the button would otherwise show nothing.
	api.events.Publish(events.TopicDownloads)
	api.writeJSON(response, http.StatusAccepted, inboxCleanup(row))
}

// listInboxCleanups answers with the most recent passes. The file rows are the
// record and are deliberately not listed here: one pass holds tens of thousands
// of them.
func (api *API) listInboxCleanups(response http.ResponseWriter, request *http.Request) {
	if !api.inboxCleanupsAvailable(response) {
		return
	}
	rows, err := api.cleanups.ListInboxCleanups(request.Context(), howManyCleanups)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items := make([]inboxCleanupResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, inboxCleanup(row))
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (api *API) getInboxCleanup(response http.ResponseWriter, request *http.Request) {
	if !api.inboxCleanupsAvailable(response) {
		return
	}
	cleanupID, err := uuid.Parse(chi.URLParam(request, "cleanupID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "no pass over the inbox with that ID", nil)
		return
	}
	row, err := api.cleanups.InboxCleanup(request.Context(), cleanupID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "no pass over the inbox with that ID", nil)
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, inboxCleanup(row))
}

func (api *API) inboxCleanupsAvailable(response http.ResponseWriter) bool {
	if api.cleanups == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"cleaning the download inbox is not configured", nil)
		return false
	}
	return true
}
