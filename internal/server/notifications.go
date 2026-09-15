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
	"github.com/pxldi/schall/internal/notify"
)

// NotificationSettingsStore holds where Schall sends word that the review queue
// has a question. It is its own store because nothing else reads it: no
// provider, no matcher, no import path.
type NotificationSettingsStore interface {
	NotificationSettings(context.Context) (db.NotificationSettingsRow, error)
	SaveNotificationSettings(context.Context, db.SaveNotificationSettingsParams) (db.NotificationSettingsRow, error)
}

// QueueNotifier sends one message to the configured endpoint. It is the same
// component the worker calls after a pass; here it is what the test button
// presses, so the address is proved by the thing that will actually use it.
type QueueNotifier interface {
	Send(context.Context, notify.Message) error
}

// WithNotifier registers what sends word. Optional: without it the test route
// reports itself unavailable and reading and saving the settings still work.
func WithNotifier(notifier QueueNotifier) Option {
	return func(api *API) {
		api.notifier = notifier
	}
}

type notificationSettingsResponse struct {
	Configured       bool       `json:"configured"`
	Kind             string     `json:"kind"`
	Endpoint         string     `json:"endpoint"`
	TokenSet         bool       `json:"tokenSet"`
	Enabled          bool       `json:"enabled"`
	ConnectionStatus string     `json:"connectionStatus"`
	ConnectionError  *string    `json:"connectionError,omitempty"`
	LastCheckedAt    *time.Time `json:"lastCheckedAt"`
}

// notificationResponse never includes the token. Callers learn only whether one
// is stored, so the secret cannot leak through the interface or a proxy log.
func notificationResponse(row db.NotificationSettingsRow) notificationSettingsResponse {
	return notificationSettingsResponse{
		Configured:       true,
		Kind:             row.Kind,
		Endpoint:         row.Endpoint,
		TokenSet:         strings.TrimSpace(row.Token.String) != "",
		Enabled:          row.Enabled,
		ConnectionStatus: row.ConnectionStatus,
		ConnectionError:  textPointer(row.ConnectionError),
		LastCheckedAt:    nullableTime(row.LastCheckedAt.Valid, row.LastCheckedAt.Time),
	}
}

// unconfiguredNotifications is what an installation that has never opened this
// section is shown: switched off, with no address. There is no default address
// to fill in — Schall has no idea where this person wants to be told.
func unconfiguredNotifications() notificationSettingsResponse {
	return notificationSettingsResponse{
		Kind:             db.NotifyNtfy,
		ConnectionStatus: "unknown",
	}
}

func (api *API) getNotificationSettings(response http.ResponseWriter, request *http.Request) {
	if !api.notificationSettingsAvailable(response) {
		return
	}
	row, err := api.notifications.NotificationSettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.writeJSON(response, http.StatusOK, unconfiguredNotifications())
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, notificationResponse(row))
}

type notificationSettingsRequest struct {
	Kind     string `json:"kind"`
	Endpoint string `json:"endpoint"`
	// Token empty means "keep the stored one"; ClearToken is how the stored one
	// is removed, because the interface is never shown it and so cannot send it
	// back to mean "leave it alone".
	Token      string `json:"token"`
	ClearToken bool   `json:"clearToken"`
	Enabled    bool   `json:"enabled"`
}

func (api *API) saveNotificationSettings(response http.ResponseWriter, request *http.Request) {
	if !api.notificationSettingsAvailable(response) {
		return
	}
	var input notificationSettingsRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	input.Kind = strings.TrimSpace(input.Kind)
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if input.Kind == "" {
		input.Kind = db.NotifyNtfy
	}
	if problems := validateNotificationSettings(input); len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	row, err := api.notifications.SaveNotificationSettings(request.Context(),
		db.SaveNotificationSettingsParams{
			Kind:       input.Kind,
			Endpoint:   input.Endpoint,
			Token:      strings.TrimSpace(input.Token),
			ClearToken: input.ClearToken,
			Enabled:    input.Enabled,
		})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, notificationResponse(row))
}

func validateNotificationSettings(input notificationSettingsRequest) []string {
	problems := make([]string, 0, 3)
	if input.Kind != db.NotifyNtfy && input.Kind != db.NotifyWebhook {
		problems = append(problems, "kind must be ntfy or webhook")
	}
	// Off with no address is the row every installation starts with, so it is
	// only an address that is wanted once this is switched on.
	if input.Enabled && input.Endpoint == "" {
		problems = append(problems, "Enter the address to send to.")
	}
	if input.Endpoint != "" {
		address, err := url.Parse(input.Endpoint)
		switch {
		case err != nil:
			problems = append(problems, "The address is not a web address.")
		case address.Scheme != "http" && address.Scheme != "https":
			problems = append(problems, "The address must start with http:// or https://.")
		case address.Host == "":
			problems = append(problems, "The address has no server in it.")
		}
	}
	if input.Token != "" && input.ClearToken {
		problems = append(problems, "A token cannot be set and removed in one save.")
	}
	return problems
}

// testNotification sends one message now, so the address is proved by the thing
// that will use it rather than by a check that looks like it.
//
// The message says what it is. Somebody pressing this is looking at their phone
// waiting for it, and a test that arrived looking exactly like a real question
// would be a question they then went looking for and could not find.
func (api *API) testNotification(response http.ResponseWriter, request *http.Request) {
	if !api.notificationSettingsAvailable(response) {
		return
	}
	if api.notifier == nil {
		api.problem(response, http.StatusServiceUnavailable, "notifications are unavailable",
			[]string{"This installation cannot send them."})
		return
	}
	err := api.notifier.Send(request.Context(), notify.Message{
		Title: "Schall is connected",
		Body:  "This is the test message. Real ones arrive when the review queue has a question.",
	})
	switch {
	case errors.Is(err, notify.ErrNotConfigured):
		api.problem(response, http.StatusUnprocessableEntity, "there is nowhere to send to",
			[]string{"Enter the address and save it, then test again."})
		return
	case err != nil:
		// The address is the user's, the server at the end of it is the user's,
		// and what it said is the only thing that tells them which of the two is
		// wrong. So the reason is passed on rather than replaced.
		api.problem(response, http.StatusBadGateway, "the message could not be sent",
			[]string{err.Error()})
		return
	}
	row, err := api.notifications.NotificationSettings(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, notificationResponse(row))
}

func (api *API) notificationSettingsAvailable(response http.ResponseWriter) bool {
	if api.notifications == nil {
		api.problem(response, http.StatusServiceUnavailable, "notification settings are unavailable", nil)
		return false
	}
	return true
}
