package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/sources"
)

// connectionCheckTimeout bounds the provider health check so a wedged or
// unreachable slskd cannot hold a request open.
const connectionCheckTimeout = 10 * time.Second

// rateLimitPause is how long an interactive search waits before asking again
// after the provider refused it for asking too often. It is short because
// somebody is watching: the bulk worker, which nobody is watching, waits far
// longer. A variable so a test does not have to wait it out.
var rateLimitPause = 2 * time.Second

// waitBeforeRetry sleeps unless the caller gives up first, reporting whether it
// slept through.
func waitBeforeRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type slskdSettingsResponse struct {
	Configured       bool       `json:"configured"`
	BaseURL          string     `json:"baseUrl"`
	APIKeySet        bool       `json:"apiKeySet"`
	Enabled          bool       `json:"enabled"`
	SearchTimeoutSec int32      `json:"searchTimeoutSeconds"`
	ConnectionStatus string     `json:"connectionStatus"`
	ConnectionDetail *string    `json:"connectionDetail,omitempty"`
	ConnectionError  *string    `json:"connectionError,omitempty"`
	LastCheckedAt    *time.Time `json:"lastCheckedAt"`
}

// settingsResponse never includes the API key. Callers only learn whether one
// is stored, so the secret cannot leak through the UI or a proxy log.
func settingsResponse(row db.SlskdSettingsRow) slskdSettingsResponse {
	return slskdSettingsResponse{
		Configured:       true,
		BaseURL:          row.BaseURL,
		APIKeySet:        strings.TrimSpace(row.APIKey) != "",
		Enabled:          row.Enabled,
		SearchTimeoutSec: row.SearchTimeoutSeconds,
		ConnectionStatus: row.ConnectionStatus,
		ConnectionDetail: textPointer(row.ConnectionDetail),
		ConnectionError:  textPointer(row.ConnectionError),
		LastCheckedAt:    nullableTime(row.LastCheckedAt.Valid, row.LastCheckedAt.Time),
	}
}

func (api *API) getSlskdSettings(response http.ResponseWriter, request *http.Request) {
	if !api.settingsAvailable(response) {
		return
	}
	row, err := api.settings.SlskdSettings(request.Context())
	if errors.Is(err, pgx.ErrNoRows) {
		api.writeJSON(response, http.StatusOK, slskdSettingsResponse{
			ConnectionStatus: "unknown", SearchTimeoutSec: 20,
		})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, settingsResponse(row))
}

type slskdSettingsRequest struct {
	BaseURL          string `json:"baseUrl"`
	APIKey           string `json:"apiKey"`
	Enabled          bool   `json:"enabled"`
	SearchTimeoutSec int32  `json:"searchTimeoutSeconds"`
}

func (api *API) saveSlskdSettings(response http.ResponseWriter, request *http.Request) {
	if !api.settingsAvailable(response) {
		return
	}
	var input slskdSettingsRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.APIKey = strings.TrimSpace(input.APIKey)
	if input.SearchTimeoutSec == 0 {
		input.SearchTimeoutSec = 20
	}
	if problems := validateSlskdSettings(input); len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	// A first-time configuration has no stored key to fall back on.
	if input.APIKey == "" {
		if _, err := api.settings.SlskdSettings(request.Context()); errors.Is(err, pgx.ErrNoRows) {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{"apiKey is required"})
			return
		} else if err != nil {
			api.internalError(response, request, err)
			return
		}
	}

	row, err := api.settings.SaveSlskdSettings(request.Context(), db.SaveSlskdSettingsParams{
		BaseURL:              input.BaseURL,
		APIKey:               input.APIKey,
		Enabled:              input.Enabled,
		SearchTimeoutSeconds: input.SearchTimeoutSec,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, settingsResponse(row))
}

func validateSlskdSettings(input slskdSettingsRequest) []string {
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
	if len(input.APIKey) > 500 {
		problems = append(problems, "apiKey must be 500 characters or fewer")
	}
	if input.SearchTimeoutSec < 5 || input.SearchTimeoutSec > 120 {
		problems = append(problems, "searchTimeoutSeconds must be between 5 and 120")
	}
	return problems
}

// testSlskdConnection runs a bounded health check and records the outcome so
// the stored status stays meaningful between checks.
func (api *API) testSlskdConnection(response http.ResponseWriter, request *http.Request) {
	if !api.settingsAvailable(response) {
		return
	}
	provider, _, err := api.sourceProvider(request.Context())
	if errors.Is(err, sources.ErrNotConfigured) {
		api.problem(response, http.StatusUnprocessableEntity, "slskd is not configured", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), connectionCheckTimeout)
	defer cancel()

	status, checkErr := provider.CheckConnection(ctx)
	statusName, failure := "ok", ""
	if checkErr != nil {
		statusName, failure = "failed", checkErr.Error()
	}
	row, err := api.settings.RecordSlskdConnection(request.Context(), statusName, status.Detail, failure)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if checkErr != nil {
		api.logger.Warn().Err(checkErr).Msg("slskd connection check failed")
	}
	api.writeJSON(response, http.StatusOK, settingsResponse(row))
}

// sourceProvider builds a provider from the stored settings. It returns
// sources.ErrNotConfigured when slskd has not been set up or is disabled.
func (api *API) sourceProvider(ctx context.Context) (sources.Provider, db.SlskdSettingsRow, error) {
	if api.settings == nil || api.providers == nil {
		return nil, db.SlskdSettingsRow{}, sources.ErrNotConfigured
	}
	row, err := api.settings.SlskdSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, db.SlskdSettingsRow{}, sources.ErrNotConfigured
	}
	if err != nil {
		return nil, db.SlskdSettingsRow{}, err
	}
	if !row.Enabled {
		return nil, row, sources.ErrNotConfigured
	}
	provider, err := api.providers.Build(sources.Settings{
		BaseURL:       row.BaseURL,
		APIKey:        row.APIKey,
		SearchTimeout: time.Duration(row.SearchTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return nil, row, err
	}
	return provider, row, nil
}

func (api *API) settingsAvailable(response http.ResponseWriter) bool {
	if api.settings == nil {
		api.problem(response, http.StatusServiceUnavailable, "settings are unavailable", nil)
		return false
	}
	return true
}

type sourceFileResponse struct {
	// Path is the remote name exactly as the peer reported it. The interface
	// shows Name, but a download request has to carry Path, because that is
	// what a transfer asks for.
	Path            string `json:"path"`
	Name            string `json:"name"`
	Extension       string `json:"extension"`
	SizeBytes       int64  `json:"sizeBytes"`
	BitRate         int    `json:"bitRate,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	VariableBitRate bool   `json:"variableBitRate"`
}

// sourceMatchResponse is the evidence that a candidate holds the release that
// was searched for. It travels separately from Score because the two answer
// different questions: this one says whether the music is right, Score says
// only how good a copy it is.
type sourceMatchResponse struct {
	Checked   bool   `json:"checked"`
	Expected  int    `json:"expected"`
	Confirmed int    `json:"confirmed"`
	Partial   int    `json:"partial"`
	Absent    int    `json:"absent"`
	Complete  bool   `json:"complete"`
	Summary   string `json:"summary"`
}

// catalogueQueryTracks reduces catalogue tracks to what a peer discloses about
// a file it has not sent: its name and how long it runs. A track without a
// stored length still carries its title, which is partial evidence on its own.
func catalogueQueryTracks(tracks []db.ListAlbumTracksRow) []sources.QueryTrack {
	result := make([]sources.QueryTrack, 0, len(tracks))
	for _, track := range tracks {
		queryTrack := sources.QueryTrack{Title: track.Title}
		if track.DurationMs.Valid {
			queryTrack.DurationSeconds = int(track.DurationMs.Int32 / 1000)
		}
		result = append(result, queryTrack)
	}
	return result
}

func matchResponse(match sources.Match) sourceMatchResponse {
	return sourceMatchResponse{
		Checked: match.Known(), Expected: match.Expected, Confirmed: match.Confirmed,
		Partial: match.Partial, Absent: match.Absent, Complete: match.Complete(),
		Summary: match.Summary(),
	}
}

type sourceCandidateResponse struct {
	Provider       string               `json:"provider"`
	Username       string               `json:"username"`
	Directory      string               `json:"directory"`
	TrackCount     int                  `json:"trackCount"`
	TotalSizeBytes int64                `json:"totalSizeBytes"`
	Format         string               `json:"format"`
	AverageBitRate int                  `json:"averageBitRate,omitempty"`
	FreeUploadSlot bool                 `json:"freeUploadSlot"`
	QueueLength    int                  `json:"queueLength"`
	UploadSpeed    int64                `json:"uploadSpeed"`
	Score          float64              `json:"score"`
	Match          sourceMatchResponse  `json:"match"`
	Reasons        []string             `json:"reasons"`
	Files          []sourceFileResponse `json:"files"`
}

// listAlbumSources searches configured providers for a release and returns
// ranked, inspectable candidates. It never starts a download.
func (api *API) listAlbumSources(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	album, err := api.store.GetAlbum(request.Context(), albumID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	// The catalogue's own tracks travel with the query, so a candidate can be
	// corroborated against the release rather than only scored for quality.
	// Failing to load them costs the evidence, not the search.
	tracks, err := api.store.ListAlbumTracks(request.Context(), albumID)
	if err != nil {
		api.logger.Warn().Err(err).Str("album_id", albumID.String()).
			Msg("could not load catalogue tracks to corroborate sources against")
	}

	// The searched phrase is normalized before it is used, and reported back as
	// it was searched, so the interface never shows a phrase Soulseek could not
	// have matched.
	// A release whose title says nothing a peer could be asked has no ladder at
	// all, and falls through to the refusal below rather than being searched for.
	queryTexts := sources.CatalogueQueryTexts(album.ArtistName, album.Title)
	query := sources.Query{
		ExpectedTrackCount: int(album.TrackCount),
		Tracks:             catalogueQueryTracks(tracks),
	}
	if len(queryTexts) > 0 {
		query.Text = queryTexts[0]
	}
	if override := strings.TrimSpace(request.URL.Query().Get("q")); override != "" {
		if len([]rune(override)) < 2 || len([]rune(override)) > 200 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"q must be between 2 and 200 characters"})
			return
		}
		query.Text = sources.NormalizeQueryText(override)
		queryTexts = []string{query.Text}
	}
	if query.Text == "" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"the search terms contain no letters or digits to search for"})
		return
	}

	provider, _, err := api.sourceProvider(request.Context())
	if errors.Is(err, sources.ErrNotConfigured) {
		api.problem(response, http.StatusServiceUnavailable,
			"no download source is configured", []string{"Configure and enable slskd in Settings."})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	var candidates []sources.Candidate
	for _, queryText := range queryTexts {
		query.Text = queryText
		candidates, err = provider.Search(request.Context(), query)
		// Being asked to slow down is not a failed search. One short wait is
		// worth more to the person watching than an error telling them to try
		// again, which is the same wait with extra steps.
		if errors.Is(err, sources.ErrRateLimited) {
			if !waitBeforeRetry(request.Context(), rateLimitPause) {
				return
			}
			candidates, err = provider.Search(request.Context(), query)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			if errors.Is(err, sources.ErrRateLimited) {
				api.problem(response, http.StatusServiceUnavailable,
					"the download source is busy",
					[]string{"slskd is refusing further searches for the moment. Try again shortly."})
				return
			}
			// Not busy — unable. Waiting and asking again is no use until
			// whatever it is has passed, so this says what happened rather
			// than inviting a retry, and carries the provider's own account
			// of it because that is what names the thing to go and fix.
			if errors.Is(err, sources.ErrProviderUnavailable) {
				api.problem(response, http.StatusServiceUnavailable,
					"the download source cannot search", []string{err.Error()})
				return
			}
			api.logger.Error().Err(err).Str("provider", provider.Name()).Msg("source search failed")
			api.problem(response, http.StatusBadGateway, "source search failed", []string{err.Error()})
			return
		}
		if len(candidates) > 0 {
			break
		}
	}

	api.writeJSON(response, http.StatusOK, map[string]any{
		"query":              query.Text,
		"expectedTrackCount": query.ExpectedTrackCount,
		"items":              sourceCandidateResponses(candidates),
	})
}

// maxSearchRunAlbums bounds one bulk run. Every release is a separate provider
// search bounded by the configured timeout, and the worker runs them one after
// another, so a run of fifty is already the better part of an hour of asking
// slskd questions. A larger run would not be more useful, only more patient.
const maxSearchRunAlbums = 50

type createSourceSearchInput struct {
	AlbumIDs []uuid.UUID `json:"albumIds"`
	// AutoRequest lets the run record the download itself for a release whose
	// best candidate has every catalogue track confirmed by name and length.
	// It is asked per run rather than kept as a setting: it is permission given
	// by somebody about to look at these results, not a switch left on.
	AutoRequest bool `json:"autoRequest"`
}

type sourceSearchResultResponse struct {
	AlbumID          uuid.UUID                 `json:"albumId"`
	AlbumTitle       string                    `json:"albumTitle"`
	ArtistID         uuid.UUID                 `json:"artistId"`
	ArtistName       string                    `json:"artistName"`
	TrackCount       int32                     `json:"trackCount"`
	OwnedTrackCount  int32                     `json:"ownedTrackCount"`
	FirstReleaseDate string                    `json:"firstReleaseDate"`
	Status           string                    `json:"status"`
	Query            string                    `json:"query"`
	Candidates       []sourceCandidateResponse `json:"candidates"`
	// Refused are the copies the user's own format preferences took out of this
	// search. Without them a release whose every offer was under the bitrate
	// floor reads as one nobody is sharing, which points at the network when the
	// only thing that can change the answer is a setting.
	Refused []sourceRefusalResponse `json:"refused"`
	// RefusedBelowBitRate is how many of those were under the floor. The screen
	// counts one rule rather than reading the sentences back.
	RefusedBelowBitRate int        `json:"refusedBelowBitRate"`
	Error               *string    `json:"error,omitempty"`
	SearchedAt          *time.Time `json:"searchedAt"`
	// RequestedDownloadID names the download already recorded for this release,
	// so a review screen reopened after acting on it says so rather than
	// inviting the same folder to be requested twice.
	RequestedDownloadID *uuid.UUID `json:"requestedDownloadId,omitempty"`
	// AutoRequestedAt is when the run recorded that download itself, if it did.
	// A download nobody remembers asking for has to say where it came from.
	AutoRequestedAt *time.Time `json:"autoRequestedAt"`
}

type sourceSearchRunResponse struct {
	ID uuid.UUID `json:"id"`
	// Pending is how many releases have not been searched yet. It is what the
	// interface waits on, and a run is done exactly when it reaches zero.
	Pending    int `json:"pending"`
	Total      int `json:"total"`
	FoundCount int `json:"foundCount"`
	NoneCount  int `json:"noneCount"`
	// FailedCount counted somebody looking and not managing it; CancelledCount
	// counts nobody looking at all. They are separate for the reason 'none' and
	// 'failed' are: only one of them is a thing that went wrong.
	CancelledCount int                          `json:"cancelledCount"`
	FailedCount    int                          `json:"failedCount"`
	RequestedAt    time.Time                    `json:"requestedAt"`
	Items          []sourceSearchResultResponse `json:"items"`
}

// createSourceSearch queues a source search for each of several releases. It
// records nothing about what should be downloaded: a run is a list of answers
// to look at, and every request that follows is still made one release at a
// time, with the duplicate question asked as it always was.
func (api *API) createSourceSearch(response http.ResponseWriter, request *http.Request) {
	if api.searches == nil {
		api.problem(response, http.StatusServiceUnavailable, "source searches are unavailable", nil)
		return
	}

	var input createSourceSearchInput
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	switch {
	case len(input.AlbumIDs) == 0:
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"albumIds must hold at least one release"})
		return
	case len(input.AlbumIDs) > maxSearchRunAlbums:
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{fmt.Sprintf("albumIds must hold %d releases or fewer", maxSearchRunAlbums)})
		return
	}

	// Refused up front rather than queued and failed fifty times: an
	// unconfigured provider is a thing to fix, not a run to wait out.
	if _, _, err := api.sourceProvider(request.Context()); errors.Is(err, sources.ErrNotConfigured) {
		api.problem(response, http.StatusServiceUnavailable, "no download source is configured",
			[]string{"Configure and enable slskd in Settings."})
		return
	} else if err != nil {
		api.internalError(response, request, err)
		return
	}

	runID, queued, err := api.searches.CreateSourceSearchRun(
		request.Context(), input.AlbumIDs, input.AutoRequest)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"none of those releases exist"})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	api.logger.Info().Str("run_id", runID.String()).Int("releases", queued).
		Msg("source search queued")

	rows, err := api.searches.SourceSearchRun(request.Context(), runID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusCreated, sourceSearchRunResponseFrom(runID, rows))
}

func (api *API) getSourceSearch(response http.ResponseWriter, request *http.Request) {
	if api.searches == nil {
		api.problem(response, http.StatusServiceUnavailable, "source searches are unavailable", nil)
		return
	}
	runID, err := uuid.Parse(chi.URLParam(request, "runID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "search not found", nil)
		return
	}
	rows, err := api.searches.SourceSearchRun(request.Context(), runID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "search not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, sourceSearchRunResponseFrom(runID, rows))
}

// cancelSourceSearch stops the releases of a run that have not been searched
// yet. It exists because a run is claimed one release at a time in creation
// order, so a run started with the wrong permission cannot be corrected by
// starting a second one: the second waits out the first. What has already been
// answered is left exactly as it is, because a result is something somebody may
// act on and cancelling a run is not a reason to withdraw it.
func (api *API) cancelSourceSearch(response http.ResponseWriter, request *http.Request) {
	if api.searches == nil {
		api.problem(response, http.StatusServiceUnavailable, "source searches are unavailable", nil)
		return
	}
	runID, err := uuid.Parse(chi.URLParam(request, "runID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "search not found", nil)
		return
	}
	rows, stopped, err := api.searches.CancelSourceSearchRun(request.Context(), runID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "search not found", nil)
		return
	case errors.Is(err, db.ErrNothingToCancel):
		// Refused rather than answered with a run that says it was cancelled:
		// every release has an answer already, and reporting success would
		// suggest something was withdrawn.
		api.problem(response, http.StatusConflict, "there is nothing left to cancel",
			[]string{err.Error()})
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}

	api.logger.Info().Str("run_id", runID.String()).Int("releases", stopped).
		Msg("source search cancelled")
	// The tab that asked has the run in its response; every other one showing
	// it is still counting those releases as waiting for an answer that is not
	// coming. Announced rather than left to their next poll, the same way the
	// worker announces each release it settles.
	api.events.Publish(events.TopicSourceSearches)
	api.writeJSON(response, http.StatusOK, sourceSearchRunResponseFrom(runID, rows))
}

func sourceSearchRunResponseFrom(runID uuid.UUID, rows []db.SourceSearchRow) sourceSearchRunResponse {
	result := sourceSearchRunResponse{
		ID:    runID,
		Total: len(rows),
		Items: make([]sourceSearchResultResponse, 0, len(rows)),
	}
	for _, row := range rows {
		switch row.Status {
		case "queued", "searching":
			result.Pending++
		case "found":
			result.FoundCount++
		case "none":
			result.NoneCount++
		case "failed":
			result.FailedCount++
		case "cancelled":
			result.CancelledCount++
		}
		if result.RequestedAt.IsZero() || row.RequestedAt.Time.Before(result.RequestedAt) {
			result.RequestedAt = row.RequestedAt.Time
		}

		item := sourceSearchResultResponse{
			AlbumID: row.AlbumID, AlbumTitle: row.AlbumTitle,
			ArtistID: row.ArtistID, ArtistName: row.ArtistName,
			TrackCount: row.TrackCount, OwnedTrackCount: row.OwnedTrackCount,
			FirstReleaseDate: row.FirstReleaseDate,
			Status:           row.Status, Query: row.Query,
			Candidates:      storedCandidateResponses(row.Candidates),
			Refused:         refusalResponses(row.Refused),
			Error:           textPointer(row.ErrorMessage),
			SearchedAt:      nullableTime(row.SearchedAt.Valid, row.SearchedAt.Time),
			AutoRequestedAt: nullableTime(row.AutoRequestedAt.Valid, row.AutoRequestedAt.Time),
		}
		for _, refusal := range row.Refused {
			if refusal.Kind == sources.RefusedBitRate {
				item.RefusedBelowBitRate++
			}
		}
		if row.RequestedDownloadID.Valid {
			requested := row.RequestedDownloadID.UUID
			item.RequestedDownloadID = &requested
		}
		result.Items = append(result.Items, item)
	}
	return result
}

// sourceRefusalResponse is one copy the format preferences took out of a
// search: who was offering it, what it was, and the sentence saying why it went.
type sourceRefusalResponse struct {
	Username  string `json:"username"`
	Directory string `json:"directory"`
	Format    string `json:"format"`
	Reason    string `json:"reason"`
	Kind      string `json:"kind"`
}

func refusalResponses(refused []db.SourceSearchRefusal) []sourceRefusalResponse {
	result := make([]sourceRefusalResponse, 0, len(refused))
	for _, one := range refused {
		result = append(result, sourceRefusalResponse{
			Username: one.Username, Directory: one.Directory,
			Format: one.Format, Reason: one.Reason, Kind: one.Kind,
		})
	}
	return result
}

// storedCandidateResponses renders what a search stored. It is separate from
// sourceCandidateResponses because the stored shape is a record of what was
// offered rather than a live provider result, and the two must be free to
// differ without one silently reshaping the other.
func storedCandidateResponses(candidates []db.SourceSearchCandidate) []sourceCandidateResponse {
	result := make([]sourceCandidateResponse, 0, len(candidates))
	for _, candidate := range candidates {
		files := make([]sourceFileResponse, 0, len(candidate.Files))
		for _, file := range candidate.Files {
			files = append(files, sourceFileResponse{
				Path: file.Path, Name: file.Name, Extension: file.Extension,
				SizeBytes: file.SizeBytes, BitRate: file.BitRate,
				DurationSeconds: file.DurationSeconds, VariableBitRate: file.VariableBitRate,
			})
		}
		result = append(result, sourceCandidateResponse{
			Provider: candidate.Provider, Username: candidate.Username,
			Directory: candidate.Directory, TrackCount: len(files),
			TotalSizeBytes: candidate.TotalSizeBytes, Format: candidate.Format,
			AverageBitRate: candidate.AverageBitRate, FreeUploadSlot: candidate.FreeUploadSlot,
			QueueLength: candidate.QueueLength, UploadSpeed: candidate.UploadSpeed,
			Score: candidate.Score, Match: matchResponse(sources.Match{
				Expected:  candidate.Match.Expected,
				Confirmed: candidate.Match.Confirmed,
				Partial:   candidate.Match.Partial,
				Absent:    candidate.Match.Absent,
			}), Reasons: candidate.Reasons, Files: files,
		})
	}
	return result
}

func sourceCandidateResponses(candidates []sources.Candidate) []sourceCandidateResponse {
	result := make([]sourceCandidateResponse, 0, len(candidates))
	for _, candidate := range candidates {
		files := make([]sourceFileResponse, 0, len(candidate.Files))
		for _, file := range candidate.Files {
			files = append(files, sourceFileResponse{
				Path: file.Path,
				Name: file.Name, Extension: file.Extension, SizeBytes: file.SizeBytes,
				BitRate: file.BitRate, DurationSeconds: file.DurationSeconds,
				VariableBitRate: file.VariableBitRate,
			})
		}
		result = append(result, sourceCandidateResponse{
			Provider: candidate.Provider, Username: candidate.Username,
			Directory: candidate.Directory, TrackCount: candidate.TrackCount(),
			TotalSizeBytes: candidate.TotalSizeBytes, Format: candidate.Format,
			AverageBitRate: candidate.AverageBitRate, FreeUploadSlot: candidate.FreeUploadSlot,
			QueueLength: candidate.QueueLength, UploadSpeed: candidate.UploadSpeed,
			Score: candidate.Score, Match: matchResponse(candidate.Match),
			Reasons: candidate.Reasons, Files: files,
		})
	}
	return result
}
