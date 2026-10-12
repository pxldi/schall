package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/removals"
)

// Removals takes music back out: the want at once, a file Schall downloaded
// for it 30 days later.
type Removals interface {
	Remove(ctx context.Context, targetID uuid.UUID) (removals.Outcome, error)
	Keep(ctx context.Context, targetID uuid.UUID) (int, error)
	KeepRecording(ctx context.Context, recordingID uuid.UUID) ([]uuid.UUID, error)
}

// WithRemovals registers what removing a want goes through. Without it the
// remove routes answer 503 and wanting again only takes back the want.
func WithRemovals(removals Removals) Option {
	return func(api *API) {
		api.removals = removals
	}
}

// removedResponse says what one remove press did: the wants it took away, the
// files now waiting to be deleted and when, and the files that stay because
// Schall did not download them.
type removedResponse struct {
	Removed      int                        `json:"removed"`
	Scheduled    []removals.Scheduled       `json:"scheduled"`
	Kept         int                        `json:"kept"`
	Unresolvable int                        `json:"unresolvable"`
	Target       *acquisitionTargetResponse `json:"target,omitempty"`
}

func (result *removedResponse) add(outcome removals.Outcome) {
	result.Removed++
	result.Scheduled = append(result.Scheduled, outcome.Scheduled...)
	result.Kept += outcome.Kept
}

func (api *API) removalsAvailable(response http.ResponseWriter) bool {
	if !api.acquisitionsAvailable(response) {
		return false
	}
	if api.removals == nil {
		api.problem(response, http.StatusServiceUnavailable, "removing is unavailable", nil)
		return false
	}
	return true
}

// removeTrack takes away the want for one track's recording. A track nobody
// asked for, or one the library already holds, gets a want recorded and
// removed in the same press, so the follow feed does not ask for it again once
// its file is gone.
func (api *API) removeTrack(response http.ResponseWriter, request *http.Request) {
	if !api.removalsAvailable(response) {
		return
	}
	track, ok := api.wantedTrack(response, request)
	if !ok {
		return
	}
	target, _, err := api.wants.Create(request.Context(), trackEntry(track))
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	outcome, err := api.removals.Remove(request.Context(), target.ID)
	if !api.removed(response, request, err) {
		return
	}
	result := removedResponse{Scheduled: []removals.Scheduled{}}
	result.add(outcome)
	api.withTarget(request.Context(), &result, target.ID)
	api.writeJSON(response, http.StatusOK, result)
}

// removeRelease is removeTrack for every track of a release with a recording.
func (api *API) removeRelease(response http.ResponseWriter, request *http.Request) {
	if !api.removalsAvailable(response) {
		return
	}
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
	tracks, err := api.store.ListAlbumTracks(request.Context(), albumID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := removedResponse{Scheduled: []removals.Scheduled{}}
	seen := make(map[uuid.UUID]struct{}, len(tracks))
	for _, track := range tracks {
		if !track.MusicbrainzRecordingID.Valid {
			result.Unresolvable++
			continue
		}
		if _, repeat := seen[track.MusicbrainzRecordingID.UUID]; repeat {
			continue
		}
		seen[track.MusicbrainzRecordingID.UUID] = struct{}{}
		targetID := track.WantID.UUID
		if !track.WantID.Valid {
			target, _, err := api.wants.Create(request.Context(), acquisition.Entry{
				Origin:         "manual",
				OriginAlbumID:  uuid.NullUUID{UUID: albumID, Valid: true},
				Artist:         album.ArtistName,
				Title:          track.Title,
				Album:          album.Title,
				DurationMS:     int32Pointer(track.DurationMs),
				ISRC:           track.Isrc.String,
				RecordingID:    track.MusicbrainzRecordingID,
				ReleaseGroupID: album.MusicbrainzReleaseGroupID,
			})
			if err != nil {
				api.internalError(response, request, err)
				return
			}
			targetID = target.ID
		}
		outcome, err := api.removals.Remove(request.Context(), targetID)
		if errors.Is(err, removals.ErrNothingToRemove) {
			continue
		}
		if err != nil {
			api.internalError(response, request, err)
			return
		}
		result.add(outcome)
	}
	api.logger.Info().
		Str("album_id", albumID.String()).
		Int("removed", result.Removed).
		Int("scheduled", len(result.Scheduled)).
		Int("kept", result.Kept).
		Msg("release removed")
	api.writeJSON(response, http.StatusOK, result)
}

// removeAcquisitionTarget is the same press on one wishlist row.
func (api *API) removeAcquisitionTarget(response http.ResponseWriter, request *http.Request) {
	if !api.removalsAvailable(response) {
		return
	}
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	outcome, err := api.removals.Remove(request.Context(), targetID)
	if !api.removed(response, request, err) {
		return
	}
	result := removedResponse{Scheduled: []removals.Scheduled{}}
	result.add(outcome)
	api.withTarget(request.Context(), &result, targetID)
	api.writeJSON(response, http.StatusOK, result)
}

// removed writes the problem for a failed remove and reports whether there was
// none.
func (api *API) removed(response http.ResponseWriter, request *http.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "acquisition target not found", nil)
	case errors.Is(err, removals.ErrNothingToRemove):
		api.problem(response, http.StatusConflict, "this want was merged into another one", nil)
	default:
		api.internalError(response, request, err)
	}
	return false
}

// withTarget adds the want as it now stands, for a page that draws it. A
// failed read leaves it out: the remove already happened.
func (api *API) withTarget(ctx context.Context, result *removedResponse, targetID uuid.UUID) {
	target, err := api.wants.Target(ctx, targetID)
	if err != nil {
		return
	}
	payload := acquisitionTargetResponseFrom(target)
	result.Target = &payload
}

// wantAgain takes a removal back: the files waiting to be deleted stay, and
// the want is pursued again. A want with a file in the library settles on that
// file at the loop's next look rather than fetching another copy.
func (api *API) wantAgain(ctx context.Context, targetID uuid.UUID) error {
	if api.removals != nil {
		if _, err := api.removals.Keep(ctx, targetID); err != nil {
			return err
		}
	}
	_, err := api.wants.PursueAgain(ctx, targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not stopped, so there is nothing to take back.
		return nil
	}
	return err
}

// keepOwnedTrack is wanting a track the library holds. It is only meaningful
// while a file of it is waiting to be deleted: it cancels that and wants the
// recording again. It reports whether there was anything to cancel.
func (api *API) keepOwnedTrack(ctx context.Context, recordingID uuid.UUID) (uuid.UUID, bool, error) {
	if api.removals == nil {
		return uuid.Nil, false, nil
	}
	targets, err := api.removals.KeepRecording(ctx, recordingID)
	if err != nil || len(targets) == 0 {
		return uuid.Nil, false, err
	}
	for _, targetID := range targets {
		if err := api.wantAgain(ctx, targetID); err != nil {
			return uuid.Nil, false, err
		}
	}
	return targets[0], true, nil
}
