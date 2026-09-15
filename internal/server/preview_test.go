package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/preview"
	"github.com/rs/zerolog"
)

// fakePreviews stands in for the transcoder. It hands back a file that is
// already on disc, because what these tests check is the route: which files may
// be played, and what is said about the ones that may not.
type fakePreviews struct {
	playable string
	err      error
	asked    []string
	warmed   []string
}

func (previews *fakePreviews) Playable(_ context.Context, source string) (string, error) {
	previews.asked = append(previews.asked, source)
	return previews.playable, previews.err
}

func (previews *fakePreviews) Warm(sources []string) {
	previews.warmed = append(previews.warmed, sources...)
}

func (previews *fakePreviews) ContentType() string { return "audio/mpeg" }

func TestLibraryFileAudioPlaysTheFileTheLibraryHolds(t *testing.T) {
	transcoded := filepath.Join(t.TempDir(), "played.mp3")
	if err := os.WriteFile(transcoded, []byte("ID3 and then some audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(held, []byte("the real thing"), 0o600); err != nil {
		t.Fatal(err)
	}

	fileID := uuid.New()
	store := &fakeStore{libraryFile: db.LibraryFileRefRow{ID: fileID, Path: held}}
	previews := &fakePreviews{playable: transcoded}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPreviews(previews))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+fileID.String()+"/audio", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if got := response.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("content type = %q", got)
	}
	// A scrubber only works if the browser may ask for part of the file.
	if got := response.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("accept-ranges = %q", got)
	}
	if len(previews.asked) != 1 || previews.asked[0] != held {
		t.Fatalf("transcoded %v, want the library's own path %q", previews.asked, held)
	}
	if response.Body.String() != "ID3 and then some audio" {
		t.Fatalf("body = %q", response.Body)
	}
}

// A phone seeking through a preview asks for a slice of it, and the answer has
// to carry only that slice and say so with a 206 and a Content-Range.
func TestLibraryFileAudioAnswersARangeRequest(t *testing.T) {
	transcoded := filepath.Join(t.TempDir(), "played.mp3")
	if err := os.WriteFile(transcoded, []byte("ID3 and then some audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(held, []byte("the real thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileID := uuid.New()
	store := &fakeStore{libraryFile: db.LibraryFileRefRow{ID: fileID, Path: held}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPreviews(&fakePreviews{playable: transcoded}))

	request := httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+fileID.String()+"/audio", nil)
	request.Header.Set("Range", "bytes=0-9")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", response.Code)
	}
	if response.Body.String() != "ID3 and th" {
		t.Fatalf("body = %q, want the first ten bytes", response.Body.String())
	}
	if got := response.Header().Get("Content-Range"); got != "bytes 0-9/23" {
		t.Errorf("content-range = %q", got)
	}
}

// A phone that already holds this preview sends back the ETag it was given, and
// gets a 304 rather than downloading the file again.
func TestLibraryFileAudioAnswersIfNoneMatchWithNotModified(t *testing.T) {
	transcoded := filepath.Join(t.TempDir(), "played.mp3")
	if err := os.WriteFile(transcoded, []byte("ID3 and then some audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(held, []byte("the real thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileID := uuid.New()
	store := &fakeStore{libraryFile: db.LibraryFileRefRow{ID: fileID, Path: held}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPreviews(&fakePreviews{playable: transcoded}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+fileID.String()+"/audio", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	request := httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+fileID.String()+"/audio", nil)
	request.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty on a 304", response.Body.String())
	}
}

// A file the library has no row for is not played, and nothing is transcoded on
// the way to finding that out.
func TestLibraryFileAudioIsNotFoundForAFileTheLibraryDoesNotHold(t *testing.T) {
	store := &fakeStore{libraryFileErr: pgx.ErrNoRows}
	previews := &fakePreviews{playable: "/nowhere"}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPreviews(previews))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+uuid.NewString()+"/audio", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(previews.asked) != 0 {
		t.Fatalf("transcoded %v for a file that is not held", previews.asked)
	}
}

// The row is there and the bytes are not. That is a statement about the disc
// rather than about the database, so it says which path it looked at.
func TestLibraryFileAudioSaysWhenTheFileIsGoneFromTheDisc(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "deleted.flac")
	store := &fakeStore{libraryFile: db.LibraryFileRefRow{ID: uuid.New(), Path: missing}}
	previews := &fakePreviews{playable: "/nowhere"}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPreviews(previews))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+uuid.NewString()+"/audio", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), missing) {
		t.Fatalf("body = %s; want the path it looked at", response.Body)
	}
	if len(previews.asked) != 0 {
		t.Fatalf("transcoded %v for a file that is not there", previews.asked)
	}
}

// Without ffmpeg the file cannot be played, and every other way of deciding
// about it still works — so this is said in one sentence and fails nothing.
func TestLibraryFileAudioReportsAMissingTranscoder(t *testing.T) {
	held := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(held, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{libraryFile: db.LibraryFileRefRow{ID: uuid.New(), Path: held}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPreviews(&fakePreviews{err: preview.ErrUnavailable}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+uuid.NewString()+"/audio", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// An installation started without a transcoder at all answers the same way,
// rather than looking like a file that could not be found.
func TestLibraryFileAudioReportsAnInstallationWithNoPreviews(t *testing.T) {
	store := &fakeStore{libraryFile: db.LibraryFileRefRow{ID: uuid.New(), Path: "/music/a.flac"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files/"+uuid.NewString()+"/audio", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}
