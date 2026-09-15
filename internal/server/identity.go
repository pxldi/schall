package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/identity"
)

// IdentityResolver answers what an independently acquired file is, and records
// what a person decided when the providers could not settle it.
type IdentityResolver interface {
	File(context.Context, uuid.UUID) (identity.FileResolution, error)
	Accept(context.Context, uuid.UUID, uuid.UUID) error
	MarkLocalOnly(context.Context, uuid.UUID) error
	Clear(context.Context, uuid.UUID) error
}

// WithIdentityResolver registers the component that resolves file identities.
// Without it, the library still scans and matches; it simply cannot say what a
// file is when no followed artist accounts for it.
func WithIdentityResolver(identities IdentityResolver) Option {
	return func(api *API) {
		api.identities = identities
	}
}

type identityResponse struct {
	RecordingID    *uuid.UUID `json:"recordingId,omitempty"`
	ReleaseGroupID *uuid.UUID `json:"releaseGroupId,omitempty"`
	ArtistName     string     `json:"artistName,omitempty"`
	ReleaseTitle   string     `json:"releaseTitle,omitempty"`
	TrackTitle     string     `json:"trackTitle,omitempty"`
	DurationMS     *int       `json:"durationMs,omitempty"`
	ISRC           string     `json:"isrc,omitempty"`
	Kind           string     `json:"kind"`
	// Method is the short name Schall records evidence under, kept for programs
	// reading this answer. MethodText is the same thing in words, and it is what
	// a person is shown: "recording-id" means nothing to a reader.
	Method     string    `json:"method"`
	MethodText string    `json:"methodText"`
	Confidence float32   `json:"confidence"`
	Manual     bool      `json:"manual"`
	Summary    string    `json:"summary"`
	Evidence   []string  `json:"evidence"`
	DecidedAt  time.Time `json:"decidedAt"`
}

type identityCandidateResponse struct {
	RecordingID    uuid.UUID  `json:"recordingId"`
	ReleaseGroupID *uuid.UUID `json:"releaseGroupId,omitempty"`
	ArtistName     string     `json:"artistName"`
	ReleaseTitle   string     `json:"releaseTitle,omitempty"`
	TrackTitle     string     `json:"trackTitle"`
	DurationMS     *int       `json:"durationMs,omitempty"`
	ISRC           string     `json:"isrc,omitempty"`
	Rank           int        `json:"rank"`
	Agrees         []string   `json:"agrees"`
	Differs        []string   `json:"differs"`
	Summary        string     `json:"summary"`
}

type fileIdentityResponse struct {
	FileID      uuid.UUID                   `json:"fileId"`
	Status      string                      `json:"status"`
	Summary     string                      `json:"summary,omitempty"`
	Error       string                      `json:"error,omitempty"`
	AttemptedAt *time.Time                  `json:"attemptedAt,omitempty"`
	MatchStatus string                      `json:"matchStatus"`
	Identity    *identityResponse           `json:"identity"`
	Candidates  []identityCandidateResponse `json:"candidates"`
}

func (api *API) getFileIdentity(response http.ResponseWriter, request *http.Request) {
	fileID, ok := api.identityFileID(response, request)
	if !ok {
		return
	}
	resolution, err := api.identities.File(request.Context(), fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, describeResolution(resolution))
}

func describeResolution(resolution identity.FileResolution) fileIdentityResponse {
	result := fileIdentityResponse{
		FileID: resolution.FileID, Status: resolution.Status,
		Summary: resolution.Summary, Error: resolution.Error,
		AttemptedAt: resolution.AttemptedAt, MatchStatus: resolution.MatchStatus,
		Candidates: make([]identityCandidateResponse, 0, len(resolution.Candidates)),
	}
	if stored := resolution.Identity; stored != nil {
		result.Identity = &identityResponse{
			RecordingID: optionalUUID(stored.RecordingID), ReleaseGroupID: optionalUUID(stored.ReleaseGroupID),
			ArtistName: stored.ArtistName, ReleaseTitle: stored.ReleaseTitle,
			TrackTitle: stored.TrackTitle, DurationMS: stored.DurationMS, ISRC: stored.ISRC,
			Kind: stored.Kind, Method: stored.Method,
			MethodText: identity.MethodSentence(stored.Method), Confidence: stored.Confidence,
			Manual: stored.IsManual, Summary: stored.Summary, Evidence: stored.Evidence,
			DecidedAt: stored.DecidedAt,
		}
	}
	for _, candidate := range resolution.Candidates {
		result.Candidates = append(result.Candidates, identityCandidateResponse{
			RecordingID: candidate.RecordingID, ReleaseGroupID: optionalUUID(candidate.ReleaseGroupID),
			ArtistName: candidate.ArtistName, ReleaseTitle: candidate.ReleaseTitle,
			TrackTitle: candidate.TrackTitle, DurationMS: candidate.DurationMS,
			ISRC: candidate.ISRC, Rank: candidate.Rank, Agrees: candidate.Agrees,
			Differs: candidate.Differs, Summary: candidate.Summary,
		})
	}
	return result
}

// decideIdentityRequest is a person answering a question the providers could
// not settle. Exactly one of the two answers is expected: this recording, or no
// external identity at all.
type decideIdentityRequest struct {
	RecordingID uuid.UUID `json:"recordingId"`
	LocalOnly   bool      `json:"localOnly"`
}

func (api *API) decideFileIdentity(response http.ResponseWriter, request *http.Request) {
	fileID, ok := api.identityFileID(response, request)
	if !ok {
		return
	}
	var input decideIdentityRequest
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if (input.RecordingID == uuid.Nil) == !input.LocalOnly {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"provide either recordingId or localOnly"})
		return
	}

	var err error
	if input.LocalOnly {
		err = api.identities.MarkLocalOnly(request.Context(), fileID)
	} else {
		err = api.identities.Accept(request.Context(), fileID, input.RecordingID)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "library file not found", nil)
	case errors.Is(err, identity.ErrUnknownCandidate):
		api.problem(response, http.StatusUnprocessableEntity, err.Error(), nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		// The file is answered from here on, whether by a recording or by being
		// declared local-only. Withdrawing that answer already announced itself
		// (see clearFileIdentity); making it never did, so a second interface
		// kept counting the file among the unresolved.
		api.decisions.Publish()
		api.readBackIdentity(response, request, fileID)
	}
}

func (api *API) clearFileIdentity(response http.ResponseWriter, request *http.Request) {
	fileID, ok := api.identityFileID(response, request)
	if !ok {
		return
	}
	err := api.identities.Clear(request.Context(), fileID)
	switch {
	case errors.Is(err, identity.ErrNoDecision):
		api.problem(response, http.StatusNotFound, "no decision has been recorded for this file", nil)
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "library file not found", nil)
	case err != nil:
		api.internalError(response, request, err)
	default:
		// Withdrawing a decision puts the question back to the providers, which
		// is the only useful thing to do with a file nobody has answered for.
		if api.library != nil {
			if _, err := api.library.QueueFileResolution(request.Context(), fileID); err != nil {
				api.internalError(response, request, err)
				return
			}
		}
		// See queueLibraryScan: the file counts as unresolved from here on, and
		// stays queued until the worker claims it.
		api.events.Publish(events.TopicLibrary)
		api.readBackIdentity(response, request, fileID)
	}
}

// resolveLibraryFile asks the providers about one file on request, so a user
// who has just fixed a tag or followed an artist need not wait for a scan.
func (api *API) resolveLibraryFile(response http.ResponseWriter, request *http.Request) {
	if !api.libraryAvailable(response) {
		return
	}
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	job, err := api.library.QueueFileResolution(request.Context(), fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	// See queueLibraryScan: the worker may be minutes away from claiming this,
	// and a library view elsewhere would otherwise show the old answer as
	// settled until it got there.
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusAccepted, refreshResponse{
		JobID: job.ID, Status: job.Status, CreatedAt: job.CreatedAt,
	})
}

func (api *API) readBackIdentity(response http.ResponseWriter, request *http.Request, fileID uuid.UUID) {
	resolution, err := api.identities.File(request.Context(), fileID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, describeResolution(resolution))
}

func (api *API) identityFileID(response http.ResponseWriter, request *http.Request) (uuid.UUID, bool) {
	if api.identities == nil {
		api.problem(response, http.StatusServiceUnavailable, "identity resolution is unavailable", nil)
		return uuid.Nil, false
	}
	fileID, err := uuid.Parse(chi.URLParam(request, "fileID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "library file not found", nil)
		return uuid.Nil, false
	}
	return fileID, true
}

func optionalUUID(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}
