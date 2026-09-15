package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/weekly"
)

// WeeklyPlaylist is the weekly playlist as the interface uses it: how it is set
// up, what is on trial, and one control to keep a song.
type WeeklyPlaylist interface {
	Overview(ctx context.Context) (weekly.Overview, error)
	Settings(ctx context.Context) (db.WeeklyPlaylistSettingsRow, error)
	SaveSettings(ctx context.Context, params db.SaveWeeklyPlaylistSettingsParams) (db.WeeklyPlaylistSettingsRow, error)
	Keep(ctx context.Context, fileID uuid.UUID) (db.LibraryFileLeaseRow, error)
	LeaseEvidence(ctx context.Context, leaseID uuid.UUID) ([]db.LibraryFileKeepReadRow, error)
}

// WithWeeklyPlaylist registers the weekly playlist. Optional: without it the
// routes report themselves unavailable and nothing else on the installation
// changes.
func WithWeeklyPlaylist(service WeeklyPlaylist) Option {
	return func(api *API) {
		api.weekly = service
	}
}

func (api *API) weeklyAvailable(response http.ResponseWriter) bool {
	if api.weekly == nil {
		api.problem(response, http.StatusServiceUnavailable, "the weekly playlist is unavailable", nil)
		return false
	}
	return true
}

// weeklySettingsResponse is how the weekly playlist is set up.
type weeklySettingsResponse struct {
	Enabled      bool   `json:"enabled"`
	SongsPerWeek int32  `json:"songsPerWeek"`
	Mode         string `json:"mode"`
	LibraryShare int32  `json:"libraryShare"`
	OnePerArtist bool   `json:"onePerArtist"`
}

// weeklyLeaseResponse is one song on trial.
//
// State and the two dates are sent apart rather than as one sentence, because
// the screen says different things about a song whose week is still running and
// one that has been announced for removal, and a client that had to parse a
// sentence to tell them apart would eventually get it wrong.
type weeklyLeaseResponse struct {
	ID            string  `json:"id"`
	LibraryFileID *string `json:"libraryFileId"`
	Path          string  `json:"path"`
	Artist        string  `json:"artist"`
	Title         string  `json:"title"`
	State         string  `json:"state"`
	GrantedAt     string  `json:"grantedAt"`
	ExpiresAt     string  `json:"expiresAt"`
	RemovesAt     *string `json:"removesAt"`
	Extensions    int32   `json:"extensions"`
	// ExtendedReason is why this song got more time. It is the sentence the
	// screen shows when a week could not be read, so that a date moving has a
	// visible cause.
	ExtendedReason string `json:"extendedReason"`
}

// weeklyRunResponse is the account of one refresh.
type weeklyRunResponse struct {
	ID         string  `json:"id"`
	Mode       string  `json:"mode"`
	Status     string  `json:"status"`
	Detail     string  `json:"detail"`
	Chosen     int32   `json:"chosen"`
	Wanted     int32   `json:"wanted"`
	Arrived    int32   `json:"arrived"`
	Kept       int32   `json:"kept"`
	Extended   int32   `json:"extended"`
	Leaving    int32   `json:"leaving"`
	Removed    int32   `json:"removed"`
	Library    int32   `json:"library"`
	Replaced   int32   `json:"replaced"`
	StartedAt  string  `json:"startedAt"`
	FinishedAt *string `json:"finishedAt"`
}

type weeklyOverviewResponse struct {
	Settings   weeklySettingsResponse `json:"settings"`
	PlaylistID *string                `json:"playlistId"`
	Leases     []weeklyLeaseResponse  `json:"leases"`
	Runs       []weeklyRunResponse    `json:"runs"`
}

// getWeeklyPlaylist returns the whole screen in one read.
func (api *API) getWeeklyPlaylist(response http.ResponseWriter, request *http.Request) {
	if !api.weeklyAvailable(response) {
		return
	}
	overview, err := api.weekly.Overview(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	payload := weeklyOverviewResponse{
		Settings: weeklySettingsResponse{
			Enabled:      overview.Settings.Enabled,
			SongsPerWeek: overview.Settings.SongsPerWeek,
			Mode:         overview.Settings.Mode,
			LibraryShare: overview.Settings.LibraryShare,
			OnePerArtist: overview.Settings.OnePerArtist,
		},
		Leases: make([]weeklyLeaseResponse, 0, len(overview.Leases)),
		Runs:   make([]weeklyRunResponse, 0, len(overview.Runs)),
	}
	if overview.Playlist != nil {
		id := overview.Playlist.ID.String()
		payload.PlaylistID = &id
	}
	for _, lease := range overview.Leases {
		payload.Leases = append(payload.Leases, weeklyLeaseResponseFrom(lease))
	}
	for _, run := range overview.Runs {
		payload.Runs = append(payload.Runs, weeklyRunResponseFrom(run))
	}
	api.writeJSON(response, http.StatusOK, payload)
}

func weeklyLeaseResponseFrom(lease db.LibraryFileLeaseRow) weeklyLeaseResponse {
	row := weeklyLeaseResponse{
		ID:             lease.ID.String(),
		Path:           lease.LibraryPath,
		Artist:         lease.EntryArtist,
		Title:          lease.EntryTitle,
		State:          lease.State,
		GrantedAt:      weeklyStamp(lease.GrantedAt.Time),
		ExpiresAt:      weeklyStamp(lease.ExpiresAt.Time),
		Extensions:     lease.Extensions,
		ExtendedReason: lease.ExtendedReason,
	}
	if lease.LibraryFileID.Valid {
		id := lease.LibraryFileID.UUID.String()
		row.LibraryFileID = &id
	}
	if lease.RemovesAt.Valid {
		at := weeklyStamp(lease.RemovesAt.Time)
		row.RemovesAt = &at
	}
	return row
}

func weeklyRunResponseFrom(run db.WeeklyPlaylistRunRow) weeklyRunResponse {
	row := weeklyRunResponse{
		ID: run.ID.String(), Mode: run.Mode, Status: run.Status, Detail: run.Detail,
		Chosen: run.ChosenCount, Wanted: run.WantedCount, Arrived: run.ArrivedCount,
		Kept: run.KeptCount, Extended: run.ExtendedCount,
		Leaving: run.LeavingCount, Removed: run.RemovedCount,
		Library: run.LibraryCount, Replaced: run.ReplacedCount,
		StartedAt: weeklyStamp(run.StartedAt.Time),
	}
	if run.FinishedAt.Valid {
		at := weeklyStamp(run.FinishedAt.Time)
		row.FinishedAt = &at
	}
	return row
}

func weeklyStamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339)
}

// weeklySettingsRequest is what the settings screen sends.
type weeklySettingsRequest struct {
	Enabled      bool   `json:"enabled"`
	SongsPerWeek int32  `json:"songsPerWeek"`
	Mode         string `json:"mode"`
	LibraryShare int32  `json:"libraryShare"`
	OnePerArtist bool   `json:"onePerArtist"`
}

// saveWeeklyPlaylistSettings writes how the weekly playlist runs.
func (api *API) saveWeeklyPlaylistSettings(response http.ResponseWriter, request *http.Request) {
	if !api.weeklyAvailable(response) {
		return
	}
	var body weeklySettingsRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", nil)
		return
	}

	var complaints []string
	if body.SongsPerWeek < 1 || body.SongsPerWeek > 50 {
		complaints = append(complaints, "songsPerWeek must be between 1 and 50")
	}
	if body.Mode != weekly.ModeReport && body.Mode != weekly.ModeRemove {
		complaints = append(complaints, "mode must be report or remove")
	}
	if body.LibraryShare < 0 || body.LibraryShare > 100 {
		complaints = append(complaints, "libraryShare must be between 0 and 100")
	}
	if len(complaints) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", complaints)
		return
	}

	saved, err := api.weekly.SaveSettings(request.Context(), db.SaveWeeklyPlaylistSettingsParams{
		Enabled: body.Enabled, SongsPerWeek: body.SongsPerWeek, Mode: body.Mode,
		LibraryShare: body.LibraryShare, OnePerArtist: body.OnePerArtist,
	})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, weeklySettingsResponse{
		Enabled: saved.Enabled, SongsPerWeek: saved.SongsPerWeek, Mode: saved.Mode,
		LibraryShare: saved.LibraryShare, OnePerArtist: saved.OnePerArtist,
	})
}

// keepWeeklySong is the Keep control: this song stays.
//
// It is keyed by the library file rather than by the lease, because the file is
// what the reader is looking at, and because pressing Keep twice — on the row
// and again after a refresh — must mean the same thing both times.
func (api *API) keepWeeklySong(response http.ResponseWriter, request *http.Request) {
	if !api.weeklyAvailable(response) {
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid file id", nil)
		return
	}
	lease, err := api.weekly.Keep(request.Context(), fileID)
	switch {
	case errors.Is(err, weekly.ErrNotLeased):
		api.problem(response, http.StatusNotFound, err.Error(), nil)
		return
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "this song is not on the weekly playlist", nil)
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, weeklyLeaseResponseFrom(lease))
}

// weeklyLeaseEvidence returns what was read about one song and what each answer
// said. It is the account behind a removal.
func (api *API) weeklyLeaseEvidence(response http.ResponseWriter, request *http.Request) {
	if !api.weeklyAvailable(response) {
		return
	}
	leaseID, err := uuid.Parse(chi.URLParam(request, "leaseID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid lease id", nil)
		return
	}
	reads, err := api.weekly.LeaseEvidence(request.Context(), leaseID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	type readResponse struct {
		Signal  string `json:"signal"`
		Outcome string `json:"outcome"`
		Detail  string `json:"detail"`
		ReadAt  string `json:"readAt"`
	}
	items := make([]readResponse, 0, len(reads))
	for _, read := range reads {
		items = append(items, readResponse{
			Signal: read.Signal, Outcome: read.Outcome,
			Detail: read.Detail, ReadAt: weeklyStamp(read.ReadAt.Time),
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": items})
}
