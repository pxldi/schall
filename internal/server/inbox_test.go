package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// Two passes over the inbox at once would walk the same folders and race each
// other's unlinks. The store refuses the second one; this is what the person who
// pressed the button is told.

type fakeCleanups struct {
	row  db.InboxCleanupRow
	err  error
	rows []db.InboxCleanupRow
	// What the handler asked for, so the test can prove the body reached it.
	dryRun      bool
	requestedBy string
}

func (cleanups *fakeCleanups) QueueInboxCleanup(
	_ context.Context, requestedBy string, dryRun bool, _ time.Time,
) (db.InboxCleanupRow, error) {
	cleanups.dryRun = dryRun
	cleanups.requestedBy = requestedBy
	return cleanups.row, cleanups.err
}

func (cleanups *fakeCleanups) InboxCleanup(
	_ context.Context, _ uuid.UUID,
) (db.InboxCleanupRow, error) {
	return cleanups.row, cleanups.err
}

func (cleanups *fakeCleanups) ListInboxCleanups(
	_ context.Context, _ int32,
) ([]db.InboxCleanupRow, error) {
	return cleanups.rows, cleanups.err
}

func TestAskingForASecondPassOverTheInboxIsRefused(t *testing.T) {
	cleanups := &fakeCleanups{err: db.ErrInboxCleanupRunning}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithInboxCleanups(cleanups))
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/inbox/cleanups", strings.NewReader(`{"dryRun":true}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body)
	}
	if !strings.Contains(response.Body.String(), "already running") {
		t.Errorf("body = %s, want the reason it was refused", response.Body)
	}
}

func TestAskingToCountWhatCanGoQueuesADryRun(t *testing.T) {
	cleanups := &fakeCleanups{row: db.InboxCleanupRow{
		ID: uuid.New(), DryRun: true, RequestedAt: time.Now(),
		Counts: map[string]db.InboxCleanupCount{
			db.InboxImported:     {Files: 12, Bytes: 300},
			db.InboxKeptQuestion: {Files: 2, Bytes: 40},
		},
	}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithInboxCleanups(cleanups))
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/inbox/cleanups", strings.NewReader(`{"dryRun":true}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body)
	}
	if !cleanups.dryRun {
		t.Error("the handler asked for a real deletion")
	}
	var body inboxCleanupResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.DeleteFiles != 12 || body.DeleteBytes != 300 {
		t.Errorf("it would delete %d files (%d bytes), want 12 (300)", body.DeleteFiles, body.DeleteBytes)
	}
	if body.KeptFiles != 2 || body.KeptBytes != 40 {
		t.Errorf("it would keep %d files (%d bytes), want 2 (40)", body.KeptFiles, body.KeptBytes)
	}
	// Every class is answered for, in a fixed order, so the table on screen does
	// not reorder itself between two passes.
	if len(body.Classes) != len(db.InboxDeleteClasses)+len(db.InboxKeepClasses) {
		t.Fatalf("classes = %#v", body.Classes)
	}
	if body.Classes[0].Class != db.InboxImported || !body.Classes[0].Deletes {
		t.Errorf("the first class is %#v, want the imported one marked as deleting", body.Classes[0])
	}
}

func TestCleaningTheInboxIsUnavailableWithoutADownloadFolder(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/inbox/cleanups", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
