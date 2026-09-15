package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// A file nothing has fetched a picture for answers with the picture packed
// into the file itself, and a file carrying none answers 404 as before.
func TestAFileCoverIsReadOutOfTheFileWhenNothingIsCached(t *testing.T) {
	dir := t.TempDir()
	pictured := filepath.Join(dir, "pictured.mp3")
	writeTaggedMP3(t, pictured, jpegOfSize(t, 300, 300))
	bare := filepath.Join(dir, "bare.mp3")
	writeTaggedMP3(t, bare, nil)

	for _, tc := range []struct {
		name   string
		path   string
		status int
	}{
		{"pictured", pictured, http.StatusOK},
		{"bare", bare, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			store.libraryFile = db.LibraryFileRefRow{ID: uuid.New(), Path: tc.path}
			handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
				"/api/v1/library/files/"+store.libraryFile.ID.String()+"/cover", nil))
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if tc.status == http.StatusOK && recorder.Header().Get("Content-Type") != "image/jpeg" {
				t.Errorf("content type = %q, want image/jpeg", recorder.Header().Get("Content-Type"))
			}
		})
	}
}

func jpegOfSize(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			picture.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 90, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, nil); err != nil {
		t.Fatalf("encode a test picture: %v", err)
	}
	return encoded.Bytes()
}

// writeTaggedMP3 writes an ID3v2.4 header with an APIC frame when a picture is
// given; the tag reader needs no audio after it.
func writeTaggedMP3(t *testing.T, path string, picture []byte) {
	t.Helper()
	var frames bytes.Buffer
	if picture != nil {
		var body bytes.Buffer
		body.WriteByte(0) // ISO-8859-1 text
		body.WriteString("image/jpeg")
		body.WriteByte(0)
		body.WriteByte(3) // front cover
		body.WriteByte(0) // empty description
		body.Write(picture)

		frames.WriteString("APIC")
		frames.Write(syncSafe(body.Len()))
		frames.Write([]byte{0, 0})
		frames.Write(body.Bytes())
	}
	title := append([]byte{0}, []byte("a title")...)
	frames.WriteString("TIT2")
	frames.Write(syncSafe(len(title)))
	frames.Write([]byte{0, 0})
	frames.Write(title)

	var file bytes.Buffer
	file.WriteString("ID3")
	file.Write([]byte{4, 0, 0})
	file.Write(syncSafe(frames.Len()))
	file.Write(frames.Bytes())

	if err := os.WriteFile(path, file.Bytes(), 0o644); err != nil {
		t.Fatalf("write a test file: %v", err)
	}
}

// syncSafe is how ID3v2 writes a length: seven bits per byte.
func syncSafe(size int) []byte {
	return []byte{
		byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f),
		byte(size >> 7 & 0x7f), byte(size & 0x7f),
	}
}
