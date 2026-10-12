package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/removals"
	"github.com/rs/zerolog"
)

type fakeRemovals struct {
	removed   []uuid.UUID
	kept      []uuid.UUID
	scheduled []removals.Scheduled
	leaving   []uuid.UUID
}

func (fake *fakeRemovals) Remove(_ context.Context, targetID uuid.UUID) (removals.Outcome, error) {
	fake.removed = append(fake.removed, targetID)
	return removals.Outcome{TargetID: targetID, Scheduled: fake.scheduled}, nil
}

func (fake *fakeRemovals) Keep(_ context.Context, targetID uuid.UUID) (int, error) {
	fake.kept = append(fake.kept, targetID)
	return 0, nil
}

func (fake *fakeRemovals) KeepRecording(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return fake.leaving, nil
}

func removalHandler(store *fakeStore, targets *fakeAcquisitionTargets, gone *fakeRemovals) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithRemovals(gone))
}

func TestRemovingATrackRemovesItsWant(t *testing.T) {
	trackID, store := trackOnDummy()
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "pending", Origin: "manual"},
	}
	gone := &fakeRemovals{}

	response := httptest.NewRecorder()
	removalHandler(store, targets, gone).ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/tracks/"+trackID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(gone.removed) != 1 || gone.removed[0] != targetID {
		t.Fatalf("removed = %v, want %s", gone.removed, targetID)
	}
}

// The page says when the file goes, so the date is in the answer.
func TestRemovingATrackSaysWhenItsFileIsDeleted(t *testing.T) {
	trackID, store := trackOnDummy()
	store.trackForWanting.Owned = true
	removesAt := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "acquired", Origin: "manual"},
	}
	gone := &fakeRemovals{scheduled: []removals.Scheduled{
		{FileID: uuid.New(), Path: "/music/Mysterons.flac", RemovesAt: removesAt},
	}}

	response := httptest.NewRecorder()
	removalHandler(store, targets, gone).ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/tracks/"+trackID.String()+"/wanted", nil))

	var body removedResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Scheduled) != 1 || !body.Scheduled[0].RemovesAt.Equal(removesAt) {
		t.Fatalf("scheduled = %+v, want the file and its date", body.Scheduled)
	}
}

func TestRemovingAReleaseRemovesEachRecordingOnce(t *testing.T) {
	albumID := uuid.New()
	shared := uuid.NullUUID{UUID: uuid.New(), Valid: true}
	openWant := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{
			{ID: uuid.New(), Title: "Owned", MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true}, Owned: true},
			{ID: uuid.New(), Title: "Wanted", MusicbrainzRecordingID: shared,
				WantID:     uuid.NullUUID{UUID: openWant, Valid: true},
				WantStatus: pgtype.Text{String: "searching", Valid: true}},
			{ID: uuid.New(), Title: "Wanted (reprise)", MusicbrainzRecordingID: shared,
				WantID:     uuid.NullUUID{UUID: openWant, Valid: true},
				WantStatus: pgtype.Text{String: "searching", Valid: true}},
			{ID: uuid.New(), Title: "No recording"},
		},
	}
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "acquired", Origin: "manual"},
	}
	gone := &fakeRemovals{}

	response := httptest.NewRecorder()
	removalHandler(store, targets, gone).ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var body removedResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Removed != 2 || body.Unresolvable != 1 || len(gone.removed) != 2 {
		t.Fatalf("body = %+v removed = %v, want two recordings removed once each", body, gone.removed)
	}
}

func TestRemovingIsUnavailableWithoutRemovals(t *testing.T) {
	trackID, store := trackOnDummy()

	response := httptest.NewRecorder()
	releaseHandler(store, &fakeAcquisitionTargets{}).ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/tracks/"+trackID.String()+"/wanted", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Wanting a removed track again takes the removal back.
func TestWantingARemovedTrackWantsItAgain(t *testing.T) {
	trackID, store := trackOnDummy()
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "not_wanted", Origin: "manual"},
	}
	gone := &fakeRemovals{}

	response := httptest.NewRecorder()
	removalHandler(store, targets, gone).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/tracks/"+trackID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.restored != targetID {
		t.Fatalf("restored = %s, want %s", targets.restored, targetID)
	}
}

// An owned track whose file is waiting to be deleted can be wanted again, and
// that keeps the file.
func TestWantingAnOwnedTrackWithAFileLeavingKeepsIt(t *testing.T) {
	trackID, store := trackOnDummy()
	store.trackForWanting.Owned = true
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "pending", Origin: "manual"},
	}
	gone := &fakeRemovals{leaving: []uuid.UUID{targetID}}

	response := httptest.NewRecorder()
	removalHandler(store, targets, gone).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/tracks/"+trackID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.restored != targetID {
		t.Fatalf("restored = %s, want %s", targets.restored, targetID)
	}
}

func TestWantingAReleaseWantsItsRemovedTracksAgain(t *testing.T) {
	albumID := uuid.New()
	removedWant := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{
			{ID: uuid.New(), Title: "Removed", MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
				WantID:     uuid.NullUUID{UUID: removedWant, Valid: true},
				WantStatus: pgtype.Text{String: "not_wanted", Valid: true}},
		},
	}
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: removedWant, Status: "pending", Origin: "manual"},
	}
	gone := &fakeRemovals{}

	response := httptest.NewRecorder()
	removalHandler(store, targets, gone).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.restored != removedWant || len(gone.kept) != 1 || gone.kept[0] != removedWant {
		t.Fatalf("restored = %s kept = %v, want %s wanted again", targets.restored, gone.kept, removedWant)
	}
}
