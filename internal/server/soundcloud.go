package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/coverart"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/soundcloud"
)

// SoundCloudTracks answers what one SoundCloud address holds, and records that
// a library file is the track at it.
//
// Optional. Without it the routes report themselves unavailable, which is what
// an installation with no outbound access to SoundCloud should see.
type SoundCloudTracks interface {
	Preview(ctx context.Context, permalink string) (soundcloud.Track, error)
	Adopt(ctx context.Context, fileID uuid.UUID, permalink string,
		confirmed soundcloud.Naming) (soundcloud.Adopted, error)
}

// WithSoundCloud registers the component that reads SoundCloud's oEmbed answer.
func WithSoundCloud(tracks SoundCloudTracks) Option {
	return func(api *API) {
		api.soundcloud = tracks
	}
}

// soundCloudTrackResponse is what SoundCloud said, in the shape the dialog
// draws: the whole title as it arrived, the title with oEmbed's own suffix off,
// who uploaded it, and where the picture is.
type soundCloudTrackResponse struct {
	ExternalID string `json:"externalId"`
	// Title is oEmbed's title, verbatim, and it is stored verbatim.
	Title string `json:"title"`
	// TrackTitle is what goes into the file: Title without the " by <uploader>"
	// that oEmbed itself adds.
	TrackTitle  string `json:"trackTitle"`
	Uploader    string `json:"uploader"`
	UploaderURL string `json:"uploaderUrl,omitempty"`
	ArtworkURL  string `json:"artworkUrl,omitempty"`
	Permalink   string `json:"permalink"`
	// Suggested is what Schall reads off the title: who the track is by, what
	// it is called, and who edited it. It is what the dialog's three fields
	// start at, and the person sends back whatever they made of it.
	Suggested soundCloudNamingBody `json:"suggested"`
}

// soundCloudNamingBody is one naming, going either way: out as the suggestion
// and back as the answer.
type soundCloudNamingBody struct {
	Artist  string `json:"artist"`
	Title   string `json:"title"`
	Remixer string `json:"remixer"`
}

func (body soundCloudNamingBody) naming() soundcloud.Naming {
	return soundcloud.Naming{
		Artist:  strings.TrimSpace(body.Artist),
		Title:   strings.TrimSpace(body.Title),
		Remixer: strings.TrimSpace(body.Remixer),
	}
}

func soundCloudTrack(track soundcloud.Track) soundCloudTrackResponse {
	suggested := soundcloud.ReadNaming(track.TrackTitle(), track.Uploader)
	return soundCloudTrackResponse{
		ExternalID:  track.ExternalID,
		Title:       track.Title,
		TrackTitle:  track.TrackTitle(),
		Uploader:    track.Uploader,
		UploaderURL: track.UploaderURL,
		ArtworkURL:  track.ArtworkURL,
		Permalink:   track.Permalink,
		Suggested: soundCloudNamingBody{
			Artist:  suggested.Artist,
			Title:   suggested.Title,
			Remixer: suggested.Remixer,
		},
	}
}

type soundCloudAdoptRequest struct {
	URL string `json:"url"`
	// Artist, Title and Remixer are the person's answer to the suggestion. A
	// body that carries none of them is named from the title alone, which is
	// what Schall did before the dialog offered a split.
	Artist  string `json:"artist"`
	Title   string `json:"title"`
	Remixer string `json:"remixer"`
}

type soundCloudAdoptResponse struct {
	Track soundCloudTrackResponse `json:"track"`
	// Named is what was written, which is what the person confirmed.
	Named    soundCloudNamingBody `json:"named"`
	ArtistID uuid.UUID            `json:"artistId"`
	Summary  string               `json:"summary"`
	Pictured bool                 `json:"pictured"`
}

func (api *API) soundCloudAvailable(response http.ResponseWriter) bool {
	if api.soundcloud == nil {
		api.problem(response, http.StatusServiceUnavailable,
			"Schall cannot reach SoundCloud",
			[]string{"This installation is not set up to read SoundCloud track pages."})
		return false
	}
	return true
}

// soundCloudProblem turns the two refusals a person can cause into the two
// sentences they can act on: what happened to this address, and what to do.
func (api *API) soundCloudProblem(response http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, soundcloud.ErrNotATrack):
		api.problem(response, http.StatusBadRequest,
			"That link is not a SoundCloud track",
			[]string{"Open the track on SoundCloud and copy the link from the address bar."})
		return true
	case errors.Is(err, soundcloud.ErrTrackGone):
		api.problem(response, http.StatusNotFound,
			"SoundCloud no longer has that track",
			[]string{"Check the link, or open it on SoundCloud to see whether it moved."})
		return true
	case errors.Is(err, soundcloud.ErrFileNotFound):
		api.problem(response, http.StatusNotFound,
			"That file is not in the library",
			[]string{"Scan the library and open the file again."})
		return true
	case errors.Is(err, soundcloud.ErrFileAlreadyMatched):
		api.problem(response, http.StatusConflict,
			"A scan already matched this file",
			[]string{"Check the match on the file's page before naming it from SoundCloud."})
		return true
	}
	return false
}

// lookUpSoundCloudTrack shows what SoundCloud says about an address, and
// changes nothing.
//
// It exists so that a permanent decision can be looked at before it is made:
// the person pastes a link, sees the title, the uploader and the artwork that
// came back, and only then confirms.
func (api *API) lookUpSoundCloudTrack(response http.ResponseWriter, request *http.Request) {
	if !api.soundCloudAvailable(response) {
		return
	}
	permalink := strings.TrimSpace(request.URL.Query().Get("url"))
	track, err := api.soundcloud.Preview(request.Context(), permalink)
	if api.soundCloudProblem(response, err) {
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, soundCloudTrack(track))
}

// nameFileFromSoundCloud records that a library file holds the track at one
// SoundCloud address.
//
// This is a manual decision and it is permanent: the file stops being asked
// about, and its tags and its picture come from what SoundCloud published. It
// says nothing about any MusicBrainz recording, so the file still cannot
// satisfy a want.
func (api *API) nameFileFromSoundCloud(response http.ResponseWriter, request *http.Request) {
	if !api.soundCloudAvailable(response) {
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	var payload soundCloudAdoptRequest
	if err := decodeJSON(response, request, &payload); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	confirmed := soundCloudNamingBody{
		Artist: payload.Artist, Title: payload.Title, Remixer: payload.Remixer,
	}.naming()
	adopted, err := api.soundcloud.Adopt(request.Context(), fileID,
		strings.TrimSpace(payload.URL), confirmed)
	if api.soundCloudProblem(response, err) {
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusOK, soundCloudAdoptResponse{
		Track:    soundCloudTrack(adopted.Track),
		Named:    soundCloudNamingBody{Artist: adopted.Naming.Artist, Title: adopted.Naming.Title, Remixer: adopted.Naming.Remixer},
		ArtistID: adopted.ArtistID,
		Summary:  adopted.Summary,
		Pictured: adopted.Pictured,
	})
}

// libraryFileCover serves the picture held for one file.
//
// A release's cover is served from the release. A track that belongs to no
// release has its picture held against the file, and this is where the library
// list reads it from.
func (api *API) libraryFileCover(response http.ResponseWriter, request *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if api.fileCovers == nil {
		api.problem(response, http.StatusNotFound, "no cover art", nil)
		return
	}
	cached, err := api.fileCovers.FileCoverArt(request.Context(), fileID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.embeddedFileCover(response, request, fileID)
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeCover(response, request, cached.Image, cached.ContentType)
	}
}

// embeddedFileCover serves the picture packed into a file nothing has fetched a
// picture for: a file that belongs to no release yet, or one whose release the
// archives could not picture. It is read from the file on each request and not
// cached in file_cover_art, because that table is also the list of pictures the
// disc pass writes beside a file, and a release folder is pictured by the
// release pass. The browser caches the answer for a day.
func (api *API) embeddedFileCover(response http.ResponseWriter, request *http.Request, fileID uuid.UUID) {
	if api.library == nil {
		api.cachedCoverMiss(response, "no cover art")
		return
	}
	file, err := api.library.LibraryFileByID(request.Context(), fileID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	artwork, err := coverart.Embedded([]string{file.Path})
	if err != nil {
		if !errors.Is(err, coverart.ErrNoArtwork) {
			api.logger.Warn().Err(err).Str("file_id", fileID.String()).Msg("read the picture in a file")
		}
		api.cachedCoverMiss(response, "no cover art")
		return
	}
	api.writeCover(response, request, artwork.Image, artwork.ContentType)
}
