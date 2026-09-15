package server

import (
	"context"
	"net/http"

	"github.com/pxldi/schall/internal/db"
)

// UpgradeService is the settings screen's whole reach into the low-quality-file
// sweep: read and change the switch, count what is under the floor right now,
// and ask for an immediate pass.
type UpgradeService interface {
	Settings(ctx context.Context) (db.UpgradeSettingsRow, error)
	SaveSettings(ctx context.Context, enabled bool) (db.UpgradeSettingsRow, error)
	CountBelowFloor(ctx context.Context) (int, error)
	ScanNow(ctx context.Context) error
}

// WithUpgrades registers the low-quality-file sweep. Optional: without it the
// routes report themselves unavailable and the sweep never runs.
func WithUpgrades(service UpgradeService) Option {
	return func(api *API) {
		api.upgrades = service
	}
}

func (api *API) upgradesAvailable(response http.ResponseWriter) bool {
	if api.upgrades == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"the low-quality-file sweep is unavailable", nil)
		return false
	}
	return true
}

type upgradeSettingsResponse struct {
	Enabled bool `json:"enabled"`
	// BelowFloor is how many library files are under the stored bit-rate floor
	// right now. It is read fresh on every request rather than cached: the
	// floor can change between two loads of the settings screen, and a stale
	// count would be read as a promise about files that no longer qualify.
	BelowFloor int `json:"belowFloor"`
}

func (api *API) upgradeSettingsResponse(request *http.Request) (upgradeSettingsResponse, error) {
	settings, err := api.upgrades.Settings(request.Context())
	if err != nil {
		return upgradeSettingsResponse{}, err
	}
	count, err := api.upgrades.CountBelowFloor(request.Context())
	if err != nil {
		return upgradeSettingsResponse{}, err
	}
	return upgradeSettingsResponse{Enabled: settings.Enabled, BelowFloor: count}, nil
}

func (api *API) getUpgradeSettings(response http.ResponseWriter, request *http.Request) {
	if !api.upgradesAvailable(response) {
		return
	}
	body, err := api.upgradeSettingsResponse(request)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, body)
}

type upgradeSettingsRequest struct {
	Enabled bool `json:"enabled"`
}

func (api *API) saveUpgradeSettings(response http.ResponseWriter, request *http.Request) {
	if !api.upgradesAvailable(response) {
		return
	}
	var input upgradeSettingsRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if _, err := api.upgrades.SaveSettings(request.Context(), input.Enabled); err != nil {
		api.internalError(response, request, err)
		return
	}
	body, err := api.upgradeSettingsResponse(request)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, body)
}

// scanForUpgrades queues an immediate pass over the library against the
// stored floor, the "Scan now" button's whole implementation.
func (api *API) scanForUpgrades(response http.ResponseWriter, request *http.Request) {
	if !api.upgradesAvailable(response) {
		return
	}
	if err := api.upgrades.ScanNow(request.Context()); err != nil {
		api.internalError(response, request, err)
		return
	}
	response.WriteHeader(http.StatusAccepted)
}
