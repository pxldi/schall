package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/navidrome"
)

// NavidromeSettingsStore holds where the player is and who Schall speaks to it
// as. It is separate from SettingsStore because the player is not a source:
// nothing here can search, request or import anything.
type NavidromeSettingsStore interface {
	NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error)
	SaveNavidromeSettings(context.Context, db.SaveNavidromeSettingsParams) (db.NavidromeSettingsRow, error)
	RecordNavidromeConnection(context.Context, string, string, string) (db.NavidromeSettingsRow, error)
}

// PlaybackNotifier asks the player to look at the library again. It is the
// same notifier a library change queues a job for; here it is what the
// "Tell Navidrome now" button calls directly, so the press can answer with
// the notice actually having been made rather than only having been queued.
type PlaybackNotifier interface {
	NoticeLibraryChange(context.Context) error
}

// WithPlaybackNotifier registers what tells the player. Optional: without it
// the manual rescan route reports itself unavailable.
func WithPlaybackNotifier(playback PlaybackNotifier) Option {
	return func(api *API) {
		api.playback = playback
	}
}

type navidromeSettingsResponse struct {
	Configured       bool       `json:"configured"`
	BaseURL          string     `json:"baseUrl"`
	Username         string     `json:"username"`
	PasswordSet      bool       `json:"passwordSet"`
	Enabled          bool       `json:"enabled"`
	ConnectionStatus string     `json:"connectionStatus"`
	ConnectionDetail *string    `json:"connectionDetail,omitempty"`
	ConnectionError  *string    `json:"connectionError,omitempty"`
	LastCheckedAt    *time.Time `json:"lastCheckedAt"`
	LastNotifiedAt   *time.Time `json:"lastNotifiedAt"`
}

// navidromeResponse never includes the password. Callers only learn whether one
// is stored, so the secret cannot leak through the UI or a proxy log.
func navidromeResponse(row db.NavidromeSettingsRow) navidromeSettingsResponse {
	return navidromeSettingsResponse{
		Configured:       true,
		BaseURL:          row.BaseURL,
		Username:         row.Username,
		PasswordSet:      strings.TrimSpace(row.Password) != "",
		Enabled:          row.Enabled,
		ConnectionStatus: row.ConnectionStatus,
		ConnectionDetail: textPointer(row.ConnectionDetail),
		ConnectionError:  textPointer(row.ConnectionError),
		LastCheckedAt:    nullableTime(row.LastCheckedAt.Valid, row.LastCheckedAt.Time),
		LastNotifiedAt:   nullableTime(row.LastNotifiedAt.Valid, row.LastNotifiedAt.Time),
	}
}

func (api *API) getNavidromeSettings(response http.ResponseWriter, request *http.Request) {
	if !api.playerAvailable(response) {
		return
	}
	row, err := api.players.NavidromeSettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.writeJSON(response, http.StatusOK, navidromeSettingsResponse{ConnectionStatus: "unknown"})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, navidromeResponse(row))
}

type navidromeSettingsRequest struct {
	BaseURL  string `json:"baseUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
	Enabled  bool   `json:"enabled"`
}

func (api *API) saveNavidromeSettings(response http.ResponseWriter, request *http.Request) {
	if !api.playerAvailable(response) {
		return
	}
	var input navidromeSettingsRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.Username = strings.TrimSpace(input.Username)
	if problems := validateNavidromeSettings(input); len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	// A first-time configuration has no stored password to fall back on.
	if input.Password == "" {
		if _, err := api.players.NavidromeSettings(request.Context()); errors.Is(err, pgx.ErrNoRows) {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"password is required"})
			return
		} else if err != nil {
			api.internalError(response, request, err)
			return
		}
	}

	row, err := api.players.SaveNavidromeSettings(request.Context(), db.SaveNavidromeSettingsParams{
		BaseURL:  input.BaseURL,
		Username: input.Username,
		Password: input.Password,
		Enabled:  input.Enabled,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, navidromeResponse(row))
}

func validateNavidromeSettings(input navidromeSettingsRequest) []string {
	problems := make([]string, 0, 3)
	switch parsed, err := url.ParseRequestURI(input.BaseURL); {
	case input.BaseURL == "":
		problems = append(problems, "baseUrl is required")
	case err != nil:
		problems = append(problems, "baseUrl must be an absolute URL")
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		problems = append(problems, "baseUrl must use http or https")
	case parsed.Host == "":
		problems = append(problems, "baseUrl must include a host")
	}
	if len(input.BaseURL) > 500 {
		problems = append(problems, "baseUrl must be 500 characters or fewer")
	}
	if input.Username == "" {
		problems = append(problems, "username is required")
	}
	if len(input.Username) > 200 {
		problems = append(problems, "username must be 200 characters or fewer")
	}
	if len(input.Password) > 500 {
		problems = append(problems, "password must be 500 characters or fewer")
	}
	return problems
}

// testNavidromeConnection runs a bounded health check and records the outcome so
// the stored status stays meaningful between checks. It only asks the player who
// it is; nothing here starts a scan.
func (api *API) testNavidromeConnection(response http.ResponseWriter, request *http.Request) {
	if !api.playerAvailable(response) {
		return
	}
	row, err := api.players.NavidromeSettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusUnprocessableEntity, "navidrome is not configured", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	client, err := navidrome.NewClient(navidrome.Options{
		BaseURL:  row.BaseURL,
		Username: row.Username,
		Password: row.Password,
	})
	if err != nil {
		api.problem(response, http.StatusUnprocessableEntity, "navidrome is not configured",
			[]string{err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), connectionCheckTimeout)
	defer cancel()

	status, checkErr := client.CheckConnection(ctx)
	statusName, failure := "ok", ""
	if checkErr != nil {
		statusName, failure = "failed", checkErr.Error()
	}
	saved, err := api.players.RecordNavidromeConnection(request.Context(), statusName, status.Detail, failure)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if checkErr != nil {
		api.logger.Warn().Err(checkErr).Msg("navidrome connection check failed")
	}
	api.writeJSON(response, http.StatusOK, navidromeResponse(saved))
}

// rescanNavidrome tells the player to look at the library again, right now,
// and answers with the settings the telling updated. Unlike the automatic
// path — every library change queues notify_player and waits out its thirty
// seconds — this runs the notice inline, because a person who pressed a
// button is owed the result of the press and not a job they cannot see.
func (api *API) rescanNavidrome(response http.ResponseWriter, request *http.Request) {
	if !api.playerAvailable(response) {
		return
	}
	if api.playback == nil {
		api.problem(response, http.StatusServiceUnavailable, "the player cannot be told", nil)
		return
	}
	row, err := api.players.NavidromeSettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusUnprocessableEntity, "navidrome is not configured", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if !row.Enabled {
		api.problem(response, http.StatusUnprocessableEntity, "navidrome is switched off", nil)
		return
	}

	if err := api.playback.NoticeLibraryChange(request.Context()); err != nil {
		api.logger.Warn().Err(err).Msg("tell the player the library changed")
		api.problem(response, http.StatusBadGateway, "the player could not be told", []string{err.Error()})
		return
	}

	updated, err := api.players.NavidromeSettings(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, navidromeResponse(updated))
}

func (api *API) playerAvailable(response http.ResponseWriter) bool {
	if api.players == nil {
		api.problem(response, http.StatusServiceUnavailable, "settings are unavailable", nil)
		return false
	}
	return true
}
