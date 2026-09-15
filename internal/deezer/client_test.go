package deezer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The hit body carries the field names and the shape of a real answer, taken
// from a live lookup on 2026-08-16. The preview address is shortened; the real
// one is signed and carries an expiry, which is why nothing stores it.
const hitBody = `{"id":4166953962,"readable":true,"title":"Cutscene","title_short":"Cutscene",
"isrc":"QZTAT2400123","duration":115,"rank":123456,
"preview":"https://cdnt-preview.dzcdn.net/api/1/1/2/3/5/0/235374666f07ff02a52d3a17021d9bf2.mp3?hdnea=exp=1786867734",
"artist":{"id":1,"name":"Himera"},"album":{"id":2,"title":"Cutscene"}}`

// Deezer reports a key it holds nothing for inside a body sent with HTTP 200.
const missBody = `{"error":{"type":"DataException","message":"no data","code":800}}`

const quotaBody = `{"error":{"type":"Exception","message":"Quota limit exceeded","code":4}}`

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(Options{BaseURL: server.URL, HTTPClient: server.Client()})
}

func TestTrackByISRCReadsTheTrack(t *testing.T) {
	var asked string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(hitBody))
	})

	track, err := client.TrackByISRC(context.Background(), "QZTAT2400123")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if asked != "/track/isrc:QZTAT2400123" {
		t.Fatalf("asked %q", asked)
	}
	if track.ID != 4166953962 || track.Title != "Cutscene" || track.DurationSeconds != 115 {
		t.Fatalf("read %+v", track)
	}
	if !strings.HasPrefix(track.PreviewURL, "https://cdnt-preview.dzcdn.net/") {
		t.Fatalf("preview address is %q", track.PreviewURL)
	}
}

func TestTrackByISRCReportsAMissingTrack(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(missBody))
	})
	if _, err := client.TrackByISRC(context.Background(), "ZZZZZ0000000"); !errors.Is(err, ErrNoTrack) {
		t.Fatalf("got %v, want ErrNoTrack", err)
	}
}

// The quota answer arrives the same way as a missing track, and must not be
// recorded as one: nothing was learned about the recording.
func TestTrackByISRCTellsAQuotaFromAMiss(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(quotaBody))
	})
	if _, err := client.TrackByISRC(context.Background(), "QZTAT2400123"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
}

// A track Deezer holds but publishes no excerpt of is silence. It was observed
// on QZAPK2000739 during the calibration run.
func TestTrackByISRCReportsAnAbsentPreview(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":123,"title":"Held back","duration":200,"preview":""}`))
	})
	if _, err := client.TrackByISRC(context.Background(), "QZAPK2000739"); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("got %v, want ErrNoPreview", err)
	}
}

func TestTrackByISRCReportsRateLimitingByStatus(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := client.TrackByISRC(context.Background(), "QZTAT2400123"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
}

func TestTrackByISRCRefusesWhatIsNotAnISRC(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request was sent for a code that is not an ISRC")
	})
	for name, code := range map[string]string{
		"empty":        "",
		"too short":    "QZTAT24001",
		"too long":     "QZTAT24001234",
		"path escape":  "../../track/1",
		"query escape": "QZTAT2400123?x=1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.TrackByISRC(context.Background(), code); !errors.Is(err, ErrNotAnISRC) {
				t.Fatalf("got %v, want ErrNotAnISRC", err)
			}
		})
	}
}

func TestNormalizeISRCAcceptsHowPeopleWriteIt(t *testing.T) {
	for _, written := range []string{"QZTAT2400123", "qztat2400123", "QZ-TAT-24-00123", " QZTAT2400123 "} {
		code, err := normalizeISRC(written)
		if err != nil {
			t.Fatalf("%q: %v", written, err)
		}
		if code != "QZTAT2400123" {
			t.Fatalf("%q became %q", written, code)
		}
	}
}

func TestFetchPreviewReadsTheAudio(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ID3\x04audio"))
	})
	audio, err := client.FetchPreview(context.Background(), client.baseURL+"/preview.mp3")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(audio) != "ID3\x04audio" {
		t.Fatalf("read %q", audio)
	}
}

// A signed address that has gone stale is a check that could not run, not a
// track without a preview and never a copy that failed.
func TestFetchPreviewReportsAStaleAddress(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := client.FetchPreview(context.Background(), client.baseURL+"/gone.mp3"); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("got %v, want ErrNoPreview", err)
	}
}

func TestFetchPreviewRefusesAnEmptyBody(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := client.FetchPreview(context.Background(), client.baseURL+"/empty.mp3"); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("got %v, want ErrNoPreview", err)
	}
	if _, err := client.FetchPreview(context.Background(), "  "); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("got %v, want ErrNoPreview for an empty address", err)
	}
}
