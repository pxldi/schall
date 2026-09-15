package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The artist page shows the community's genres as chips, so they are answered
// with the artist.
func TestGetArtistAnswersWithTheGenres(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{artist: db.GetArtistRow{
		ID: id, Name: "Talk Talk", Genres: []string{"post-rock", "art rock"},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/artists/"+id.String(), nil))

	var body artistDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Genres) != 2 || body.Genres[0] != "post-rock" {
		t.Errorf("genres = %v, want them in the order they were voted", body.Genres)
	}
}

// An artist nobody has asked about answers with an empty list rather than with
// nothing at all, so the page has one shape to draw.
func TestGetArtistAnswersWithNoGenresAsAnEmptyList(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{artist: db.GetArtistRow{ID: id, Name: "Marram"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/artists/"+id.String(), nil))

	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	genres, ok := body["genres"].([]any)
	if !ok || len(genres) != 0 {
		t.Errorf("genres = %#v, want an empty list", body["genres"])
	}
}

// The biography arrives with the article it came from, because the licence the
// words come under requires the page showing them to link it.
func TestGetArtistAnswersWithTheBiographyAndItsArticle(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{artist: db.GetArtistRow{
		ID: id, Name: "Talk Talk",
		Biography:          "Talk Talk were an English band.",
		BiographySourceUrl: "https://en.wikipedia.org/wiki/Talk_Talk",
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/artists/"+id.String(), nil))

	var body artistDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Biography != "Talk Talk were an English band." {
		t.Errorf("biography = %q", body.Biography)
	}
	if body.BiographySourceURL != "https://en.wikipedia.org/wiki/Talk_Talk" {
		t.Errorf("source = %q", body.BiographySourceURL)
	}
}

// An article address with no words behind it is withheld, so no page can draw
// an attribution line under nothing.
func TestGetArtistWithholdsAnArticleWithNoWords(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{artist: db.GetArtistRow{
		ID: id, Name: "Marram",
		BiographySourceUrl: "https://en.wikipedia.org/wiki/Marram",
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/artists/"+id.String(), nil))

	var body artistDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.BiographySourceURL != "" {
		t.Errorf("source = %q, want nothing to attribute", body.BiographySourceURL)
	}
}

// The genre the browser picked is passed to the list and to the counts that
// label it, so the numbers describe the list the reader is looking at.
func TestListArtistsPassesTheGenreOn(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodGet, "/api/v1/artists?genre=post-rock", nil))

	if store.artistParams.Genre != "post-rock" {
		t.Errorf("genre = %q, want the genre the reader picked", store.artistParams.Genre)
	}
}

// The picklist names the genres the catalogue actually files somebody under.
func TestCatalogueGenresAnswersWithTheGenresHeld(t *testing.T) {
	store := &fakeStore{catalogueGenres: []string{"ambient", "post-rock"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/artists/genres", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body struct {
		Items []string `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 2 || body.Items[0] != "ambient" {
		t.Errorf("items = %v", body.Items)
	}
}

// A catalogue nobody has genres for answers with an empty list, not with null.
func TestCatalogueGenresAnswersWithAnEmptyListWhenNoneAreHeld(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/artists/genres", nil))

	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 0 {
		t.Errorf("items = %#v, want an empty list", body["items"])
	}
}

// The release page shows the same chips, from the release group's own votes.
func TestGetAlbumAnswersWithTheGenres(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{album: db.GetAlbumRow{
		ID: id, Title: "Spirit of Eden", ArtistName: "Talk Talk",
		Genres: []string{"post-rock"},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+id.String(), nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body albumDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Genres) != 1 || body.Genres[0] != "post-rock" {
		t.Errorf("genres = %v", body.Genres)
	}
}
