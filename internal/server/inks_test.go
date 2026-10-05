package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

func TestAReleasePageCarriesItsInksAndPalette(t *testing.T) {
	albumID := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", AlbumType: "album"},
		inks: []db.ReleaseInksRow{{
			AlbumID: albumID, InkDark: "#0d1420", InkLight: "#f2b48c",
			Palette: []string{"#1a2840", "#f2b48c"},
		}},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+albumID.String(), nil))
	var detail albumDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Inks == nil || detail.Inks.Dark != "#0d1420" || detail.Inks.Light != "#f2b48c" ||
		!slices.Equal(detail.Inks.Palette, []string{"#1a2840", "#f2b48c"}) {
		t.Fatalf("inks = %+v, want the stored inks and palette", detail.Inks)
	}
}

func TestAReleaseWithNoInksAnswersNull(t *testing.T) {
	albumID := uuid.New()
	store := &fakeStore{album: db.GetAlbumRow{ID: albumID, Title: "Dummy", AlbumType: "album"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+albumID.String(), nil))
	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(body["inks"]) != "null" {
		t.Fatalf("inks = %s, want null so the page uses the house inks", body["inks"])
	}
}

func TestReleaseListRowsCarryInksWithoutThePalette(t *testing.T) {
	inked, plain := uuid.New(), uuid.New()
	store := &fakeStore{
		albums: db.AlbumPage{Items: []db.AlbumRow{{ID: inked, Title: "Dummy"}, {ID: plain, Title: "Third"}}},
		inks: []db.ReleaseInksRow{{
			AlbumID: inked, InkDark: "#0d1420", InkLight: "#f2b48c", Palette: []string{"#1a2840"},
		}},
	}

	body := listAlbumsPage(t, store, "/api/v1/albums")

	first, second := body.Items[0].Inks, body.Items[1].Inks
	if first == nil || first.Dark != "#0d1420" || first.Light != "#f2b48c" || first.Palette != nil {
		t.Fatalf("first row inks = %+v, want both inks and no palette", first)
	}
	if second != nil {
		t.Fatalf("second row inks = %+v, want none", second)
	}
}

func TestAnArtistCardCarriesItsFirstCoversInks(t *testing.T) {
	inked, other := uuid.New(), uuid.New()
	store := &fakeStore{
		artists: []db.ListArtistsRow{
			{ID: uuid.New(), Name: "Portishead", CoverAlbumIds: []uuid.UUID{inked, other}},
			{ID: uuid.New(), Name: "Burial"},
		},
		inks: []db.ReleaseInksRow{{
			AlbumID: inked, InkDark: "#0d1420", InkLight: "#f2b48c", Palette: []string{"#1a2840"},
		}},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artists", nil))
	var body struct {
		Items []artistListItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(body.Items))
	}
	first := body.Items[0].CoverInks
	if first == nil || first.Dark != "#0d1420" || first.Light != "#f2b48c" || first.Palette != nil {
		t.Fatalf("first card inks = %+v, want both inks and no palette", first)
	}
	if body.Items[1].CoverInks != nil {
		t.Fatalf("second card inks = %+v, want none", body.Items[1].CoverInks)
	}
}

func TestAnArtistPageCarriesItsLeadReleasesInks(t *testing.T) {
	lead := uuid.New()
	store := &fakeStore{
		artist: db.GetArtistRow{
			ID: uuid.New(), Name: "Portishead", MonitorLevel: "everything",
			LeadAlbumID: uuid.NullUUID{UUID: lead, Valid: true},
		},
		inks: []db.ReleaseInksRow{{AlbumID: lead, InkDark: "#0d1420", InkLight: "#f2b48c"}},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artists/"+store.artist.ID.String(), nil))
	var detail artistDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.LeadAlbumID == nil || *detail.LeadAlbumID != lead {
		t.Fatalf("lead = %v, want %s", detail.LeadAlbumID, lead)
	}
	if detail.Inks == nil || detail.Inks.Dark != "#0d1420" || detail.Inks.Light != "#f2b48c" {
		t.Fatalf("inks = %+v, want the lead release's inks", detail.Inks)
	}
}
