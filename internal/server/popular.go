package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
	"github.com/pxldi/schall/internal/popular"
)

// PopularReader answers which of an artist's recordings people listen to most.
type PopularReader interface {
	ForArtist(ctx context.Context, artistMBID string) ([]listenbrainz.PopularRecording, error)
}

// PopularTrackStore says what the catalogue holds of those recordings.
type PopularTrackStore interface {
	PopularTracks(ctx context.Context, artistID uuid.UUID, recordings []uuid.UUID) ([]db.PopularTrackRow, error)
}

// WithPopular registers the source of an artist's popular tracks.
func WithPopular(reader PopularReader) Option {
	return func(api *API) {
		api.popular = reader
	}
}

type popularTrackResponse struct {
	RecordingID uuid.UUID `json:"recordingId"`
	Title       string    `json:"title"`
	// ReleaseTitle is the release ListenBrainz counted the listens under, used
	// when the catalogue has no release carrying the recording.
	ReleaseTitle string `json:"releaseTitle"`
	LengthMS     int64  `json:"lengthMs"`
	ListenCount  int64  `json:"listenCount"`
	// The track and release that carry the recording in the catalogue, when
	// one does. The recording is matched by MusicBrainz ID and nothing else.
	TrackID    *uuid.UUID `json:"trackId"`
	AlbumID    *uuid.UUID `json:"albumId"`
	AlbumTitle *string    `json:"albumTitle"`
	HasCover   bool       `json:"hasCover"`
	Held       bool       `json:"held"`
	WantID     *uuid.UUID `json:"wantId"`
	WantStatus *string    `json:"wantStatus"`
	// RemovesAt is when a file of the recording is deleted, because somebody
	// removed it. Absent when nothing is leaving.
	RemovesAt *time.Time `json:"removesAt,omitempty"`
}

type popularResponse struct {
	Items []popularTrackResponse `json:"items"`
	// Unavailable says why there is no list, when the reason is one somebody
	// can fix. It is absent when ListenBrainz simply has no counts.
	Unavailable string `json:"unavailable,omitempty"`
}

func (api *API) artistPopular(response http.ResponseWriter, request *http.Request) {
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	artist, err := api.store.GetArtist(request.Context(), artistID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	empty := popularResponse{Items: []popularTrackResponse{}}
	tracks, _ := api.store.(PopularTrackStore)
	if api.popular == nil || tracks == nil || !artist.MusicbrainzID.Valid {
		api.writeJSON(response, http.StatusOK, empty)
		return
	}

	recordings, err := api.popular.ForArtist(request.Context(), artist.MusicbrainzID.UUID.String())
	if errors.Is(err, popular.ErrNoToken) {
		empty.Unavailable = "Add a ListenBrainz token in Settings to see popular tracks."
		api.writeJSON(response, http.StatusOK, empty)
		return
	}
	if err != nil {
		// A box on an artist's page, read from a service outside the house: its
		// absence is logged and the page shows the rest.
		api.logger.Warn().Err(err).Str("artist_id", artistID.String()).Msg("popular tracks unavailable")
		empty.Unavailable = "ListenBrainz did not answer. Try again later."
		api.writeJSON(response, http.StatusOK, empty)
		return
	}

	ids := make([]uuid.UUID, 0, len(recordings))
	byID := make(map[uuid.UUID]listenbrainz.PopularRecording, len(recordings))
	for _, recording := range recordings {
		id, err := uuid.Parse(recording.RecordingMBID)
		if err != nil {
			continue
		}
		if _, seen := byID[id]; seen {
			continue
		}
		ids = append(ids, id)
		byID[id] = recording
	}
	rows, err := tracks.PopularTracks(request.Context(), artistID, ids)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	result := popularResponse{Items: make([]popularTrackResponse, 0, len(rows))}
	for _, row := range rows {
		recording := byID[row.RecordingID]
		result.Items = append(result.Items, popularTrackResponse{
			RecordingID:  row.RecordingID,
			Title:        recording.Name,
			ReleaseTitle: recording.ReleaseName,
			LengthMS:     recording.LengthMS,
			ListenCount:  recording.ListenCount,
			TrackID:      nullableUUID(row.TrackID),
			AlbumID:      nullableUUID(row.AlbumID),
			AlbumTitle:   textPointer(row.AlbumTitle),
			HasCover:     row.HasCover,
			Held:         row.Held,
			WantID:       nullableUUID(row.WantID),
			WantStatus:   textPointer(row.WantStatus),
			RemovesAt:    nullableTime(row.RemovesAt.Valid, row.RemovesAt.Time),
		})
	}
	api.writeJSON(response, http.StatusOK, result)
}
