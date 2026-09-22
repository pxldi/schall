package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tracksource"
)

// SourceKeys keys a want to the address of a track, and sets a keyed want's
// floor (ADR 0038). Optional: without it the routes report themselves
// unavailable.
type SourceKeys interface {
	LookUpSource(ctx context.Context, address string) (tracksource.Track, error)
	KeyToSource(ctx context.Context, id uuid.UUID, address, confirmedID string) (db.AcquisitionTargetRow, error)
	SetMinimumBitrate(ctx context.Context, id uuid.UUID, kbps int) (db.AcquisitionTargetRow, error)
}

// WithSourceKeys registers what keys a want to an address.
func WithSourceKeys(keys SourceKeys) Option {
	return func(api *API) {
		api.sourceKeys = keys
	}
}

// sourceTrackResponse is what yt-dlp said an address holds, in the words the
// person confirms: the service's own title and artist, the uploader and the
// length. Nothing here is compared with the entry.
type sourceTrackResponse struct {
	Source     string `json:"source"`
	ExternalID string `json:"externalId"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Uploader   string `json:"uploader"`
	AccountID  string `json:"accountId,omitempty"`
	DurationMS *int   `json:"durationMs"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
}

func sourceTrackResponseFrom(track tracksource.Track) sourceTrackResponse {
	response := sourceTrackResponse{
		Source: track.Source, ExternalID: track.ExternalID, URL: track.URL,
		Title: track.Title, Artist: track.Artist, Uploader: track.Uploader,
		AccountID: track.AccountID, ArtworkURL: track.ArtworkURL,
	}
	if track.DurationMS > 0 {
		duration := track.DurationMS
		response.DurationMS = &duration
	}
	return response
}

type keySourceRequest struct {
	URL string `json:"url"`
	// ExternalID is the track the person was shown. When it is sent and the
	// address now names another, nothing is keyed.
	ExternalID string `json:"externalId"`
}

type minimumBitrateRequest struct {
	MinimumBitrate *int `json:"minimumBitrate"`
}

func (api *API) sourceKeysAvailable(response http.ResponseWriter) bool {
	if api.sourceKeys == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"Schall cannot read track addresses",
			[]string{"This installation is not set up to read SoundCloud, YouTube or Bandcamp addresses."})
		return false
	}
	return true
}

// sourceKeyProblem turns what reading an address or keying a want can refuse
// into what the person can act on. It reports whether it wrote a response.
func (api *API) sourceKeyProblem(response http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, tracksource.ErrNotATrack):
		api.problem(response, http.StatusBadRequest, "That link is not a single track",
			[]string{"Paste the link to one track on SoundCloud, YouTube or Bandcamp."})
	case errors.Is(err, tracksource.ErrGone):
		api.problem(response, http.StatusNotFound, "The service no longer has that track",
			[]string{"Open the link to check it still plays."})
	case errors.Is(err, tracksource.ErrRateLimited):
		api.problem(response, http.StatusServiceUnavailable, "The service is refusing requests",
			[]string{"Try again in a few minutes."})
	case errors.Is(err, tracksource.ErrUnavailable), errors.Is(err, acquisition.ErrNoTrackSources),
		errors.Is(err, chromaprint.ErrNoFpcalc):
		api.problem(response, http.StatusServiceUnavailable, "Schall cannot read track addresses",
			[]string{"yt-dlp and fpcalc have to be installed to key a want to an address."})
	case errors.Is(err, acquisition.ErrExcerptUnreadable):
		api.problem(response, http.StatusUnprocessableEntity, "The audio at that link could not be read",
			[]string{"Nothing was keyed. Try another link to the same track."})
	case errors.Is(err, acquisition.ErrAddressChanged):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"Look the link up again and check the track before you confirm."})
	case errors.Is(err, db.ErrAlreadyKeyed):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"An address a person confirmed is permanent."})
	case errors.Is(err, db.ErrNotKeyable):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"Only an unresolved want, or one asking which version it is, takes an address."})
	case errors.Is(err, db.ErrNotKeyed):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"Key the want to an address first."})
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "acquisition target not found", nil)
	default:
		return false
	}
	return true
}

// lookUpSourceTrack says what an address holds, and changes nothing.
func (api *API) lookUpSourceTrack(response http.ResponseWriter, request *http.Request) {
	if !api.sourceKeysAvailable(response) {
		return
	}
	address := strings.TrimSpace(request.URL.Query().Get("url"))
	track, err := api.sourceKeys.LookUpSource(request.Context(), address)
	if api.sourceKeyProblem(response, err) {
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, sourceTrackResponseFrom(track))
}

// keyAcquisitionTargetToSource keys a want to the track at an address. It is a
// person's decision and permanent (ADR 0038 §1).
func (api *API) keyAcquisitionTargetToSource(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok || !api.sourceKeysAvailable(response) {
		return
	}
	var input keySourceRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if strings.TrimSpace(input.URL) == "" {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"url must be the link to one track"})
		return
	}
	target, err := api.sourceKeys.KeyToSource(request.Context(), targetID,
		strings.TrimSpace(input.URL), input.ExternalID)
	if api.sourceKeyProblem(response, err) {
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
}

// setAcquisitionTargetMinimumBitrate sets a keyed want's floor (ADR 0038 §7).
func (api *API) setAcquisitionTargetMinimumBitrate(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok || !api.sourceKeysAvailable(response) {
		return
	}
	var input minimumBitrateRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if input.MinimumBitrate == nil || *input.MinimumBitrate <= 0 || *input.MinimumBitrate > 10000 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"minimumBitrate must be a bit rate in kbit/s, from 1 to 10000"})
		return
	}
	target, err := api.sourceKeys.SetMinimumBitrate(request.Context(), targetID, *input.MinimumBitrate)
	if api.sourceKeyProblem(response, err) {
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
}
