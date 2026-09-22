package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tracksource"
	"github.com/rs/zerolog"
)

type fakeSourceKeys struct {
	track      tracksource.Track
	target     db.AcquisitionTargetRow
	err        error
	address    string
	confirmed  string
	keyedID    uuid.UUID
	floor      int
	floorForID uuid.UUID
}

func (keys *fakeSourceKeys) LookUpSource(_ context.Context, address string) (tracksource.Track, error) {
	keys.address = address
	return keys.track, keys.err
}

func (keys *fakeSourceKeys) KeyToSource(
	_ context.Context, id uuid.UUID, address, confirmed string,
) (db.AcquisitionTargetRow, error) {
	keys.keyedID, keys.address, keys.confirmed = id, address, confirmed
	return keys.target, keys.err
}

func (keys *fakeSourceKeys) SetMinimumBitrate(
	_ context.Context, id uuid.UUID, kbps int,
) (db.AcquisitionTargetRow, error) {
	keys.floorForID, keys.floor = id, kbps
	return keys.target, keys.err
}

func sourceKeyHandler(keys *fakeSourceKeys) http.Handler {
	options := []Option{WithAcquisitionTargets(&fakeAcquisitionTargets{})}
	if keys != nil {
		options = append(options, WithSourceKeys(keys))
	}
	return NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), options...)
}

func TestLookingUpAnAddressShowsWhatTheServiceSays(t *testing.T) {
	keys := &fakeSourceKeys{track: tracksource.Track{
		Source: "soundcloud", ExternalID: "soundcloud:293", URL: "https://soundcloud.com/abo/illegal-edit",
		Title: "PinkPantheress - Illegal (Abo Edit)", Artist: "Abo", Uploader: "Abo",
		DurationMS: 187000,
	}}
	response := httptest.NewRecorder()
	sourceKeyHandler(keys).ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/source-tracks?url=https%3A%2F%2Fsoundcloud.com%2Fabo%2Fillegal-edit", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["externalId"] != "soundcloud:293" || body["title"] != "PinkPantheress - Illegal (Abo Edit)" ||
		body["durationMs"] != float64(187000) {
		t.Errorf("body = %v", body)
	}
	if keys.address != "https://soundcloud.com/abo/illegal-edit" {
		t.Errorf("asked about %q", keys.address)
	}
}

func TestLookingUpAnAddressIsUnavailableWithoutAReader(t *testing.T) {
	response := httptest.NewRecorder()
	sourceKeyHandler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/source-tracks?url=x", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestKeyingAWantSendsTheAddressAndReturnsTheKeyedWant(t *testing.T) {
	targetID := uuid.New()
	keys := &fakeSourceKeys{target: db.AcquisitionTargetRow{
		ID: targetID, Status: "unresolved", Origin: "playlist",
		Source: "soundcloud", ExternalID: "soundcloud:293",
		ExternalURL:    "https://soundcloud.com/abo/illegal-edit",
		MinimumBitrate: pgtype.Int4{Int32: 320, Valid: true},
	}}
	response := httptest.NewRecorder()
	sourceKeyHandler(keys).ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+targetID.String()+"/source",
		strings.NewReader(`{"url":" https://soundcloud.com/abo/illegal-edit ","externalId":"soundcloud:293"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if keys.keyedID != targetID || keys.address != "https://soundcloud.com/abo/illegal-edit" ||
		keys.confirmed != "soundcloud:293" {
		t.Errorf("keyed %s with %q (%q)", keys.keyedID, keys.address, keys.confirmed)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["source"] != "soundcloud" || body["externalId"] != "soundcloud:293" ||
		body["minimumBitrate"] != float64(320) {
		t.Errorf("body = %v, want the key and the floor", body)
	}
}

func TestKeyingAWantReportsWhatRefusedIt(t *testing.T) {
	for _, test := range []struct {
		err  error
		code int
	}{
		{db.ErrNotKeyable, http.StatusConflict},
		{db.ErrAlreadyKeyed, http.StatusConflict},
		{tracksource.ErrNotATrack, http.StatusBadRequest},
		{tracksource.ErrGone, http.StatusNotFound},
		{tracksource.ErrRateLimited, http.StatusServiceUnavailable},
	} {
		keys := &fakeSourceKeys{err: test.err}
		response := httptest.NewRecorder()
		sourceKeyHandler(keys).ServeHTTP(response, httptest.NewRequest(http.MethodPost,
			"/api/v1/acquisition-targets/"+uuid.NewString()+"/source",
			strings.NewReader(`{"url":"https://soundcloud.com/a/b"}`)))
		if response.Code != test.code {
			t.Errorf("%v: status = %d, want %d", test.err, response.Code, test.code)
		}
	}
}

func TestSettingAKeyedWantsFloor(t *testing.T) {
	targetID := uuid.New()
	keys := &fakeSourceKeys{target: db.AcquisitionTargetRow{ID: targetID, Status: "unresolved"}}
	handler := sourceKeyHandler(keys)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/acquisition-targets/"+targetID.String()+"/minimum-bitrate",
		strings.NewReader(`{"minimumBitrate":128}`)))
	if response.Code != http.StatusOK || keys.floor != 128 || keys.floorForID != targetID {
		t.Fatalf("status = %d, floor %d for %s; body = %s", response.Code, keys.floor,
			keys.floorForID, response.Body)
	}

	for _, body := range []string{`{}`, `{"minimumBitrate":0}`, `{"minimumBitrate":-5}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
			"/api/v1/acquisition-targets/"+targetID.String()+"/minimum-bitrate", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", body, response.Code)
		}
	}

	keys.err = db.ErrNotKeyed
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/acquisition-targets/"+targetID.String()+"/minimum-bitrate",
		strings.NewReader(`{"minimumBitrate":256}`)))
	if response.Code != http.StatusConflict {
		t.Errorf("floor on a want with no key: status = %d, want 409", response.Code)
	}
}
