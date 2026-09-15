package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
