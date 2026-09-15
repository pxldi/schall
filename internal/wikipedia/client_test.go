package wikipedia

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(Options{BaseURL: server.URL, UserAgent: "schall-test/1.0"})
}

// The summary is asked for by the exact title, and what comes back is the plain
// text and the article behind it. The response below is the shape Wikipedia's
// REST endpoint answers with.
func TestSummaryReadsTheExtractAndTheArticle(t *testing.T) {
	var gotPath, gotAgent string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotPath, gotAgent = request.URL.Path, request.Header.Get("User-Agent")
		_, _ = response.Write([]byte(`{
			"type": "standard",
			"title": "Talk Talk",
			"extract": "Talk Talk were an English band formed in 1981.",
			"content_urls": {
				"desktop": {"page": "https://en.wikipedia.org/wiki/Talk_Talk"},
				"mobile": {"page": "https://en.m.wikipedia.org/wiki/Talk_Talk"}
			}
		}`))
	})

	summary, err := client.Summary(context.Background(), "Talk Talk")
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	// The title becomes one path segment, spaces and all, so the article asked
	// for is the article named and never a search.
	if gotPath != "/api/rest_v1/page/summary/Talk_Talk" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAgent != "schall-test/1.0" {
		t.Errorf("user agent = %q", gotAgent)
	}
	if summary.Text != "Talk Talk were an English band formed in 1981." {
		t.Errorf("text = %q", summary.Text)
	}
	if summary.URL != "https://en.wikipedia.org/wiki/Talk_Talk" {
		t.Errorf("url = %q", summary.URL)
	}
}

// A title with a slash in it is one path segment and stays one.
func TestSummaryEscapesTheTitle(t *testing.T) {
	var gotPath string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.EscapedPath()
		_, _ = response.Write([]byte(
			`{"extract": "x", "content_urls": {"desktop": {"page": "https://example.test/x"}}}`))
	})

	if _, err := client.Summary(context.Background(), "AC/DC"); err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if !strings.HasSuffix(gotPath, "AC%2FDC") {
		t.Errorf("path = %q, want the slash escaped into one segment", gotPath)
	}
}

// No article is an answer about the artist, so the caller can record it and
// stop asking.
func TestSummaryReportsNoArticle(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	_, err := client.Summary(context.Background(), "Nobody At All")
	if !errors.Is(err, ErrNoArticle) {
		t.Fatalf("Summary() error = %v, want ErrNoArticle", err)
	}
}

// A disambiguation page says the title names several things. That is no answer
// about this artist, and showing it would put somebody else's life under their
// name.
func TestSummaryRefusesADisambiguationPage(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"type": "disambiguation",
			"extract": "Marram may refer to:",
			"content_urls": {"desktop": {"page": "https://en.wikipedia.org/wiki/Marram"}}
		}`))
	})

	_, err := client.Summary(context.Background(), "Marram")
	if !errors.Is(err, ErrNoArticle) {
		t.Fatalf("Summary() error = %v, want ErrNoArticle", err)
	}
}

// An article with no words is nothing to show, and so is one with no address:
// the licence the words come under requires the link, so a summary without one
// is not usable.
func TestSummaryRefusesWhatCannotBeShown(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"no words", `{"extract": "   ",
			"content_urls": {"desktop": {"page": "https://en.wikipedia.org/wiki/X"}}}`},
		{"no article address", `{"extract": "Some words about a band."}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
				_, _ = response.Write([]byte(testCase.body))
			})
			if _, err := client.Summary(context.Background(), "X"); !errors.Is(err, ErrNoArticle) {
				t.Fatalf("Summary() error = %v, want ErrNoArticle", err)
			}
		})
	}
}

// A service in difficulty is not an absence. It is reported as a failure, so
// nothing is written down and the artist is asked about again later.
func TestSummaryReportsAnOutageRatherThanAnAbsence(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(status)
		})
		_, err := client.Summary(context.Background(), "Talk Talk")
		if err == nil || errors.Is(err, ErrNoArticle) {
			t.Errorf("Summary() error = %v at %d, want a failure", err, status)
		}
	}
}

// The mobile address stands in where the desktop one is missing, because either
// one credits the article the words came from.
func TestSummaryFallsBackToTheMobileArticle(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"extract": "A band.",
			"content_urls": {"mobile": {"page": "https://en.m.wikipedia.org/wiki/Band"}}
		}`))
	})

	summary, err := client.Summary(context.Background(), "Band")
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if summary.URL != "https://en.m.wikipedia.org/wiki/Band" {
		t.Errorf("url = %q", summary.URL)
	}
}
