package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// LabelStore holds the record labels a person follows and what those labels
// published. It is optional in the same way the other stores here are: an
// installation whose store does not provide it simply has no labels.
type LabelStore interface {
	ListLabels(context.Context) ([]db.ListLabelsRow, error)
	GetLabel(context.Context, uuid.UUID) (db.GetLabelRow, error)
	LabelReleases(context.Context, uuid.UUID) ([]db.LabelReleasesRow, error)
	CreateLabel(context.Context, db.CreateLabelParams) (db.CreateLabelRow, error)
	UnfollowLabel(context.Context, uuid.UUID) (db.UnfollowLabelRow, error)
	SetLabelMonitorLevel(context.Context, db.SetLabelMonitorLevelParams) (db.SetLabelMonitorLevelRow, error)
	QueueLabelRefresh(context.Context, uuid.UUID) (db.QueueLabelRefreshRow, error)
}

// LabelSearcher asks MusicBrainz which labels answer to a name. It is the same
// client that searches for artists, asked a different question.
type LabelSearcher interface {
	SearchLabels(context.Context, string, int) ([]musicbrainz.Label, error)
}

type labelResponse struct {
	ID             uuid.UUID  `json:"id"`
	MusicBrainzID  uuid.UUID  `json:"musicbrainzId"`
	Name           string     `json:"name"`
	Type           string     `json:"type,omitempty"`
	Country        string     `json:"country,omitempty"`
	Disambiguation string     `json:"disambiguation,omitempty"`
	MonitorLevel   string     `json:"monitorLevel"`
	Followed       bool       `json:"followed"`
	FollowedAt     *time.Time `json:"followedAt"`
	// LastRefreshedAt is when a walk over the label's releases last reached the
	// end. Empty means no walk has finished yet, which is what a label followed
	// a minute ago reads as.
	LastRefreshedAt *time.Time `json:"lastRefreshedAt"`
	RefreshStatus   string     `json:"refreshStatus"`
}

type labelListItem struct {
	labelResponse
	ReleaseCount      int64 `json:"releaseCount"`
	OwnedReleaseCount int64 `json:"ownedReleaseCount"`
	TrackCount        int64 `json:"trackCount"`
	OwnedTrackCount   int64 `json:"ownedTrackCount"`
}

type labelReleaseResponse struct {
	ID                        uuid.UUID  `json:"id"`
	Title                     string     `json:"title"`
	MusicBrainzReleaseGroupID *uuid.UUID `json:"musicbrainzReleaseGroupId"`
	ReleaseDate               string     `json:"releaseDate,omitempty"`
	AlbumType                 string     `json:"albumType"`
	ArtistID                  uuid.UUID  `json:"artistId"`
	ArtistName                string     `json:"artistName"`
	TrackCount                int64      `json:"trackCount"`
	OwnedTrackCount           int64      `json:"ownedTrackCount"`
	// Monitored says whether the label's monitor level counts this release.
	// An unmonitored release stays listed and can still be wanted by hand; it
	// just stops being missing.
	Monitored bool `json:"monitored"`
}

type labelSearchItem struct {
	MusicBrainzID  uuid.UUID `json:"musicbrainzId"`
	Name           string    `json:"name"`
	Type           string    `json:"type,omitempty"`
	Country        string    `json:"country,omitempty"`
	Area           string    `json:"area,omitempty"`
	Disambiguation string    `json:"disambiguation,omitempty"`
	Score          int       `json:"score"`
}

// searchLabels asks MusicBrainz about labels Schall does not hold, so a person
// can pick the one they mean. Nothing is stored by asking.
func (api *API) searchLabels(response http.ResponseWriter, request *http.Request) {
	if api.labelSearch == nil {
		api.problem(response, http.StatusNotFound, "label search is not available", nil)
		return
	}
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"q must be at least 2 characters"})
		return
	}
	if len([]rune(query)) > 200 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"q must be 200 characters or fewer"})
		return
	}

	labels, err := api.labelSearch.SearchLabels(request.Context(), query, 10)
	if err != nil {
		var providerError *musicbrainz.HTTPError
		if errors.As(err, &providerError) && providerError.StatusCode == http.StatusServiceUnavailable {
			api.problem(response, http.StatusServiceUnavailable,
				"MusicBrainz is temporarily unavailable", nil)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		api.logger.Error().Err(err).Msg("MusicBrainz label search failed")
		api.problem(response, http.StatusBadGateway, "label search provider failed", nil)
		return
	}

	items := make([]labelSearchItem, 0, len(labels))
	for _, label := range labels {
		items = append(items, labelSearchItem{
			MusicBrainzID:  label.ID,
			Name:           label.Name,
			Type:           label.Type,
			Country:        label.Country,
			Area:           label.Area,
			Disambiguation: label.Disambiguation,
			Score:          label.Score,
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (api *API) listLabels(response http.ResponseWriter, request *http.Request) {
	if api.labels == nil {
		api.problem(response, http.StatusNotFound, "labels are not available", nil)
		return
	}
	rows, err := api.labels.ListLabels(request.Context())
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items := make([]labelListItem, 0, len(rows))
	followed := 0
	for _, row := range rows {
		if row.FollowedAt.Valid {
			followed++
		}
		items = append(items, labelListItem{
			labelResponse: labelResponse{
				ID:              row.ID,
				MusicBrainzID:   row.MusicbrainzID,
				Name:            row.Name,
				Type:            row.LabelType,
				Country:         row.Country,
				Disambiguation:  row.Disambiguation,
				MonitorLevel:    row.MonitorLevel,
				Followed:        row.FollowedAt.Valid,
				FollowedAt:      nullableTime(row.FollowedAt.Valid, row.FollowedAt.Time),
				LastRefreshedAt: nullableTime(row.LastRefreshedAt.Valid, row.LastRefreshedAt.Time),
				RefreshStatus:   row.RefreshStatus,
			},
			ReleaseCount:      row.ReleaseCount,
			OwnedReleaseCount: row.OwnedReleaseCount,
			TrackCount:        row.TrackCount,
			OwnedTrackCount:   row.OwnedTrackCount,
		})
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"items": items, "total": len(items), "followedCount": followed,
	})
}

// getLabel answers with one label and everything it published, because the page
// showing a label shows both and asking twice would be two round trips for one
// screen.
func (api *API) getLabel(response http.ResponseWriter, request *http.Request) {
	if api.labels == nil {
		api.problem(response, http.StatusNotFound, "labels are not available", nil)
		return
	}
	labelID, err := uuid.Parse(chi.URLParam(request, "labelID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	row, err := api.labels.GetLabel(request.Context(), labelID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	releases, err := api.labels.LabelReleases(request.Context(), labelID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	items := make([]labelReleaseResponse, 0, len(releases))
	for _, release := range releases {
		item := labelReleaseResponse{
			ID:                        release.ID,
			Title:                     release.Title,
			MusicBrainzReleaseGroupID: nullableUUID(release.MusicbrainzReleaseGroupID),
			AlbumType:                 release.AlbumType,
			ArtistID:                  release.ArtistID,
			ArtistName:                release.ArtistName,
			TrackCount:                release.TrackCount,
			OwnedTrackCount:           release.OwnedTrackCount,
			Monitored:                 release.Monitored,
		}
		if release.ReleaseDate.Valid {
			item.ReleaseDate = release.ReleaseDate.Time.Format("2006-01-02")
		}
		items = append(items, item)
	}

	api.writeJSON(response, http.StatusOK, map[string]any{
		"label": labelResponse{
			ID:              row.ID,
			MusicBrainzID:   row.MusicbrainzID,
			Name:            row.Name,
			Type:            row.LabelType,
			Country:         row.Country,
			Disambiguation:  row.Disambiguation,
			MonitorLevel:    row.MonitorLevel,
			Followed:        row.FollowedAt.Valid,
			FollowedAt:      nullableTime(row.FollowedAt.Valid, row.FollowedAt.Time),
			LastRefreshedAt: nullableTime(row.LastRefreshedAt.Valid, row.LastRefreshedAt.Time),
			RefreshStatus:   row.RefreshStatus,
		},
		"releases": items,
	})
}

type followLabelRequest struct {
	MusicBrainzID  uuid.UUID `json:"musicbrainzId"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Country        string    `json:"country"`
	Disambiguation string    `json:"disambiguation"`
}

// followLabel records the standing request to keep what a label publishes
// complete, and queues the first walk over its releases.
func (api *API) followLabel(response http.ResponseWriter, request *http.Request) {
	if api.labels == nil {
		api.problem(response, http.StatusNotFound, "labels are not available", nil)
		return
	}
	var input followLabelRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.MusicBrainzID == uuid.Nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"musicbrainzId is required"})
		return
	}
	if input.Name == "" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"name is required"})
		return
	}
	if len(input.Name) > 300 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"name must be 300 characters or fewer"})
		return
	}

	label, err := api.labels.CreateLabel(request.Context(), db.CreateLabelParams{
		MusicbrainzID:  input.MusicBrainzID,
		Name:           input.Name,
		LabelType:      strings.TrimSpace(input.Type),
		Country:        strings.TrimSpace(input.Country),
		Disambiguation: strings.TrimSpace(input.Disambiguation),
	})
	// A label Schall already holds is followed again rather than rejected, and
	// the query returns no row only when it was genuinely already followed.
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusConflict, "label is already followed", nil)
		return
	}
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			api.problem(response, http.StatusConflict, "label is already followed", nil)
			return
		}
		api.internalError(response, request, err)
		return
	}
	api.events.Publish(events.TopicCatalogue)

	api.writeJSON(response, http.StatusCreated, labelResponse{
		ID:              label.ID,
		MusicBrainzID:   label.MusicbrainzID,
		Name:            label.Name,
		Type:            label.LabelType,
		Country:         label.Country,
		Disambiguation:  label.Disambiguation,
		MonitorLevel:    label.MonitorLevel,
		Followed:        label.FollowedAt.Valid,
		FollowedAt:      nullableTime(label.FollowedAt.Valid, label.FollowedAt.Time),
		LastRefreshedAt: nullableTime(label.LastRefreshedAt.Valid, label.LastRefreshedAt.Time),
		RefreshStatus:   "queued",
	})
}

// unfollowLabel withdraws that request and keeps everything. The label's row
// and the releases it published stay, so following again costs no refetch, and
// nothing the library holds is touched: the releases belong to their artists.
func (api *API) unfollowLabel(response http.ResponseWriter, request *http.Request) {
	if api.labels == nil {
		api.problem(response, http.StatusNotFound, "labels are not available", nil)
		return
	}
	labelID, err := uuid.Parse(chi.URLParam(request, "labelID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	label, err := api.labels.UnfollowLabel(request.Context(), labelID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row means the label is gone or was never followed. Saying which is
		// more useful than reporting success for a change nobody made.
		if _, lookupErr := api.labels.GetLabel(request.Context(), labelID); lookupErr == nil {
			api.problem(response, http.StatusConflict, "label is not followed", nil)
			return
		}
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.events.Publish(events.TopicCatalogue)

	api.writeJSON(response, http.StatusOK, labelResponse{
		ID:              label.ID,
		MusicBrainzID:   label.MusicbrainzID,
		Name:            label.Name,
		Type:            label.LabelType,
		Country:         label.Country,
		Disambiguation:  label.Disambiguation,
		MonitorLevel:    label.MonitorLevel,
		Followed:        false,
		FollowedAt:      nil,
		LastRefreshedAt: nullableTime(label.LastRefreshedAt.Valid, label.LastRefreshedAt.Time),
		RefreshStatus:   "cancelled",
	})
}

// setLabelMonitorLevel records what counts as one of this label's releases.
func (api *API) setLabelMonitorLevel(response http.ResponseWriter, request *http.Request) {
	if api.labels == nil {
		api.problem(response, http.StatusNotFound, "labels are not available", nil)
		return
	}
	labelID, err := uuid.Parse(chi.URLParam(request, "labelID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	var body struct {
		Level string `json:"level"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	// The same four levels an artist has, and the same closed set the schema
	// enforces.
	if !artistMonitorLevels[body.Level] {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"level must be one of everything, main, albums_eps, or owned"})
		return
	}
	result, err := api.labels.SetLabelMonitorLevel(request.Context(), db.SetLabelMonitorLevelParams{
		LabelID: labelID, MonitorLevel: body.Level,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().
		Str("label_id", labelID.String()).
		Str("monitor_level", result.MonitorLevel).
		Msg("label monitor level set")
	api.writeJSON(response, http.StatusOK, map[string]any{
		"labelId": result.ID, "monitorLevel": result.MonitorLevel,
	})
}

// refreshLabel asks for the label's releases to be read again now. Asking while
// a walk is queued or running answers with that walk.
func (api *API) refreshLabel(response http.ResponseWriter, request *http.Request) {
	if api.labels == nil {
		api.problem(response, http.StatusNotFound, "labels are not available", nil)
		return
	}
	labelID, err := uuid.Parse(chi.URLParam(request, "labelID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	job, err := api.labels.QueueLabelRefresh(request.Context(), labelID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "label not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.events.Publish(events.TopicCatalogue)

	api.writeJSON(response, http.StatusAccepted, refreshResponse{
		JobID:     job.ID,
		Status:    job.Status,
		CreatedAt: job.CreatedAt.Time,
	})
}
