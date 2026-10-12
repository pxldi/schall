package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesUnderscorePrefixedAssets(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/_app/version.json", nil)
	response := httptest.NewRecorder()

	spaHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if !strings.Contains(response.Body.String(), `"version"`) {
		t.Errorf("body = %q, want embedded version asset", response.Body.String())
	}
}

func TestSPAHandlerFallsBackWithoutMutatingRequestPath(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/artists/example", nil)
	response := httptest.NewRecorder()

	spaHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if request.URL.Path != "/artists/example" {
		t.Errorf("request path = %q, want original path", request.URL.Path)
	}
}

// The interface is a page to read, not somewhere to send anything. A request
// that means to change something and reached here has missed the API entirely,
// and answering it with the page would say it worked.
func TestSPAHandlerAnswersOnlyReads(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response := httptest.NewRecorder()
		spaHandler().ServeHTTP(response, httptest.NewRequest(method, "/artists/example", nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", method, response.Code)
		}
	}
}

func testDist() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                         {Data: []byte("<!doctype html><title>Schall</title>")},
		"fonts/face.woff2":                   {Data: []byte("font")},
		"_app/immutable/entry/start.abc1.js": {Data: []byte("export const start = 1;")},
	}
}

// A hashed chunk changes its name when it changes, so it is kept for good.
// Before this, every load downloaded every chunk again.
func TestSPAHandlerLetsBrowsersKeepHashedChunks(t *testing.T) {
	response := httptest.NewRecorder()
	spaHandlerFS(testDist()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/_app/immutable/entry/start.abc1.js", nil))

	if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want immutable for a year", got)
	}
}

// The shell names the current build's chunks, so it is confirmed on every load
// and answered with a 304 when nothing changed.
func TestSPAHandlerConfirmsTheShellWithAnETag(t *testing.T) {
	handler := spaHandlerFS(testDist())
	for _, target := range []string{"/", "/artists/example", "/fonts/face.woff2"} {
		first := httptest.NewRecorder()
		handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, target, nil))
		if got := first.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", target, got)
		}
		tag := first.Header().Get("ETag")
		if tag == "" {
			t.Fatalf("%s: no ETag", target)
		}

		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("If-None-Match", tag)
		second := httptest.NewRecorder()
		handler.ServeHTTP(second, request)
		if second.Code != http.StatusNotModified {
			t.Errorf("%s: revalidation status = %d, want 304", target, second.Code)
		}
	}
}
