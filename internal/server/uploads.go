package server

import (
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/uploads"
)

// maxUploadBytes is the most one upload may carry. A lossless album fits with
// room to spare, including a high-resolution one; a whole discography does not,
// and does not belong in a single request that has to succeed or fail as a
// whole.
const maxUploadBytes = 4 << 30

// uploadDeadline is how long one upload may take. It replaces the server's own
// thirty seconds, which is a sensible bound for a request that answers a
// question and an absurd one for a request that carries half a gigabyte over
// somebody's domestic upstream.
const uploadDeadline = 2 * time.Hour

// uploadListLimit is how far back the uploads view reaches. It is a page of
// history rather than all of it: an upload that succeeded is a folder in the
// library from then on, and the library is where it is looked at.
const uploadListLimit = 100

// UploadStaging is the folder browser uploads land in. Optional: without one
// the routes report themselves unavailable rather than half-working, which is
// what an installation that has not configured a staging path should see.
type UploadStaging interface {
	Root() string
	CheckHeadroom(expected int64) error
	Accept(ctx context.Context, parts *multipart.Reader) (uploads.Upload, uploads.Address, error)
	Open(id uuid.UUID) (uploads.Upload, error)
	List() ([]uploads.Upload, error)
	Discard(id uuid.UUID) error
}

// UploadStore records that an upload is waiting to be imported and reads back
// what became of the ones before it.
type UploadStore interface {
	QueueUploadImport(ctx context.Context, uploadID uuid.UUID, files []string, sizeBytes int64, naming *db.UploadNaming) (db.LibraryScanJobRow, error)
	UploadImports(ctx context.Context, limit int32, staged []string) ([]db.UploadImportRow, error)
	UploadImportStatus(ctx context.Context, uploadID uuid.UUID) (string, error)
}

// WithUploadStaging registers the staging folder browser uploads land in.
func WithUploadStaging(staging UploadStaging) Option {
	return func(api *API) {
		api.uploads = staging
	}
}

func (api *API) uploadsAvailable(response http.ResponseWriter) bool {
	if api.uploads == nil || api.uploadStore == nil {
		api.problem(response, http.StatusServiceUnavailable, "uploading is not configured",
			[]string{"Set SCHALL_UPLOAD_STAGING_PATH and SCHALL_IMPORT_LIBRARY_PATH to upload music through the browser."})
		return false
	}
	return true
}

// uploadResponse is one upload and what it came to. Status is derived rather
// than stored: the staging folder says whether the bytes are still there and
// the job row says what was made of them, and between them there is nothing
// left for a third record to know.
type uploadResponse struct {
	ID     uuid.UUID `json:"id"`
	Status string    `json:"status"`
	Files  []string  `json:"files"`
	// FileStates describes each file once the import has reached the library
	// scanner. The nested file is the exact row at the exact landed path, so the
	// resolver never has to find it by a name that another file may share.
	FileStates []uploadFileResponse `json:"fileStates,omitempty"`
	// SizeBytes is what arrived, which is what the browser sent rather than what
	// is on the volume now: an imported upload's staging folder is gone.
	SizeBytes int64 `json:"sizeBytes"`
	// ImportPath is where the upload was filed, once it was.
	ImportPath string `json:"importPath,omitempty"`
	// Detail is why an upload failed, in the words the job recorded.
	Detail string `json:"detail,omitempty"`
	// Staged is whether the folder is still holding the bytes. A failed upload
	// keeps them, so that it can be looked at and discarded rather than having
	// disappeared along with the reason.
	Staged      bool       `json:"staged"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type uploadFileResponse struct {
	Name  string               `json:"name"`
	State string               `json:"state"`
	File  *libraryFileResponse `json:"file,omitempty"`
}

type uploadListResponse struct {
	Items []uploadResponse `json:"items"`
	// StagingPath is where uploads wait, shown so that an operator can see which
	// volume is filling up.
	StagingPath string `json:"stagingPath"`
	// MaxBytes is the most one upload may carry, so the browser can refuse a
	// selection before spending an hour on it.
	MaxBytes int64 `json:"maxBytes"`
}

// createUpload streams one multipart request into staging and asks for it to be
// imported.
//
// Nothing is buffered: the request is read part by part straight onto the
// volume, because a release is 300–500 MB and a handler that held one in memory
// would be one upload away from taking the process with it. The import itself
// is a job — validating and copying an album takes minutes, and a browser
// holding a connection open for them is a connection a proxy will close.
func (api *API) createUpload(response http.ResponseWriter, request *http.Request) {
	if !api.uploadsAvailable(response) {
		return
	}
	// The server's own read and write deadlines are set for requests that answer
	// a question in a moment. This one carries an album, so it lifts them to a
	// bound of its own rather than being cut off mid-transfer.
	controller := http.NewResponseController(response)
	deadline := time.Now().Add(uploadDeadline)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)

	// Refused on what the request says about itself before it is read, so that
	// an upload nobody could accept costs nobody an hour of upstream first. A
	// request that understates its length is caught again by the reader below.
	if request.ContentLength > maxUploadBytes {
		api.refuseUpload(response, request, &http.MaxBytesError{Limit: maxUploadBytes})
		return
	}
	// Asked before a byte is read, so that a volume with no room says so now
	// rather than after the whole transfer.
	if err := api.uploads.CheckHeadroom(request.ContentLength); err != nil {
		if errors.Is(err, uploads.ErrNoHeadroom) {
			api.problem(response, http.StatusInsufficientStorage,
				"the staging volume has no room for this upload", []string{err.Error()})
			return
		}
		api.internalError(response, request, err)
		return
	}

	request.Body = http.MaxBytesReader(response, request.Body, maxUploadBytes)
	parts, err := request.MultipartReader()
	if err != nil {
		api.problem(response, http.StatusBadRequest,
			"the upload must be sent as multipart/form-data", []string{err.Error()})
		return
	}

	upload, address, err := api.uploads.Accept(request.Context(), parts)
	if err != nil {
		api.refuseUpload(response, request, err)
		return
	}

	if _, err := api.uploadStore.QueueUploadImport(
		request.Context(), upload.ID, upload.Names(), upload.TotalBytes(),
		api.uploadNaming(upload, address),
	); err != nil {
		// The bytes are staged but nothing will ever look at them, so they go
		// rather than sit on the volume as an upload nobody can see.
		if discardErr := api.uploads.Discard(upload.ID); discardErr != nil {
			api.logger.Error().Err(discardErr).Str("upload_id", upload.ID.String()).
				Msg("clear the staging folder of an upload that could not be queued")
		}
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("upload_id", upload.ID.String()).
		Int("files", len(upload.Files)).Int64("bytes", upload.TotalBytes()).
		Msg("upload staged")
	// The import is a job the worker may be a moment away from claiming, and an
	// interface that is not the one that uploaded has no other way to learn that
	// something arrived.
	api.events.Publish(events.TopicLibrary)
	api.writeJSON(response, http.StatusAccepted, uploadResponse{
		ID: upload.ID, Status: "queued", Files: upload.Names(),
		FileStates: uploadFileStates(request.Context(), api, "queued", "", upload.Names()),
		SizeBytes:  upload.TotalBytes(), Staged: true, CreatedAt: upload.StagedAt,
	})
}

// uploadNaming turns the address a browser sent alongside an upload into what
// the import job carries, or nil where there is nothing to carry.
//
// One address names one track. A selection of several files staged as one
// upload has no single file for it to be about, so the address is logged and
// dropped here rather than let through to name whichever file happened to
// land first.
func (api *API) uploadNaming(upload uploads.Upload, address uploads.Address) *db.UploadNaming {
	if !address.Sent() {
		return nil
	}
	if len(upload.Files) != 1 {
		api.logger.Warn().Str("upload_id", upload.ID.String()).Int("files", len(upload.Files)).
			Msg("a SoundCloud address was sent with more than one file; it names none of them")
		return nil
	}
	return &db.UploadNaming{
		URL: address.URL, Artist: address.Artist, Title: address.Title, Remixer: address.Remixer,
	}
}

// refuseUpload maps what went wrong with an upload onto a status and a
// sentence. Everything the upload itself is the answer to is the caller's to
// fix; anything else is ours.
func (api *API) refuseUpload(response http.ResponseWriter, request *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		api.problem(response, http.StatusRequestEntityTooLarge, "the upload is too large",
			[]string{"One upload may carry up to 4 GiB. Send the release in more than one go."})
	case errors.Is(err, uploads.ErrNoFiles):
		api.problem(response, http.StatusUnprocessableEntity, "the upload contains no files", nil)
	case errors.Is(err, uploads.ErrUnsafeName),
		errors.Is(err, uploads.ErrUnsupported),
		errors.Is(err, uploads.ErrDuplicateName):
		api.problem(response, http.StatusUnprocessableEntity, "the upload was refused",
			[]string{err.Error()})
	case errors.Is(err, context.Canceled), request.Context().Err() != nil:
		// The browser went away mid-transfer. The staging folder has already been
		// cleared; there is nobody left to tell.
		//
		// The request's own context is asked as well as the error, because most of
		// an album's transfer time is spent inside one part: a disconnect there
		// surfaces as a read error rather than as a cancellation, and logging the
		// ordinary way an upload ends at Error level would bury the ones that
		// matter.
		api.logger.Info().Msg("an upload was abandoned before it finished")
	default:
		api.internalError(response, request, err)
	}
}

// listUploads reports what is staged and what each upload came to.
//
// It is assembled from the staging folder and the job rows, which between them
// hold everything there is to know: the folder says whether the bytes are still
// on the volume, and the job says whether anything has been made of them yet.
// A folder with no job behind it is an upload a restart interrupted before it
// could be queued — it is listed as staged rather than hidden, and startup asks
// for it again.
func (api *API) listUploads(response http.ResponseWriter, request *http.Request) {
	if !api.uploadsAvailable(response) {
		return
	}
	staged, err := api.uploads.List()
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	stagedByID := make(map[uuid.UUID]uploads.Upload, len(staged))
	stagedIDs := make([]string, 0, len(staged))
	for _, upload := range staged {
		stagedByID[upload.ID] = upload
		stagedIDs = append(stagedIDs, upload.ID.String())
	}
	// The window is a page of history; the staged identifiers are what makes an
	// upload still holding bytes fall inside it however old its job is.
	imports, err := api.uploadStore.UploadImports(request.Context(), uploadListLimit, stagedIDs)
	if err != nil {
		api.internalError(response, request, err)
		return
	}
	items := make([]uploadResponse, 0, len(imports)+len(staged))
	for _, row := range imports {
		upload, stillStaged := stagedByID[row.UploadID]
		delete(stagedByID, row.UploadID)
		item := uploadResponse{
			ID: row.UploadID, Status: uploadStatus(row.Status), Files: row.Files,
			FileStates: uploadFileStates(request.Context(), api, row.Status, row.ImportPath, row.Files),
			SizeBytes:  row.SizeBytes, ImportPath: row.ImportPath, Staged: stillStaged,
			CreatedAt: row.CreatedAt,
		}
		if row.Error.Valid {
			item.Detail = row.Error.String
		}
		if row.CompletedAt.Valid {
			completedAt := row.CompletedAt.Time
			item.CompletedAt = &completedAt
		}
		// A job whose payload predates its files being recorded still has a
		// folder to read them from.
		if len(item.Files) == 0 && stillStaged {
			item.Files, item.SizeBytes = upload.Names(), upload.TotalBytes()
			item.FileStates = uploadFileStates(request.Context(), api, "staged", "", item.Files)
		}
		items = append(items, item)
	}
	// Whatever is left in the map is a folder no job accounts for.
	for _, upload := range staged {
		if _, unqueued := stagedByID[upload.ID]; !unqueued {
			continue
		}
		items = append(items, uploadResponse{
			ID: upload.ID, Status: "staged", Files: upload.Names(),
			FileStates: uploadFileStates(request.Context(), api, "staged", "", upload.Names()),
			SizeBytes:  upload.TotalBytes(), Staged: true, CreatedAt: upload.StagedAt,
		})
	}
	api.writeJSON(response, http.StatusOK, uploadListResponse{
		Items: items, StagingPath: api.uploads.Root(), MaxBytes: maxUploadBytes,
	})
}

// uploadFileStates reads each imported file by its exact destination path. A
// scan can lag behind a completed import, so a missing row still means the
// file is importing and will be read again through the event stream or safety
// poll. A failed job has no file to wait for.
func uploadFileStates(
	ctx context.Context, api *API, jobStatus, importPath string, names []string,
) []uploadFileResponse {
	states := make([]uploadFileResponse, 0, len(names))
	for _, name := range names {
		state := "importing"
		if uploadStatus(jobStatus) == "failed" {
			state = "failed"
		}
		item := uploadFileResponse{Name: name, State: state}
		if jobStatus == "completed" && api.library != nil && importPath != "" {
			page, err := api.library.ListLibraryFiles(ctx, db.ListLibraryFilesParams{
				Limit: 1, Path: filepath.Join(importPath, name),
			})
			if err == nil && len(page.Items) > 0 {
				file := page.Items[0]
				response := libraryFileResponseFrom(file)
				item.File = &response
				item.State = uploadFileState(file)
			}
		}
		states = append(states, item)
	}
	return states
}

func uploadFileState(file db.LibraryFileRow) string {
	if file.MatchStatus == "ambiguous" ||
		file.ResolutionStatus == identity.StatusNeedsReview ||
		file.ResolutionStatus == identity.StatusConflict {
		if file.SetAside {
			return "set_aside"
		}
		return "waiting"
	}
	return "imported"
}

// uploadStatus says what a job's state means for the upload behind it. A
// cancelled job reads as failed: either way nothing was imported, and the
// distinction is about the queue rather than about the music.
func uploadStatus(jobStatus string) string {
	switch jobStatus {
	case "queued":
		return "queued"
	case "running":
		return "validating"
	case "completed":
		return "imported"
	case "staged":
		return "staged"
	default:
		return "failed"
	}
}

// discardUpload removes a staged upload from the volume. It is how an upload
// that was refused stops taking up room — the folder is deliberately kept when
// validation fails, so that the reason and the files it names are both still
// there to look at.
func (api *API) discardUpload(response http.ResponseWriter, request *http.Request) {
	if !api.uploadsAvailable(response) {
		return
	}
	uploadID, err := uuid.Parse(chi.URLParam(request, "uploadID"))
	if err != nil {
		api.problem(response, http.StatusBadRequest, "invalid upload id", nil)
		return
	}
	status, err := api.uploadStore.UploadImportStatus(request.Context(), uploadID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		api.internalError(response, request, err)
		return
	}
	// Removing a folder a worker is reading would fail that import for a reason
	// it could never explain. Waiting costs the caller one attempt.
	if status == "queued" || status == "running" {
		api.problem(response, http.StatusConflict, "this upload is still being imported",
			[]string{"Wait for it to finish, then discard it."})
		return
	}
	if _, err := api.uploads.Open(uploadID); err != nil {
		if errors.Is(err, uploads.ErrNotStaged) {
			api.problem(response, http.StatusNotFound, "that upload is not staged", nil)
			return
		}
		api.internalError(response, request, err)
		return
	}
	if err := api.uploads.Discard(uploadID); err != nil {
		api.internalError(response, request, err)
		return
	}
	api.logger.Info().Str("upload_id", uploadID.String()).Msg("staged upload discarded")
	api.events.Publish(events.TopicLibrary)
	response.WriteHeader(http.StatusNoContent)
}
