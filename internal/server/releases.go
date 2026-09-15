package server

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/events"
)

// selectionCap bounds how many releases one press may decide about.
//
// The Releases browser holds sixty thousand rows, and a selection is a list of
// identifiers the server checks rather than a filter it runs again — so the
// list has to have an end. This is the same number the source-search run
// already allows, because it is the same selection bar with more buttons on it,
// and a cap that differed per button would be a rule nobody could hold in their
// head.
const selectionCap = 50

// releaseSelection is what all three bulk presses are given: the releases the
// user ticked, by identifier.
//
// Identifiers rather than the filter that produced them, deliberately. A filter
// re-run on the server is a different set of releases from the one the user was
// looking at — rows move between "partial" and "complete" while somebody reads
// the page — and the three things this decides are not things to do to a set
// nobody saw.
type releaseSelection struct {
	ReleaseIDs []uuid.UUID `json:"releaseIds"`
}

// The three answers, one row each.
//
// Every one of them says what happened per release rather than reporting a
// total, because that is the only honest sentence: "ignored 4, 2 already
// ignored" is true of a selection of six, and "6 releases ignored" is not.
type (
	ignoredReleasesResponse struct {
		Releases []dismissAlbumResponse `json:"releases"`
		// NotFound names the releases that were gone by the time the press
		// arrived. A selection is read from a page that was drawn a moment ago.
		NotFound []uuid.UUID `json:"notFound"`
	}

	rematchedRelease struct {
		ReleaseID uuid.UUID `json:"releaseId"`
		// Queued is false where a pass was already queued or already running
		// for this release, which is the ordinary answer to pressing twice.
		Queued bool `json:"queued"`
	}

	rematchedReleasesResponse struct {
		Releases []rematchedRelease `json:"releases"`
		// Unaskable names the releases there is nothing to ask MusicBrainz
		// about: one the catalogue no longer holds, and one with no release
		// group. Kept apart from a pass that is merely already running, because
		// those read the same and are not the same.
		Unaskable []uuid.UUID `json:"unaskable"`
	}

	retriedRelease struct {
		ReleaseID uuid.UUID `json:"releaseId"`
		// Retried is how many of this release's failed jobs went back on the
		// queue. Zero is a release with nothing that failed, which is the usual
		// answer for most of a selection.
		Retried int `json:"retried"`
	}

	retriedReleasesResponse struct {
		Releases []retriedRelease `json:"releases"`
	}
)

// ignoreReleases records not-wanted for every missing track of every selected
// release.
//
// It is the release-level sibling of the track-scoped decision, applied to many
// releases at once, and it writes exactly what the single-release press writes:
// the same track-scoped rows, permanent, and taken back one track at a time.
// The Missing view and the completeness counts read those rows already, so a
// release ignored here leaves both without anything new being taught to either.
func (api *API) ignoreReleases(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	selection, ok := api.releaseSelection(response, request)
	if !ok {
		return
	}
	result := ignoredReleasesResponse{
		Releases: make([]dismissAlbumResponse, 0, len(selection)),
		NotFound: []uuid.UUID{},
	}
	for _, albumID := range selection {
		one, err := api.dismissRelease(request.Context(), albumID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Gone between the page being drawn and the press. One release the
			// catalogue no longer holds does not stop the other forty-nine.
			result.NotFound = append(result.NotFound, albumID)
		case err != nil:
			api.internalError(response, request, err)
			return
		default:
			result.Releases = append(result.Releases, one)
		}
	}
	api.events.Publish(events.TopicCatalogue)
	api.events.Publish(events.TopicAcquisitions)
	api.writeJSON(response, http.StatusOK, result)
}

// rematchReleases asks for each selected release to be looked at again.
//
// What it queues is the pass a release already gets when it is first taken in:
// its track list fetched from MusicBrainz once more, and the library examined
// against it. Nothing about how that pass decides changes here — this only asks
// for it, which is the whole reason a bulk button over it is safe.
//
// One pass per release. A release already queued or already running keeps the
// row it has and is reported as not queued, so pressing twice says so rather
// than doubling the work.
func (api *API) rematchReleases(response http.ResponseWriter, request *http.Request) {
	selection, ok := api.releaseSelection(response, request)
	if !ok {
		return
	}
	result := rematchedReleasesResponse{
		Releases:  make([]rematchedRelease, 0, len(selection)),
		Unaskable: []uuid.UUID{},
	}
	for _, albumID := range selection {
		queued, askable, err := api.store.QueueAlbumRefresh(request.Context(), albumID)
		if err != nil {
			api.internalError(response, request, err)
			return
		}
		if !askable {
			result.Unaskable = append(result.Unaskable, albumID)
			continue
		}
		result.Releases = append(result.Releases, rematchedRelease{ReleaseID: albumID, Queued: queued})
	}
	api.logger.Info().Int("releases", len(selection)).Msg("releases queued to be matched again")
	api.events.Publish(events.TopicCatalogue)
	api.writeJSON(response, http.StatusOK, result)
}

// retryReleases puts each selected release's failed jobs back on the queue.
//
// The jobs are the ones the Releases screen's "Problems" filter already reads:
// the pass that fetches a release's track list, and the run that searches
// sources for it. Nothing else is touched, and a release with nothing that
// failed answers zero rather than being left out of the reply — a selection of
// forty where two had problems is a true answer of forty rows.
func (api *API) retryReleases(response http.ResponseWriter, request *http.Request) {
	if !api.queueAvailable(response) {
		return
	}
	selection, ok := api.releaseSelection(response, request)
	if !ok {
		return
	}
	failed, err := api.queue.FailedReleaseJobs(request.Context(), selection)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := retriedReleasesResponse{Releases: make([]retriedRelease, 0, len(selection))}
	for _, albumID := range selection {
		jobIDs := failed[albumID]
		if len(jobIDs) == 0 {
			result.Releases = append(result.Releases, retriedRelease{ReleaseID: albumID})
			continue
		}
		moved, err := api.queue.RequeueFailedJobs(request.Context(), jobIDs)
		if err != nil {
			api.internalError(response, request, err)
			return
		}
		result.Releases = append(result.Releases,
			retriedRelease{ReleaseID: albumID, Retried: int(moved)})
	}
	api.logger.Info().Int("releases", len(selection)).Msg("failed release jobs put back on the queue")
	api.events.Publish(events.TopicCatalogue)
	api.writeJSON(response, http.StatusOK, result)
}

// releaseSelection reads and checks the list of releases a press is about.
//
// Everything a bulk press can refuse is refused here, in one place and in one
// sentence each: a body that is not the shape asked for, an empty selection,
// one past the cap, and an identifier that is not one.
func (api *API) releaseSelection(
	response http.ResponseWriter, request *http.Request,
) ([]uuid.UUID, bool) {
	var body releaseSelection
	if err := decodeJSON(response, request, &body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return nil, false
	}
	if len(body.ReleaseIDs) == 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"Select at least one release."})
		return nil, false
	}
	if len(body.ReleaseIDs) > selectionCap {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"Select no more than 50 releases at a time."})
		return nil, false
	}
	// The same release ticked twice is one release. Ignoring would be harmless
	// asked twice; asking for two refresh passes would not be.
	seen := make(map[uuid.UUID]struct{}, len(body.ReleaseIDs))
	selection := make([]uuid.UUID, 0, len(body.ReleaseIDs))
	for _, id := range body.ReleaseIDs {
		if id == uuid.Nil {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"One of the releases is not a release."})
			return nil, false
		}
		if _, repeat := seen[id]; repeat {
			continue
		}
		seen[id] = struct{}{}
		selection = append(selection, id)
	}
	return selection, true
}
