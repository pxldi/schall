package applemusic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(Options{BaseURL: server.URL, HTTPClient: server.Client()})
}

// The field names of a live answer for "The Less I Know the Better", taken on
// 2026-10-13 and cut to what is read.
func TestSongReadsTheSongAndWhereItSits(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "1440838488" || r.URL.Query().Get("country") != "gb" {
			t.Errorf("asked %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"resultCount":1,"results":[{"wrapperType":"track","kind":"song",
			"collectionId":1440838039,"trackId":1440838488,"trackName":"The Less I Know the Better",
			"discNumber":1,"trackNumber":7,"trackCount":13,"trackTimeMillis":216320,
			"previewUrl":"https://audio-ssl.itunes.apple.com/x.m4a"}]}`))
	})

	song, err := client.Song(context.Background(), 1440838488, "GB")
	if err != nil {
		t.Fatalf("Song: %v", err)
	}
	if song.Disc != 1 || song.Track != 7 || song.DurationMS != 216320 || song.PreviewURL == "" {
		t.Fatalf("read %+v", song)
	}
}

func TestSongForANumberAppleDoesNotHaveIsNotFound(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"resultCount":0,"results":[]}`))
	})
	if _, err := client.Song(context.Background(), 1, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAlbumReadsTheTrackCountAndTheSongs(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("entity") != "song" {
			t.Errorf("asked %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"resultCount":3,"results":[
			{"wrapperType":"collection","collectionId":5,"trackCount":2},
			{"wrapperType":"track","collectionId":5,"trackId":51,"discNumber":1,"trackNumber":1},
			{"wrapperType":"track","collectionId":5,"trackId":52,"discNumber":1,"trackNumber":2}]}`))
	})

	album, err := client.Album(context.Background(), 5, "us")
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if album.Tracks != 2 || len(album.Songs) != 2 || album.Songs[1].ID != 52 {
		t.Fatalf("read %+v", album)
	}
}

func TestAnAnswerAppleRefusesForLoadIsRateLimiting(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := client.Song(context.Background(), 1, ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}
