package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
	"github.com/pxldi/schall/internal/recommendations"
)

// ListenBrainzSettingsStore holds the account recommendations are read under.
// It is separate from SettingsStore because the source is not a provider:
// nothing here can search, request or import anything, and Schall never writes
// to it.
type ListenBrainzSettingsStore interface {
	ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error)
	SaveListenBrainzSettings(context.Context, db.SaveListenBrainzSettingsParams) (db.ListenBrainzSettingsRow, error)
	// QueueRecommendationSweep is here because saving the account is the moment
	// there is something to ask. The sweep schedules every later pass itself, but
	// it can only do that once it has run once, and the only other thing that
	// queues a first one is a restart — so without this, connecting the account
	// left the feature switched on and silent until the process happened to
	// bounce. Queueing is idempotent: a pass already queued is moved earlier, and
	// one already running is left alone.
	QueueRecommendationSweep(context.Context, time.Time) error
}

// RecommendationSource checks that the configured account is reachable. It is
// the service layer rather than a client, because the settings row is what says
// which account to ask and it is read per use.
type RecommendationSource interface {
	CheckConnection(context.Context) (db.ListenBrainzSettingsRow, error)
}

// WithRecommendationSource registers what can ask ListenBrainz whether the
// stored account is there. Optional: without it the test route reports itself
// unavailable and reading and saving the settings still work.
func WithRecommendationSource(source RecommendationSource) Option {
	return func(api *API) {
		api.recommendations = source
	}
}

type listenBrainzSettingsResponse struct {
	Configured          bool       `json:"configured"`
	BaseURL             string     `json:"baseUrl"`
	LabsURL             string     `json:"labsUrl"`
	Username            string     `json:"username"`
	UserTokenSet        bool       `json:"userTokenSet"`
	SimilarityAlgorithm string     `json:"similarityAlgorithm"`
	Enabled             bool       `json:"enabled"`
	ConnectionStatus    string     `json:"connectionStatus"`
	ConnectionDetail    *string    `json:"connectionDetail,omitempty"`
	ConnectionError     *string    `json:"connectionError,omitempty"`
	LastCheckedAt       *time.Time `json:"lastCheckedAt"`
}

// listenBrainzResponse never includes the token. Callers only learn whether one
// is stored, so the secret cannot leak through the UI or a proxy log.
func listenBrainzResponse(row db.ListenBrainzSettingsRow) listenBrainzSettingsResponse {
	return listenBrainzSettingsResponse{
		Configured:          true,
		BaseURL:             row.BaseURL,
		LabsURL:             row.LabsURL,
		Username:            row.Username,
		UserTokenSet:        strings.TrimSpace(row.UserToken.String) != "",
		SimilarityAlgorithm: row.SimilarityAlgorithm,
		Enabled:             row.Enabled,
		ConnectionStatus:    row.ConnectionStatus,
		ConnectionDetail:    textPointer(row.ConnectionDetail),
		ConnectionError:     textPointer(row.ConnectionError),
		LastCheckedAt:       nullableTime(row.LastCheckedAt.Valid, row.LastCheckedAt.Time),
	}
}

// unconfiguredListenBrainz is what an installation that has never opened this
// section is shown: the addresses that have a right answer, already filled in,
// and an empty account. Unlike slskd or Navidrome there is nothing to look up
// here — the service is hosted and the only real question is who the user is.
func unconfiguredListenBrainz() listenBrainzSettingsResponse {
	return listenBrainzSettingsResponse{
		BaseURL:             listenbrainz.DefaultBaseURL,
		LabsURL:             listenbrainz.DefaultLabsURL,
		SimilarityAlgorithm: listenbrainz.DefaultSimilarityAlgorithm,
		Enabled:             true,
		ConnectionStatus:    "unknown",
	}
}

func (api *API) getListenBrainzSettings(response http.ResponseWriter, request *http.Request) {
	if !api.recommendationSettingsAvailable(response) {
		return
	}
	row, err := api.listenbrainz.ListenBrainzSettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.writeJSON(response, http.StatusOK, unconfiguredListenBrainz())
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, listenBrainzResponse(row))
}

// listenBrainzSettingsRequest carries the account and nothing else.
//
// There is deliberately no address on it. A caller that could set baseUrl could
// name a host of its own, omit userToken so the save keeps the stored one, and
// then read the credential off the Authorization header the connection check
// sends — so the write path does not accept an address at all. The response
// still reports the stored addresses; they are read-only from here.
type listenBrainzSettingsRequest struct {
	Username string `json:"username"`
	// UserToken empty means "keep the stored one"; ClearUserToken is how the
	// stored one is removed, because the interface is never shown it and so
	// cannot send it back.
	UserToken           string `json:"userToken"`
	ClearUserToken      bool   `json:"clearUserToken"`
	SimilarityAlgorithm string `json:"similarityAlgorithm"`
	Enabled             bool   `json:"enabled"`
}

func (api *API) saveListenBrainzSettings(response http.ResponseWriter, request *http.Request) {
	if !api.recommendationSettingsAvailable(response) {
		return
	}
	var input listenBrainzSettingsRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	// The algorithm has a right answer, so a field left empty is the default
	// rather than a validation failure.
	input.Username = strings.TrimSpace(input.Username)
	input.SimilarityAlgorithm = strings.TrimSpace(input.SimilarityAlgorithm)
	if input.SimilarityAlgorithm == "" {
		input.SimilarityAlgorithm = listenbrainz.DefaultSimilarityAlgorithm
	}
	if problems := validateListenBrainzSettings(input); len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	row, err := api.listenbrainz.SaveListenBrainzSettings(request.Context(), db.SaveListenBrainzSettingsParams{
		Username:            input.Username,
		UserToken:           strings.TrimSpace(input.UserToken),
		ClearUserToken:      input.ClearUserToken,
		SimilarityAlgorithm: input.SimilarityAlgorithm,
		Enabled:             input.Enabled,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// The account is saved either way. A queue that would not take the sweep is
	// worth a line in the log and nothing more: the settings the user just wrote
	// are stored, and the next start queues a pass for an enabled account.
	if row.Enabled {
		if err := api.listenbrainz.QueueRecommendationSweep(request.Context(), time.Now()); err != nil {
			api.logger.Error().Err(err).Msg("queue recommendation sweep")
		}
	}
	api.writeJSON(response, http.StatusOK, listenBrainzResponse(row))
}

func validateListenBrainzSettings(input listenBrainzSettingsRequest) []string {
	problems := make([]string, 0, 4)
	if input.Username == "" {
		problems = append(problems, "username is required")
	}
	if len(input.Username) > 200 {
		problems = append(problems, "username must be 200 characters or fewer")
	}
	if len(input.UserToken) > 500 {
		problems = append(problems, "userToken must be 500 characters or fewer")
	}
	if len(input.SimilarityAlgorithm) > 200 {
		problems = append(problems, "similarityAlgorithm must be 200 characters or fewer")
	}
	return problems
}

// testListenBrainzConnection asks the source whether it knows the stored
// account and records the outcome, so the stored status stays meaningful
// between checks. It reads; nothing here is written to ListenBrainz.
func (api *API) testListenBrainzConnection(response http.ResponseWriter, request *http.Request) {
	if api.recommendations == nil {
		api.problem(response, http.StatusServiceUnavailable, "the recommendation source is unavailable", nil)
		return
	}
	saved, err := api.recommendations.CheckConnection(request.Context())
	if errors.Is(err, recommendations.ErrNotConfigured) {
		api.problem(response, http.StatusUnprocessableEntity, "ListenBrainz is not configured", nil)
		return
	}
	// The account changed under the check. Saying so is the honest answer: the
	// verdict that came back is about an account this installation no longer
	// asks, and showing it would attribute one account's result to another.
	if errors.Is(err, recommendations.ErrCheckSuperseded) {
		api.problem(response, http.StatusConflict,
			"the ListenBrainz account changed while the check was running", []string{"test again"})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, listenBrainzResponse(saved))
}

func (api *API) recommendationSettingsAvailable(response http.ResponseWriter) bool {
	if api.listenbrainz == nil {
		api.problem(response, http.StatusServiceUnavailable, "settings are unavailable", nil)
		return false
	}
	return true
}
