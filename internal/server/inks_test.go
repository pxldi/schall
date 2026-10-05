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
