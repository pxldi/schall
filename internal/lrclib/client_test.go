package lrclib

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The one question this client asks. What is pinned here is that it always sends
// the length — which is the only thing stopping a title match handing back
// somebody else's song — and that a service with no answer is an answer rather
// than a failure.

func TestTheLengthIsAlwaysSent(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"syncedLyrics":"[00:01.00]words"}`))
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL})
	if _, err := client.Get(context.Background(), Song{
		Artist: "Tyler, the Creator", Title: "EARFQUAKE",
		Album: "IGOR", Duration: 190 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	want := "album_name=IGOR&artist_name=Tyler%2C+the+Creator&duration=190&track_name=EARFQUAKE"
	if asked != want {
		t.Fatalf("asked %q", asked)
	}
}

// A recording whose length the catalogue does not know is not asked about at
// all: without it the service falls back to matching names, which is the fuzzy
// agreement this codebase refuses.
func TestASongWithNoLengthIsNotAskedAbout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the service was asked about a song with no length")
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL})
	_, err := client.Get(context.Background(), Song{Artist: "An Artist", Title: "A Song"})

	if !errors.Is(err, ErrNoLyrics) {
		t.Fatalf("err = %v; want ErrNoLyrics", err)
	}
}

func TestASongTheServiceDoesNotHoldIsAnAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL})
	_, err := client.Get(context.Background(), Song{
		Artist: "An Artist", Title: "A Song", Duration: time.Minute,
	})

	if !errors.Is(err, ErrNoLyrics) {
		t.Fatalf("err = %v; want ErrNoLyrics", err)
	}
}

func TestAnInstrumentalIsReportedAsOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"instrumental":true,"plainLyrics":"","syncedLyrics":""}`))
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL})
	lyrics, err := client.Get(context.Background(), Song{
		Artist: "An Artist", Title: "A Song", Duration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !lyrics.Instrumental {
		t.Fatal("an instrumental was not reported as one")
	}
}

// A row holding neither kind of words is the same as no row at all.
func TestAnAnswerWithNoWordsInItIsAMiss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"instrumental":false,"plainLyrics":"","syncedLyrics":""}`))
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL})
	_, err := client.Get(context.Background(), Song{
		Artist: "An Artist", Title: "A Song", Duration: time.Minute,
	})

	if !errors.Is(err, ErrNoLyrics) {
		t.Fatalf("err = %v; want ErrNoLyrics", err)
	}
}

func TestTimedWordsAreToldApartFromPlainOnes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"plainLyrics":"words","syncedLyrics":"[00:01.00]words"}`))
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL})
	lyrics, err := client.Get(context.Background(), Song{
		Artist: "An Artist", Title: "A Song", Duration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !lyrics.Timed() {
		t.Fatal("timed words were not reported as timed")
	}
}

// A service in difficulty is a failure, not an absence: caching it as "there are
// no words" would be permanent.
func TestAServiceThatFailedIsNotAnAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL})
	_, err := client.Get(context.Background(), Song{
		Artist: "An Artist", Title: "A Song", Duration: time.Minute,
	})

	if err == nil || errors.Is(err, ErrNoLyrics) {
		t.Fatalf("err = %v; want a failure that is not an absence", err)
	}
}

func TestTheInstallationSaysWhoIsAsking(t *testing.T) {
	var agent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"plainLyrics":"words"}`))
	}))
	defer server.Close()

	client := NewClient(Options{BaseURL: server.URL, UserAgent: "schall/1.0 (someone@example.com)"})
	if _, err := client.Get(context.Background(), Song{
		Artist: "An Artist", Title: "A Song", Duration: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	if agent != "schall/1.0 (someone@example.com)" {
		t.Fatalf("user agent = %q", agent)
	}
}
