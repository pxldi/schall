package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

type fakeLabelStore struct {
	list        []db.ListLabelsRow
	listErr     error
	get         db.GetLabelRow
	getErr      error
	releases    []db.LabelReleasesRow
	created     db.CreateLabelRow
	createErr   error
	unfollowed  db.UnfollowLabelRow
	unfollowErr error
	monitor     db.SetLabelMonitorLevelRow
	monitorErr  error
	refreshed   db.QueueLabelRefreshRow
	refreshErr  error
	askedID     uuid.UUID
}

func (store *fakeLabelStore) ListLabels(context.Context) ([]db.ListLabelsRow, error) {
	return store.list, store.listErr
}

func (store *fakeLabelStore) GetLabel(_ context.Context, id uuid.UUID) (db.GetLabelRow, error) {
	store.askedID = id
	return store.get, store.getErr
}

func (store *fakeLabelStore) LabelReleases(context.Context, uuid.UUID) ([]db.LabelReleasesRow, error) {
	return store.releases, nil
}

func (store *fakeLabelStore) CreateLabel(context.Context, db.CreateLabelParams) (db.CreateLabelRow, error) {
	return store.created, store.createErr
}

func (store *fakeLabelStore) UnfollowLabel(_ context.Context, id uuid.UUID) (db.UnfollowLabelRow, error) {
	store.askedID = id
	return store.unfollowed, store.unfollowErr
}

func (store *fakeLabelStore) SetLabelMonitorLevel(context.Context, db.SetLabelMonitorLevelParams) (db.SetLabelMonitorLevelRow, error) {
	return store.monitor, store.monitorErr
}

func (store *fakeLabelStore) QueueLabelRefresh(context.Context, uuid.UUID) (db.QueueLabelRefreshRow, error) {
	return store.refreshed, store.refreshErr
}

type fakeLabelSearcher struct {
	labels []musicbrainz.Label
	query  string
	err    error
}

func (searcher *fakeLabelSearcher) SearchLabels(_ context.Context, query string, _ int) ([]musicbrainz.Label, error) {
	searcher.query = query
	return searcher.labels, searcher.err
}

// Without a store, the label routes report themselves unavailable rather than
// panicking — the same rule every optional store on this API follows.
func TestLabelRoutesAreUnavailableWithoutAStore(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/labels", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestSearchLabelsAsksMusicBrainz(t *testing.T) {
	searcher := &fakeLabelSearcher{labels: []musicbrainz.Label{
		{ID: uuid.New(), Name: "Warp Records", Type: "Original Production", Country: "GB", Score: 100},
	}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabelSearch(searcher))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/search/labels?q=Warp", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if searcher.query != "Warp" {
		t.Errorf("query = %q, want %q", searcher.query, "Warp")
	}
	if !strings.Contains(response.Body.String(), "Warp Records") {
		t.Errorf("body = %s, want it to name the label", response.Body)
	}
}

func TestSearchLabelsRejectsATooShortQuery(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabelSearch(&fakeLabelSearcher{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/search/labels?q=a", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestFollowLabelSendsWhatWasAsked(t *testing.T) {
	musicBrainzID := uuid.New()
	id := uuid.New()
	store := &fakeLabelStore{created: db.CreateLabelRow{
		ID: id, MusicbrainzID: musicBrainzID, Name: "Warp Records", MonitorLevel: "main",
	}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(store))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/labels", strings.NewReader(
		`{"musicbrainzId":"`+musicBrainzID.String()+`","name":"Warp Records"}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusCreated, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"name":"Warp Records"`) {
		t.Errorf("body = %s, want the followed label's name", response.Body)
	}
}

func TestFollowLabelRequiresAMusicBrainzID(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(&fakeLabelStore{}))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/labels", strings.NewReader(
		`{"name":"Warp Records"}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusUnprocessableEntity, response.Body)
	}
}

// A label already followed answers no row, and that is a conflict rather than
// a silent success — the same rule CreateArtist follows.
func TestFollowLabelConflictsWhenAlreadyFollowed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(&fakeLabelStore{createErr: pgx.ErrNoRows}))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/labels", strings.NewReader(
		`{"musicbrainzId":"`+uuid.New().String()+`","name":"Warp Records"}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body)
	}
}

func TestFollowLabelConflictsOnAUniqueViolation(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(&fakeLabelStore{createErr: &pgconn.PgError{Code: "23505"}}))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/labels", strings.NewReader(
		`{"musicbrainzId":"`+uuid.New().String()+`","name":"Warp Records"}`,
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body)
	}
}

func TestListLabelsReportsWhatIsHeld(t *testing.T) {
	store := &fakeLabelStore{list: []db.ListLabelsRow{
		{ID: uuid.New(), Name: "Warp Records", MonitorLevel: "main", RefreshStatus: "completed"},
	}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(store))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/labels", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if !strings.Contains(response.Body.String(), "Warp Records") {
		t.Errorf("body = %s, want the label listed", response.Body)
	}
}

func TestGetLabelNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(&fakeLabelStore{getErr: pgx.ErrNoRows}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/labels/"+uuid.New().String(), nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

// Unfollowing sends the label named in the URL, and demotes rather than
// deletes: the response still names the label, just no longer followed.
func TestUnfollowLabelSendsTheLabelInTheURL(t *testing.T) {
	id := uuid.New()
	store := &fakeLabelStore{unfollowed: db.UnfollowLabelRow{ID: id, Name: "Warp Records"}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(store))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/labels/"+id.String()+"/follow", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if store.askedID != id {
		t.Errorf("asked about %s, want %s", store.askedID, id)
	}
	if strings.Contains(response.Body.String(), `"followed":true`) {
		t.Errorf("body = %s, want it to say the label is no longer followed", response.Body)
	}
}

func TestSetLabelMonitorLevelRejectsAnUnknownLevel(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(&fakeLabelStore{}))

	request := httptest.NewRequest(http.MethodPut, "/api/v1/labels/"+uuid.New().String()+"/monitor",
		strings.NewReader(`{"level":"whatever"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusUnprocessableEntity, response.Body)
	}
}

func TestRefreshLabelQueuesAJob(t *testing.T) {
	jobID := uuid.New()
	store := &fakeLabelStore{refreshed: db.QueueLabelRefreshRow{
		ID:        jobID,
		Status:    "queued",
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLabels(store))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/labels/"+uuid.New().String()+"/refresh", nil))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body)
	}
	if !strings.Contains(response.Body.String(), jobID.String()) {
		t.Errorf("body = %s, want the queued job's ID", response.Body)
	}
}
