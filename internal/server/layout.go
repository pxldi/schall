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
	"github.com/pxldi/schall/internal/library"
)

// libraryLayoutResponse reports the template together with a worked example of
// what it files, because a template is only readable once you can see where a
// release lands under it. The example is rendered by the same code an import
// would use, so it cannot describe a layout the importer would not write.
type libraryLayoutResponse struct {
	Template  string     `json:"template"`
	Default   string     `json:"default"`
	Example   string     `json:"example"`
	IsDefault bool       `json:"isDefault"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// layoutExample is one invented release, used only to show the shape of a
// template. It names nothing in anybody's library on purpose.
var layoutExample = library.Fields{
	Artist: "Boards of Canada",
	Album:  "Geogaddi",
	ID:     "a3f19c2b",
}

func (api *API) layoutResponse(row db.LibraryLayoutRow, stored bool) libraryLayoutResponse {
	response := libraryLayoutResponse{
		Template:  row.Template,
		Default:   library.DefaultTemplate,
		IsDefault: row.Template == library.DefaultTemplate,
	}
	if stored {
		response.UpdatedAt = nullableTime(row.UpdatedAt.Valid, row.UpdatedAt.Time)
	}
	// A stored template always parses — it was parsed before it was stored —
	// so a failure here is a template that reached the table another way, and
	// an example nobody can render is better than one that is invented.
	if template, err := library.ParseTemplate(row.Template); err == nil {
		response.Example = template.Render(layoutExample)
	}
	return response
}

// getLibraryLayout answers with the stored template, or with the default when
// nothing has departed from it. An installation that has never opened this
// setting still has a layout; what it does not have is a row.
func (api *API) getLibraryLayout(response http.ResponseWriter, request *http.Request) {
	if !api.layoutAvailable(response) {
		return
	}
	row, err := api.layout.LibraryLayoutSettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.writeJSON(response, http.StatusOK,
			api.layoutResponse(db.LibraryLayoutRow{Template: library.DefaultTemplate}, false))
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, api.layoutResponse(row, true))
}

type libraryLayoutRequest struct {
	Template string `json:"template"`
}

// saveLibraryLayout stores a template, having first proved it can be rendered.
//
// Nothing moves. The layout describes where Schall files music it imports from
// now on; the library on disc is left exactly where it is until a migration is
// asked for, and a setting that quietly reorganised sixty thousand files
// behind a save button would be the worst possible way to learn that.
func (api *API) saveLibraryLayout(response http.ResponseWriter, request *http.Request) {
	if !api.layoutAvailable(response) {
		return
	}
	var input libraryLayoutRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	template, err := library.ParseTemplate(input.Template)
	if err != nil {
		api.problem(response, http.StatusUnprocessableEntity, "invalid layout template",
			[]string{err.Error()})
		return
	}
	row, err := api.layout.SaveLibraryLayoutSettings(request.Context(), template.String())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("template", row.Template).Msg("library layout template saved")
	api.writeJSON(response, http.StatusOK, api.layoutResponse(row, true))
}

func (api *API) layoutAvailable(response http.ResponseWriter) bool {
	if api.layout == nil {
		api.problem(response, http.StatusServiceUnavailable, "the library layout is not configured", nil)
		return false
	}
	return true
}

// layoutRunResponse is a migration as the settings page reads it: what is left,
// what happened, and enough of the rows to judge it by.
type layoutRunResponse struct {
	RunID     uuid.UUID         `json:"runId"`
	PlannedAt time.Time         `json:"plannedAt"`
	Planned   int               `json:"planned"`
	Moved     int               `json:"moved"`
	Failed    int               `json:"failed"`
	Reverted  int               `json:"reverted"`
	Active    bool              `json:"active"`
	Moves     []layoutMoveEntry `json:"moves"`
	// Plan is what planning decided, including what it left where it was. It is
	// answered by the plan itself and by nothing afterwards: the journal records
	// moves, and a file that is not moving leaves no row to count later.
	Plan *layoutPlanSummary `json:"plan,omitempty"`
}

type layoutMoveEntry struct {
	ID       uuid.UUID  `json:"id"`
	FileID   uuid.UUID  `json:"libraryFileId"`
	FromPath string     `json:"fromPath"`
	ToPath   string     `json:"toPath"`
	Status   string     `json:"status"`
	Error    string     `json:"error,omitempty"`
	MovedAt  *time.Time `json:"movedAt,omitempty"`
}

type layoutPlanSummary struct {
	Planned   int `json:"planned"`
	Colliding int `json:"colliding"`
	InPlace   int `json:"inPlace"`
	Unfiled   int `json:"unfiled"`
	Absent    int `json:"absent"`
}

func layoutRun(summary library.RunSummary) layoutRunResponse {
	response := layoutRunResponse{
		RunID:     summary.RunID,
		PlannedAt: summary.PlannedAt,
		Planned:   summary.Planned,
		Moved:     summary.Moved,
		Failed:    summary.Failed,
		Reverted:  summary.Reverted,
		Active:    summary.Remaining(),
		Moves:     make([]layoutMoveEntry, 0, len(summary.Moves)),
	}
	for _, move := range summary.Moves {
		response.Moves = append(response.Moves, layoutMoveEntry{
			ID: move.ID, FileID: move.LibraryFileID, FromPath: move.FromPath,
			ToPath: move.ToPath, Status: move.Status, Error: move.Error,
			MovedAt: move.MovedAt,
		})
	}
	return response
}

// getLayoutRun answers with the most recent migration, or with nothing at all
// when none has ever been planned. Nothing is not an error: a library that was
// never migrated is the ordinary state of one.
func (api *API) getLayoutRun(response http.ResponseWriter, request *http.Request) {
	if !api.moverAvailable(response) {
		return
	}
	summary, err := api.mover.LatestRun(request.Context())
	if errors.Is(err, library.ErrRunNotFound) {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, layoutRun(summary))
}

// planLayoutRun writes down what the current template would do with the library
// and touches nothing. Reading it is the point: a migration is agreed to before
// it happens, not explained afterwards.
func (api *API) planLayoutRun(response http.ResponseWriter, request *http.Request) {
	if !api.moverAvailable(response) {
		return
	}
	plan, err := api.mover.Plan(request.Context())
	if errors.Is(err, library.ErrRunOpen) {
		api.problem(response, http.StatusConflict, "a layout migration is already planned",
			[]string{err.Error()})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	summary, err := api.mover.Run(request.Context(), plan.RunID)
	if errors.Is(err, library.ErrRunNotFound) {
		// A plan that would move nothing is not a run: there is no journal for
		// it, because a journal of nothing records nothing.
		api.writeJSON(response, http.StatusOK, layoutRunResponse{Plan: planSummary(plan)})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	answer := layoutRun(summary)
	answer.Plan = planSummary(plan)
	api.writeJSON(response, http.StatusCreated, answer)
}

func planSummary(plan library.PlanResult) *layoutPlanSummary {
	return &layoutPlanSummary{
		Planned: plan.Planned, Colliding: plan.Colliding, InPlace: plan.InPlace,
		Unfiled: plan.Unfiled, Absent: plan.Absent,
	}
}

// applyLayoutRun sets the renames going. The work is a job: a library is tens
// of thousands of files and nobody is holding a browser connection open for it.
func (api *API) applyLayoutRun(response http.ResponseWriter, request *http.Request) {
	if !api.moverAvailable(response) {
		return
	}
	runID, ok := api.layoutRunID(response, request)
	if !ok {
		return
	}
	summary, err := api.mover.Run(request.Context(), runID)
	if errors.Is(err, library.ErrRunNotFound) {
		api.problem(response, http.StatusNotFound, "layout migration not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if !summary.Remaining() {
		api.problem(response, http.StatusConflict, "that layout migration has nothing left to move", nil)
		return
	}
	if err := api.mover.QueueApply(request.Context(), runID); err != nil {
		api.internalError(response, request, err)
		return
	}
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusAccepted, layoutRun(summary))
}

// discardLayoutRun throws away what a run planned and never did, which is also
// what frees the files for a plan somebody would rather have. What was moved
// and what was refused stay: those happened.
func (api *API) discardLayoutRun(response http.ResponseWriter, request *http.Request) {
	if !api.moverAvailable(response) {
		return
	}
	runID, ok := api.layoutRunID(response, request)
	if !ok {
		return
	}
	summary, err := api.mover.Discard(request.Context(), runID)
	if errors.Is(err, library.ErrRunNotFound) {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusOK, layoutRun(summary))
}

func (api *API) layoutRunID(response http.ResponseWriter, request *http.Request) (uuid.UUID, bool) {
	runID, err := uuid.Parse(chi.URLParam(request, "runID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "layout migration not found", nil)
		return uuid.Nil, false
	}
	return runID, true
}

func (api *API) moverAvailable(response http.ResponseWriter) bool {
	if api.mover == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"migrating the library to the layout is not configured", nil)
		return false
	}
	return true
}
