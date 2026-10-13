package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func linksClient(t *testing.T, body string, status int) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if status != http.StatusOK {
			response.WriteHeader(status)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL: server.URL, UserAgent: "Schall/test (test@example.com)", Interval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

// "The Less I Know the Better" as MusicBrainz sends it, cut to what is read.
func TestRecordingLinksReadsTheLinksAndWhereTheRecordingSits(t *testing.T) {
	client := linksClient(t, `{
		"relations": [
			{"type": "free streaming", "url": {"resource": "https://www.deezer.com/track/103052662"}},
			{"type": "streaming", "url": {"resource": "https://music.apple.com/gb/song/1440838488"}}
		],
		"releases": [{
			"id": "6438e9ba-41ad-478c-81f1-323dcb212903", "status": "Official",
			"media": [{"position": 1, "track-count": 13, "tracks": [{"position": 7, "number": "7"}]}]
		}]
	}`, http.StatusOK)

	links, err := client.RecordingLinks(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("RecordingLinks() error = %v", err)
	}
	if len(links.Links) != 2 || links.Links[0].Resource != "https://www.deezer.com/track/103052662" {
		t.Errorf("links = %+v", links.Links)
	}
	want := Placement{
		ReleaseID: uuid.MustParse("6438e9ba-41ad-478c-81f1-323dcb212903"),
		Status:    "Official", Medium: 1, Track: 7,
	}
	if len(links.Placements) != 1 || links.Placements[0] != want {
		t.Errorf("placements = %+v, want %+v", links.Placements, want)
	}
}

func TestRecordingLinksForARecordingMusicBrainzDoesNotHaveIsNotFound(t *testing.T) {
	client := linksClient(t, "", http.StatusNotFound)
	if _, err := client.RecordingLinks(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RecordingLinks() error = %v, want ErrNotFound", err)
	}
}

func TestReleaseLinksCountsTheTracksOnEachMedium(t *testing.T) {
	client := linksClient(t, `{
		"relations": [{"type": "free streaming", "url": {"resource": "https://www.deezer.com/album/9"}}],
		"media": [{"position": 1, "track-count": 10}, {"position": 2, "track-count": 4}]
	}`, http.StatusOK)

	release, err := client.ReleaseLinks(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ReleaseLinks() error = %v", err)
	}
	if release.Tracks() != 14 || len(release.MediaTracks) != 2 {
		t.Errorf("media = %v", release.MediaTracks)
	}
	if len(release.Links) != 1 {
		t.Errorf("links = %+v", release.Links)
	}
}

// Counts that cannot be told apart by medium are not given, so no position can
// be read off them.
func TestReleaseLinksGivesNoCountsWhenTheMediaAreOutOfOrder(t *testing.T) {
	client := linksClient(t, `{
		"media": [{"position": 2, "track-count": 4}, {"position": 1, "track-count": 10}]
	}`, http.StatusOK)

	release, err := client.ReleaseLinks(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ReleaseLinks() error = %v", err)
	}
	if release.MediaTracks != nil {
		t.Errorf("media = %v, want none", release.MediaTracks)
	}
}
