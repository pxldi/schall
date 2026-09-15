package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/downloads"
	"github.com/pxldi/schall/internal/peerchallenge"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// fakeDownloadStore keeps requests in memory and reproduces the one rule the
// handlers depend on: a folder already requested for a release stays a single
// open request.
type fakeDownloadStore struct {
	*fakeSettingsStore
	requests []db.DownloadRequestRow
	created  []db.CreateDownloadRequestParams
	listed   db.ListDownloadRequestsParams
	// counts is what the store says each pile of the list holds, which the
	// handler passes through untouched.
	counts  db.DownloadRequestCounts
	decided []db.RecordImportDecisionParams
	// duplicates is what the library is pretending to hold against the release
	// under test. Empty means a library with nothing to say about it.
	duplicates db.DuplicateEvidence
	// Each of these breaks one query on its own, so a test can fail exactly the
	// call it is about rather than every call the handler makes.
	evidenceErr    error
	createErr      error
	listErr        error
	revalidateErr  error
	decisionErr    error
	withdrawErr    error
	acknowledgeErr error
}

func (store *fakeDownloadStore) AlbumDuplicateEvidence(
	_ context.Context, albumID uuid.UUID,
) (db.DuplicateEvidence, error) {
	if store.evidenceErr != nil {
		return db.DuplicateEvidence{}, store.evidenceErr
	}
	evidence := store.duplicates
	evidence.AlbumID = albumID
	if evidence.Files == nil {
		evidence.Files = []db.DuplicateFile{}
	}
	return evidence, nil
}

func (store *fakeDownloadStore) AcknowledgeDuplicates(
	_ context.Context, requestID uuid.UUID, evidence db.DuplicateEvidence,
) (db.DownloadRequestRow, error) {
	if store.acknowledgeErr != nil {
		return db.DownloadRequestRow{}, store.acknowledgeErr
	}
	for index, row := range store.requests {
		if row.ID != requestID || row.Status != "requested" {
			continue
		}
		store.requests[index].DuplicateAcknowledgedAt = pgtype.Timestamptz{Valid: true}
		store.requests[index].DuplicateEvidence = &evidence
		return store.requests[index], nil
	}
	return db.DownloadRequestRow{}, pgx.ErrNoRows
}

func (store *fakeDownloadStore) key(albumID uuid.NullUUID, provider, username, directory string) string {
	return strings.Join([]string{albumID.UUID.String(), provider, username, directory}, "\x00")
}

func (store *fakeDownloadStore) CreateDownloadRequest(_ context.Context, params db.CreateDownloadRequestParams) (uuid.UUID, bool, error) {
	if store.createErr != nil {
		return uuid.Nil, false, store.createErr
	}
	store.created = append(store.created, params)
	wanted := store.key(uuid.NullUUID{UUID: params.AlbumID, Valid: true}, params.Provider, params.SourceUsername, params.SourceDirectory)
	for _, existing := range store.requests {
		if existing.Status != "requested" {
			continue
		}
		if store.key(existing.AlbumID, existing.Provider, existing.SourceUsername, existing.SourceDirectory) == wanted {
			return existing.ID, false, nil
		}
	}
	row := db.DownloadRequestRow{
		ID: uuid.New(), AlbumID: uuid.NullUUID{UUID: params.AlbumID, Valid: true}, AlbumTitle: "Dummy", ArtistName: "Portishead",
		Provider: params.Provider, SourceUsername: params.SourceUsername,
		SourceDirectory: params.SourceDirectory, Status: "requested",
		FileCount: params.FileCount, ExpectedTrackCount: params.ExpectedTrackCount,
		TotalSizeBytes: params.TotalSizeBytes, Format: params.Format,
		AverageBitRate: params.AverageBitRate, Score: params.Score,
		Reasons: params.Reasons, Files: params.Files,
		RequestedAt:       pgtype.Timestamptz{Valid: true},
		DuplicateEvidence: params.DuplicateEvidence,
	}
	if params.DuplicateEvidence != nil {
		row.DuplicateAcknowledgedAt = pgtype.Timestamptz{Valid: true}
	}
	store.requests = append(store.requests, row)
	return row.ID, true, nil
}

func (store *fakeDownloadStore) DownloadRequest(_ context.Context, id uuid.UUID) (db.DownloadRequestRow, error) {
	for _, row := range store.requests {
		if row.ID == id {
			return row, nil
		}
	}
	return db.DownloadRequestRow{}, pgx.ErrNoRows
}

func (store *fakeDownloadStore) ListDownloadRequests(_ context.Context, params db.ListDownloadRequestsParams) (db.DownloadRequestPage, error) {
	store.listed = params
	return db.DownloadRequestPage{
		Items: store.requests, Total: int64(len(store.requests)), Counts: store.counts,
	}, store.listErr
}

func (store *fakeDownloadStore) CancelDownloadRequest(_ context.Context, id uuid.UUID) (db.DownloadRequestRow, error) {
	for index, row := range store.requests {
		if row.ID != id || row.Status != "requested" {
			continue
		}
		store.requests[index].Status = "cancelled"
		store.requests[index].CancelledAt = pgtype.Timestamptz{Valid: true}
		return store.requests[index], nil
	}
	return db.DownloadRequestRow{}, pgx.ErrNoRows
}

// RevalidateDownloadImport mirrors the store rule the handler depends on: only
// a completed request whose import is waiting for review can be retried.
func (store *fakeDownloadStore) RevalidateDownloadImport(_ context.Context, id uuid.UUID) (db.DownloadRequestRow, error) {
	if store.revalidateErr != nil {
		return db.DownloadRequestRow{}, store.revalidateErr
	}
	for index, row := range store.requests {
		if row.ID != id || row.Status != "completed" || row.ImportStatus != "needs_review" {
			continue
		}
		store.requests[index].ImportStatus = "pending"
		store.requests[index].ImportError = pgtype.Text{}
		store.requests[index].ImportReviews = append(
			store.requests[index].ImportReviews, db.ImportReview{Kind: "revalidated"})
		return store.requests[index], nil
	}
	return db.DownloadRequestRow{}, pgx.ErrNoRows
}

func (store *fakeDownloadStore) RecordImportDecision(
	_ context.Context, params db.RecordImportDecisionParams,
) (db.DownloadRequestRow, error) {
	if store.decisionErr != nil {
		return db.DownloadRequestRow{}, store.decisionErr
	}
	for index, row := range store.requests {
		if row.ID != params.RequestID || row.ImportStatus != "needs_review" {
			continue
		}
		store.decided = append(store.decided, params)
		store.requests[index].ImportDecisions = append(store.requests[index].ImportDecisions,
			db.ImportDecision{
				ID: uuid.New(), FileName: params.FileName, TrackID: params.TrackID,
				Fingerprint: params.Fingerprint, Note: params.Note,
			})
		store.requests[index].ImportReviews = append(
			store.requests[index].ImportReviews, db.ImportReview{Kind: "resolved"})
		return store.requests[index], nil
	}
	return db.DownloadRequestRow{}, pgx.ErrNoRows
}

func (store *fakeDownloadStore) WithdrawImportDecision(
	_ context.Context, requestID, decisionID uuid.UUID,
) (db.DownloadRequestRow, error) {
	if store.withdrawErr != nil {
		return db.DownloadRequestRow{}, store.withdrawErr
	}
	for index, row := range store.requests {
		if row.ID != requestID {
			continue
		}
		for at, decision := range row.ImportDecisions {
			if decision.ID != decisionID {
				continue
			}
			store.requests[index].ImportDecisions = append(
				row.ImportDecisions[:at], row.ImportDecisions[at+1:]...)
			return store.requests[index], nil
		}
	}
	return db.DownloadRequestRow{}, pgx.ErrNoRows
}

func downloadStore() *fakeDownloadStore {
	return &fakeDownloadStore{fakeSettingsStore: &fakeSettingsStore{
		fakeStore:  &fakeStore{album: db.GetAlbumRow{ArtistName: "Portishead", Title: "Dummy", TrackCount: 11}},
		configured: true, settings: configuredSettings(),
	}}
}

const requestBody = `{
	"provider": "fake",
	"username": "peer",
	"directory": "Music/Dummy",
	"format": "FLAC",
	"score": 0.93,
	"reasons": ["Lossless FLAC", "All 11 catalogue tracks present"],
	"files": [
		{"path": "@@peer\\Music\\Dummy\\01 Mysterons.flac", "name": "01 Mysterons.flac",
		 "extension": ".FLAC", "sizeBytes": 30000000, "durationSeconds": 305},
		{"path": "@@peer\\Music\\Dummy\\02 Sour Times.flac", "name": "02 Sour Times.flac",
		 "extension": "flac", "sizeBytes": 25000000, "durationSeconds": 254}
	]
}`

func TestRequestAlbumDownloadRecordsProvenanceWithoutStartingATransfer(t *testing.T) {
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	var payload downloadRequestResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "requested" || payload.Username != "peer" || payload.Directory != "Music/Dummy" {
		t.Fatalf("payload = %#v", payload)
	}
	// The catalogue supplies the target, and the totals are derived from the
	// stored file list rather than trusted from the client.
	if payload.ExpectedTrackCount != 11 || payload.FileCount != 2 || payload.TotalSizeBytes != 55000000 {
		t.Fatalf("provenance = %#v", payload)
	}
	if payload.Format != "flac" || payload.Files[0].Extension != "flac" {
		t.Fatalf("format normalization failed: %#v", payload)
	}
	if len(store.created) != 1 || store.created[0].AlbumID.String() != albumUUID {
		t.Fatalf("created = %#v", store.created)
	}
}

func TestRequestAlbumDownloadDoesNotDuplicateAnOpenRequest(t *testing.T) {
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))

	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d then %d, want 201 then 200", first.Code, second.Code)
	}
	if len(store.requests) != 1 {
		t.Fatalf("stored %d requests, want the open one reused", len(store.requests))
	}
}

func TestRequestAlbumDownloadValidatesTheChosenSource(t *testing.T) {
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	for name, body := range map[string]string{
		"no files":         `{"provider":"fake","username":"peer","files":[]}`,
		"no username":      `{"provider":"fake","username":"","files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"unknown provider": `{"provider":"other","username":"peer","files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"impossible score": `{"provider":"fake","username":"peer","score":4,"files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"unnamed file":     `{"provider":"fake","username":"peer","files":[{"path":"p/a.flac","name":"  ","sizeBytes":1}]}`,
		"negative size":    `{"provider":"fake","username":"peer","files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":-1}]}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d; body = %s", name, response.Code, response.Body)
		}
	}
	if len(store.requests) != 0 {
		t.Fatalf("stored %d requests, want none", len(store.requests))
	}
}

func TestRequestAlbumDownloadRequiresAConfiguredProvider(t *testing.T) {
	store := downloadStore()
	store.configured = false
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestListAlbumDownloadsFiltersByRelease(t *testing.T) {
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/downloads?status=requested", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !store.listed.AlbumID.Valid || store.listed.AlbumID.UUID.String() != albumUUID {
		t.Fatalf("album filter = %#v", store.listed.AlbumID)
	}
	if store.listed.Status != "requested" {
		t.Fatalf("status filter = %q", store.listed.Status)
	}

	all := httptest.NewRecorder()
	handler.ServeHTTP(all, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if all.Code != http.StatusOK || store.listed.AlbumID.Valid {
		t.Fatalf("status = %d; album filter = %#v", all.Code, store.listed.AlbumID)
	}

	// Every state the table allows can be filtered by, and nothing else.
	for _, status := range []string{"requested", "started", "completed", "failed", "cancelled"} {
		accepted := httptest.NewRecorder()
		handler.ServeHTTP(accepted, httptest.NewRequest(
			http.MethodGet, "/api/v1/downloads?status="+status, nil))
		if accepted.Code != http.StatusOK {
			t.Errorf("status filter %q = %d; body = %s", status, accepted.Code, accepted.Body)
		}
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/downloads?status=downloading", nil))
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown status filter = %d", invalid.Code)
	}
}

func TestCancelDownloadWithdrawsAnOpenRequest(t *testing.T) {
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	var request downloadRequestResponse
	if err := json.NewDecoder(created.Body).Decode(&request); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/downloads/"+request.ID.String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var cancelled downloadRequestResponse
	if err := json.NewDecoder(response.Body).Decode(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" || cancelled.CancelledAt == nil {
		t.Fatalf("cancelled = %#v", cancelled)
	}

	// Cancelling again is not an open request any more.
	repeat := httptest.NewRecorder()
	handler.ServeHTTP(repeat, httptest.NewRequest(
		http.MethodDelete, "/api/v1/downloads/"+request.ID.String(), nil))
	if repeat.Code != http.StatusNotFound {
		t.Fatalf("second cancel status = %d, want 404", repeat.Code)
	}

	// The withdrawn request stays visible, so the decision is not lost.
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodDelete, "/api/v1/downloads/"+uuid.New().String(), nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown request status = %d, want 404", unknown.Code)
	}
}

func TestDownloadEndpointsReportWhenTheStoreCannotServeThem(t *testing.T) {
	// A store without the download queries must not look like an empty list.
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// fakeTransferController stands in for the component that talks to the provider.
type fakeTransferController struct {
	started     uuid.UUID
	retried     uuid.UUID
	retriedWith []string
	cancelled   uuid.UUID
	row         db.DownloadRequestRow
	startErr    error
	retryErr    error
	cancelErr   error
	// acknowledged records the source-change answer the handler passed on, and
	// offer is what a re-check reports.
	acknowledged bool
	offer        downloads.SourceOffer
	offerErr     error
}

func (controller *fakeTransferController) Start(
	_ context.Context, id uuid.UUID, acknowledgeSourceChange bool,
) (db.DownloadRequestRow, error) {
	controller.started, controller.acknowledged = id, acknowledgeSourceChange
	return controller.row, controller.startErr
}

func (controller *fakeTransferController) CheckSource(
	_ context.Context, _ uuid.UUID,
) (downloads.SourceOffer, error) {
	return controller.offer, controller.offerErr
}

func (controller *fakeTransferController) Retry(_ context.Context, id uuid.UUID, paths []string) (db.DownloadRequestRow, error) {
	controller.retried, controller.retriedWith = id, paths
	return controller.row, controller.retryErr
}

func (controller *fakeTransferController) Cancel(_ context.Context, id uuid.UUID) (db.DownloadRequestRow, error) {
	controller.cancelled = id
	return controller.row, controller.cancelErr
}

func TestStartDownloadHandsTheRequestToTheTransferController(t *testing.T) {
	requestID := uuid.New()
	controller := &fakeTransferController{row: db.DownloadRequestRow{
		ID: requestID, Status: "started", StartedAt: pgtype.Timestamptz{Valid: true},
		Files: []db.DownloadRequestFile{{Path: "p/01.flac", Name: "01.flac"}},
	}}
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/start", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if controller.started != requestID {
		t.Fatalf("started = %s, want %s", controller.started, requestID)
	}

	var payload downloadRequestResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "started" || payload.StartedAt == nil || payload.Startable {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestRetryDownloadPassesTheNamedFilesToTheTransferController(t *testing.T) {
	requestID := uuid.New()
	controller := &fakeTransferController{row: db.DownloadRequestRow{
		ID: requestID, Status: "started",
		Files: []db.DownloadRequestFile{{Path: `p\02.flac`, Name: "02.flac"}},
	}}
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/retry",
		strings.NewReader(`{"paths":["p\\02.flac"]}`)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if controller.retried != requestID {
		t.Fatalf("retried = %s, want %s", controller.retried, requestID)
	}
	// The remote path travels verbatim, backslashes and all: it is the only
	// name the peer will serve.
	if len(controller.retriedWith) != 1 || controller.retriedWith[0] != `p\02.flac` {
		t.Fatalf("retried with %#v", controller.retriedWith)
	}
}

// Retrying without a body is retrying everything that failed, so it must not be
// mistaken for a malformed request.
func TestRetryDownloadWithoutABodyRetriesEveryFailedFile(t *testing.T) {
	requestID := uuid.New()
	controller := &fakeTransferController{row: db.DownloadRequestRow{ID: requestID, Status: "started"}}
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/retry", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(controller.retriedWith) != 0 {
		t.Fatalf("retried with %#v, want every failed file", controller.retriedWith)
	}
}

func TestRetryDownloadReportsWhyItCouldNotRetry(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want int
	}{
		"request never failed":  {err: pgx.ErrNoRows, want: http.StatusConflict},
		"nothing failed":        {err: db.ErrNothingToRetry, want: http.StatusConflict},
		"named file is not one": {err: db.ErrNotRetryable, want: http.StatusUnprocessableEntity},
		"no remote paths":       {err: db.ErrNoRemotePath, want: http.StatusUnprocessableEntity},
		"no provider":           {err: sources.ErrNotConfigured, want: http.StatusServiceUnavailable},
		"search-only provider":  {err: downloads.ErrNotSupported, want: http.StatusServiceUnavailable},
		"provider refused":      {err: errors.New("peer is offline"), want: http.StatusBadGateway},
	} {
		controller := &fakeTransferController{retryErr: testCase.err}
		handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
			WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/downloads/"+uuid.New().String()+"/retry", nil))
		if response.Code != testCase.want {
			t.Fatalf("%s: status = %d, want %d; body = %s", name, response.Code, testCase.want, response.Body)
		}
	}
}

func TestRetryDownloadRefusesWithoutATransferController(t *testing.T) {
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+uuid.New().String()+"/retry", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A file is only offered for retry once the request has settled. While it is
// still moving, the poller may yet have something else to say about it.
func TestOnlyASettledRequestOffersItsFailedFilesForRetry(t *testing.T) {
	files := []db.DownloadRequestFile{
		{Path: `p\01.flac`, Name: "01.flac"}, {Path: `p\02.flac`, Name: "02.flac"},
	}
	transfers := []db.FileTransfer{
		{Path: `p\01.flac`, Status: "completed"},
		{Path: `p\02.flac`, Status: "failed", Error: "peer is offline"},
	}

	moving := downloadRequestResponseFrom(db.DownloadRequestRow{
		Status: "started", Files: files, Transfers: transfers,
	})
	if moving.RetryableCount != 0 {
		t.Fatalf("retryableCount = %d while transfers are still moving", moving.RetryableCount)
	}

	settled := downloadRequestResponseFrom(db.DownloadRequestRow{
		Status: "failed", Files: files, Transfers: transfers,
	})
	if settled.RetryableCount != 1 {
		t.Fatalf("retryableCount = %d, want the one file that failed", settled.RetryableCount)
	}
	if settled.Files[0].Transfer.Retryable {
		t.Fatal("a file that arrived is offered for retry")
	}
	if !settled.Files[1].Transfer.Retryable || settled.Files[1].Transfer.Error != "peer is offline" {
		t.Fatalf("failed file = %#v", settled.Files[1].Transfer)
	}
}

// A copy fetched for a want is the loop's to ask for again, from this peer once
// it is free or from the next one. Offering the user a button whose only answer
// is what was going to happen anyway is how one busy peer became a screen of
// rows nobody could do anything with.
func TestACopyFetchedForAWantIsNotOfferedForRetry(t *testing.T) {
	response := downloadRequestResponseFrom(db.DownloadRequestRow{
		Status:              "failed",
		AcquisitionTargetID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		Files:               []db.DownloadRequestFile{{Path: `p\01.flac`, Name: "01.flac"}},
		Transfers: []db.FileTransfer{
			{Path: `p\01.flac`, Status: "failed", Error: "Rejected: Too many files"},
		},
	})

	if response.RetryableCount != 0 {
		t.Fatalf("retryableCount = %d, want the loop's own copy left to the loop",
			response.RetryableCount)
	}
	if response.Files[0].Transfer.Retryable {
		t.Fatal("a want's copy is offered for retry file by file")
	}
	// What failed still has to be readable. Nothing is being hidden here except
	// a button that answered nothing.
	if response.Files[0].Transfer.Error != "Rejected: Too many files" {
		t.Fatalf("transfer = %#v, want the reason kept", response.Files[0].Transfer)
	}
}

// A release somebody chose by hand has nobody else to ask, so retrying it is
// still their only recourse and stays where it is.
func TestAReleaseChosenByHandKeepsItsRetry(t *testing.T) {
	response := downloadRequestResponseFrom(db.DownloadRequestRow{
		Status:    "failed",
		AlbumID:   uuid.NullUUID{UUID: uuid.New(), Valid: true},
		Files:     []db.DownloadRequestFile{{Path: `p\01.flac`, Name: "01.flac"}},
		Transfers: []db.FileTransfer{{Path: `p\01.flac`, Status: "failed"}},
	})

	if response.RetryableCount != 1 {
		t.Fatalf("retryableCount = %d, want the one file that failed", response.RetryableCount)
	}
}

// A file's attempt history reaches the client, because the current state alone
// cannot say that a file which arrived took four tries to do it.
func TestAFileReportsWhatItsEarlierAttemptsCameTo(t *testing.T) {
	response := downloadRequestResponseFrom(db.DownloadRequestRow{
		Status: "completed",
		Files:  []db.DownloadRequestFile{{Path: `p\01.flac`, Name: "01.flac"}},
		Transfers: []db.FileTransfer{{
			Path: `p\01.flac`, Status: "completed", Attempt: 2,
			Attempts: []db.TransferAttempt{
				{Attempt: 1, Outcome: "failed", Detail: "Completed, TimedOut"},
				{Attempt: 2, Outcome: "completed", TransferredBytes: 100},
			},
		}},
	})

	transfer := response.Files[0].Transfer
	if transfer.Attempt != 2 {
		t.Fatalf("attempt = %d, want the try it is on", transfer.Attempt)
	}
	if len(transfer.Attempts) != 2 || transfer.Attempts[0].Detail != "Completed, TimedOut" {
		t.Fatalf("attempts = %#v, want both tries with the provider's own words", transfer.Attempts)
	}
}

// A file that has no history must still serialise as an empty list rather than
// null, so the client can read it without a guard.
func TestAFileWithoutAHistoryReportsAnEmptyOne(t *testing.T) {
	response := downloadRequestResponseFrom(db.DownloadRequestRow{
		Status:    "started",
		Files:     []db.DownloadRequestFile{{Path: `p\01.flac`, Name: "01.flac"}},
		Transfers: []db.FileTransfer{{Path: `p\01.flac`, Status: "downloading", Attempt: 1}},
	})

	if attempts := response.Files[0].Transfer.Attempts; attempts == nil || len(attempts) != 0 {
		t.Fatalf("attempts = %#v, want an empty list", attempts)
	}
}

// A source that changed is a conflict carrying its evidence, so the user can
// see what is gone before deciding to take what is left.
func TestStartDownloadReportsASourceThatChangedSinceItWasRecorded(t *testing.T) {
	controller := &fakeTransferController{
		startErr: downloads.SourceChangedError{Offer: downloads.SourceOffer{
			Found: true, Missing: []string{"02.flac"},
		}},
	}
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+uuid.New().String()+"/start", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want a conflict; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "02.flac") {
		t.Fatalf("body = %s, want the file that is gone named", response.Body)
	}
}

// The acknowledgement reaches the controller, which is the only thing that lets
// a start past the re-check.
func TestStartDownloadPassesOnAnAcknowledgedSourceChange(t *testing.T) {
	controller := &fakeTransferController{row: db.DownloadRequestRow{Status: "started"}}
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+uuid.New().String()+"/start",
		strings.NewReader(`{"acknowledgeSourceChange": true}`)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !controller.acknowledged {
		t.Fatal("the acknowledgement never reached the controller")
	}
}

// Without a controller the API must say transfers are unavailable rather than
// silently reporting success.
func TestStartDownloadRefusesWithoutATransferController(t *testing.T) {
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+uuid.New().String()+"/start", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestStartDownloadReportsWhyItCouldNotStart(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want int
	}{
		"nothing waiting to start": {err: pgx.ErrNoRows, want: http.StatusConflict},
		"no provider":              {err: sources.ErrNotConfigured, want: http.StatusServiceUnavailable},
		"search-only provider":     {err: downloads.ErrNotSupported, want: http.StatusServiceUnavailable},
		"no remote paths":          {err: db.ErrNoRemotePath, want: http.StatusUnprocessableEntity},
		"provider refused":         {err: errors.New("slskd rejected the API key"), want: http.StatusBadGateway},
	} {
		controller := &fakeTransferController{startErr: testCase.err}
		handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
			WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/downloads/"+uuid.New().String()+"/start", nil))
		if response.Code != testCase.want {
			t.Errorf("%s: status = %d, want %d; body = %s", name, response.Code, testCase.want, response.Body)
		}
	}
}

// Cancelling goes through the controller when there is one, so a started
// request is stopped at the provider rather than only in the database.
func TestCancelDownloadGoesThroughTheTransferController(t *testing.T) {
	requestID := uuid.New()
	controller := &fakeTransferController{row: db.DownloadRequestRow{
		ID: requestID, Status: "cancelled", CancelledAt: pgtype.Timestamptz{Valid: true},
	}}
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/downloads/"+requestID.String(), nil))
	if response.Code != http.StatusOK || controller.cancelled != requestID {
		t.Fatalf("status = %d, cancelled = %s", response.Code, controller.cancelled)
	}
}

func TestRevalidateSendsAPausedImportBackThroughValidation(t *testing.T) {
	store := downloadStore()
	paused := db.DownloadRequestRow{
		ID: uuid.New(), AlbumID: uuid.NullUUID{UUID: uuid.MustParse(albumUUID), Valid: true}, Status: "completed",
		ImportStatus: "needs_review",
		ImportError:  pgtype.Text{String: "track 3 tags do not identify the release", Valid: true},
		ImportReviews: []db.ImportReview{
			{Kind: "paused", Detail: "track 3 tags do not identify the release"},
		},
	}
	store.requests = append(store.requests, paused)
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+paused.ID.String()+"/revalidate", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var retried downloadRequestResponse
	if err := json.NewDecoder(response.Body).Decode(&retried); err != nil {
		t.Fatal(err)
	}
	// The import goes back to pending. It is not imported: validation decides.
	if retried.ImportStatus != "pending" || retried.ImportError != nil || retried.Revalidatable {
		t.Fatalf("retried = %#v", retried)
	}
	// The earlier reason survives the retry, so review stays auditable.
	if len(retried.ImportReviews) != 2 ||
		retried.ImportReviews[0].Detail != "track 3 tags do not identify the release" ||
		retried.ImportReviews[1].Kind != "revalidated" {
		t.Fatalf("history = %#v", retried.ImportReviews)
	}

	// Retrying an import that is no longer waiting for review is a conflict.
	repeat := httptest.NewRecorder()
	handler.ServeHTTP(repeat, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+paused.ID.String()+"/revalidate", nil))
	if repeat.Code != http.StatusConflict {
		t.Fatalf("second revalidate status = %d, want 409", repeat.Code)
	}
}

// Review needs the comparison, not just the sentence. The evidence of the
// latest pause is surfaced on the request itself; once the import moves on, it
// stays in history but no longer describes the current state.
func TestPausedImportsCarryTheEvidenceBehindTheirReason(t *testing.T) {
	store := downloadStore()
	evidence := &db.ImportEvidence{
		Files: []db.ImportFileEvidence{{
			Name: "03.flac", Position: "1-3",
			Observed: &db.ImportTags{Title: "Third (live)", TrackNumber: 3, DurationMS: 400_000},
			Expected: &db.ImportTags{Title: "Third", TrackNumber: 3, DurationMS: 200_000},
			Problems: []string{`"03.flac" duration differs from the catalogue by more than five seconds`},
		}},
		UnmatchedTracks: []db.ImportTags{},
		Problems:        []string{},
	}
	paused := db.DownloadRequestRow{
		ID: uuid.New(), Status: "completed", ImportStatus: "needs_review",
		ImportError: pgtype.Text{String: "track 3 is a different recording", Valid: true},
		ImportReviews: []db.ImportReview{
			{Kind: "paused", Detail: "an earlier attempt", Evidence: &db.ImportEvidence{}},
			{Kind: "revalidated"},
			{Kind: "paused", Detail: "track 3 is a different recording", Evidence: evidence},
		},
	}
	store.requests = append(store.requests, paused)
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	var payload struct {
		Items []downloadRequestResponse `json:"items"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %#v", payload.Items)
	}
	current := payload.Items[0].ImportEvidence
	if current == nil || len(current.Files) != 1 || current.Files[0].Observed == nil ||
		current.Files[0].Observed.DurationMS != 400_000 ||
		current.Files[0].Expected == nil || current.Files[0].Expected.DurationMS != 200_000 {
		t.Fatalf("current evidence = %#v", current)
	}
	// Every attempt keeps its own evidence in history.
	if payload.Items[0].ImportReviews[0].Evidence == nil ||
		payload.Items[0].ImportReviews[1].Evidence != nil {
		t.Fatalf("history = %#v", payload.Items[0].ImportReviews)
	}

	// An import that is no longer paused reports no current evidence.
	retried := httptest.NewRecorder()
	handler.ServeHTTP(retried, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+paused.ID.String()+"/revalidate", nil))
	var after downloadRequestResponse
	if err := json.NewDecoder(retried.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if after.ImportEvidence != nil {
		t.Fatalf("evidence after retry = %#v", after.ImportEvidence)
	}
}

// pausedWithEvidence builds a request waiting for review: one file whose
// identity is disputed and can be decided, one file that is simply damaged.
func pausedWithEvidence() (*fakeDownloadStore, db.DownloadRequestRow, uuid.UUID) {
	store := downloadStore()
	trackID := uuid.New()
	evidence := &db.ImportEvidence{
		Files: []db.ImportFileEvidence{
			{
				Name: "03.flac", Position: "1-3",
				Observed:   &db.ImportTags{Title: "Third (alternate)", TrackNumber: 3},
				Expected:   &db.ImportTags{TrackID: trackID.String(), Title: "Third", TrackNumber: 3},
				Problems:   []string{`"03.flac" tags do not identify the release`},
				Resolvable: true,
			},
			{
				Name:     "04.flac",
				Problems: []string{`"04.flac" is not in the provider folder`},
			},
		},
		UnmatchedTracks: []db.ImportTags{},
		Problems:        []string{},
	}
	paused := db.DownloadRequestRow{
		ID: uuid.New(), Status: "completed", ImportStatus: "needs_review",
		ImportError: pgtype.Text{String: "2 of 2 downloaded files do not match", Valid: true},
		ImportReviews: []db.ImportReview{
			{Kind: "paused", Detail: "2 of 2 downloaded files do not match", Evidence: evidence},
		},
	}
	store.requests = append(store.requests, paused)
	return store, paused, trackID
}

// Review has to be answerable, not only readable. A disputed identity can be
// decided for one track; the decision is stored against what the reviewer saw.
func TestResolvingATrackRecordsAJudgementAgainstTheObservedFile(t *testing.T) {
	store, paused, trackID := pausedWithEvidence()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+paused.ID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"03.flac","trackId":"`+trackID.String()+`","note":"alternate title, same recording"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.decided) != 1 || store.decided[0].FileName != "03.flac" ||
		store.decided[0].TrackID != trackID || store.decided[0].Note != "alternate title, same recording" {
		t.Fatalf("decided = %#v", store.decided)
	}
	// The fingerprint comes from the evidence, never from the request body, so
	// a decision cannot be recorded against something nobody looked at.
	want := (&db.ImportTags{Title: "Third (alternate)", TrackNumber: 3}).Fingerprint()
	if store.decided[0].Fingerprint != want {
		t.Fatalf("fingerprint = %q, want %q", store.decided[0].Fingerprint, want)
	}

	var payload downloadRequestResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	// Deciding imports nothing by itself: the request still waits for review.
	if payload.ImportStatus != "needs_review" || len(payload.ImportDecisions) != 1 {
		t.Fatalf("payload = %#v", payload)
	}
}

// Damage, absence, and unreadable audio are not judgement calls. Refusing them
// here is what keeps a decision from becoming a way to import anything.
func TestResolvingRefusesAFileThatIsNotAJudgementCall(t *testing.T) {
	store, paused, trackID := pausedWithEvidence()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+paused.ID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"04.flac","trackId":"`+trackID.String()+`"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}

	// A file that is not in the evidence at all cannot be decided either.
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+paused.ID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"99.flac","trackId":"`+trackID.String()+`"}`)))
	if unknown.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown file status = %d, want 422", unknown.Code)
	}
	if len(store.decided) != 0 {
		t.Fatalf("decided = %#v", store.decided)
	}
}

func TestResolvingRequiresAnImportWaitingForReview(t *testing.T) {
	store := downloadStore()
	imported := db.DownloadRequestRow{ID: uuid.New(), Status: "completed", ImportStatus: "imported"}
	store.requests = append(store.requests, imported)
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+imported.ID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"03.flac","trackId":"`+uuid.New().String()+`"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
}

func TestWithdrawingAResolutionRemovesIt(t *testing.T) {
	store, paused, trackID := pausedWithEvidence()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+paused.ID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"03.flac","trackId":"`+trackID.String()+`"}`)))
	var payload downloadRequestResponse
	if err := json.NewDecoder(recorded.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/downloads/"+paused.ID.String()+"/resolutions/"+payload.ImportDecisions[0].ID.String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var after downloadRequestResponse
	if err := json.NewDecoder(response.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if len(after.ImportDecisions) != 0 {
		t.Fatalf("decisions = %#v", after.ImportDecisions)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete,
		"/api/v1/downloads/"+paused.ID.String()+"/resolutions/"+uuid.New().String(), nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", missing.Code)
	}
}

func TestRevalidateIsOfferedOnlyWhileAnImportWaitsForReview(t *testing.T) {
	store := downloadStore()
	imported := db.DownloadRequestRow{ID: uuid.New(), Status: "completed", ImportStatus: "imported"}
	store.requests = append(store.requests, imported)
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	var payload struct {
		Items []downloadRequestResponse `json:"items"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Revalidatable {
		t.Fatalf("items = %#v", payload.Items)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+imported.ID.String()+"/revalidate", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
}

// heldLibrary is a library that already says something about the release under
// test: one owned track and one file that is plainly this release but was
// never resolved to a track.
func heldLibrary() db.DuplicateEvidence {
	return db.DuplicateEvidence{
		AlbumTitle: "Dummy", ArtistName: "Portishead",
		TrackCount: 11, OwnedTrackCount: 1, UnresolvedCount: 1,
		Summary: "The library already holds 1 of the 11 tracks of this release, " +
			"and 1 file that looks like this release without a proven track identity.",
		Files: []db.DuplicateFile{
			{
				ID: uuid.New(), Path: "/music/Portishead/Dummy/01 Mysterons.flac",
				State: "owned", MatchStatus: "matched", Track: "1. Mysterons",
				Evidence: "is already matched by MusicBrainz recording ID to 1. Mysterons.",
			},
			{
				ID: uuid.New(), Path: "/music/inbox/sour times.flac",
				State: "unresolved", MatchStatus: "unmatched", Tags: "Portishead — Dummy — Sour Times",
				Evidence: "is tagged as this release, but no catalogue track was proven for it.",
			},
		},
	}
}

// A download is an acquisition. When the library already holds part of the
// release, or holds a file that looks like it and was never resolved, nothing
// is recorded until someone answers for it.
func TestRequestAlbumDownloadRefusesUntilDuplicatesAreAnswered(t *testing.T) {
	store := downloadStore()
	store.duplicates = heldLibrary()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
	if len(store.requests) != 0 || len(store.created) != 0 {
		t.Fatalf("a refused request was recorded anyway: %#v", store.requests)
	}

	// The refusal carries the evidence, because the answer it asks for is only
	// as good as what the person answering was shown.
	var problem struct {
		Details    []string             `json:"details"`
		Duplicates db.DuplicateEvidence `json:"duplicates"`
	}
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if len(problem.Duplicates.Files) != 2 || problem.Details[0] != store.duplicates.Summary {
		t.Fatalf("problem = %#v", problem)
	}
	if problem.Duplicates.Files[0].Evidence == "" || problem.Duplicates.Files[1].Evidence == "" {
		t.Fatalf("evidence does not say why: %#v", problem.Duplicates.Files)
	}
}

// The answer is recorded with what was shown, so a request carries the reason
// it was allowed rather than only the fact that it was.
func TestAcknowledgingDuplicatesRecordsWhatWasShown(t *testing.T) {
	store := downloadStore()
	store.duplicates = heldLibrary()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	body := strings.TrimSuffix(strings.TrimSpace(requestBody), "}") + `, "acknowledgeDuplicates": true}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	if len(store.created) != 1 || store.created[0].DuplicateEvidence == nil {
		t.Fatalf("created = %#v", store.created)
	}
	// Stored from the library as it stood, not from the request body.
	if store.created[0].DuplicateEvidence.OwnedTrackCount != 1 ||
		len(store.created[0].DuplicateEvidence.Files) != 2 {
		t.Fatalf("evidence = %#v", store.created[0].DuplicateEvidence)
	}

	var payload downloadRequestResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.DuplicateAcknowledgedAt == nil || payload.DuplicateEvidence == nil {
		t.Fatalf("payload = %#v", payload)
	}
}

// A library with nothing to say about the release asks nothing.
func TestRequestAlbumDownloadAsksNothingWhenTheLibraryHoldsNothing(t *testing.T) {
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.created[0].DuplicateEvidence != nil {
		t.Fatalf("evidence recorded for a library that holds nothing: %#v", store.created[0].DuplicateEvidence)
	}
}

// The library moves between recording a request and starting it, so the check
// runs again at the point the transfer would actually happen. This is also what
// holds a request recorded before duplicate protection existed to the same rule.
func TestStartDownloadRefusesUnansweredDuplicates(t *testing.T) {
	store := downloadStore()
	controller := &fakeTransferController{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if created.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", created.Code, created.Body)
	}
	requestID := store.requests[0].ID
	controller.row = store.requests[0]

	// The release arrived in the library after the request was recorded.
	store.duplicates = heldLibrary()

	refused := httptest.NewRecorder()
	handler.ServeHTTP(refused, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/start", nil))
	if refused.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", refused.Code, refused.Body)
	}
	if controller.started != uuid.Nil {
		t.Fatalf("a refused transfer was started anyway: %s", controller.started)
	}

	answered := httptest.NewRecorder()
	handler.ServeHTTP(answered, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/start",
		strings.NewReader(`{"acknowledgeDuplicates": true}`)))
	if answered.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", answered.Code, answered.Body)
	}
	if controller.started != requestID {
		t.Fatalf("started = %s, want %s", controller.started, requestID)
	}
	if !store.requests[0].DuplicateAcknowledgedAt.Valid || store.requests[0].DuplicateEvidence == nil {
		t.Fatalf("the answer was not recorded: %#v", store.requests[0])
	}
}

// A question already answered is not asked again. Re-asking because a scan has
// run since would teach the user to click through the one refusal that matters.
func TestStartDownloadDoesNotReaskAnAnsweredRequest(t *testing.T) {
	store := downloadStore()
	store.duplicates = heldLibrary()
	controller := &fakeTransferController{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(controller))

	body := strings.TrimSuffix(strings.TrimSpace(requestBody), "}") + `, "acknowledgeDuplicates": true}`
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(body)))
	if created.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", created.Code, created.Body)
	}
	requestID := store.requests[0].ID
	controller.row = store.requests[0]

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/start", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestAlbumDuplicatesAreReadableBeforeRequesting(t *testing.T) {
	store := downloadStore()
	store.duplicates = heldLibrary()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumUUID+"/duplicates", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload db.DuplicateEvidence
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.OwnedTrackCount != 1 || payload.UnresolvedCount != 1 || len(payload.Files) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.AlbumID.String() != albumUUID {
		t.Fatalf("album = %s, want %s", payload.AlbumID, albumUUID)
	}
}

// downloadHandler is the API with a download store, a provider and, when one is
// given, the component that talks to the peer.
func downloadHandler(store *fakeDownloadStore, options ...Option) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		append([]Option{WithSourceProvider(&fakeProvider{})}, options...)...)
}

// An id that is not a UUID names no release and no request, and the routes say
// so without asking the store about it.
func TestAMalformedIDNamesNoDownload(t *testing.T) {
	handler := downloadHandler(downloadStore(), WithTransferController(&fakeTransferController{}))
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/albums/not-a-uuid/downloads", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/not-a-uuid/downloads", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/not-a-uuid/duplicates", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/downloads/not-a-uuid/source", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/not-a-uuid/start", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/not-a-uuid/retry", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/not-a-uuid/revalidate", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/not-a-uuid/resolutions", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/downloads/not-a-uuid", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", request.Method, request.URL.Path, response.Code)
		}
	}
}

// Without the download queries the routes say so, rather than looking like a
// release nobody has ever asked for anything about.
func TestEveryDownloadRouteReportsBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	requestID := uuid.NewString()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+albumUUID+"/downloads", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+albumUUID+"/duplicates", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/downloads/"+requestID+"/source", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+requestID+"/start", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+requestID+"/retry", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+requestID+"/revalidate", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+requestID+"/resolutions", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/downloads/"+requestID+"/resolutions/"+uuid.NewString(), nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/downloads/"+requestID, nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestRequestingADownloadRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := downloadHandler(downloadStore())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// The release is what the request is provenance for, so one that is not there
// is not a request to record.
func TestRequestingADownloadForAReleaseThatIsNotThereIsNotFound(t *testing.T) {
	store := downloadStore()
	store.fakeStore.err = pgx.ErrNoRows
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRequestingADownloadReportsAStoreThatFailed(t *testing.T) {
	store := downloadStore()
	store.fakeStore.err = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// What the library already holds has to be known before anything is recorded,
// so a lookup that failed stops the request rather than skipping the question.
func TestRequestingADownloadStopsWhenTheDuplicateCheckCannotBeRun(t *testing.T) {
	store := downloadStore()
	store.evidenceErr = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if len(store.created) != 0 {
		t.Fatalf("created = %#v, want nothing recorded", store.created)
	}
}

func TestRecordingADownloadRequestReportsAStoreThatFailed(t *testing.T) {
	store := downloadStore()
	store.createErr = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A request that names no provider is for the one that is configured: there is
// only ever one, and asking the caller to repeat it invites naming another.
func TestARequestWithoutAProviderTakesTheConfiguredOne(t *testing.T) {
	store := downloadStore()
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(
			`{"username":"peer","directory":"Music/Dummy","averageBitRate":1000,`+
				`"files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.created) != 1 || store.created[0].Provider != "fake" {
		t.Fatalf("created = %#v, want the configured provider", store.created)
	}
	if !store.created[0].AverageBitRate.Valid || store.created[0].AverageBitRate.Int32 != 1000 {
		t.Fatalf("bit rate = %#v, want the reported one kept", store.created[0].AverageBitRate)
	}
}

// Every bound on a recorded source is a bound on what one request can put in
// the database, and what is outside it is refused rather than truncated.
func TestARequestIsRefusedWhenItsSourceIsOutsideWhatCanBeRecorded(t *testing.T) {
	handler := downloadHandler(downloadStore())
	longPath := strings.Repeat("p", 4001)
	reasons := make([]string, 21)
	for index := range reasons {
		reasons[index] = "a reason"
	}
	longReasons, _ := json.Marshal(reasons)
	files := make([]map[string]any, 501)
	for index := range files {
		files[index] = map[string]any{"path": "p/a.flac", "name": "a.flac", "sizeBytes": 1}
	}
	manyFiles, _ := json.Marshal(files)

	for name, body := range map[string]string{
		"long username":  `{"username":"` + strings.Repeat("u", 201) + `","files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"long directory": `{"username":"peer","directory":"` + longPath + `","files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"negative bitrate": `{"username":"peer","averageBitRate":-1,` +
			`"files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"too many reasons": `{"username":"peer","reasons":` + string(longReasons) +
			`,"files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"long reason": `{"username":"peer","reasons":["` + strings.Repeat("r", 201) +
			`"],"files":[{"path":"p/a.flac","name":"a.flac","sizeBytes":1}]}`,
		"too many files": `{"username":"peer","files":` + string(manyFiles) + `}`,
		"unnamed path":   `{"username":"peer","files":[{"path":"   ","name":"a.flac","sizeBytes":1}]}`,
		"long path":      `{"username":"peer","files":[{"path":"` + longPath + `","name":"a.flac","sizeBytes":1}]}`,
		"long file name": `{"username":"peer","files":[{"path":"p/a.flac","name":"` +
			strings.Repeat("n", 1001) + `","sizeBytes":1}]}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
			"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422; body = %s", name, response.Code, response.Body)
		}
	}
}

// The downloads list is paged and filtered by a closed set of states; what is
// outside either is refused rather than answered with a different list.
func TestListingDownloadsRefusesFiltersOutsideWhatItAnswers(t *testing.T) {
	handler := downloadHandler(downloadStore())
	for _, query := range []string{
		"?status=probably", "?view=probably", "?limit=0", "?limit=201", "?limit=many",
		"?offset=-1", "?offset=soon",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/downloads"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", query, response.Code)
		}
	}
}

func TestListingDownloadsAnswersWithThePageThatWasAskedFor(t *testing.T) {
	store := downloadStore()
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/downloads?limit=5&offset=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.listed.Limit != 5 || store.listed.Offset != 10 {
		t.Fatalf("paging = %#v", store.listed)
	}
}

// The Downloads screen shows one pile of the list at a time — the transfers
// still under way, the copies waiting on a person, what was imported, what was
// not used, what failed — and it says which pile it wants.
func TestListingDownloadsAsksTheStoreForThePileThatWasNamed(t *testing.T) {
	store := downloadStore()
	handler := downloadHandler(store)

	for _, view := range []string{"open", "review", "imported", "discarded", "failed", "all"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/downloads?view="+view, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("view %q: status = %d; body = %s", view, response.Code, response.Body)
		}
		if store.listed.View != view {
			t.Errorf("view %q: asked the store for %q", view, store.listed.View)
		}
		if store.listed.Limit != 0 {
			t.Errorf("view %q: limit = %d, want omitted", view, store.listed.Limit)
		}
	}
}

// How many requests each pile holds travels with the page, so the figure on a
// filter and the rows under it are one answer rather than two reads that can
// disagree.
func TestListingDownloadsCarriesWhatEachPileHolds(t *testing.T) {
	store := downloadStore()
	store.counts = db.DownloadRequestCounts{
		Open: 20, Review: 3, Imported: 620, Discarded: 40, Failed: 288, All: 971,
	}
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/downloads?view=open", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var body struct {
		Counts db.DownloadRequestCounts `json:"counts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Counts != store.counts {
		t.Fatalf("counts = %#v", body.Counts)
	}
}

func TestListingDownloadsReportsAStoreThatFailed(t *testing.T) {
	store := downloadStore()
	store.listErr = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestReadingDuplicatesForAReleaseThatIsNotThereIsNotFound(t *testing.T) {
	store := downloadStore()
	store.evidenceErr = pgx.ErrNoRows
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/duplicates", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestReadingDuplicatesReportsAStoreThatFailed(t *testing.T) {
	store := downloadStore()
	store.evidenceErr = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+albumUUID+"/duplicates", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The re-check says what the peer offers now, so the interface can report a
// source as gone stale before anybody presses start.
func TestCheckingTheSourceReportsWhatThePeerStillOffers(t *testing.T) {
	controller := &fakeTransferController{offer: downloads.SourceOffer{
		Found: true, Missing: []string{"02.flac"},
	}}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/downloads/"+uuid.NewString()+"/source", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload sourceOfferResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Offered || !payload.Stale || len(payload.Missing) != 1 {
		t.Fatalf("payload = %#v", payload)
	}
}

// A peer offering exactly what was recorded is not stale, and the empty lists
// are sent as lists rather than as null.
func TestAnUnchangedSourceReportsNothingMissingOrChanged(t *testing.T) {
	controller := &fakeTransferController{offer: downloads.SourceOffer{Found: true}}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/downloads/"+uuid.NewString()+"/source", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "null") {
		t.Fatalf("body = %s, want empty lists rather than null", response.Body)
	}
}

func TestCheckingTheSourceIsUnavailableWithoutATransferController(t *testing.T) {
	handler := downloadHandler(downloadStore())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/downloads/"+uuid.NewString()+"/source", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestCheckingTheSourceReportsWhyItCouldNotAsk(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want int
	}{
		"no recorded request":  {err: pgx.ErrNoRows, want: http.StatusNotFound},
		"no provider":          {err: sources.ErrNotConfigured, want: http.StatusServiceUnavailable},
		"search-only provider": {err: downloads.ErrNotSupported, want: http.StatusServiceUnavailable},
		"peer refused":         {err: errors.New("peer is offline"), want: http.StatusBadGateway},
	} {
		controller := &fakeTransferController{offerErr: testCase.err}
		handler := downloadHandler(downloadStore(), WithTransferController(controller))

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
			"/api/v1/downloads/"+uuid.NewString()+"/source", nil))
		if response.Code != testCase.want {
			t.Errorf("%s: status = %d, want %d", name, response.Code, testCase.want)
		}
	}
}

// Nobody is left to read the answer once the client has gone.
func TestACancelledSourceCheckIsNotAnswered(t *testing.T) {
	controller := &fakeTransferController{offerErr: context.Canceled}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/downloads/"+uuid.NewString()+"/source", nil))
	if response.Body.Len() != 0 {
		t.Fatalf("body = %s, want nothing written", response.Body)
	}
}

func TestStartingADownloadRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := downloadHandler(downloadStore(), WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/start", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestACancelledStartIsNotAnswered(t *testing.T) {
	controller := &fakeTransferController{startErr: context.Canceled}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/start", nil))
	if response.Body.Len() != 0 {
		t.Fatalf("body = %s, want nothing written", response.Body)
	}
}

func TestRetryingADownloadRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := downloadHandler(downloadStore(), WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/retry", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestRetryingRefusesMoreFilesThanARequestCanHold(t *testing.T) {
	paths := make([]string, 501)
	for index := range paths {
		paths[index] = "p/a.flac"
	}
	body, _ := json.Marshal(map[string]any{"paths": paths})
	handler := downloadHandler(downloadStore(), WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/retry", strings.NewReader(string(body))))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

// A wanted recording is pursued one copy at a time. Retrying a request the loop
// has already moved past would collide with the copy that replaced it.
func TestRetryingAWantWhoseLoopHasMovedOnIsAConflict(t *testing.T) {
	controller := &fakeTransferController{retryErr: db.ErrWantHasAnotherCopy}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/retry", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestACancelledRetryIsNotAnswered(t *testing.T) {
	controller := &fakeTransferController{retryErr: context.Canceled}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/retry", nil))
	if response.Body.Len() != 0 {
		t.Fatalf("body = %s, want nothing written", response.Body)
	}
}

// The peer is still offering the folder but not the files that were recorded
// for it, and the difference is put to the user rather than decided for them.
func TestStartingIsRefusedWhenTheSourceChangedSize(t *testing.T) {
	requestID := uuid.New()
	controller := &fakeTransferController{startErr: downloads.SourceChangedError{
		Offer: downloads.SourceOffer{Found: true, Changed: []string{"01.flac"}},
	}}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/start", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "different size") {
		t.Fatalf("body = %s, want the changed files named", response.Body)
	}
}

// A request nobody recorded has nothing to protect, so the duplicate check
// stands aside and starting says why it cannot start.
func TestStartingARequestNobodyRecordedIsLeftToTheController(t *testing.T) {
	controller := &fakeTransferController{startErr: pgx.ErrNoRows}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/start", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

// A request serving a want has no release to ask about: what the library holds
// for that recording was settled before the want existed.
func TestStartingARequestWithNoReleaseAsksNoDuplicateQuestion(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.duplicates = heldLibrary()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "requested",
	})
	controller := &fakeTransferController{row: db.DownloadRequestRow{ID: requestID, Status: "started"}}
	handler := downloadHandler(store, WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/start", nil))
	if response.Code != http.StatusAccepted || controller.started != requestID {
		t.Fatalf("status = %d started = %s; body = %s", response.Code, controller.started, response.Body)
	}
}

func TestStartingStopsWhenTheDuplicateCheckCannotBeRun(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "requested",
		AlbumID: uuid.NullUUID{UUID: uuid.MustParse(albumUUID), Valid: true},
	})
	store.evidenceErr = errors.New("the database is unreachable")
	handler := downloadHandler(store, WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/start", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The answer is recorded with what was shown, so a request that stopped waiting
// to start between the question and the answer has nothing to record it against.
func TestAcknowledgingDuplicatesForARequestThatIsNoLongerWaitingIsAConflict(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.duplicates = heldLibrary()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "requested",
		AlbumID: uuid.NullUUID{UUID: uuid.MustParse(albumUUID), Valid: true},
	})
	store.acknowledgeErr = pgx.ErrNoRows
	handler := downloadHandler(store, WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/start",
		strings.NewReader(`{"acknowledgeDuplicates":true}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestAcknowledgingDuplicatesReportsAStoreThatFailed(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.duplicates = heldLibrary()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "requested",
		AlbumID: uuid.NullUUID{UUID: uuid.MustParse(albumUUID), Valid: true},
	})
	store.acknowledgeErr = errors.New("the database is unreachable")
	handler := downloadHandler(store, WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/start",
		strings.NewReader(`{"acknowledgeDuplicates":true}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A provider that refused the cancellation leaves the transfer where it was,
// and saying so is better than reporting a stop that did not happen.
func TestCancellingReportsAProviderThatRefused(t *testing.T) {
	controller := &fakeTransferController{cancelErr: errors.New("slskd did not respond")}
	handler := downloadHandler(downloadStore(), WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/downloads/"+uuid.NewString(), nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.Code)
	}
}

func TestRevalidatingReportsAStoreThatFailed(t *testing.T) {
	store := downloadStore()
	store.revalidateErr = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/revalidate", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestResolvingATrackRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := downloadHandler(downloadStore())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/resolutions", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// A judgement has to name the file it is about and the track it is to, and the
// note beside it is bounded like every other free-text field.
func TestAJudgementIsRefusedWhenItNamesNothingUsable(t *testing.T) {
	handler := downloadHandler(downloadStore())
	for name, body := range map[string]string{
		"no file name": `{"trackId":"` + uuid.NewString() + `"}`,
		"no track":     `{"fileName":"01.flac"}`,
		"long note": `{"fileName":"01.flac","trackId":"` + uuid.NewString() +
			`","note":"` + strings.Repeat("n", 201) + `"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
			"/api/v1/downloads/"+uuid.NewString()+"/resolutions", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422; body = %s", name, response.Code, response.Body)
		}
	}
}

func TestResolvingATrackOnARequestNobodyRecordedIsNotFound(t *testing.T) {
	handler := downloadHandler(downloadStore())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/resolutions",
		strings.NewReader(`{"fileName":"01.flac","trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRecordingAJudgementReportsAStoreThatFailed(t *testing.T) {
	store := downloadStore()
	requestID := pausedImportWithAResolvableFile(store)
	store.decisionErr = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"01.flac","trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A judgement is only ever about a track of the release the request was made
// for; anything else would file one release's music under another.
func TestAJudgementToATrackOfAnotherReleaseIsRefused(t *testing.T) {
	store := downloadStore()
	requestID := pausedImportWithAResolvableFile(store)
	store.decisionErr = db.ErrTrackNotInRelease
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"01.flac","trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

// The import stopped waiting for review between the page and the press, so
// there is no question left for the judgement to answer.
func TestAJudgementOnAnImportThatStoppedWaitingIsAConflict(t *testing.T) {
	store := downloadStore()
	requestID := pausedImportWithAResolvableFile(store)
	store.decisionErr = pgx.ErrNoRows
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"01.flac","trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

// pausedImportWithAResolvableFile records one request whose import is waiting
// for review over a file a person may decide about.
func pausedImportWithAResolvableFile(store *fakeDownloadStore) uuid.UUID {
	requestID := uuid.New()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "completed", ImportStatus: "needs_review",
		ImportReviews: []db.ImportReview{{
			Kind: "paused",
			Evidence: &db.ImportEvidence{Files: []db.ImportFileEvidence{{
				Name: "01.flac", Resolvable: true,
				Observed: &db.ImportTags{Title: "Mysterons"},
			}}},
		}},
	})
	return requestID
}

// A decision id that is not a UUID names no resolution.
func TestAMalformedDecisionIDNamesNoResolution(t *testing.T) {
	handler := downloadHandler(downloadStore())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/downloads/"+uuid.NewString()+"/resolutions/not-a-uuid", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestWithdrawingAResolutionReportsAStoreThatFailed(t *testing.T) {
	store := downloadStore()
	store.withdrawErr = errors.New("the database is unreachable")
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/downloads/"+uuid.NewString()+"/resolutions/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A recorded file the peer gave no remote path for cannot be asked for, so the
// request is not offered as startable however complete it otherwise looks.
func TestARequestWithAPathlessFileIsNotOfferedAsStartable(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "requested",
		Files: []db.DownloadRequestFile{{Path: "  ", Name: "01.flac"}},
	})
	handler := downloadHandler(store, WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []downloadRequestResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Startable {
		t.Fatalf("items = %+v, want a request nothing can be asked for", payload.Items)
	}
}

// A provider that could not be built from the stored settings is not the same
// as one nobody configured, and neither records a request.
func TestRequestingADownloadReportsAProviderThatCouldNotBeBuilt(t *testing.T) {
	store := downloadStore()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{buildErr: errors.New("the base URL cannot be parsed")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if len(store.requests) != 0 {
		t.Fatalf("stored %d requests, want none", len(store.requests))
	}
}

// The recorded request is read back so the caller sees what was stored; a read
// that failed is reported rather than answered with an empty request.
func TestReadingBackARecordedRequestReportsAStoreThatFailed(t *testing.T) {
	store := &lookupFailingStore{fakeDownloadStore: downloadStore()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumUUID+"/downloads", strings.NewReader(requestBody)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// lookupFailingStore records requests normally and cannot read one back, which
// is the one ordering the read-back path exists for.
type lookupFailingStore struct {
	*fakeDownloadStore
}

func (store *lookupFailingStore) DownloadRequest(
	context.Context, uuid.UUID,
) (db.DownloadRequestRow, error) {
	return db.DownloadRequestRow{}, errors.New("the database is unreachable")
}

func TestStartingReportsARequestThatCouldNotBeRead(t *testing.T) {
	store := &lookupFailingStore{fakeDownloadStore: downloadStore()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithTransferController(&fakeTransferController{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/start", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A release the library holds nothing of has no duplicate question to ask, so
// starting goes straight through.
func TestStartingARequestForALibraryThatHoldsNothingAsksNothing(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "requested",
		AlbumID: uuid.NullUUID{UUID: uuid.MustParse(albumUUID), Valid: true},
	})
	controller := &fakeTransferController{row: db.DownloadRequestRow{ID: requestID, Status: "started"}}
	handler := downloadHandler(store, WithTransferController(controller))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/start", nil))
	if response.Code != http.StatusAccepted || controller.started != requestID {
		t.Fatalf("status = %d started = %s; body = %s", response.Code, controller.started, response.Body)
	}
}

func TestResolvingATrackReportsARequestThatCouldNotBeRead(t *testing.T) {
	store := &lookupFailingStore{fakeDownloadStore: downloadStore()}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+uuid.NewString()+"/resolutions",
		strings.NewReader(`{"fileName":"01.flac","trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Only a disagreement about which track a file is can be decided. A file that
// is missing, damaged, or unreadable has to be downloaded again, and saying so
// is what stops a judgement standing in for the bytes.
func TestAFileThatIsNotAJudgementCallCannotBeResolved(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.requests = append(store.requests, db.DownloadRequestRow{
		ID: requestID, Status: "completed", ImportStatus: "needs_review",
		ImportReviews: []db.ImportReview{{
			Kind: "paused",
			Evidence: &db.ImportEvidence{Files: []db.ImportFileEvidence{{
				Name: "01.flac", Resolvable: false,
				Observed: &db.ImportTags{Title: "Mysterons"},
				Problems: []string{"the file could not be read as audio"},
			}}},
		}},
	})
	handler := downloadHandler(store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/downloads/"+requestID.String()+"/resolutions",
		strings.NewReader(`{"fileName":"01.flac","trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "could not be read as audio") {
		t.Fatalf("body = %s, want the evidence carried through", response.Body)
	}
	if len(store.decided) != 0 {
		t.Fatalf("decided = %+v, want nothing recorded", store.decided)
	}
}

type fakePeerChallenges struct {
	questions map[string]db.UnansweredPeerChallengesRow
	replied   []string
	replyErr  error
}

func (challenges *fakePeerChallenges) Questions(
	context.Context, []string,
) (map[string]db.UnansweredPeerChallengesRow, error) {
	return challenges.questions, nil
}

func (challenges *fakePeerChallenges) Reply(_ context.Context, username, message string) error {
	if challenges.replyErr != nil {
		return challenges.replyErr
	}
	challenges.replied = append(challenges.replied, username+": "+message)
	return nil
}

// A question nobody could read belongs beside the request it is holding up.
func TestListDownloadsCarriesWhatThePeerAsked(t *testing.T) {
	store := downloadStore()
	store.requests = []db.DownloadRequestRow{{
		ID: uuid.New(), Provider: "slskd", SourceUsername: "PSXDupe", Status: "failed",
	}}
	challenges := &fakePeerChallenges{questions: map[string]db.UnansweredPeerChallengesRow{
		"PSXDupe": {Username: "PSXDupe", Challenge: "I am happy to share these files with anyone who is sharing."},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithPeerChallenges(challenges))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var decoded struct {
		Items []downloadRequestResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].PeerQuestion == nil {
		t.Fatalf("items = %#v", decoded.Items)
	}
	if decoded.Items[0].PeerQuestion.Username != "PSXDupe" {
		t.Fatalf("question = %#v", decoded.Items[0].PeerQuestion)
	}
}

// One peer serves several requests. A message about the files it is holding says
// nothing about the ones already imported, and a reply box on those was a
// question about music the library has.
func TestListDownloadsShowsAPeerMessageOnlyWhereFilesAreStillWaiting(t *testing.T) {
	failed := uuid.New()
	store := downloadStore()
	store.requests = []db.DownloadRequestRow{
		{ID: failed, Provider: "slskd", SourceUsername: "PSXDupe", Status: "failed"},
		{ID: uuid.New(), Provider: "slskd", SourceUsername: "PSXDupe", Status: "completed"},
	}
	challenges := &fakePeerChallenges{questions: map[string]db.UnansweredPeerChallengesRow{
		"PSXDupe": {Username: "PSXDupe", Challenge: "something nobody here can read"},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithPeerChallenges(challenges))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var decoded struct {
		Items []downloadRequestResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Items) != 2 {
		t.Fatalf("items = %#v", decoded.Items)
	}
	for _, item := range decoded.Items {
		if item.ID == failed && item.PeerQuestion == nil {
			t.Error("the failed request carries nothing the peer said")
		}
		if item.ID != failed && item.PeerQuestion != nil {
			t.Errorf("an imported request carries %#v", item.PeerQuestion)
		}
	}
}

func TestReplyToPeerSendsWhatWasTypedToThePeerOfThatRequest(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.requests = []db.DownloadRequestRow{{
		ID: requestID, Provider: "slskd", SourceUsername: "delightful", Status: "failed",
	}}
	challenges := &fakePeerChallenges{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithPeerChallenges(challenges))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/peer-reply",
		strings.NewReader(`{"username":"delightful","message":"please daddy give it to me"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(challenges.replied) != 1 || challenges.replied[0] != "delightful: please daddy give it to me" {
		t.Fatalf("replied = %#v", challenges.replied)
	}
}

// The peer is named in the body and checked against the request it was sent
// from. This endpoint can only ever write to somebody Schall already asked for
// music.
func TestReplyToPeerRefusesAPeerThatIsNotTheRequestsOwn(t *testing.T) {
	requestID := uuid.New()
	store := downloadStore()
	store.requests = []db.DownloadRequestRow{{
		ID: requestID, Provider: "slskd", SourceUsername: "delightful", Status: "failed",
	}}
	challenges := &fakePeerChallenges{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSourceProvider(&fakeProvider{}), WithPeerChallenges(challenges))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/peer-reply",
		strings.NewReader(`{"username":"somebody_else","message":"hello"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(challenges.replied) != 0 {
		t.Fatalf("replied = %#v", challenges.replied)
	}
}

func TestReplyToPeerReportsWhyItCouldNotSend(t *testing.T) {
	for name, testCase := range map[string]struct {
		err  error
		want int
	}{
		"nothing was asked": {err: peerchallenge.ErrNoQuestion, want: http.StatusConflict},
		"no provider":       {err: sources.ErrNotConfigured, want: http.StatusServiceUnavailable},
		"provider refused":  {err: errors.New("slskd is not connected"), want: http.StatusBadGateway},
	} {
		requestID := uuid.New()
		store := downloadStore()
		store.requests = []db.DownloadRequestRow{{
			ID: requestID, Provider: "slskd", SourceUsername: "delightful", Status: "failed",
		}}
		handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
			WithSourceProvider(&fakeProvider{}),
			WithPeerChallenges(&fakePeerChallenges{replyErr: testCase.err}))

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/downloads/"+requestID.String()+"/peer-reply",
			strings.NewReader(`{"username":"delightful","message":"hello"}`)))
		if response.Code != testCase.want {
			t.Errorf("%s: status = %d, want %d; body = %s", name, response.Code, testCase.want, response.Body)
		}
	}
}
