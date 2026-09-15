package server

import (
	"context"
	"net/http"

	"github.com/pxldi/schall/internal/db"
)

// DuplicateResolution says whether Schall settles the recordings the library
// holds more than once by itself, and lets a person change that.
type DuplicateResolution interface {
	Settings(ctx context.Context) (db.DuplicateResolutionSettingsRow, error)
	SaveSettings(ctx context.Context, enabled bool) (db.DuplicateResolutionSettingsRow, error)
}

// WithDuplicateResolution names the store behind that setting.
func WithDuplicateResolution(resolution DuplicateResolution) Option {
	return func(api *API) { api.duplicateResolution = resolution }
}

type duplicateResolutionRequest struct {
	Enabled bool `json:"enabled"`
}

func (api *API) getDuplicateResolution(response http.ResponseWriter, request *http.Request) {
	if api.duplicateResolution == nil {
		api.problem(response, http.StatusServiceUnavailable, "this setting is unavailable", nil)
		return
	}
	settings, err := api.duplicateResolution.Settings(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, settings)
}

// saveDuplicateResolution writes the setting and, when it has just been turned
// on, asks for a pass now.
//
// Turning it on is the whole instruction. Saving and then waiting six hours for
// the backstop, or for the next scan that happens to bring a file in, is a
// press that did nothing a person could see — the same thing that had to be
// fixed for the new-releases playlist.
func (api *API) saveDuplicateResolution(response http.ResponseWriter, request *http.Request) {
	if api.duplicateResolution == nil {
		api.problem(response, http.StatusServiceUnavailable, "this setting is unavailable", nil)
		return
	}
	var body duplicateResolutionRequest
	if err := decodeJSON(response, request, &body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	saved, err := api.duplicateResolution.SaveSettings(request.Context(), body.Enabled)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, saved)
}
