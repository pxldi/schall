package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/newreleases"
)

// NewReleasesPlaylist is the new-releases playlist as the interface uses it:
// how it is set up, and which list to open.
type NewReleasesPlaylist interface {
	Overview(ctx context.Context) (newreleases.Playlist, error)
	SaveSettings(ctx context.Context, params db.SaveNewReleasesPlaylistSettingsParams) (db.NewReleasesPlaylistSettingsRow, error)
}

// WithNewReleasesPlaylist registers the new-releases playlist. Optional:
// without it the routes report themselves unavailable and nothing else on the
// installation changes.
func WithNewReleasesPlaylist(service NewReleasesPlaylist) Option {
	return func(api *API) {
		api.newReleases = service
	}
}

func (api *API) newReleasesAvailable(response http.ResponseWriter) bool {
	if api.newReleases == nil {
		api.problem(response, http.StatusServiceUnavailable, "the new-releases playlist is unavailable", nil)
		return false
	}
	return true
}

// newReleasesSettingsResponse is how the list is kept.
type newReleasesSettingsResponse struct {
	Enabled    bool  `json:"enabled"`
	WindowDays int32 `json:"windowDays"`
}

// newReleasesResponse is the whole screen in one read. The playlist id is null
// until the first refresh has made a list, which is what tells the page whether
// there is anything to open yet.
type newReleasesResponse struct {
	Settings   newReleasesSettingsResponse `json:"settings"`
	PlaylistID *string                     `json:"playlistId"`
	Name       string                      `json:"name"`
	TrackCount int64                       `json:"trackCount"`
}

// getNewReleasesPlaylist returns the settings and the list.
func (api *API) getNewReleasesPlaylist(response http.ResponseWriter, request *http.Request) {
	if !api.newReleasesAvailable(response) {
		return
	}
	overview, err := api.newReleases.Overview(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	payload := newReleasesResponse{
		Settings: newReleasesSettingsResponse{
			Enabled:    overview.Settings.Enabled,
			WindowDays: overview.Settings.WindowDays,
		},
		Name: newreleases.PlaylistName,
	}
	if overview.Exists {
		id := overview.List.ID.String()
		payload.PlaylistID = &id
		payload.Name = overview.List.Name
		payload.TrackCount = overview.List.EntryCount
	}
	api.writeJSON(response, http.StatusOK, payload)
}

// newReleasesSettingsRequest is what the settings screen sends.
type newReleasesSettingsRequest struct {
	Enabled    bool  `json:"enabled"`
	WindowDays int32 `json:"windowDays"`
}

// saveNewReleasesPlaylistSettings writes how the list is kept.
func (api *API) saveNewReleasesPlaylistSettings(response http.ResponseWriter, request *http.Request) {
	if !api.newReleasesAvailable(response) {
		return
	}
	var body newReleasesSettingsRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	saved, err := api.newReleases.SaveSettings(request.Context(), db.SaveNewReleasesPlaylistSettingsParams{
		Enabled: body.Enabled, WindowDays: body.WindowDays,
	})
	if errors.Is(err, newreleases.ErrWindowOutOfRange) {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"windowDays must be between 1 and 365"})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, newReleasesSettingsResponse{
		Enabled: saved.Enabled, WindowDays: saved.WindowDays,
	})
}
