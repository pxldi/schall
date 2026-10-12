package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/coverart"
	"github.com/pxldi/schall/internal/db"
)

func storedCover(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 500, 500))
	for x := range 500 {
		for y := range 500 {
			picture.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, nil); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func askCover(t *testing.T, stored []byte, query string) *httptest.ResponseRecorder {
	t.Helper()
	store := &fakeStore{cachedCover: db.ReleaseCoverArtRow{
		Image: stored, ContentType: "image/jpeg", Source: coverart.Name,
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithCoverArt(&fakeArchive{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+uuid.New().String()+"/cover"+query, nil))
	return response
}

// A list draws a cover at 40 to 100 pixels; the stored one is 500.
func TestACoverAskedForAtAListSizeIsServedThatWide(t *testing.T) {
	response := askCover(t, storedCover(t), "?size=128")

	config, err := jpeg.DecodeConfig(response.Body)
	if response.Code != http.StatusOK || err != nil {
		t.Fatalf("status = %d, decode error %v", response.Code, err)
	}
	if config.Width != 128 || config.Height != 128 {
		t.Fatalf("served %dx%d, want 128x128", config.Width, config.Height)
	}
}

func TestACoverAskedForAtASizeNotServedIsTheStoredOne(t *testing.T) {
	stored := storedCover(t)

	response := askCover(t, stored, "?size=77")

	if !bytes.Equal(response.Body.Bytes(), stored) {
		t.Fatal("a size outside the served ones did not answer the stored picture")
	}
}

// The browser keeps both sizes of one cover, so they cannot share a validator.
func TestEachCoverSizeHasItsOwnETag(t *testing.T) {
	stored := storedCover(t)

	small := askCover(t, stored, "?size=128").Header().Get("ETag")
	full := askCover(t, stored, "").Header().Get("ETag")

	if small == "" || small == full {
		t.Fatalf("ETag small = %q, full = %q, want two different ones", small, full)
	}
}
