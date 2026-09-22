package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/preview"
	"github.com/pxldi/schall/internal/tracksource"
)

// AcquisitionTargets records what somebody wants and what became of it.
type AcquisitionTargets interface {
	Create(context.Context, acquisition.Entry) (db.AcquisitionTargetRow, bool, error)
	Target(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error)
	List(context.Context, db.ListAcquisitionTargetsParams) (db.AcquisitionTargetPage, error)
	StopPursuing(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error)
	TakeBestAvailable(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error)
	KeepFloor(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error)
	PursueAgain(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error)
	Candidates(context.Context, uuid.UUID) ([]db.AcquisitionTargetCandidateRow, error)
	Copies(context.Context, uuid.UUID) ([]db.AcquisitionTargetFileRow, error)
	RejectResolution(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error)
	ChooseResolution(ctx context.Context, id, recordingID uuid.UUID) (db.AcquisitionTargetRow, error)
	ReviewQueue(ctx context.Context, limit, offset int32) (db.AcquisitionReviewPage, error)
	Copy(context.Context, uuid.UUID) (db.AcquisitionTargetFileRow, error)
	RecordCopyWaveform(ctx context.Context, copyID uuid.UUID, peaks []byte) error
	AcceptCopy(context.Context, uuid.UUID) error
	AcceptFiledCopy(context.Context, uuid.UUID) error
	RefuseCopies(context.Context, uuid.UUID) error
	Reversible(ctx context.Context, answer db.Answer, id uuid.UUID) (bool, error)
	TakeBack(ctx context.Context, answer db.Answer, id uuid.UUID) error
}

// Previews turns a copy Schall is holding into something a browser will play.
// Optional: without one every other way of deciding about a copy still works.
type Previews interface {
	Playable(ctx context.Context, source string) (string, error)
	// Warm encodes copies ahead of anybody asking for them and returns at once.
	// It takes no context because it must outlive the response that started it.
	Warm(sources []string)
	ContentType() string
}

// WithPreviews registers what makes a held copy audible.
func WithPreviews(previews Previews) Option {
	return func(api *API) {
		api.previews = previews
	}
}

// Waveforms reads the shape of a copy's audio for the card that draws it.
//
// The importer writes a waveform onto every copy it holds, so this is only for
// the copies that were held before it did. Optional: without one those copies
// have no waveform and the card draws none.
type Waveforms interface {
	Available() bool
	Peaks(ctx context.Context, path string) ([]byte, error)
}

// WithWaveforms registers what draws the shape of a copy held before the
// importer started storing one.
func WithWaveforms(waveforms Waveforms) Option {
	return func(api *API) {
		api.waveforms = waveforms
	}
}

// WithAcquisitionTargets registers the component that holds wanted recordings.
func WithAcquisitionTargets(targets AcquisitionTargets) Option {
	return func(api *API) {
		api.wants = targets
	}
}

var targetStatuses = map[string]bool{
	"unresolved":      true,
	"pending":         true,
	"searching":       true,
	"awaiting_review": true,
	"acquired":        true,
	"not_wanted":      true,
	"superseded":      true,
}

type acquisitionTargetResponse struct {
	ID            uuid.UUID  `json:"id"`
	Origin        string     `json:"origin"`
	OriginAlbumID *uuid.UUID `json:"originAlbumId,omitempty"`
	Artist        string     `json:"artist"`
	Title         string     `json:"title"`
	Album         string     `json:"album"`
	DurationMS    *int32     `json:"durationMs"`
	ISRC          *string    `json:"isrc"`
	// EntryExplicit is the source saying the track somebody asked for is the
	// explicit recording. Absent where no source said (ADR 0032).
	EntryExplicit   *bool      `json:"entryExplicit,omitempty"`
	RecordingID     *uuid.UUID `json:"recordingId"`
	ReleaseGroupID  *uuid.UUID `json:"releaseGroupId,omitempty"`
	Method          *string    `json:"resolutionMethod,omitempty"`
	ResolvedAt      *time.Time `json:"resolvedAt,omitempty"`
	Status          string     `json:"status"`
	ReviewReason    *string    `json:"reviewReason,omitempty"`
	Summary         string     `json:"summary"`
	LastError       *string    `json:"lastError,omitempty"`
	Attempts        int32      `json:"attempts"`
	LastAttemptAt   *time.Time `json:"lastAttemptAt"`
	NextAttemptAt   *time.Time `json:"nextAttemptAt"`
	BelowFloorSince *time.Time `json:"belowFloorSince,omitempty"`
	FloorWaivedAt   *time.Time `json:"floorWaivedAt,omitempty"`
	LibraryFileID   *uuid.UUID `json:"libraryFileId,omitempty"`
	AcquiredAt      *time.Time `json:"acquiredAt"`
	NotWantedAt     *time.Time `json:"notWantedAt"`
	SupersededByID  *uuid.UUID `json:"supersededById,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	// AnchorUnavailable is why the sample could not be stored, when there is no
	// sample. AnchorAttempts counts sample attempts, and AnchorNextAttemptAt is
	// when a deferred sample fetch will run.
	AnchorUnavailable   *string    `json:"anchorUnavailable,omitempty"`
	AnchorAttempts      int32      `json:"anchorAttempts"`
	AnchorNextAttemptAt *time.Time `json:"anchorNextAttemptAt,omitempty"`
	// UpgradeOfPath is where the file this want exists to replace sits, set
	// only when Origin is "upgrade". Empty once the replacement has already
	// happened and the old file is gone.
	UpgradeOfPath string `json:"upgradeOfPath,omitempty"`
	// Source, ExternalID and ExternalURL are the address a person keyed the
	// want to, absent on every other want. SourceLookup is what the address
	// said when they confirmed it, and MinimumBitrate the want's floor in
	// kbit/s (ADR 0038).
	Source         string           `json:"source,omitempty"`
	ExternalID     string           `json:"externalId,omitempty"`
	ExternalURL    string           `json:"externalUrl,omitempty"`
	SourceLookup   *db.SourceLookup `json:"sourceLookup,omitempty"`
	MinimumBitrate *int32           `json:"minimumBitrate,omitempty"`
	// WaitingOnYou says nothing is looking for this want and nothing will: it
	// holds a copy that is already a file, and the library calls that file a
	// different recording. Only the list of wants works it out, because only that
	// list has to tell a want being looked for from one that has stopped; it is
	// false everywhere else and says nothing there.
	WaitingOnYou bool `json:"waitingOnYou"`
	// Candidates is the question, when there is one: the recordings this entry
	// could name, with what agreed and what disagreed for each. Sent with one
	// target rather than with a list of them, because it is what somebody
	// deciding reads and nobody skimming does.
	Candidates []acquisitionCandidateResponse `json:"candidates,omitempty"`
	// Copies is what has been tried: every copy fetched for this want and what it
	// came to, newest first. A want that keeps looking has to be able to say what
	// it has already looked at.
	Copies []acquiredCopyResponse `json:"copies,omitempty"`
}

// acquiredCopyResponse is one copy a peer offered and what became of it.
type acquiredCopyResponse struct {
	// ID names this copy so it can be played and decided about. A copy is
	// identified by which offer it was, not by where it happens to sit on disk.
	ID        uuid.UUID                `json:"id"`
	Provider  string                   `json:"provider"`
	Username  string                   `json:"username"`
	Path      string                   `json:"path"`
	Name      string                   `json:"name"`
	SizeBytes *int64                   `json:"sizeBytes,omitempty"`
	Verdict   string                   `json:"verdict"`
	DecidedBy string                   `json:"decidedBy"`
	Summary   string                   `json:"summary"`
	Evidence  *db.AcquiredFileEvidence `json:"evidence,omitempty"`
	DecidedAt time.Time                `json:"decidedAt"`
	// The library file this copy became, once the scan has made one. It is what
	// lets a screen point at the file rather than only describe it.
	LibraryFileID *uuid.UUID `json:"libraryFileId,omitempty"`
}

func acquiredCopyResponsesFrom(rows []db.AcquisitionTargetFileRow) []acquiredCopyResponse {
	copies := make([]acquiredCopyResponse, 0, len(rows))
	for _, row := range rows {
		tried := acquiredCopyResponse{
			ID:       row.ID,
			Provider: row.Provider, Username: row.SourceUsername, Path: row.RemotePath,
			Name: row.FileName, Verdict: row.Verdict, DecidedBy: row.DecidedBy,
			Summary: row.Summary, Evidence: row.Evidence, DecidedAt: row.DecidedAt,
		}
		if row.SizeBytes.Valid {
			size := row.SizeBytes.Int64
			tried.SizeBytes = &size
		}
		if row.LibraryFileID.Valid {
			fileID := row.LibraryFileID.UUID
			tried.LibraryFileID = &fileID
		}
		copies = append(copies, tried)
	}
	return copies
}

type acquisitionCandidateResponse struct {
	RecordingID    uuid.UUID  `json:"recordingId"`
	ReleaseGroupID *uuid.UUID `json:"releaseGroupId,omitempty"`
	ArtistName     string     `json:"artistName"`
	ReleaseTitle   *string    `json:"releaseTitle,omitempty"`
	TrackTitle     string     `json:"trackTitle"`
	DurationMS     *int32     `json:"durationMs"`
	ISRC           *string    `json:"isrc"`
	Rank           int32      `json:"rank"`
	Agrees         []string   `json:"agrees"`
	Differs        []string   `json:"differs"`
	Summary        string     `json:"summary"`
}

func acquisitionCandidateResponsesFrom(
	rows []db.AcquisitionTargetCandidateRow,
) []acquisitionCandidateResponse {
	candidates := make([]acquisitionCandidateResponse, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, acquisitionCandidateResponse{
			RecordingID:    row.MusicBrainzRecordingID,
			ReleaseGroupID: row.MusicBrainzReleaseGroupID,
			ArtistName:     row.ArtistName,
			ReleaseTitle:   row.ReleaseTitle,
			TrackTitle:     row.TrackTitle,
			DurationMS:     row.DurationMS,
			ISRC:           row.ISRC,
			Rank:           row.Rank,
			Agrees:         row.Agrees,
			Differs:        row.Differs,
			Summary:        row.Summary,
		})
	}
	return candidates
}

func acquisitionTargetResponseFrom(row db.AcquisitionTargetRow) acquisitionTargetResponse {
	return acquisitionTargetResponse{
		ID:             row.ID,
		Origin:         row.Origin,
		OriginAlbumID:  nullableUUID(row.OriginAlbumID),
		Artist:         row.EntryArtist,
		Title:          row.EntryTitle,
		Album:          row.EntryAlbum,
		DurationMS:     int32Pointer(row.EntryDurationMS),
		ISRC:           textPointer(row.EntryISRC),
		EntryExplicit:  boolPointer(row.EntryExplicit),
		RecordingID:    nullableUUID(row.MusicBrainzRecordingID),
		ReleaseGroupID: nullableUUID(row.MusicBrainzReleaseGroupID),
		WaitingOnYou: row.Status == "pending" && !row.NextAttemptAt.Valid &&
			row.HoldsAnImportedCopy,
		Method:              textPointer(row.ResolutionMethod),
		ResolvedAt:          nullableTime(row.ResolvedAt.Valid, row.ResolvedAt.Time),
		Status:              row.Status,
		ReviewReason:        textPointer(row.ReviewReason),
		Summary:             row.Summary,
		LastError:           textPointer(row.LastError),
		Attempts:            row.Attempts,
		LastAttemptAt:       nullableTime(row.LastAttemptAt.Valid, row.LastAttemptAt.Time),
		NextAttemptAt:       nullableTime(row.NextAttemptAt.Valid, row.NextAttemptAt.Time),
		BelowFloorSince:     nullableTime(row.BelowFloorSince.Valid, row.BelowFloorSince.Time),
		FloorWaivedAt:       nullableTime(row.FloorWaivedAt.Valid, row.FloorWaivedAt.Time),
		LibraryFileID:       nullableUUID(row.AcquiredLibraryFileID),
		AcquiredAt:          nullableTime(row.AcquiredAt.Valid, row.AcquiredAt.Time),
		NotWantedAt:         nullableTime(row.NotWantedAt.Valid, row.NotWantedAt.Time),
		SupersededByID:      nullableUUID(row.SupersededByID),
		CreatedAt:           row.CreatedAt,
		AnchorUnavailable:   textPointer(row.AnchorUnavailable),
		AnchorAttempts:      row.AnchorAttempts,
		AnchorNextAttemptAt: nullableTime(row.AnchorNextAttemptAt.Valid, row.AnchorNextAttemptAt.Time),
		UpgradeOfPath:       row.UpgradeOfPath,
		Source:              row.Source,
		ExternalID:          row.ExternalID,
		ExternalURL:         row.ExternalURL,
		SourceLookup:        row.SourceLookup,
		MinimumBitrate:      int32Pointer(row.MinimumBitrate),
	}
}

type createAcquisitionTargetRequest struct {
	Origin         string `json:"origin"`
	OriginAlbumID  string `json:"originAlbumId"`
	Artist         string `json:"artist"`
	Title          string `json:"title"`
	Album          string `json:"album"`
	DurationMS     *int32 `json:"durationMs"`
	ISRC           string `json:"isrc"`
	RecordingID    string `json:"recordingId"`
	ReleaseGroupID string `json:"releaseGroupId"`
}

// createAcquisitionTarget records that a recording is wanted.
//
// A second request for the same recording is not a second want: it returns the
// target that already exists with 200, the way requesting the same folder twice
// returns the open request rather than acquiring it again.
func (api *API) createAcquisitionTarget(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	var input createAcquisitionTargetRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}

	entry := acquisition.Entry{
		Origin:     strings.TrimSpace(input.Origin),
		Artist:     input.Artist,
		Title:      input.Title,
		Album:      input.Album,
		DurationMS: input.DurationMS,
		ISRC:       input.ISRC,
	}
	if entry.Origin == "" {
		entry.Origin = "manual"
	}
	problems := make([]string, 0)
	if strings.TrimSpace(input.Title) == "" && strings.TrimSpace(input.ISRC) == "" {
		problems = append(problems, "title or isrc is required")
	}
	if input.DurationMS != nil && *input.DurationMS < 0 {
		problems = append(problems, "durationMs must be zero or greater")
	}
	entry.OriginAlbumID, problems = optionalID(input.OriginAlbumID, "originAlbumId", problems)
	entry.RecordingID, problems = optionalID(input.RecordingID, "recordingId", problems)
	entry.ReleaseGroupID, problems = optionalID(input.ReleaseGroupID, "releaseGroupId", problems)
	if len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	target, created, err := api.wants.Create(request.Context(), entry)
	switch {
	case errors.Is(err, acquisition.ErrEntryNotIdentifiable),
		errors.Is(err, acquisition.ErrUnknownOrigin):
		api.problem(response, http.StatusUnprocessableEntity, err.Error(), nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		status := http.StatusOK
		if created {
			status = http.StatusCreated
			api.logger.Info().
				Str("acquisition_target_id", target.ID.String()).
				Str("origin", target.Origin).
				Msg("recording wanted")
		}
		api.writeJSON(response, status, acquisitionTargetResponseFrom(target))
	}
}

// wantAlbumResponse is one release's worth of a walk. The counting is
// acquisition.Counts, which is where the walk itself lives: the same walk runs
// from here, from the artist's page, and from a tracklist arriving hours later
// under a standing want, and three copies of it would drift.
type wantAlbumResponse struct {
	AlbumID uuid.UUID `json:"albumId"`
	acquisition.Counts
}

// wantArtistResponse is a discography's worth, with the number of releases
// something was missing from — the one thing that says how wide the walk was.
type wantArtistResponse struct {
	ArtistID uuid.UUID `json:"artistId"`
	acquisition.Missing
}

// artistWantMissingResponse is that, plus whether the standing want is now on.
type artistWantMissingResponse struct {
	ArtistID    uuid.UUID `json:"artistId"`
	WantMissing bool      `json:"wantMissing"`
	acquisition.Missing
}

// wantAlbum records a want for every track of a release the library does not
// have. These are the wants the playlist importer creates, one per unowned
// track, except that the catalogue already knows each track's recording ID —
// so nothing has to be resolved and the targets land due immediately.
//
// Pressing it twice is not two wants. Creation is idempotent on the recording
// ID, so the second press answers with the targets that exist, which is also
// what keeps a track somebody stopped pursuing stopped: wanting the release
// around it must never quietly re-open a not-wanted decision.
//
// Nothing here decides anything about a file. It creates rows the loop already
// knows how to drain, and every copy the loop fetches is still proven by its
// audio before it enters the library.
func (api *API) wantAlbum(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
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

	// Either way of holding the music counts as held. A file proven to be the
	// recording but never mapped onto this release is one the loop would settle
	// the want against the moment it looked, so wanting it would be asking
	// strangers for music already on the disc.
	missing := make([]acquisition.MissingTrack, 0, len(tracks))
	for _, track := range tracks {
		missing = append(missing, acquisition.MissingTrack{
			AlbumID:        albumID,
			AlbumTitle:     album.Title,
			ArtistName:     album.ArtistName,
			Title:          track.Title,
			DurationMS:     int32Pointer(track.DurationMs),
			ISRC:           track.Isrc.String,
			RecordingID:    track.MusicbrainzRecordingID,
			ReleaseGroupID: album.MusicbrainzReleaseGroupID,
			Held:           track.Owned || track.HeldElsewhere,
		})
	}
	walked, err := acquisition.WantMissing(request.Context(), api.wants, "manual", missing)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	result := wantAlbumResponse{AlbumID: albumID, Counts: walked.Counts}
	api.logger.Info().
		Str("album_id", albumID.String()).
		Int("wanted", result.Wanted).
		Int("already_wanted", result.AlreadyWanted).
		Int("owned", result.Owned).
		Msg("release wanted")
	api.writeJSON(response, http.StatusOK, result)
}

// wantArtist is the same walk over a whole discography: every release the
// artist's monitor level counts, every track of it the library does not hold.
//
// The walk follows the monitor level, always — at 'owned' it wants nothing —
// and an unmonitored release can still be wanted by hand from its own page.
// The same recording appearing on an album, a single and a compilation is one
// want rather than three: the walk passes over a recording it has already
// dealt with, and creation is idempotent on the recording ID underneath that
// anyway. That idempotency is also what keeps a track somebody stopped
// pursuing stopped: wanting the discography around it must never quietly
// re-open a not-wanted decision.
//
// What this creates is reach, never a decision. Every copy the loop fetches for
// these wants is still proven by its audio before it enters the library, so the
// worst a discography-sized press can cost is bandwidth and a longer queue of
// questions.
func (api *API) wantArtist(response http.ResponseWriter, request *http.Request) {
	artistID, ok := api.artistIDForWanting(response, request)
	if !ok {
		return
	}
	walked, ok := api.walkDiscography(response, request, artistID)
	if !ok {
		return
	}
	api.writeJSON(response, http.StatusOK, wantArtistResponse{
		ArtistID: artistID,
		Missing:  walked,
	})
}

// artistIDForWanting reads the artist a want-the-discography request names, and
// refuses the request when there is nothing to record wants with.
func (api *API) artistIDForWanting(
	response http.ResponseWriter, request *http.Request,
) (uuid.UUID, bool) {
	if !api.acquisitionsAvailable(response) {
		return uuid.Nil, false
	}
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return uuid.Nil, false
	}
	return artistID, true
}

// walkDiscography is the walk itself: every release the artist's monitor level
// counts, every track of it the library does not hold. It reports whether it
// answered the request, having written the problem itself if it did not.
func (api *API) walkDiscography(
	response http.ResponseWriter, request *http.Request, artistID uuid.UUID,
) (acquisition.Missing, bool) {
	tracks, err := api.store.ArtistTracksForWanting(request.Context(), artistID)
	if err != nil {
		api.internalError(response, request, err)
		return acquisition.Missing{}, false
	}
	if len(tracks) == 0 {
		// An artist with no track anywhere is either unknown or not refreshed
		// yet. Either way there is nothing to want, and saying so beats a 404
		// for an artist that exists.
		if _, err := api.store.GetArtist(request.Context(), artistID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				api.problem(response, http.StatusNotFound, "artist not found", nil)
				return acquisition.Missing{}, false
			}
			api.internalError(response, request, err)
			return acquisition.Missing{}, false
		}
	}

	walked, err := acquisition.WantMissing(
		request.Context(), api.wants, "manual", acquisition.MissingFromArtistTracks(tracks))
	if err != nil {
		api.internalError(response, request, err)
		return acquisition.Missing{}, false
	}
	api.logger.Info().
		Str("artist_id", artistID.String()).
		Int("releases", walked.Releases).
		Int("wanted", walked.Wanted).
		Int("already_wanted", walked.AlreadyWanted).
		Int("owned", walked.Owned).
		Msg("discography wanted")
	return walked, true
}

// setArtistWantMissing turns the standing "want what is missing" on or off.
//
// On, it walks the discography now and leaves the mark that makes every release
// whose tracklist arrives later want its missing tracks too (StandingWantTracks
// and the album refresh in internal/jobs). That is the difference between this
// and the one-off press: following an artist queues one release refresh per
// release group, they land one at a time behind MusicBrainz's rate limit, and a
// press reaches only the releases whose tracks are already in the catalogue.
//
// Off, it stops future wanting and nothing else. Every want made under it
// stays, because those were asked for — the same rule changing the monitor
// level follows.
func (api *API) setArtistWantMissing(response http.ResponseWriter, request *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	// Turning it off records a decision and creates nothing, so it is answered
	// on an installation whose acquisition loop is unavailable. Turning it on
	// makes wants, and cannot be.
	if body.On && !api.acquisitionsAvailable(response) {
		return
	}
	artistID, err := uuid.Parse(chi.URLParam(request, "artistID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}

	marked, err := api.store.SetArtistWantMissing(request.Context(), db.SetArtistWantMissingParams{
		ArtistID: artistID,
		Wanted:   body.On,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "artist not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().
		Str("artist_id", artistID.String()).
		Bool("want_missing", marked.WantMissingSince.Valid).
		Msg("standing want set")

	result := artistWantMissingResponse{
		ArtistID:    artistID,
		WantMissing: marked.WantMissingSince.Valid,
	}
	if body.On {
		// The mark is written first, so a tracklist that arrives while this walk
		// is still running is wanted by the worker rather than missed by both.
		walked, ok := api.walkDiscography(response, request, artistID)
		if !ok {
			return
		}
		result.Missing = walked
	}
	api.writeJSON(response, http.StatusOK, result)
}

// wantedTrack loads one catalogue track and refuses the two cases no want can
// come of: music the library already holds, and a track the catalogue holds no
// MusicBrainz recording against. Creation is idempotent on the recording ID, so
// a track without one would gain a sibling want on every press — the same rule
// wantTrack applies walking a release.
func (api *API) wantedTrack(
	response http.ResponseWriter, request *http.Request,
) (db.GetTrackForWantingRow, bool) {
	if !api.acquisitionsAvailable(response) {
		return db.GetTrackForWantingRow{}, false
	}
	trackID, err := uuid.Parse(chi.URLParam(request, "trackID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "track not found", nil)
		return db.GetTrackForWantingRow{}, false
	}
	track, err := api.store.GetTrackForWanting(request.Context(), trackID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "track not found", nil)
		return db.GetTrackForWantingRow{}, false
	}
	if err != nil {
		api.internalError(response, request, err)
		return db.GetTrackForWantingRow{}, false
	}
	if !track.MusicbrainzRecordingID.Valid {
		api.problem(response, http.StatusUnprocessableEntity,
			"this track cannot be asked for",
			[]string{"The catalogue holds no MusicBrainz recording for this track, " +
				"so there is nothing a wishlist entry could be keyed on."})
		return db.GetTrackForWantingRow{}, false
	}
	return track, true
}

// trackEntry is the want one catalogue track would be asked for as: the same
// entry wantAlbum builds walking a release, for exactly one row of it.
func trackEntry(track db.GetTrackForWantingRow) acquisition.Entry {
	return acquisition.Entry{
		Origin:         "manual",
		OriginAlbumID:  uuid.NullUUID{UUID: track.AlbumID, Valid: true},
		Artist:         track.ArtistName,
		Title:          track.Title,
		Album:          track.AlbumTitle,
		DurationMS:     int32Pointer(track.DurationMs),
		ISRC:           track.Isrc.String,
		RecordingID:    track.MusicbrainzRecordingID,
		ReleaseGroupID: track.MusicbrainzReleaseGroupID,
	}
}

// wantOneTrack records the want for a single catalogue track, which is what a
// per-track toggle on a release page presses. Asking for a track somebody
// stopped pursuing does not quietly re-open that decision: the existing
// not_wanted target comes back unchanged, and taking the decision back is the
// explicit pursue-again call.
func (api *API) wantOneTrack(response http.ResponseWriter, request *http.Request) {
	track, ok := api.wantedTrack(response, request)
	if !ok {
		return
	}
	if track.Owned || track.HeldElsewhere {
		api.problem(response, http.StatusConflict,
			"this track is already in your library", nil)
		return
	}
	target, _, err := api.wants.Create(request.Context(), trackEntry(track))
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().
		Str("track_id", track.ID.String()).
		Str("target_id", target.ID.String()).
		Str("status", target.Status).
		Msg("track wanted")
	api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
}

// dismissTrack records that this track's recording is not wanted, whether or
// not anything was being pursued for it yet. A track nothing was ever asked
// for still gets the decision remembered: the want is recorded and stopped in
// the same breath, so the completion arithmetic and the loop both read it as
// deliberately dismissed rather than missing.
func (api *API) dismissTrack(response http.ResponseWriter, request *http.Request) {
	track, ok := api.wantedTrack(response, request)
	if !ok {
		return
	}
	if track.Owned || track.HeldElsewhere {
		api.problem(response, http.StatusConflict,
			"this track is already in your library",
			[]string{"Nothing is being pursued for it, and dismissing owned music " +
				"would say something about the file rather than the search."})
		return
	}
	target, _, err := api.wants.Create(request.Context(), trackEntry(track))
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if target.Status == "not_wanted" {
		// Already decided, by this press or an earlier one. Saying so again is
		// the same decision, not a conflict.
		api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
		return
	}
	stopped, err := api.wants.StopPursuing(request.Context(), target.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusConflict, "this target is already finished with", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().
		Str("track_id", track.ID.String()).
		Str("target_id", stopped.ID.String()).
		Msg("track dismissed")
	api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(stopped))
}

// dismissedTracks says what became of every track one release-wide press
// walked over, the same accounting wantedTracks gives the other direction.
type dismissedTracks struct {
	// Dismissed is decisions this press recorded; AlreadyDismissed is the ones
	// that were already on record before it.
	Dismissed        int `json:"dismissed"`
	AlreadyDismissed int `json:"alreadyDismissed"`
	Owned            int `json:"owned"`
	// Unresolvable is a track with no MusicBrainz recording to key the
	// decision on. It cannot be wanted either, so it never counts as missing.
	Unresolvable int `json:"unresolvable"`
}

// dismissAlbumResponse is one release's worth of that.
type dismissAlbumResponse struct {
	AlbumID uuid.UUID `json:"albumId"`
	dismissedTracks
}

// dismissAlbumRemainder records not-wanted for every missing track of one
// release — an EP that turns out to be a live dump is one press, not six. Each
// decision is still the same track-scoped one the single-track path records:
// owned music is untouched, and every dismissal can be taken back per track.
func (api *API) dismissAlbumRemainder(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	result, err := api.dismissRelease(request.Context(), albumID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, result)
}

// dismissRelease is that same work, without a response to write.
//
// It is separate because the press exists twice: once on a release page, about
// the release being looked at, and once on the Releases browser, about a
// selection of them. Both record exactly the same track-scoped decisions, and
// the second one answers per release — so the counting has to be per release
// too, which is what this returns.
func (api *API) dismissRelease(
	ctx context.Context, albumID uuid.UUID,
) (dismissAlbumResponse, error) {
	album, err := api.store.GetAlbum(ctx, albumID)
	if err != nil {
		return dismissAlbumResponse{}, err
	}
	tracks, err := api.store.ListAlbumTracks(ctx, albumID)
	if err != nil {
		return dismissAlbumResponse{}, err
	}

	result := dismissAlbumResponse{AlbumID: albumID}
	seen := make(map[uuid.UUID]struct{}, len(tracks))
	for _, track := range tracks {
		if track.Owned || track.HeldElsewhere {
			result.Owned++
			continue
		}
		if !track.MusicbrainzRecordingID.Valid {
			result.Unresolvable++
			continue
		}
		if _, repeat := seen[track.MusicbrainzRecordingID.UUID]; repeat {
			continue
		}
		seen[track.MusicbrainzRecordingID.UUID] = struct{}{}
		if track.WantStatus.String == "not_wanted" {
			result.AlreadyDismissed++
			continue
		}
		targetID := track.WantID.UUID
		if !track.WantID.Valid {
			target, _, err := api.wants.Create(ctx, acquisition.Entry{
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
				return dismissAlbumResponse{}, err
			}
			targetID = target.ID
		}
		switch _, err := api.wants.StopPursuing(ctx, targetID); {
		case errors.Is(err, pgx.ErrNoRows):
			// Finished between the list and this press — settled either way,
			// and a walk over a whole release does not stop for it.
			result.AlreadyDismissed++
		case err != nil:
			return dismissAlbumResponse{}, err
		default:
			result.Dismissed++
		}
	}
	api.logger.Info().
		Str("album_id", albumID.String()).
		Int("dismissed", result.Dismissed).
		Int("already_dismissed", result.AlreadyDismissed).
		Int("owned", result.Owned).
		Msg("release remainder dismissed")
	return result, nil
}

func (api *API) listAcquisitionTargets(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	params := db.ListAcquisitionTargetsParams{Limit: 100}
	// `status` takes one state or several separated by commas. Several, because
	// a want waiting to be resolved, one waiting for a copy and one being
	// searched for right now are one pile to whoever asked for the music, and
	// three requests for one screen would also be three page counts that do not
	// add up to the length of what it shows.
	if raw := strings.TrimSpace(request.URL.Query().Get("status")); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if !targetStatuses[name] {
				api.problem(response, http.StatusUnprocessableEntity, "validation failed",
					[]string{"status must be one or more of unresolved, pending, searching, awaiting_review, acquired, not_wanted, or superseded, separated by commas"})
				return
			}
			params.Statuses = append(params.Statuses, name)
		}
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"limit must be between 1 and 200"})
			return
		}
		params.Limit = int32(value)
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"offset must be zero or greater"})
			return
		}
		params.Offset = int32(value)
	}

	page, err := api.wants.List(request.Context(), params)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items := make([]acquisitionTargetResponse, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, acquisitionTargetResponseFrom(row))
	}
	body := map[string]any{
		"items": items, "total": page.Total, "limit": params.Limit, "offset": params.Offset,
	}
	if notice := api.waitingOnTheAudioCheck(request.Context(), page.Items); notice != "" {
		body["notice"] = notice
	}
	api.writeJSON(response, http.StatusOK, body)
}

// waitingOnTheAudioCheck is the one line the wanted list carries when nothing
// on it can ever be fetched.
//
// A want is one recording Schall looks for on Soulseek, from strangers. The
// audio check — AcoustID — is the only thing that can prove a stranger's file is
// the recording that was wanted, so the loop refuses to fetch at all while the
// check is off or has no key: the bandwidth would buy a file that could only
// ever be a question. Every pass then records that on the want, and until now
// nothing said it on the list itself, so a want that can never be answered read
// exactly like one being worked on.
//
// The line is only true while there is something waiting, so it is computed from
// what the page actually shows rather than from the settings alone. A settings
// store that will not answer says nothing: a screen full of wants is not the
// place to report that a settings read failed.
func (api *API) waitingOnTheAudioCheck(
	ctx context.Context, rows []db.AcquisitionTargetRow,
) string {
	waiting := false
	for _, row := range rows {
		if row.Status == "pending" || row.Status == "searching" {
			waiting = true
			break
		}
	}
	if !waiting || api.settings == nil {
		return ""
	}
	row, err := api.settings.ImportSettings(ctx)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(row.AcoustIDAPIKey) == "" {
		return "Wishlist tracks are waiting: add an AcoustID key in Settings to search for them."
	}
	if !row.AcoustIDEnabled {
		return "Wishlist tracks are waiting: turn on Verify imports by audio in Settings to search for them."
	}
	return ""
}

func (api *API) acquisitionTarget(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	target, err := api.wants.Target(request.Context(), targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "acquisition target not found", nil)
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	candidates, err := api.wants.Candidates(request.Context(), targetID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	tried, err := api.wants.Copies(request.Context(), targetID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	payload := acquisitionTargetResponseFrom(target)
	payload.Candidates = acquisitionCandidateResponsesFrom(candidates)
	payload.Copies = acquiredCopyResponsesFrom(tried)
	api.writeJSON(response, http.StatusOK, payload)
}

// stopPursuingAcquisitionTarget records that this recording is not wanted after
// all. It says nothing about the release, the artist, or any file already
// acquired: this is a decision to stop looking, not to throw anything away.
func (api *API) stopPursuingAcquisitionTarget(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	target, err := api.wants.StopPursuing(request.Context(), targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict,
			"this target is already finished with", nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
	}
}

// pursueAcquisitionTargetAgain withdraws that decision. Nothing automatic may
// overturn what the user decided, but they may take it back themselves.
func (api *API) pursueAcquisitionTargetAgain(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	target, err := api.wants.PursueAgain(request.Context(), targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict,
			"this target was not one you had stopped pursuing", nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
	}
}

// takeBestAvailableAcquisitionTarget records the person's decision to accept
// the best copy despite the bitrate floor for this want.
func (api *API) takeBestAvailableAcquisitionTarget(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	target, err := api.wants.TakeBestAvailable(request.Context(), targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict, "this target is not pending", nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
	}
}

// keepAcquisitionTargetFloor withdraws the person's bitrate-floor waiver.
func (api *API) keepAcquisitionTargetFloor(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	target, err := api.wants.KeepFloor(request.Context(), targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict, "this target has no active floor waiver", nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
	}
}

// rejectAcquisitionTargetResolution is the wrong-target outcome: the entry was
// resolved to a recording it does not name.
//
// It is the only review decision that is about the question rather than about an
// answer, which is why it lives on the target and not on a copy. The rejected
// recording is remembered so the lookup that follows cannot return it again.
func (api *API) rejectAcquisitionTargetResolution(
	response http.ResponseWriter, request *http.Request,
) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	target, err := api.wants.RejectResolution(request.Context(), targetID)
	switch {
	case errors.Is(err, db.ErrNothingToReject):
		api.problem(response, http.StatusConflict, "this wishlist entry has no resolution to reject",
			[]string{"Nothing has been resolved for this entry, or the answer was " +
				"withdrawn already. There is nothing to remember and nothing to re-open."})
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
	}
}

// chooseResolutionRequest names which of the offered recordings the entry means.
type chooseResolutionRequest struct {
	RecordingID string `json:"recordingId"`
}

// chooseAcquisitionTargetResolution is the other decision about the question
// rather than about an answer: this entry names that recording.
//
// It is the counterpart of the wrong-recording outcome, for a want that never got
// a recording to be wrong about. Several recordings fit the entry, the evidence
// does not choose between them, and nothing automatic ever will — so the choice
// is a person's, and it is written as a manual resolution that outlasts every
// later attempt.
func (api *API) chooseAcquisitionTargetResolution(
	response http.ResponseWriter, request *http.Request,
) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	var input chooseResolutionRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	recordingID, err := uuid.Parse(strings.TrimSpace(input.RecordingID))
	if err != nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"recordingId must be the MusicBrainz recording id of one of the " +
				"answers offered for this wishlist entry"})
		return
	}

	target, err := api.wants.ChooseResolution(request.Context(), targetID, recordingID)
	switch {
	case errors.Is(err, db.ErrNotOfferedForReview):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"Only the recordings this wishlist entry offered can answer it. If none of " +
				"them is right, this wishlist entry is not wanted rather than answered wrongly."})
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict, "this wishlist entry is not waiting on an answer",
			[]string{"Something already answered it, and an answer is never re-asked."})
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, acquisitionTargetResponseFrom(target))
	}
}

// reviewItemResponse is one want waiting on a person.
//
// Kind names which question it is, so an interface renders the right one rather
// than inferring it from which list came back empty: 'copies' asks whether one of
// these files is the recording, and 'resolution' asks which recording the entry
// names at all.
type reviewItemResponse struct {
	Kind             string                         `json:"kind"`
	Target           acquisitionTargetResponse      `json:"target"`
	Copies           []acquiredCopyResponse         `json:"copies"`
	Candidates       []acquisitionCandidateResponse `json:"candidates"`
	Ruled            int64                          `json:"ruledOut"`
	FileRecordingID  *uuid.UUID                     `json:"fileRecordingId,omitempty"`
	FileTitle        string                         `json:"fileTitle,omitempty"`
	FileArtist       string                         `json:"fileArtist,omitempty"`
	FileReleaseTitle string                         `json:"fileReleaseTitle,omitempty"`
	// CopyGroups says which of the copies above are the same piece of audio, so
	// a want holding eleven files can be read as the two masters they are. Every
	// copy appears in exactly one group, and a copy nothing could compare is a
	// group of one.
	//
	// It is a way of reading the list and never a statement about it: the copies
	// are sent whole and unchanged, nothing is decided here, and an interface
	// that ignored this field would show exactly what it shows today.
	CopyGroups []copyGroupResponse `json:"copyGroups"`
	// WaitingFor says the want is not asking anything yet: something could still
	// arrive or recover, and it will be judged again by itself when it does. It
	// is empty for a want whose evidence is in, which is the one a person can
	// answer. Nothing about the copies changes either way.
	WaitingFor string `json:"waitingFor,omitempty"`
	// WaitingUntil is when the next automatic look is due, for the kind that
	// waits on a clock. The kind that waits on a fact has none.
	WaitingUntil *time.Time `json:"waitingUntil,omitempty"`
}

// waitingFor reads what a want is still waiting for, from two facts that have to
// agree: the want's own recheck reason, and the question the queue is actually
// holding it for.
//
// Both are needed. The reason alone can be left over from a copy that has since
// been answered while another copy keeps the want in the queue for a different
// question, and the question alone cannot say whether the patience for it has
// run out. A want that has spent its schedule has no reason left, and that is
// exactly the want a person is being asked about.
func waitingFor(item db.AcquisitionReviewItem, kind string) string {
	reason := item.Target.RecheckReason.String
	switch reason {
	case db.RecheckForMusicBrainz:
		for _, file := range item.Files {
			if file.Verdict == db.AcquiredFileHeld &&
				strings.HasPrefix(file.Summary, unaskedRegistrationSummary) {
				return reason
			}
		}
	case db.RecheckForLibraryIdentity:
		if kind == reviewKindStopped && holdsAnAcceptedCopy(item) {
			return reason
		}
	}
	return ""
}

// unaskedRegistrationSummary is the opening of what a copy held on an
// unanswerable registration question says about itself. It is written by
// downloads and matched here rather than shared, because the sentence belongs to
// the package that decides and this only reads it back.
const unaskedRegistrationSummary = "The audio was identified as another recording, " +
	"and MusicBrainz could not be asked"

// copyGroupResponse is one set of copies that sound alike.
//
// Rips is how many separate rips the group holds rather than how many files:
// copies of identical size are one upload that spread, and counting them
// severally would read as several people agreeing.
type copyGroupResponse struct {
	CopyIDs []uuid.UUID `json:"copyIds"`
	Rips    int         `json:"rips"`
}

func copyGroupResponsesFrom(rows []db.AcquisitionTargetFileRow) []copyGroupResponse {
	grouped := acquisition.GroupCopies(rows)
	groups := make([]copyGroupResponse, 0, len(grouped))
	for _, group := range grouped {
		groups = append(groups, copyGroupResponse{CopyIDs: group.CopyIDs, Rips: group.Rips})
	}
	return groups
}

// The three questions a want can be waiting on.
const (
	reviewKindCopies     = "copies"
	reviewKindResolution = "resolution"
	// The want whose copy is on the disc and whose library file the library
	// files under a different recording. Nothing automatic may settle it, and
	// nothing may fetch for it, so it waits here for the person to say which.
	reviewKindStopped = "stopped"
)

// reviewKindOf reads which question a want is asking from the want itself.
//
// The resolution comes first when a want somehow has both — a copy held from a
// resolution that was later rejected, and a fresh ambiguity. Nobody can say
// whether a file is the wanted recording while which recording is wanted is the
// thing in doubt, so the answerable question is asked and the copies wait. They
// are still held, and this queue offers them the moment the want knows what it is
// looking for.
//
// A want that stopped is read from two facts, because one of them is not enough:
// it is still wanted with no time to be looked at again, and it has either an
// accepted library copy or only artist-credit refusals. A want with no time and
// no copy is one nobody has scheduled yet, and it is asking nothing.
func reviewKindOf(item db.AcquisitionReviewItem) string {
	target := item.Target
	if target.Status == "awaiting_review" && target.ReviewReason.String == "resolution" {
		return reviewKindResolution
	}
	if target.Status == "pending" && !target.NextAttemptAt.Valid &&
		(holdsAnAcceptedCopy(item) || holdsOnlyCreditRefusals(item)) {
		return reviewKindStopped
	}
	return reviewKindCopies
}

func holdsAnAcceptedCopy(item db.AcquisitionReviewItem) bool {
	for _, file := range item.Files {
		if file.Verdict == "accepted" && file.LibraryFileID.Valid {
			return true
		}
	}
	return false
}

func isCreditOnlyRefusal(file db.AcquisitionTargetFileRow) bool {
	return file.Verdict == db.AcquiredFileDiscardedTags &&
		file.Evidence != nil && len(file.Evidence.Differs) == 1 &&
		file.Evidence.Differs[0] == "artist"
}

func holdsOnlyCreditRefusals(item db.AcquisitionReviewItem) bool {
	if len(item.Files) == 0 {
		return false
	}
	for _, file := range item.Files {
		if !isCreditOnlyRefusal(file) {
			return false
		}
	}
	return true
}

// reviewQueue lists the wants holding a copy nobody could decide about, oldest
// question first.
func (api *API) reviewQueue(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	limit, offset := int32(20), int32(0)
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"limit must be between 1 and 100"})
			return
		}
		limit = int32(value)
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"offset must be zero or greater"})
			return
		}
		offset = int32(value)
	}

	page, err := api.wants.ReviewQueue(request.Context(), limit, offset)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items := make([]reviewItemResponse, 0, len(page.Items))
	for _, item := range page.Items {
		kind := reviewKindOf(item)
		review := reviewItemResponse{
			Kind:       kind,
			Target:     acquisitionTargetResponseFrom(item.Target),
			Copies:     acquiredCopyResponsesFrom(item.Files),
			CopyGroups: copyGroupResponsesFrom(item.Files),
			Candidates: acquisitionCandidateResponsesFrom(item.Candidates),
			Ruled:      item.Ruled,
			WaitingFor: waitingFor(item, kind),
		}
		if review.WaitingFor != "" && item.Target.RecheckAfter.Valid {
			due := item.Target.RecheckAfter.Time
			review.WaitingUntil = &due
		}
		api.addStoppedFileIdentity(request.Context(), &review, item)
		items = append(items, review)
	}
	api.warmPreviews(page.Items)
	api.writeJSON(response, http.StatusOK, map[string]any{
		"items": items, "total": page.Total, "limit": limit, "offset": offset,
	})
}

// addStoppedFileIdentity adds the identity already recorded for the imported
// copy. A missing resolver or identity leaves the response in its old shape,
// so an incomplete read cannot put an empty comparison on the review card.
func (api *API) addStoppedFileIdentity(
	ctx context.Context, response *reviewItemResponse, item db.AcquisitionReviewItem,
) {
	if response.Kind != reviewKindStopped || api.identities == nil {
		return
	}
	for _, file := range item.Files {
		if file.Verdict != db.AcquiredFileAccepted || !file.LibraryFileID.Valid {
			continue
		}
		resolution, err := api.identities.File(ctx, file.LibraryFileID.UUID)
		if err != nil {
			api.logger.Warn().Err(err).
				Str("library_file_id", file.LibraryFileID.UUID.String()).
				Msg("could not read the stopped copy's identity")
			return
		}
		if resolution.Identity == nil || resolution.Identity.RecordingID == uuid.Nil {
			return
		}
		response.FileRecordingID = &resolution.Identity.RecordingID
		response.FileTitle = resolution.Identity.TrackTitle
		response.FileArtist = resolution.Identity.ArtistName
		response.FileReleaseTitle = resolution.Identity.ReleaseTitle
		return
	}
}

// warmPreviews starts encoding the copies on this page of the queue.
//
// Reading the queue is the earliest moment Schall knows which audio somebody is
// about to judge, and the queue is worked through rather than dipped into — the
// wait for a transcode was being paid once per copy, on a page where nearly every
// copy gets played. Nothing here is waited on and nothing is reported: a page of
// questions still renders when the audio behind it does not.
//
// Held copies and credit-only stopped copies are warmed, which is the same rule
// reviewCopyAudio serves by. A copy whose file has gone is skipped here rather
// than logged, because the request that eventually asks for it says so properly.
func (api *API) warmPreviews(items []db.AcquisitionReviewItem) {
	if api.previews == nil || api.inboxPath == "" {
		return
	}
	sources := make([]string, 0, len(items))
	for _, item := range items {
		creditOnly := item.Target.Status == "pending" && !item.Target.NextAttemptAt.Valid &&
			holdsOnlyCreditRefusals(item)
		for _, file := range item.Files {
			if file.Verdict != db.AcquiredFileHeld && !(creditOnly && isCreditOnlyRefusal(file)) {
				continue
			}
			source, err := api.copyFile(file)
			if err != nil {
				continue
			}
			sources = append(sources, source)
		}
	}
	api.previews.Warm(sources)
}

// acceptReviewCopy is the accept outcome: this copy is the wanted recording.
//
// The file is not brought in here. The decision is written and the import that
// follows honours it — copying a file into the library reads tags, writes to disk
// and queues a scan, which is not work to put behind an HTTP timeout.
func (api *API) acceptReviewCopy(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	copyID, err := uuid.Parse(chi.URLParam(request, "copyID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "copy not found", nil)
		return
	}
	err = api.wants.AcceptCopy(request.Context(), copyID)
	switch {
	case errors.Is(err, db.ErrNotWaitingForADecision):
		api.problem(response, http.StatusConflict, "this copy is not waiting for a decision",
			[]string{"Something already answered it, and an answer is never re-asked."})
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusAccepted, map[string]any{"accepted": true})
	}
}

// refuseReviewCopies is the none-of-these outcome: none of the copies held for
// this want are the recording, so keep looking. Each refusal is permanent, and
// those files are never offered for this want again.
func (api *API) refuseReviewCopies(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	err := api.wants.RefuseCopies(request.Context(), targetID)
	switch {
	case errors.Is(err, db.ErrNotWaitingForADecision):
		api.problem(response, http.StatusConflict, "this wishlist entry has no copies waiting for a decision",
			[]string{"Nothing is held for it, so there is nothing to refuse."})
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, map[string]any{"refused": true})
	}
}

// takeBackRequest names the answer to withdraw: which of the four outcomes it
// was, and the row it was recorded against — the copy for an acceptance, the
// want for the other three.
type takeBackRequest struct {
	Outcome string `json:"outcome"`
	ID      string `json:"id"`
}

// takenBack reads that pair from a request, whether it arrives as a query or as
// a body, and refuses anything that does not name one of the four answers.
func (api *API) takenBack(
	response http.ResponseWriter, outcome, id string,
) (db.Answer, uuid.UUID, bool) {
	answer := db.Answer(strings.TrimSpace(outcome))
	if !db.KnownAnswer(answer) {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"outcome must be accept, none-of-these, not-wanted, or wrong-recording"})
		return "", uuid.Nil, false
	}
	subject, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"id must be the copy or wishlist entry the answer was recorded against"})
		return "", uuid.Nil, false
	}
	return answer, subject, true
}

// answerIsReversible reports whether the last answer somebody gave can still be
// taken back.
//
// The queue asks it so that Undo is offered only while it would work. The
// condition is a fact in the database rather than a length of time — the copy is
// not in the library yet, the want has taken no other answer — so the control
// follows the fact instead of a countdown. Nothing is written on the strength of
// this answer: the statement that performs the reversal carries every condition
// itself.
func (api *API) answerIsReversible(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	query := request.URL.Query()
	answer, subject, ok := api.takenBack(response, query.Get("outcome"), query.Get("id"))
	if !ok {
		return
	}
	reversible, err := api.wants.Reversible(request.Context(), answer, subject)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, map[string]any{
		"outcome": string(answer), "id": subject, "reversible": reversible,
	})
}

// takeBackAnswer withdraws one recorded answer.
//
// Each refusal is final and says what to do instead. None of them is an
// invitation to press the button again: the thing the answer asked for has
// happened, and the way back from that is a different action with a different
// name.
func (api *API) takeBackAnswer(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	var input takeBackRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	answer, subject, ok := api.takenBack(response, input.Outcome, input.ID)
	if !ok {
		return
	}

	err := api.wants.TakeBack(request.Context(), answer, subject)
	switch {
	case errors.Is(err, db.ErrCopyIsInTheLibrary):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"This copy is already in your library. Undoing the decision would " +
				"leave it there with nothing pointing at it. Remove the file from the " +
				"library instead."})
	case errors.Is(err, db.ErrWantAnsweredAnotherWay):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"This wishlist entry has already been answered another way since. " +
				"Nothing was changed."})
	case errors.Is(err, db.ErrRecordingWantedElsewhere):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"Another wishlist entry is already looking for this recording. " +
				"Nothing was changed."})
	case errors.Is(err, db.ErrEntryResolvedAgain):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"This entry has already been resolved again. Nothing was changed."})
	case errors.Is(err, db.ErrSchallDecidedThis):
		api.problem(response, http.StatusConflict, err.Error(),
			[]string{"Schall read the audio and decided this, so there is no answer of " +
				"yours to take back. Answer the question it raised instead."})
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "that answer is not on record", nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, map[string]any{"takenBack": true})
	}
}

// reviewCopyAudio plays one copy waiting for a decision.
//
// It serves the whole track rather than the thirty seconds the queue opens at,
// so the player gets range requests, a duration and a working scrubber — someone
// who is unsure about a copy needs to move around inside it, not hear a fixed
// window. Where playback starts is the page's business.
//
// Which copies may be heard is playableCopy's answer, shared with the waveform
// drawn over the same bytes.
func (api *API) reviewCopyAudio(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	if api.previews == nil || api.inboxPath == "" {
		api.problem(response, http.StatusServiceUnavailable, "audio preview is unavailable",
			[]string{"Schall was started without a download inbox, so there is nothing to play from."})
		return
	}
	fetched, ok := api.playableCopy(response, request)
	if !ok {
		return
	}
	source, err := api.copyFile(fetched)
	if err != nil {
		api.problem(response, http.StatusNotFound, "the copy is no longer there",
			[]string{err.Error()})
		return
	}
	api.servePlayable(response, request, source)
}

// playableCopy reads the copy the request names and answers whether a person is
// allowed to hear it.
//
// One check for the two routes that put a copy in front of somebody: the audio
// and the waveform drawn over it are the same file, so a copy one of them would
// serve and the other would refuse could not happen.
//
// Only a held copy is playable. An accepted one is in the library and has its own
// path; a discarded one was answered and its bytes are gone or going. The
// exception is a want that stopped because every copy was refused on the credit
// alone: nobody has heard those, and hearing one is how they get answered.
func (api *API) playableCopy(
	response http.ResponseWriter, request *http.Request,
) (db.AcquisitionTargetFileRow, bool) {
	copyID, err := uuid.Parse(chi.URLParam(request, "copyID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "copy not found", nil)
		return db.AcquisitionTargetFileRow{}, false
	}
	fetched, err := api.wants.Copy(request.Context(), copyID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "copy not found", nil)
		return db.AcquisitionTargetFileRow{}, false
	case err != nil:
		api.internalError(response, request, err)
		return db.AcquisitionTargetFileRow{}, false
	}
	playable := fetched.Verdict == db.AcquiredFileHeld
	if !playable {
		target, targetErr := api.wants.Target(request.Context(), fetched.AcquisitionTargetID)
		if targetErr == nil && target.Status == "pending" && !target.NextAttemptAt.Valid {
			copies, copiesErr := api.wants.Copies(request.Context(), fetched.AcquisitionTargetID)
			if copiesErr == nil && len(copies) > 0 && isCreditOnlyRefusal(fetched) {
				allCreditOnly := true
				for _, copy := range copies {
					if !isCreditOnlyRefusal(copy) {
						allCreditOnly = false
						break
					}
				}
				if allCreditOnly {
					playable = true
				}
			}
		}
	}
	if !playable {
		api.problem(response, http.StatusConflict, "this copy is not waiting for a decision",
			[]string{"Only a copy nothing could decide about is played here."})
		return db.AcquisitionTargetFileRow{}, false
	}
	return fetched, true
}

// reviewCopyWaveform draws the shape of one copy's audio, so the card it sits on
// shows where the music is before anybody presses play.
//
// The importer writes the waveform when it holds a copy. A copy held before it
// did has none, and this reads one now and keeps it: a picture never changes, so
// it is read once and cached hard from then on.
func (api *API) reviewCopyWaveform(response http.ResponseWriter, request *http.Request) {
	if !api.acquisitionsAvailable(response) {
		return
	}
	fetched, ok := api.playableCopy(response, request)
	if !ok {
		return
	}
	peaks := fetched.Waveform
	if len(peaks) == 0 {
		peaks = api.drawWaveform(request.Context(), fetched)
	}
	if len(peaks) == 0 {
		api.problem(response, http.StatusNotFound, "no waveform for this copy",
			[]string{"Nothing could read this copy's audio, so there is no shape to draw."})
		return
	}
	values := make([]int, len(peaks))
	for index, peak := range peaks {
		values[index] = int(peak)
	}
	body, err := json.Marshal(map[string]any{"peaks": values})
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// Peaks never change: they are one reading of bytes that do not move. The
	// ETag is the hash of the peaks themselves, so a phone that already has
	// this shape confirms it with a 304 instead of asking again.
	sum := sha256.Sum256(peaks)
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	response.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(response, request, "", time.Time{}, bytes.NewReader(body))
}

// drawWaveform reads a copy's shape now and stores it, for the copies that were
// held before the importer started drawing one.
//
// Everything that can go wrong is no waveform: no reader, no inbox, bytes the
// peer's copy no longer has, audio the decoder could not read. None of them is
// an error about the question the copy is part of.
func (api *API) drawWaveform(
	ctx context.Context, fetched db.AcquisitionTargetFileRow,
) []byte {
	if api.waveforms == nil || !api.waveforms.Available() || api.inboxPath == "" {
		return nil
	}
	source, err := api.copyFile(fetched)
	if err != nil {
		return nil
	}
	peaks, err := api.waveforms.Peaks(ctx, source)
	if err != nil || len(peaks) == 0 {
		api.logger.Debug().Err(err).Str("acquisition_target_file_id", fetched.ID.String()).
			Msg("could not read the shape of a held copy's audio")
		return nil
	}
	if err := api.wants.RecordCopyWaveform(ctx, fetched.ID, peaks); err != nil {
		// The picture is in hand and the reader has already run. Failing to keep
		// it costs the next reader another decode, never this response.
		api.logger.Warn().Err(err).Str("acquisition_target_file_id", fetched.ID.String()).
			Msg("could not keep the shape of a held copy's audio")
	}
	return peaks
}

// acceptWantFile records that a person says the library file a want's copy
// became is the recording that want asked for.
//
// One request for the whole answer. The want stopped because two MusicBrainz
// rows disagreed about one piece of audio and nothing could separate them; the
// person read both credits and did. The decision is written onto the file by
// hand and is permanent, and the want settles against that file on the sweep
// that follows.
func (api *API) acceptWantFile(response http.ResponseWriter, request *http.Request) {
	targetID, ok := api.acquisitionTargetID(response, request)
	if !ok {
		return
	}
	err := api.wants.AcceptFiledCopy(request.Context(), targetID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "acquisition target not found", nil)
	case errors.Is(err, acquisition.ErrWantIsNotStopped):
		api.problem(response, http.StatusConflict, "this wishlist entry is not waiting on a library file",
			[]string{"It has already been answered or is being looked at again."})
	case errors.Is(err, acquisition.ErrNoFiler):
		api.problem(response, http.StatusServiceUnavailable, "recording what a file is is unavailable",
			[]string{"Schall was started without the identity service."})
	case err != nil:
		api.internalError(response, request, err)
	default:
		api.writeJSON(response, http.StatusOK, map[string]any{"accepted": true})
	}
}

// servePlayable transcodes one audio file and writes it to the response.
//
// It is shared by every surface that plays something, because a player is only
// useful where a decision is being made and there is more than one such place:
// the review queue asks which copy of a recording to keep, and the library asks
// what a file already on disc actually is. Both need the same thing — the audio,
// seekable, in a format every browser plays — so both come through here rather
// than each growing its own copy of the same twenty lines.
func (api *API) servePlayable(response http.ResponseWriter, request *http.Request, source string) {
	playable, err := api.previews.Playable(request.Context(), source)
	switch {
	case errors.Is(err, preview.ErrUnavailable):
		api.problem(response, http.StatusServiceUnavailable, "audio preview is unavailable",
			[]string{"ffmpeg could not be found, so files cannot be played. " +
				"Everything else about this question still decides it."})
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}

	file, err := os.Open(playable)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	response.Header().Set("Content-Type", api.previews.ContentType())
	response.Header().Set("Accept-Ranges", "bytes")
	// A transcode is private to whoever is listening and cheap to reproduce.
	response.Header().Set("Cache-Control", "private, max-age=3600")
	// The cache file is named for a hash of the source path, size and mtime
	// (Transcoder.key), so its own name is already a strong ETag: a phone that
	// seeked into this preview once can confirm it is unchanged with a 304.
	etag := strings.TrimSuffix(filepath.Base(playable), filepath.Ext(playable))
	response.Header().Set("ETag", `"`+etag+`"`)
	http.ServeContent(response, request, "", info.ModTime(), file)
}

// copyFile locates one copy of a want: under the fetch folder for a track taken
// from a keyed want's address (ADR 0038 §6), and under the download inbox for a
// peer's copy. The importer chooses between the two by the same provider.
func (api *API) copyFile(copied db.AcquisitionTargetFileRow) (string, error) {
	root := api.inboxPath
	if tracksource.Serves(copied.Provider) {
		root = api.fetchPath
	}
	if root == "" {
		return "", errors.New("the folder this copy was fetched into is not configured")
	}
	return inboxFile(root, copied.RemotePath, copied.FileName)
}

// inboxFile locates one fetched copy under the download inbox.
//
// The peer's own path is the only name it has, and it is a Windows path as often
// as not, so the folder is taken from its last component exactly as the importer
// does. Nothing below the inbox is reachable by any name that leaves it.
func inboxFile(inbox, remotePath, name string) (string, error) {
	folder := path.Base(path.Dir(strings.ReplaceAll(strings.TrimSpace(remotePath), `\`, "/")))
	if folder == "" || folder == "." || folder == "/" || name == "" {
		return "", errors.New("the copy has no readable local name")
	}
	root, err := filepath.Abs(inbox)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, folder, name))
	if err != nil {
		return "", errors.New("the copy is not in the download inbox any more")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("the copy is outside the download inbox")
	}
	return resolved, nil
}

func (api *API) acquisitionTargetID(
	response http.ResponseWriter, request *http.Request,
) (uuid.UUID, bool) {
	if !api.acquisitionsAvailable(response) {
		return uuid.Nil, false
	}
	targetID, err := uuid.Parse(chi.URLParam(request, "targetID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "acquisition target not found", nil)
		return uuid.Nil, false
	}
	return targetID, true
}

func (api *API) acquisitionsAvailable(response http.ResponseWriter) bool {
	if api.wants == nil {
		api.problem(response, http.StatusServiceUnavailable, "acquisition targets are unavailable", nil)
		return false
	}
	return true
}

// optionalID reads an identifier the caller may leave out entirely. An empty
// string is absence; anything else has to be a real identifier, because a
// target pointed at nothing is worse than one pointed at nowhere.
func optionalID(raw, field string, problems []string) (uuid.NullUUID, []string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return uuid.NullUUID{}, problems
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return uuid.NullUUID{}, append(problems, field+" must be a UUID")
	}
	return uuid.NullUUID{UUID: parsed, Valid: true}, problems
}
