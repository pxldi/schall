package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/downloads"
	"github.com/pxldi/schall/internal/peerchallenge"
	"github.com/pxldi/schall/internal/sources"
)

// Limits on a stored request. A folder far larger than this is not a release,
// and the provenance record is not the place to keep an unbounded file list.
const (
	maxRequestedFiles       = 500
	maxRequestedNameLength  = 1000
	maxRequestedPathLength  = 2000
	maxRequestedUserLength  = 200
	maxRequestedReasons     = 20
	maxRequestedReasonRunes = 200
	// maxPeerReplyRunes is one chat line. A challenge asks for a word, and a
	// message longer than this is not an answer to one.
	maxPeerReplyRunes = 500
)

type downloadRequestFileResponse struct {
	Name            string `json:"name"`
	Extension       string `json:"extension"`
	SizeBytes       int64  `json:"sizeBytes"`
	BitRate         int    `json:"bitRate,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
	// Transfer is what became of this file, once a transfer for it exists. A
	// request that has not been started has none.
	Transfer *downloadFileTransferResponse `json:"transfer,omitempty"`
}

// downloadFileTransferResponse is one file's transfer, named so a settled
// request can say which file did not arrive rather than only how many.
type downloadFileTransferResponse struct {
	// Path is the remote name, which is what a retry has to ask for.
	Path             string `json:"path"`
	Status           string `json:"status"`
	TransferredBytes int64  `json:"transferredBytes"`
	Error            string `json:"error,omitempty"`
	// Retryable reports whether this file alone can be asked for again.
	Retryable bool `json:"retryable"`
	// Attempt is which try this file is on, and Attempts is what the settled
	// ones came to. A file that arrived first time has one entry and an
	// attempt of 1; anything more is the record of what it took.
	Attempt  int32                `json:"attempt"`
	Attempts []db.TransferAttempt `json:"attempts"`
}

// requestStatuses are the states a download request can be filtered by. It
// mirrors the check constraint on the table, so a state the database allows can
// always be asked for.
var requestStatuses = map[string]bool{
	"requested": true, "started": true, "completed": true,
	"failed": true, "cancelled": true,
}

type downloadProgressResponse struct {
	TransferCount  int32 `json:"transferCount"`
	CompletedCount int32 `json:"completedCount"`
	FailedCount    int32 `json:"failedCount"`
	// QueuedCount is how many files the peer has accepted but not begun
	// sending. A request whose files are all queued is waiting on somebody
	// else's upload slot, which is not the same as one that is moving.
	QueuedCount      int32 `json:"queuedCount"`
	TransferredBytes int64 `json:"transferredBytes"`
}

type downloadRequestResponse struct {
	ID uuid.UUID `json:"id"`
	// A request is about a release or about one wanted recording, never both and
	// never neither. The release fields are absent on a request that serves a
	// want, and the entry is what names the music instead.
	AlbumID             *uuid.UUID                    `json:"albumId,omitempty"`
	AlbumTitle          string                        `json:"albumTitle,omitempty"`
	ArtistID            *uuid.UUID                    `json:"artistId,omitempty"`
	ArtistName          string                        `json:"artistName,omitempty"`
	AcquisitionTargetID *uuid.UUID                    `json:"acquisitionTargetId,omitempty"`
	EntryArtist         string                        `json:"entryArtist,omitempty"`
	EntryTitle          string                        `json:"entryTitle,omitempty"`
	Provider            string                        `json:"provider"`
	Username            string                        `json:"username"`
	Directory           string                        `json:"directory"`
	Status              string                        `json:"status"`
	FileCount           int32                         `json:"fileCount"`
	ExpectedTrackCount  int32                         `json:"expectedTrackCount"`
	TotalSizeBytes      int64                         `json:"totalSizeBytes"`
	Format              string                        `json:"format"`
	AverageBitRate      *int32                        `json:"averageBitRate,omitempty"`
	Score               float64                       `json:"score"`
	Reasons             []string                      `json:"reasons"`
	Files               []downloadRequestFileResponse `json:"files"`
	Progress            downloadProgressResponse      `json:"progress"`
	Startable           bool                          `json:"startable"`
	// RetryableCount is how many of this request's files failed and can be
	// asked for again without re-downloading the ones that arrived.
	RetryableCount int                    `json:"retryableCount"`
	RequestedAt    time.Time              `json:"requestedAt"`
	StartedAt      *time.Time             `json:"startedAt"`
	CancelledAt    *time.Time             `json:"cancelledAt"`
	Error          *string                `json:"error,omitempty"`
	ImportStatus   string                 `json:"importStatus"`
	ImportError    *string                `json:"importError,omitempty"`
	ImportPath     *string                `json:"importPath,omitempty"`
	ImportedAt     *time.Time             `json:"importedAt"`
	ImportReviews  []importReviewResponse `json:"importReviews"`
	// ImportEvidence is the comparison behind the current pause, lifted from
	// the latest paused review so the UI does not have to search history for
	// the evidence that explains the state the request is in now.
	ImportEvidence *db.ImportEvidence `json:"importEvidence,omitempty"`
	// ImportDecisions are the per-track judgements recorded so far. They apply
	// the next time validation runs; recording one imports nothing by itself.
	ImportDecisions []db.ImportDecision `json:"importDecisions"`
	// Revalidatable reports whether the paused import can be sent back through
	// validation. Retrying re-runs every check; it never accepts a rejection.
	Revalidatable bool `json:"revalidatable"`
	// DuplicateAcknowledgedAt and DuplicateEvidence report that this request
	// was made against a library that already held something of this release,
	// and what the person who asked for it anyway was shown.
	DuplicateAcknowledgedAt *time.Time            `json:"duplicateAcknowledgedAt,omitempty"`
	DuplicateEvidence       *db.DuplicateEvidence `json:"duplicateEvidence,omitempty"`
	// PeerQuestion is the last thing this peer said in private chat that nobody
	// here could read. It rides only on a request whose files could still be
	// held by it, and what to do about it is a person's to decide.
	PeerQuestion *peerQuestionResponse `json:"peerQuestion,omitempty"`
}

type peerQuestionResponse struct {
	Username string    `json:"username"`
	Message  string    `json:"message"`
	AskedAt  time.Time `json:"askedAt"`
}

type importReviewResponse struct {
	Kind       string             `json:"kind"`
	Detail     string             `json:"detail,omitempty"`
	RecordedAt time.Time          `json:"recordedAt"`
	Evidence   *db.ImportEvidence `json:"evidence,omitempty"`
}

func downloadRequestResponseFrom(row db.DownloadRequestRow) downloadRequestResponse {
	files := make([]downloadRequestFileResponse, 0, len(row.Files))
	// A request recorded before Schall stored remote paths cannot be started,
	// because a peer only serves the exact name it advertised.
	startable := row.Status == "requested" && len(row.Files) > 0
	transfers := make(map[string]db.FileTransfer, len(row.Transfers))
	for _, transfer := range row.Transfers {
		transfers[transfer.Path] = transfer
	}
	// Only a settled request has anything to retry. While transfers are still
	// moving, a failed file is one the poller may yet have more to say about.
	//
	// A copy fetched for a want is never one of them. The loop asks for that
	// music again by itself — from this peer once it is free, or from the next
	// one — so a Retry button beside it would be a question whose only answer is
	// the thing that was going to happen anyway, and ten wants that all chose one
	// busy peer would be ten of them. A release somebody chose by hand has nobody
	// else to ask, and keeps its retry.
	pursued := row.AcquisitionTargetID.Valid
	retryable := 0
	for _, file := range row.Files {
		response := downloadRequestFileResponse{
			Name: file.Name, Extension: file.Extension, SizeBytes: file.SizeBytes,
			BitRate: file.BitRate, DurationSeconds: file.DurationSeconds,
		}
		if transfer, started := transfers[file.Path]; started {
			retry := !pursued && row.Status == "failed" && transfer.Status == "failed"
			attempts := transfer.Attempts
			if attempts == nil {
				attempts = []db.TransferAttempt{}
			}
			response.Transfer = &downloadFileTransferResponse{
				Path: transfer.Path, Status: transfer.Status,
				TransferredBytes: transfer.TransferredBytes,
				Error:            transfer.Error, Retryable: retry,
				Attempt: transfer.Attempt, Attempts: attempts,
			}
			if retry {
				retryable++
			}
		}
		files = append(files, response)
		if strings.TrimSpace(file.Path) == "" {
			startable = false
		}
	}
	reasons := row.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	reviews := make([]importReviewResponse, 0, len(row.ImportReviews))
	// The history is oldest first, so the last paused entry is the one that
	// explains a request that is waiting for review now.
	var currentEvidence *db.ImportEvidence
	for _, review := range row.ImportReviews {
		reviews = append(reviews, importReviewResponse{
			Kind: review.Kind, Detail: review.Detail,
			RecordedAt: review.RecordedAt, Evidence: review.Evidence,
		})
		if review.Kind == "paused" {
			currentEvidence = review.Evidence
		}
	}
	if row.ImportStatus != "needs_review" {
		currentEvidence = nil
	}
	decisions := row.ImportDecisions
	if decisions == nil {
		decisions = []db.ImportDecision{}
	}
	return downloadRequestResponse{
		ID: row.ID, AlbumID: uuidPointer(row.AlbumID), AlbumTitle: row.AlbumTitle,
		ArtistID: uuidPointer(row.ArtistID), ArtistName: row.ArtistName,
		AcquisitionTargetID: uuidPointer(row.AcquisitionTargetID),
		EntryArtist:         row.EntryArtist,
		EntryTitle:          row.EntryTitle,
		Provider:            row.Provider,
		Username:            row.SourceUsername, Directory: row.SourceDirectory, Status: row.Status,
		FileCount: row.FileCount, ExpectedTrackCount: row.ExpectedTrackCount,
		TotalSizeBytes: row.TotalSizeBytes, Format: row.Format,
		AverageBitRate: int32Pointer(row.AverageBitRate), Score: row.Score,
		Reasons: reasons, Files: files, RequestedAt: row.RequestedAt.Time,
		Progress: downloadProgressResponse{
			TransferCount: row.TransferCount, CompletedCount: row.CompletedCount,
			FailedCount: row.FailedCount, QueuedCount: row.QueuedCount,
			TransferredBytes: row.TransferredBytes,
		},
		Startable:       startable,
		RetryableCount:  retryable,
		StartedAt:       nullableTime(row.StartedAt.Valid, row.StartedAt.Time),
		CancelledAt:     nullableTime(row.CancelledAt.Valid, row.CancelledAt.Time),
		Error:           textPointer(row.ErrorMessage),
		ImportStatus:    row.ImportStatus,
		ImportError:     textPointer(row.ImportError),
		ImportPath:      textPointer(row.ImportPath),
		ImportedAt:      nullableTime(row.ImportedAt.Valid, row.ImportedAt.Time),
		ImportReviews:   reviews,
		ImportEvidence:  currentEvidence,
		ImportDecisions: decisions,
		Revalidatable:   row.Status == "completed" && row.ImportStatus == "needs_review",
		DuplicateAcknowledgedAt: nullableTime(
			row.DuplicateAcknowledgedAt.Valid, row.DuplicateAcknowledgedAt.Time),
		DuplicateEvidence: row.DuplicateEvidence,
	}
}

type downloadRequestFileInput struct {
	// Path is the remote name the peer advertised. Without it the request could
	// never be started, so it is required rather than optional.
	Path            string `json:"path"`
	Name            string `json:"name"`
	Extension       string `json:"extension"`
	SizeBytes       int64  `json:"sizeBytes"`
	BitRate         int    `json:"bitRate"`
	DurationSeconds int    `json:"durationSeconds"`
}

type createDownloadRequestInput struct {
	Provider       string                     `json:"provider"`
	Username       string                     `json:"username"`
	Directory      string                     `json:"directory"`
	Format         string                     `json:"format"`
	AverageBitRate int                        `json:"averageBitRate"`
	Score          float64                    `json:"score"`
	Reasons        []string                   `json:"reasons"`
	Files          []downloadRequestFileInput `json:"files"`
	// AcknowledgeDuplicates answers the question Schall asks when the library
	// already holds something of this release. It is deliberately a separate
	// answer rather than a flag the UI can set once and forget: the request is
	// refused until it is given, and what was shown is recorded with it.
	AcknowledgeDuplicates bool `json:"acknowledgeDuplicates"`
}

// requestAlbumDownload records the source the user chose for a release. It
// starts nothing: the request exists so that a transfer, when Schall can make
// one, carries the release it was meant to satisfy as provenance.
func (api *API) requestAlbumDownload(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	if !api.downloadsAvailable(response) {
		return
	}

	var input createDownloadRequestInput
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
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

	// The provider must be the configured one. A request naming a provider
	// Schall cannot search is provenance that could never be acted on.
	provider, _, err := api.sourceProvider(request.Context())
	if errors.Is(err, sources.ErrNotConfigured) {
		api.problem(response, http.StatusServiceUnavailable, "no download source is configured",
			[]string{"Configure and enable slskd in Settings."})
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	input.Provider = strings.TrimSpace(input.Provider)
	if input.Provider == "" {
		input.Provider = provider.Name()
	}
	if problems := validateDownloadRequest(input, provider.Name()); len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	// Nothing is recorded until Schall has said what the library already holds
	// against this release. A request that duplicates owned or unresolved music
	// is a decision for the user, not for the matcher.
	duplicates, err := api.downloads.AlbumDuplicateEvidence(request.Context(), albumID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if duplicates.Any() && !input.AcknowledgeDuplicates {
		api.logger.Info().
			Str("album_id", albumID.String()).
			Int("owned_tracks", duplicates.OwnedTrackCount).
			Int("unresolved_files", duplicates.UnresolvedCount).
			Msg("download request paused for duplicate confirmation")
		api.duplicateConflict(response, duplicates)
		return
	}

	params := db.CreateDownloadRequestParams{
		AlbumID:            albumID,
		Provider:           input.Provider,
		SourceUsername:     strings.TrimSpace(input.Username),
		SourceDirectory:    strings.TrimSpace(input.Directory),
		FileCount:          int32(len(input.Files)),
		ExpectedTrackCount: album.TrackCount,
		Format:             sources.NormalizeExtension(input.Format),
		Score:              input.Score,
		Reasons:            input.Reasons,
	}
	for _, file := range input.Files {
		params.TotalSizeBytes += file.SizeBytes
		params.Files = append(params.Files, db.DownloadRequestFile{
			Path: strings.TrimSpace(file.Path),
			Name: strings.TrimSpace(file.Name), Extension: sources.NormalizeExtension(file.Extension),
			SizeBytes: file.SizeBytes, BitRate: file.BitRate, DurationSeconds: file.DurationSeconds,
		})
	}
	// A peer reports a bitrate for lossless files too, and it describes nothing
	// about the encoding, so it is stored only where it means something.
	if input.AverageBitRate > 0 {
		params.AverageBitRate = pgtype.Int4{Int32: int32(input.AverageBitRate), Valid: true}
	}
	// The evidence is stored as it stood when the answer was given, not as the
	// request body described it, so a request cannot claim to have accepted
	// something nobody was shown.
	if duplicates.Any() {
		params.DuplicateEvidence = &duplicates
	}

	id, created, err := api.downloads.CreateDownloadRequest(request.Context(), params)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	stored, err := api.downloads.DownloadRequest(request.Context(), id)
	if err != nil {
		api.internalError(response, request, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		api.logger.Info().
			Str("album_id", albumID.String()).Str("provider", params.Provider).
			Str("username", params.SourceUsername).Str("directory", params.SourceDirectory).
			Int32("file_count", params.FileCount).
			Msg("download requested")
	}
	api.writeJSON(response, status, downloadRequestResponseFrom(stored))
}

func validateDownloadRequest(input createDownloadRequestInput, providerName string) []string {
	problems := make([]string, 0, 4)
	if input.Provider != providerName {
		problems = append(problems, "provider must be "+providerName)
	}
	switch username := strings.TrimSpace(input.Username); {
	case username == "":
		problems = append(problems, "username is required")
	case len(username) > maxRequestedUserLength:
		problems = append(problems, "username is too long")
	}
	if len(strings.TrimSpace(input.Directory)) > maxRequestedPathLength {
		problems = append(problems, "directory is too long")
	}
	if input.Score < 0 || input.Score > 1 {
		problems = append(problems, "score must be between 0 and 1")
	}
	if input.AverageBitRate < 0 {
		problems = append(problems, "averageBitRate must be zero or greater")
	}
	if len(input.Reasons) > maxRequestedReasons {
		problems = append(problems, "reasons must hold 20 entries or fewer")
	}
	for _, reason := range input.Reasons {
		if len([]rune(reason)) > maxRequestedReasonRunes {
			problems = append(problems, "each reason must be 200 characters or fewer")
			break
		}
	}
	switch {
	case len(input.Files) == 0:
		problems = append(problems, "files must hold at least one file")
	case len(input.Files) > maxRequestedFiles:
		problems = append(problems, "files must hold 500 files or fewer")
	}
	for _, file := range input.Files {
		if strings.TrimSpace(file.Name) == "" {
			problems = append(problems, "every file needs a name")
			break
		}
		if strings.TrimSpace(file.Path) == "" {
			problems = append(problems, "every file needs the remote path the peer reported")
			break
		}
		if len(file.Path) > maxRequestedPathLength {
			problems = append(problems, "a remote file path is too long")
			break
		}
		if len(file.Name) > maxRequestedNameLength {
			problems = append(problems, "a file name is too long")
			break
		}
		if file.SizeBytes < 0 {
			problems = append(problems, "a file reports a negative size")
			break
		}
	}
	return problems
}

func (api *API) listAlbumDownloads(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	api.writeDownloadRequests(response, request, uuid.NullUUID{UUID: albumID, Valid: true})
}

func (api *API) listDownloads(response http.ResponseWriter, request *http.Request) {
	api.writeDownloadRequests(response, request, uuid.NullUUID{})
}

func (api *API) writeDownloadRequests(response http.ResponseWriter, request *http.Request, albumID uuid.NullUUID) {
	if !api.downloadsAvailable(response) {
		return
	}
	params := db.ListDownloadRequestsParams{AlbumID: albumID}
	if raw := strings.TrimSpace(request.URL.Query().Get("status")); raw != "" {
		if !requestStatuses[raw] {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"status must be one of requested, started, completed, failed, or cancelled"})
			return
		}
		params.Status = raw
	}
	// A view is the pile the screen is showing: open work, the copies waiting
	// on a person, what was imported, what was not used, what failed. The names
	// and the conditions behind them belong to the store, so the screen, the
	// counts and this check cannot drift apart.
	if raw := strings.TrimSpace(request.URL.Query().Get("view")); raw != "" {
		if _, known := db.DownloadRequestView(raw); !known {
			api.problem(response, http.StatusUnprocessableEntity, "validation failed",
				[]string{"view must be one of open, review, imported, discarded, failed, or all"})
			return
		}
		params.View = raw
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

	page, err := api.downloads.ListDownloadRequests(request.Context(), params)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items := make([]downloadRequestResponse, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, downloadRequestResponseFrom(row))
	}
	api.attachPeerQuestions(request.Context(), items)
	// The counts ride with the page so the filter strip above the list and the
	// list itself are one answer rather than two reads that can disagree.
	api.writeJSON(response, http.StatusOK, map[string]any{
		"items": items, "total": page.Total, "limit": params.Limit, "offset": params.Offset,
		"counts": page.Counts,
	})
}

// listAlbumDuplicates reports what the library already holds against one
// release: tracks that are owned, and files that look like this release without
// a proven identity. It exists so the question can be asked before the download
// button is pressed rather than only in the refusal afterwards.
func (api *API) listAlbumDuplicates(response http.ResponseWriter, request *http.Request) {
	albumID, ok := api.albumID(response, request)
	if !ok {
		return
	}
	if !api.downloadsAvailable(response) {
		return
	}
	evidence, err := api.downloads.AlbumDuplicateEvidence(request.Context(), albumID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "release not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, evidence)
}

// duplicateConflict refuses an acquisition of music the library may already
// hold. It carries the whole evidence rather than a count, because the answer
// it asks for is only as good as what the person answering was shown.
func (api *API) duplicateConflict(response http.ResponseWriter, evidence db.DuplicateEvidence) {
	api.writeJSON(response, http.StatusConflict, map[string]any{
		"title":  "this release may already be in the library",
		"status": http.StatusConflict,
		"details": []string{
			evidence.Summary,
			"Confirm to download it anyway, or resolve the files in the Library view first.",
		},
		"duplicates": evidence,
	})
}

type startDownloadInput struct {
	// AcknowledgeDuplicates answers a duplicate question that was raised after
	// the request was recorded, which is how a request made before the library
	// held this release is still held to the same check.
	AcknowledgeDuplicates bool `json:"acknowledgeDuplicates"`
	// AcknowledgeSourceChange answers the re-check: the peer still offers this
	// folder but not what was recorded for it, and the user wants the files
	// that are still there anyway.
	AcknowledgeSourceChange bool `json:"acknowledgeSourceChange"`
}

// sourceChangeConflict refuses a start against a folder the peer is still
// offering but no longer offering as it was recorded. It carries the whole
// offer rather than a count, for the same reason duplicate protection does.
func (api *API) sourceChangeConflict(response http.ResponseWriter, offer downloads.SourceOffer) {
	details := make([]string, 0, 3)
	if len(offer.Missing) > 0 {
		details = append(details, "No longer offered: "+strings.Join(offer.Missing, ", "))
	}
	if len(offer.Changed) > 0 {
		details = append(details, "Offered at a different size: "+strings.Join(offer.Changed, ", "))
	}
	details = append(details,
		"Start it anyway to take what is still there, or search the release again for another source.")

	api.writeJSON(response, http.StatusConflict, map[string]any{
		"title":   "the peer no longer offers this folder as it was recorded",
		"status":  http.StatusConflict,
		"details": details,
		"sourceChange": sourceOfferResponse{
			Offered: offer.Found, Missing: offer.Missing, Changed: offer.Changed, Stale: true,
		},
	})
}

// sourceOfferResponse is what the peer offers for a recorded folder now.
type sourceOfferResponse struct {
	// Offered is whether the peer's folder was found at all. False means the
	// peer said nothing, which is what an offline peer looks like — it is not
	// a report that the folder is gone.
	Offered bool `json:"offered"`
	// Missing and Changed name the recorded files the peer no longer offers,
	// and the ones whose size no longer matches.
	Missing []string `json:"missing"`
	Changed []string `json:"changed"`
	// Stale reports whether starting would ask for something that is not there.
	Stale bool `json:"stale"`
}

// checkDownloadSource re-searches for a recorded folder without starting
// anything, so the interface can say a source has gone stale before the user
// presses start rather than after the transfer fails.
func (api *API) checkDownloadSource(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}
	if api.transfers == nil {
		api.problem(response, http.StatusServiceUnavailable, "starting transfers is unavailable", nil)
		return
	}

	offer, err := api.transfers.CheckSource(request.Context(), requestID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusNotFound, "no recorded download request with that ID", nil)
		return
	case errors.Is(err, sources.ErrNotConfigured):
		api.problem(response, http.StatusServiceUnavailable, "no download source is configured",
			[]string{"Configure and enable slskd in Settings."})
		return
	case errors.Is(err, downloads.ErrNotSupported):
		api.problem(response, http.StatusServiceUnavailable,
			"the configured download source cannot start transfers", nil)
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		api.logger.Warn().Err(err).Str("request_id", requestID.String()).Msg("source re-check failed")
		api.problem(response, http.StatusBadGateway, "could not check the source", []string{err.Error()})
		return
	}

	missing, changed := offer.Missing, offer.Changed
	if missing == nil {
		missing = []string{}
	}
	if changed == nil {
		changed = []string{}
	}
	api.writeJSON(response, http.StatusOK, sourceOfferResponse{
		Offered: offer.Found, Missing: missing, Changed: changed, Stale: offer.Stale(),
	})
}

// startDownload asks the provider to transfer the requested folder. This is the
// only endpoint in Schall that starts a transfer, and it starts nothing that was
// not already recorded as a decision.
//
// Duplicate protection is checked here as well as at the point the request was
// recorded, because the library moves between the two. A request that was
// answered for keeps that answer; one that never was is refused until it is.
func (api *API) startDownload(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}
	if api.transfers == nil {
		api.problem(response, http.StatusServiceUnavailable, "starting transfers is unavailable", nil)
		return
	}
	// The body is optional: a start that has nothing to answer for needs none.
	var input startDownloadInput
	if err := decodeJSON(response, request, &input); err != nil && !errors.Is(err, io.EOF) {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if !api.confirmDuplicatesBeforeStart(response, request, requestID, input) {
		return
	}

	row, err := api.transfers.Start(request.Context(), requestID, input.AcknowledgeSourceChange)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict,
			"no recorded download request with that ID is waiting to start", nil)
		return
	case errors.Is(err, downloads.ErrSourceChanged):
		// A conflict carrying the evidence, mirroring duplicate protection: the
		// request is untouched and can be started again once someone answers.
		var changed downloads.SourceChangedError
		errors.As(err, &changed)
		api.sourceChangeConflict(response, changed.Offer)
		return
	case errors.Is(err, sources.ErrNotConfigured):
		api.problem(response, http.StatusServiceUnavailable, "no download source is configured",
			[]string{"Configure and enable slskd in Settings."})
		return
	case errors.Is(err, downloads.ErrNotSupported):
		api.problem(response, http.StatusServiceUnavailable,
			"the configured download source cannot start transfers", nil)
		return
	case errors.Is(err, db.ErrNoRemotePath):
		api.problem(response, http.StatusUnprocessableEntity, "this request cannot be started",
			[]string{err.Error()})
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		// The request was reverted and carries the provider's own reason, so
		// the failure is reported rather than hidden behind a 500.
		api.logger.Warn().Err(err).Str("request_id", requestID.String()).Msg("start transfers failed")
		api.problem(response, http.StatusBadGateway, "could not start the transfer", []string{err.Error()})
		return
	}
	api.writeJSON(response, http.StatusAccepted, downloadRequestResponseFrom(row))
}

type retryDownloadInput struct {
	// Paths are the remote names to ask for again. They travel in the body
	// rather than the URL because a remote path is whatever the peer called it,
	// backslashes and all, and that is the only name it will serve.
	Paths []string `json:"paths"`
}

// retryDownload asks the provider for the files of a settled request that never
// arrived, leaving the ones that did exactly where they are.
//
// Duplicate protection is not asked again. It was answered for this request,
// against this release, and the files this retry covers are the ones that were
// already meant to be here; re-asking would only teach the user to click past
// the question that matters.
func (api *API) retryDownload(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}
	if api.transfers == nil {
		api.problem(response, http.StatusServiceUnavailable, "starting transfers is unavailable", nil)
		return
	}
	// The body is optional: retrying everything that failed needs none.
	var input retryDownloadInput
	if err := decodeJSON(response, request, &input); err != nil && !errors.Is(err, io.EOF) {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	if len(input.Paths) > maxRequestedFiles {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"paths must hold 500 entries or fewer"})
		return
	}

	row, err := api.transfers.Retry(request.Context(), requestID, input.Paths)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict,
			"no failed download request with that ID can be retried",
			[]string{"Only a request whose transfers settled with a failure can be retried."})
		return
	case errors.Is(err, db.ErrNothingToRetry):
		api.problem(response, http.StatusConflict, "there is nothing to retry", []string{err.Error()})
		return
	case errors.Is(err, db.ErrNotRetryable):
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{err.Error()})
		return
	case errors.Is(err, db.ErrWantHasAnotherCopy):
		api.problem(response, http.StatusConflict, "this wishlist entry is already following another copy",
			[]string{"A wanted recording is pursued one copy at a time. " +
				"This one only failed to arrive, which rules nothing out, so it stays " +
				"in the running and will be asked for again."})
		return
	case errors.Is(err, db.ErrNoRemotePath):
		api.problem(response, http.StatusUnprocessableEntity, "this request cannot be retried",
			[]string{err.Error()})
		return
	case errors.Is(err, sources.ErrNotConfigured):
		api.problem(response, http.StatusServiceUnavailable, "no download source is configured",
			[]string{"Configure and enable slskd in Settings."})
		return
	case errors.Is(err, downloads.ErrNotSupported):
		api.problem(response, http.StatusServiceUnavailable,
			"the configured download source cannot start transfers", nil)
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		// The retry was reverted and carries the provider's own reason.
		api.logger.Warn().Err(err).Str("request_id", requestID.String()).Msg("retry transfers failed")
		api.problem(response, http.StatusBadGateway, "could not retry the transfer", []string{err.Error()})
		return
	}
	// The retry itself is logged where it happens, with the count of files that
	// were actually requeued rather than the count the caller happened to name.
	api.writeJSON(response, http.StatusAccepted, downloadRequestResponseFrom(row))
}

// confirmDuplicatesBeforeStart reports whether the transfer may go ahead. A
// request that already carries an acknowledgement is never asked again: the
// question was answered, and re-asking because a scan has run since would
// teach the user to click through it.
func (api *API) confirmDuplicatesBeforeStart(
	response http.ResponseWriter, request *http.Request,
	requestID uuid.UUID, input startDownloadInput,
) bool {
	stored, err := api.downloads.DownloadRequest(request.Context(), requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		// There is nothing to protect and nothing to answer for. Starting says
		// why a request nobody recorded cannot be started.
		return true
	}
	if err != nil {
		api.internalError(response, request, err)
		return false
	}
	if stored.DuplicateAcknowledgedAt.Valid {
		return true
	}
	// Duplicate protection asks what the library already holds of this release.
	// A request serving a want has no release to ask about, and the same question
	// was answered before it existed: a want the library already holds is settled
	// from the identities and mappings that were decided for it, without a search.
	if !stored.AlbumID.Valid {
		return true
	}
	duplicates, err := api.downloads.AlbumDuplicateEvidence(request.Context(), stored.AlbumID.UUID)
	if err != nil {
		api.internalError(response, request, err)
		return false
	}
	if !duplicates.Any() {
		return true
	}
	if !input.AcknowledgeDuplicates {
		api.logger.Info().
			Str("request_id", requestID.String()).
			Int("owned_tracks", duplicates.OwnedTrackCount).
			Int("unresolved_files", duplicates.UnresolvedCount).
			Msg("transfer paused for duplicate confirmation")
		api.duplicateConflict(response, duplicates)
		return false
	}
	if _, err := api.downloads.AcknowledgeDuplicates(
		request.Context(), requestID, duplicates,
	); errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusConflict,
			"no recorded download request with that ID is waiting to start", nil)
		return false
	} else if err != nil {
		api.internalError(response, request, err)
		return false
	}
	return true
}

// cancelDownload withdraws an open request, stopping any transfer it already
// started at the provider.
func (api *API) cancelDownload(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}

	cancel := api.downloads.CancelDownloadRequest
	if api.transfers != nil {
		cancel = api.transfers.Cancel
	}
	row, err := cancel(request.Context(), requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "no open download request with that ID", nil)
		return
	}
	if err != nil {
		api.logger.Warn().Err(err).Str("request_id", requestID.String()).Msg("cancel transfers failed")
		api.problem(response, http.StatusBadGateway, "could not stop the transfer", []string{err.Error()})
		return
	}
	api.writeJSON(response, http.StatusOK, downloadRequestResponseFrom(row))
}

// revalidateDownloadImport asks validation to run again for an import that
// stopped for review, after the user corrected tags or chose a different
// representative edition. It is deliberately not a way to accept the files: the
// same checks decide, and a still-uncertain import pauses again with a new
// reason recorded beside the old ones.
func (api *API) revalidateDownloadImport(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}
	row, err := api.downloads.RevalidateDownloadImport(request.Context(), requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusConflict, "no import with that ID is waiting for review", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("request_id", requestID.String()).Msg("download import revalidation requested")
	api.writeJSON(response, http.StatusAccepted, downloadRequestResponseFrom(row))
}

type resolveImportTrackInput struct {
	FileName string    `json:"fileName"`
	TrackID  uuid.UUID `json:"trackId"`
	Note     string    `json:"note"`
}

// resolveImportTrack records that one downloaded file is one catalogue track,
// judged by a person. It is not an accept button: the decision is stored as
// evidence, the file still has to survive every check about its bytes, and
// validation runs again before anything is imported.
//
// Only a file whose evidence says it is resolvable can be decided. A file that
// is absent, damaged, or unreadable is not a judgement call, and the fingerprint
// is taken from the evidence rather than the request body, so a decision cannot
// be recorded against something the reviewer never saw.
func (api *API) resolveImportTrack(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}
	var input resolveImportTrackInput
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	input.FileName = strings.TrimSpace(input.FileName)
	problems := make([]string, 0, 2)
	if input.FileName == "" {
		problems = append(problems, "fileName is required")
	}
	if input.TrackID == uuid.Nil {
		problems = append(problems, "trackId is required")
	}
	if len([]rune(input.Note)) > maxRequestedReasonRunes {
		problems = append(problems, "note must be 200 characters or fewer")
	}
	if len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	stored, err := api.downloads.DownloadRequest(request.Context(), requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "download request not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	file, found := resolvableFile(stored, input.FileName)
	switch {
	case stored.ImportStatus != "needs_review":
		api.problem(response, http.StatusConflict, "no import with that ID is waiting for review", nil)
		return
	case !found:
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"the paused import has no file by that name"})
		return
	case !file.Resolvable:
		api.problem(response, http.StatusUnprocessableEntity, "this file cannot be resolved by hand",
			append([]string{
				"Only a disagreement about which track a file is can be decided. " +
					"A file that is missing, damaged, or unreadable has to be downloaded again.",
			}, file.Problems...))
		return
	}

	row, err := api.downloads.RecordImportDecision(request.Context(), db.RecordImportDecisionParams{
		RequestID: requestID, FileName: input.FileName, TrackID: input.TrackID,
		Fingerprint: file.Observed.Fingerprint(), Note: strings.TrimSpace(input.Note),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		api.problem(response, http.StatusConflict, "no import with that ID is waiting for review", nil)
		return
	case errors.Is(err, db.ErrTrackNotInRelease):
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", []string{err.Error()})
		return
	case err != nil:
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("request_id", requestID.String()).
		Str("file", input.FileName).Str("track_id", input.TrackID.String()).
		Msg("import track resolved by hand")
	api.writeJSON(response, http.StatusOK, downloadRequestResponseFrom(row))
}

// resolvableFile finds one file in the evidence behind the current pause. The
// evidence is what the reviewer was shown, so it is also what a decision is
// allowed to be made against.
func resolvableFile(row db.DownloadRequestRow, name string) (db.ImportFileEvidence, bool) {
	var evidence *db.ImportEvidence
	for _, review := range row.ImportReviews {
		if review.Kind == "paused" && review.Evidence != nil {
			evidence = review.Evidence
		}
	}
	if evidence == nil {
		return db.ImportFileEvidence{}, false
	}
	for _, file := range evidence.Files {
		if file.Name == name {
			return file, file.Observed != nil
		}
	}
	return db.ImportFileEvidence{}, false
}

// withdrawImportDecision takes a judgement back. The decision and its
// withdrawal both stay in the import history.
func (api *API) withdrawImportDecision(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}
	decisionID, err := uuid.Parse(chi.URLParam(request, "decisionID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "resolution not found", nil)
		return
	}
	row, err := api.downloads.WithdrawImportDecision(request.Context(), requestID, decisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "resolution not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("request_id", requestID.String()).
		Str("decision_id", decisionID.String()).Msg("import resolution withdrawn")
	api.writeJSON(response, http.StatusOK, downloadRequestResponseFrom(row))
}

func (api *API) downloadRequestID(response http.ResponseWriter, request *http.Request) (uuid.UUID, bool) {
	if !api.downloadsAvailable(response) {
		return uuid.Nil, false
	}
	requestID, err := uuid.Parse(chi.URLParam(request, "requestID"))
	if err != nil {
		api.problem(response, http.StatusNotFound, "download request not found", nil)
		return uuid.Nil, false
	}
	return requestID, true
}

// waitingOnThePeer reports a request whose files could still be held by what
// the peer said. An imported request has its music, and a cancelled one was
// called off; a message beside either of those is a message about nothing, and
// the same peer serves both.
func waitingOnThePeer(status string) bool {
	return status == "requested" || status == "started" || status == "failed"
}

func (api *API) downloadsAvailable(response http.ResponseWriter) bool {
	if api.downloads == nil {
		api.problem(response, http.StatusServiceUnavailable, "downloads are unavailable", nil)
		return false
	}
	return true
}

type peerReplyInput struct {
	Username string `json:"username"`
	Message  string `json:"message"`
}

// replyToPeer sends one peer what a person typed in answer to its question, and
// asks that peer again for the files it refused.
//
// The peer is named in the body and checked against the request it is sent
// from, so this endpoint can only ever write to the person Schall already asked
// for music. Nothing here reads the message: a person read it and decided what
// the answer is.
func (api *API) replyToPeer(response http.ResponseWriter, request *http.Request) {
	requestID, ok := api.downloadRequestID(response, request)
	if !ok {
		return
	}
	if api.challenges == nil {
		api.problem(response, http.StatusServiceUnavailable, "answering peers is unavailable", nil)
		return
	}

	var input peerReplyInput
	if err := decodeJSON(response, request, &input); err != nil {
		api.problem(response, http.StatusBadRequest, "invalid request body", []string{err.Error()})
		return
	}
	username, message := strings.TrimSpace(input.Username), strings.TrimSpace(input.Message)
	problems := make([]string, 0, 2)
	if username == "" {
		problems = append(problems, "username is required")
	}
	if message == "" {
		problems = append(problems, "message is required")
	}
	if len([]rune(message)) > maxPeerReplyRunes {
		problems = append(problems, "message must be 500 characters or fewer")
	}
	if len(problems) > 0 {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed", problems)
		return
	}

	row, err := api.downloads.DownloadRequest(request.Context(), requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		api.problem(response, http.StatusNotFound, "download request not found", nil)
		return
	}
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	if row.SourceUsername != username {
		api.problem(response, http.StatusUnprocessableEntity, "validation failed",
			[]string{"username must be the peer this download was requested from"})
		return
	}

	switch err := api.challenges.Reply(request.Context(), username, message); {
	case errors.Is(err, peerchallenge.ErrNoQuestion):
		api.problem(response, http.StatusConflict, "this peer has not asked anything",
			[]string{"The question was answered already. Reload the page."})
		return
	case errors.Is(err, sources.ErrNotConfigured):
		api.problem(response, http.StatusServiceUnavailable, "no download source is configured",
			[]string{"Configure and enable slskd in Settings."})
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case err != nil:
		api.logger.Warn().Err(err).Str("request_id", requestID.String()).Str("username", username).
			Msg("could not send a peer the reply")
		api.problem(response, http.StatusBadGateway, "could not send the reply", []string{err.Error()})
		return
	}

	refreshed, err := api.downloads.DownloadRequest(request.Context(), requestID)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	api.writeJSON(response, http.StatusOK, downloadRequestResponseFrom(refreshed))
}

// attachPeerQuestions fills in what each request's peer asked and nobody could
// read. It is one query for the whole page, and a failure leaves the page as it
// was: a question that is not shown is a transfer that waits, not a wrong
// answer.
func (api *API) attachPeerQuestions(ctx context.Context, items []downloadRequestResponse) {
	if api.challenges == nil || len(items) == 0 {
		return
	}
	usernames := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Username == "" || seen[item.Username] || !waitingOnThePeer(item.Status) {
			continue
		}
		seen[item.Username] = true
		usernames = append(usernames, item.Username)
	}

	questions, err := api.challenges.Questions(ctx, usernames)
	if err != nil {
		api.logger.Warn().Err(err).Msg("could not read what these peers asked")
		return
	}
	for index, item := range items {
		question, asked := questions[item.Username]
		if !asked || !waitingOnThePeer(item.Status) {
			continue
		}
		items[index].PeerQuestion = &peerQuestionResponse{
			Username: question.Username,
			Message:  question.Challenge,
			AskedAt:  question.CreatedAt.Time,
		}
	}
}
