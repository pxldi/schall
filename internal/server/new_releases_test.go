package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/newreleases"
	"github.com/rs/zerolog"
)

type fakeNewReleasesService struct {
	overview    newreleases.Playlist
	overviewErr error
	saved       db.SaveNewReleasesPlaylistSettingsParams
	saveResult  db.NewReleasesPlaylistSettingsRow
	saveErr     error
}

func (fake *fakeNewReleasesService) Overview(context.Context) (newreleases.Playlist, error) {
	return fake.overview, fake.overviewErr
}

func (fake *fakeNewReleasesService) SaveSettings(
	_ context.Context, params db.SaveNewReleasesPlaylistSettingsParams,
) (db.NewReleasesPlaylistSettingsRow, error) {
	fake.saved = params
	return fake.saveResult, fake.saveErr
}

// Without a store, the route reports itself unavailable rather than panicking
// — the same rule every optional collaborator on this API follows.
func TestNewReleasesRouteIsUnavailableWithoutAService(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/new-releases", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestGetNewReleasesPlaylistBeforeTheFirstRefresh(t *testing.T) {
	service := &fakeNewReleasesService{
		overview: newreleases.Playlist{
			Settings: db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 90},
		},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithNewReleasesPlaylist(service))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/new-releases", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body struct {
		Settings struct {
			Enabled    bool  `json:"enabled"`
			WindowDays int32 `json:"windowDays"`
		} `json:"settings"`
		PlaylistID *string `json:"playlistId"`
		Name       string  `json:"name"`
		TrackCount int64   `json:"trackCount"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.PlaylistID != nil {
		t.Fatalf("a playlist id was reported before any refresh made one: %v", *body.PlaylistID)
	}
	if body.Name != newreleases.PlaylistName {
		t.Fatalf("name = %q, want the fixed name %q", body.Name, newreleases.PlaylistName)
	}
	if !body.Settings.Enabled || body.Settings.WindowDays != 90 {
		t.Fatalf("settings were not passed through: %+v", body.Settings)
	}
}

func TestGetNewReleasesPlaylistOnceItExists(t *testing.T) {
	id := uuid.New()
	service := &fakeNewReleasesService{
		overview: newreleases.Playlist{
			Exists:   true,
			Settings: db.NewReleasesPlaylistSettingsRow{Enabled: true, WindowDays: 60},
			List: db.PlaylistRow{
				ID: id, Name: "New releases", EntryCount: 12,
			},
		},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithNewReleasesPlaylist(service))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/new-releases", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	body := response.Body.String()
	if !strings.Contains(body, id.String()) {
		t.Errorf("body = %s, want it to name the playlist %s", body, id)
	}
	if !strings.Contains(body, `"trackCount":12`) {
		t.Errorf("body = %s, want the track count", body)
	}
}

func TestSaveNewReleasesPlaylistSettings(t *testing.T) {
	service := &fakeNewReleasesService{
		saveResult: db.NewReleasesPlaylistSettingsRow{Enabled: false, WindowDays: 30},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithNewReleasesPlaylist(service))

	body, _ := json.Marshal(map[string]any{"enabled": false, "windowDays": 30})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/new-releases/settings", bytes.NewReader(body)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if service.saved.Enabled || service.saved.WindowDays != 30 {
		t.Fatalf("what was sent did not reach the service: %+v", service.saved)
	}
	if !strings.Contains(response.Body.String(), `"windowDays":30`) {
		t.Errorf("body = %s, want the saved window", response.Body)
	}
}

func TestSaveNewReleasesPlaylistSettingsRejectsAWindowOutOfRange(t *testing.T) {
	service := &fakeNewReleasesService{saveErr: newreleases.ErrWindowOutOfRange}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithNewReleasesPlaylist(service))

	body, _ := json.Marshal(map[string]any{"enabled": true, "windowDays": 0})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/new-releases/settings", bytes.NewReader(body)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusUnprocessableEntity, response.Body)
	}
}

func TestSaveNewReleasesPlaylistSettingsRejectsAMalformedBody(t *testing.T) {
	service := &fakeNewReleasesService{}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithNewReleasesPlaylist(service))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/new-releases/settings", strings.NewReader("{")))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
