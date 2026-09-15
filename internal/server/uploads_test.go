package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/uploads"
	"github.com/rs/zerolog"
)

// fakeUploadStore is the database side of uploading. The staging folder is a
// real one in a temporary directory, because the filesystem is what this
// handler is about.
type fakeUploadStore struct {
	*fakeStore
	queuedID     uuid.UUID
	queuedFiles  []string
	queuedBytes  int64
	queuedNaming *db.UploadNaming
	queueErr     error
	imports      []db.UploadImportRow
	importsErr   error
	askedFor     []string
	status       string
	statusErr    error
}

func (store *fakeUploadStore) QueueUploadImport(
	_ context.Context, uploadID uuid.UUID, files []string, sizeBytes int64, naming *db.UploadNaming,
) (db.LibraryScanJobRow, error) {
	if store.queueErr != nil {
		return db.LibraryScanJobRow{}, store.queueErr
	}
	store.queuedID, store.queuedFiles, store.queuedBytes, store.queuedNaming =
		uploadID, files, sizeBytes, naming
	return db.LibraryScanJobRow{ID: uuid.New(), Status: "queued", CreatedAt: time.Now()}, nil
}

func (store *fakeUploadStore) UploadImports(
	_ context.Context, _ int32, staged []string,
) ([]db.UploadImportRow, error) {
	store.askedFor = staged
	return store.imports, store.importsErr
}

func (store *fakeUploadStore) UploadImportStatus(context.Context, uuid.UUID) (string, error) {
	if store.statusErr != nil {
		return "", store.statusErr
	}
	return store.status, nil
}

// tightVolume is the staging folder with the space it reports decided by the
// test. The volume is the one thing here a test cannot arrange for itself.
type tightVolume struct {
	*uploads.Staging
	free int64
}

func (volume tightVolume) CheckHeadroom(expected int64) error {
	if volume.free < expected {
		return uploads.ErrNoHeadroom
	}
	return nil
}

func newUploadAPI(t *testing.T) (http.Handler, *uploads.Staging, *fakeUploadStore) {
	t.Helper()
	staging, err := uploads.NewStaging(filepath.Join(t.TempDir(), "staging"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeUploadStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithUploadStaging(staging))
	return handler, staging, store
}

// uploadRequest is one upload as a browser sends it.
func uploadRequest(t *testing.T, files map[string]string) *http.Request {
	t.Helper()
	return uploadRequestWithAddress(t, files, nil)
}

// uploadRequestWithAddress is one upload sent with the SoundCloud address
// fields a browser adds beside the files once somebody pastes one.
func uploadRequestWithAddress(
	t *testing.T, files map[string]string, address map[string]string,
) *http.Request {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for field, value := range address {
		if err := writer.WriteField(field, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestUploadingIsUnavailableWithoutAStagingFolder(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestTheUploadListIsUnavailableWithoutAStagingFolder(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestUploadingStagesTheFilesAndAsksForThemToBeImported(t *testing.T) {
	handler, staging, store := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{
		"01 Mysterons.flac": "first", "02 Sour Times.flac": "second",
	}))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var payload uploadResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "queued" || len(payload.Files) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	if store.queuedID != payload.ID || store.queuedBytes != int64(len("first")+len("second")) {
		t.Fatalf("queued %s with %d bytes", store.queuedID, store.queuedBytes)
	}
	if _, err := staging.Open(payload.ID); err != nil {
		t.Fatalf("open = %v, want the files staged", err)
	}
}

// One address names one track, so a SoundCloud address sent beside a single
// file is queued for the worker to write once the file has a row.
func TestUploadingQueuesTheSoundCloudNamingForAOneFileUpload(t *testing.T) {
	handler, _, store := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequestWithAddress(
		t, map[string]string{"Illegal.flac": "audio"},
		map[string]string{
			"sourceUrl": "https://soundcloud.com/abo/illegal",
			"artist":    "PinkPantheress",
			"title":     "Illegal",
			"remixer":   "Abo",
		},
	))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	if store.queuedNaming == nil {
		t.Fatal("naming = nil, want the address and its confirmed naming queued")
	}
	want := db.UploadNaming{
		URL: "https://soundcloud.com/abo/illegal", Artist: "PinkPantheress", Title: "Illegal", Remixer: "Abo",
	}
	if *store.queuedNaming != want {
		t.Fatalf("naming = %+v, want %+v", *store.queuedNaming, want)
	}
}

// A selection of more than one file has no single file for an address to be
// about, so the naming is dropped rather than guessed onto one of them — and
// the upload of both files still succeeds.
func TestUploadingDropsASoundCloudAddressSentWithMoreThanOneFile(t *testing.T) {
	handler, _, store := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequestWithAddress(
		t, map[string]string{"one.flac": "first", "two.flac": "second"},
		map[string]string{
			"sourceUrl": "https://soundcloud.com/abo/illegal",
			"artist":    "PinkPantheress", "title": "Illegal",
		},
	))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.queuedNaming != nil {
		t.Fatalf("naming = %+v, want it dropped for a selection of more than one file", store.queuedNaming)
	}
	if len(store.queuedFiles) != 2 {
		t.Fatalf("files = %v, want both files still queued for import", store.queuedFiles)
	}
}

// No address sent is the ordinary upload, unchanged by any of this.
func TestUploadingWithNoSoundCloudAddressQueuesNoNaming(t *testing.T) {
	handler, _, store := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.queuedNaming != nil {
		t.Fatalf("naming = %+v, want nil", store.queuedNaming)
	}
}

func TestUploadingRefusesABodyThatIsNotAForm(t *testing.T) {
	handler, _, _ := newUploadAPI(t)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewReader([]byte("{}")))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A volume with no room says so before the transfer rather than after it.
func TestUploadingRefusesWhatTheVolumeHasNoRoomFor(t *testing.T) {
	staging, err := uploads.NewStaging(filepath.Join(t.TempDir(), "staging"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeUploadStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithUploadStaging(tightVolume{Staging: staging, free: 0}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	if response.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.queuedID != uuid.Nil {
		t.Fatal("an upload the volume had no room for was queued")
	}
}

// Anything else the volume says is ours rather than the caller's.
func TestUploadingReportsAVolumeItCannotRead(t *testing.T) {
	staging, err := uploads.NewStaging(filepath.Join(t.TempDir(), "staging"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(staging.Root()); err != nil {
		t.Fatal(err)
	}
	handler := NewAPI(&fakeUploadStore{fakeStore: &fakeStore{}}, fakeDatabase{},
		&fakeArtistSearcher{}, zerolog.Nop(), WithUploadStaging(staging))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestUploadingRefusesMoreThanOneUploadMayCarry(t *testing.T) {
	handler, _, _ := newUploadAPI(t)

	request := uploadRequest(t, map[string]string{"01.flac": "audio"})
	request.ContentLength = maxUploadBytes + 1
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestUploadingRefusesAFileTypeTheLibraryCannotHold(t *testing.T) {
	handler, _, store := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"cover.jpg": "picture"}))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.queuedID != uuid.Nil {
		t.Fatal("a refused upload was queued")
	}
}

func TestUploadingRefusesAFilenameCarryingAPath(t *testing.T) {
	handler, _, _ := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"disc 1/01.flac": "audio"}))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestUploadingRefusesARequestWithNoFilesInIt(t *testing.T) {
	handler, _, _ := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Bytes nothing will ever look at are not left on the volume as an upload
// nobody can see.
func TestUploadingClearsStagingWhenTheImportCannotBeQueued(t *testing.T) {
	handler, staging, store := newUploadAPI(t)
	store.queueErr = errors.New("the database went away")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	entries, err := os.ReadDir(staging.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging holds %d entries", len(entries))
	}
}

// A browser that goes away mid-transfer leaves nothing behind and nobody to
// tell, so the handler says nothing rather than reporting a failure of ours.
func TestUploadingLeavesNothingBehindWhenTheClientDisconnects(t *testing.T) {
	handler, staging, _ := newUploadAPI(t)
	request := uploadRequest(t, map[string]string{"01.flac": "audio"})
	ctx, cancel := context.WithCancel(request.Context())
	cancel()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request.WithContext(ctx))
	entries, err := os.ReadDir(staging.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging holds %d entries after a disconnect", len(entries))
	}
}

func TestTheUploadListReportsWhatEachUploadCameTo(t *testing.T) {
	handler, _, store := newUploadAPI(t)
	imported := uuid.New()
	failed := uuid.New()
	store.imports = []db.UploadImportRow{
		{
			UploadID: imported, JobID: uuid.New(), Status: "completed",
			Files: []string{"01 Mysterons.flac"}, SizeBytes: 42,
			ImportPath:  "/music/Portishead/Dummy [abcdef12]",
			CreatedAt:   time.Now(),
			CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		},
		{
			UploadID: failed, JobID: uuid.New(), Status: "failed",
			Files: []string{"02 Sour Times.flac"}, SizeBytes: 7,
			Error:     pgtype.Text{String: `"02 Sour Times.flac" cannot be read as audio`, Valid: true},
			CreatedAt: time.Now(),
		},
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload uploadListResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("items = %#v", payload.Items)
	}
	if payload.Items[0].Status != "imported" || payload.Items[0].ImportPath == "" {
		t.Fatalf("first = %#v", payload.Items[0])
	}
	if payload.Items[1].Status != "failed" || payload.Items[1].Detail == "" {
		t.Fatalf("second = %#v", payload.Items[1])
	}
	if len(payload.Items[1].FileStates) != 1 || payload.Items[1].FileStates[0].State != "failed" {
		t.Fatalf("failed file states = %#v", payload.Items[1].FileStates)
	}
	if payload.Items[0].CompletedAt == nil {
		t.Fatal("an imported upload has no completion time")
	}
}

func TestTheUploadListReportsEachImportedFileState(t *testing.T) {
	handler, _, store := newUploadAPI(t)
	uploadID := uuid.New()
	importPath := "/music/Uploads/abc12345"
	setAsideID, waitingID, importedID := uuid.New(), uuid.New(), uuid.New()
	store.imports = []db.UploadImportRow{{
		UploadID: uploadID, JobID: uuid.New(), Status: "completed",
		Files: []string{"01.flac", "02.flac", "03.flac"}, SizeBytes: 42,
		ImportPath: importPath, CreatedAt: time.Now(),
	}}
	store.libraryFiles = []db.LibraryFileRow{
		{ID: setAsideID, Path: filepath.Join(importPath, "01.flac"), MatchStatus: "ambiguous", ResolutionStatus: "resolved", SetAside: true},
		{ID: waitingID, Path: filepath.Join(importPath, "02.flac"), MatchStatus: "ambiguous", ResolutionStatus: "resolved"},
		{ID: importedID, Path: filepath.Join(importPath, "03.flac"), MatchStatus: "matched", ResolutionStatus: "resolved"},
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload uploadListResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || len(payload.Items[0].FileStates) != 3 {
		t.Fatalf("file states = %#v", payload.Items)
	}
	states := payload.Items[0].FileStates
	if states[0].Name != "01.flac" || states[0].State != "set_aside" || states[0].File == nil || states[0].File.ID != setAsideID {
		t.Fatalf("set-aside file = %#v", states[0])
	}
	if states[1].Name != "02.flac" || states[1].State != "waiting" || states[1].File == nil || states[1].File.ID != waitingID {
		t.Fatalf("waiting file = %#v", states[1])
	}
	if states[2].Name != "03.flac" || states[2].State != "imported" || states[2].File == nil || states[2].File.ID != importedID {
		t.Fatalf("imported file = %#v", states[2])
	}
	if store.libraryParams.Path != filepath.Join(importPath, "03.flac") {
		t.Fatalf("last exact path = %q", store.libraryParams.Path)
	}
}

// A running job is what validating looks like, and a queued one is what waiting
// looks like. Both keep their bytes on the volume until they are settled.
func TestTheUploadListReportsAJobThatIsStillRunning(t *testing.T) {
	handler, staging, store := newUploadAPI(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	staged, err := staging.List()
	if err != nil || len(staged) != 1 {
		t.Fatalf("staged = %#v, %v", staged, err)
	}
	store.imports = []db.UploadImportRow{{
		UploadID: staged[0].ID, JobID: uuid.New(), Status: "running",
		Files: staged[0].Names(), SizeBytes: staged[0].TotalBytes(), CreatedAt: time.Now(),
	}}

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	var payload uploadListResponse
	if err := json.NewDecoder(listed.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Status != "validating" {
		t.Fatalf("items = %#v", payload.Items)
	}
	if !payload.Items[0].Staged {
		t.Fatal("a running import reports its bytes as gone")
	}
}

// A folder with no job behind it is an upload a restart interrupted. It is
// listed rather than hidden, because it is holding bytes either way.
func TestTheUploadListReportsAStagedFolderWithNoJobBehindIt(t *testing.T) {
	handler, staging, _ := newUploadAPI(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	var payload uploadListResponse
	if err := json.NewDecoder(listed.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Status != "staged" {
		t.Fatalf("items = %#v", payload.Items)
	}
	if payload.StagingPath != staging.Root() || payload.MaxBytes != maxUploadBytes {
		t.Fatalf("payload = %#v", payload)
	}
}

// A job queued before its files were recorded still has a folder to read them
// from, so the list never shows an upload with no name.
func TestTheUploadListNamesAJobFromItsStagingFolder(t *testing.T) {
	handler, staging, store := newUploadAPI(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, uploadRequest(t, map[string]string{"01 Mysterons.flac": "audio"}))
	staged, err := staging.List()
	if err != nil || len(staged) != 1 {
		t.Fatalf("staged = %#v, %v", staged, err)
	}
	store.imports = []db.UploadImportRow{{
		UploadID: staged[0].ID, JobID: uuid.New(), Status: "queued", CreatedAt: time.Now(),
	}}

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	var payload uploadListResponse
	if err := json.NewDecoder(listed.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || len(payload.Items[0].Files) != 1 {
		t.Fatalf("items = %#v", payload.Items)
	}
	if payload.Items[0].Files[0] != "01 Mysterons.flac" {
		t.Fatalf("files = %v", payload.Items[0].Files)
	}
}

func TestTheUploadListReportsAStagingFolderItCannotRead(t *testing.T) {
	handler, staging, _ := newUploadAPI(t)
	if err := os.RemoveAll(staging.Root()); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingRemovesAStagedUpload(t *testing.T) {
	handler, staging, store := newUploadAPI(t)
	store.statusErr = pgx.ErrNoRows
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	var upload uploadResponse
	if err := json.NewDecoder(posted.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+upload.ID.String(), nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if _, err := staging.Open(upload.ID); !errors.Is(err, uploads.ErrNotStaged) {
		t.Fatalf("open = %v, want the upload gone", err)
	}
}

// Removing a folder a worker is reading would fail that import for a reason it
// could never explain.
func TestDiscardingRefusesAnUploadThatIsStillBeingImported(t *testing.T) {
	handler, _, store := newUploadAPI(t)
	store.status = "running"
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	var upload uploadResponse
	if err := json.NewDecoder(posted.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+upload.ID.String(), nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingReportsAnUploadThatIsNotStaged(t *testing.T) {
	handler, _, store := newUploadAPI(t)
	store.statusErr = pgx.ErrNoRows

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+uuid.New().String(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingRefusesAnIdentifierThatIsNotOne(t *testing.T) {
	handler, _, _ := newUploadAPI(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/nope", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingReportsAQueueItCannotRead(t *testing.T) {
	handler, _, store := newUploadAPI(t)
	store.statusErr = errors.New("the database went away")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+uuid.New().String(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingReportsAnIdentifierThatNamesAFile(t *testing.T) {
	handler, staging, store := newUploadAPI(t)
	store.statusErr = pgx.ErrNoRows
	id := uuid.New()
	if err := os.WriteFile(staging.Path(id), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+id.String(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingIsUnavailableWithoutAStagingFolder(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+uuid.New().String(), nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingReportsAnUploadItCannotLookAt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	handler, staging, store := newUploadAPI(t)
	store.statusErr = pgx.ErrNoRows
	id := uuid.New()
	if err := os.Mkdir(staging.Path(id), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(staging.Path(id), 0o755) })

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+id.String(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestDiscardingReportsAnUploadItCouldNotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	handler, staging, store := newUploadAPI(t)
	store.statusErr = pgx.ErrNoRows
	id := uuid.New()
	if err := os.Mkdir(staging.Path(id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(staging.Root(), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(staging.Root(), 0o755) })

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodDelete, "/api/v1/uploads/"+id.String(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A body that stops mid-transfer for a reason that is not the browser leaving
// is ours to report, not the caller's to fix.
func TestUploadingReportsABodyItCouldNotRead(t *testing.T) {
	handler, _, _ := newUploadAPI(t)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("files", "01 Mysterons.flac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a"), 64)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// The body stops after the part's headers but before its terminating
	// boundary, which is a transfer that broke rather than one that ended.
	truncated := buffer.Bytes()[:buffer.Len()-16]
	request := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", bytes.NewReader(truncated))
	request.Header.Set("Content-Type", writer.FormDataContentType())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A refused upload keeps its files on purpose. Without asking for it by name it
// would fall out of the page of history after a hundred later uploads and come
// back as a folder nothing accounts for, losing the reason it was refused.
func TestTheUploadListAsksAboutEveryUploadStillHoldingBytes(t *testing.T) {
	handler, staging, store := newUploadAPI(t)
	posted := httptest.NewRecorder()
	handler.ServeHTTP(posted, uploadRequest(t, map[string]string{"01.flac": "audio"}))
	staged, err := staging.List()
	if err != nil || len(staged) != 1 {
		t.Fatalf("staged = %#v, %v", staged, err)
	}

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	if len(store.askedFor) != 1 || store.askedFor[0] != staged[0].ID.String() {
		t.Fatalf("asked for %v, want the staged upload named", store.askedFor)
	}
}

func TestTheUploadListReportsAQueueItCannotRead(t *testing.T) {
	handler, _, store := newUploadAPI(t)
	store.importsErr = errors.New("the database went away")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/uploads", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}
