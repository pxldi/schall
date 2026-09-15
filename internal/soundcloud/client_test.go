package soundcloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// flickermood is the shape SoundCloud really answers with, recorded from the
// track oEmbed's own documentation uses as its example. The title carries the
// uploader after the word "by", the artwork address names its size, and the
// track number is only in the player.
const flickermood = `{
  "version": 1.0,
  "type": "rich",
  "provider_name": "SoundCloud",
  "provider_url": "https://soundcloud.com",
  "height": 400,
  "width": "100%",
  "title": "Flickermood by Forss",
  "description": "from the album SOULHACK",
  "thumbnail_url": "https://i1.sndcdn.com/artworks-000004732343-cq2xpn-t500x500.jpg",
  "html": "<iframe width=\"100%\" height=\"400\" scrolling=\"no\" frameborder=\"no\" src=\"https://w.soundcloud.com/player/?visual=true&url=https%3A%2F%2Fapi.soundcloud.com%2Ftracks%2F293&show_artwork=true\"></iframe>",
  "author_name": "Forss",
  "author_url": "https://soundcloud.com/forss"
}`

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test (test@example.com)"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestLookupReadsTheTitleTheUploaderAndTheTrackNumber(t *testing.T) {
	var asked string
	client := newTestClient(t, func(response http.ResponseWriter, request *http.Request) {
		asked = request.URL.Query().Get("url")
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(flickermood))
	})

	track, err := client.Lookup(context.Background(), "https://soundcloud.com/forss/flickermood")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if asked != "https://soundcloud.com/forss/flickermood" {
		t.Fatalf("asked about %q", asked)
	}
	if track.ID != "293" || track.ExternalID != "soundcloud:293" {
		t.Fatalf("track id = %q, external id = %q", track.ID, track.ExternalID)
	}
	if track.Title != "Flickermood by Forss" {
		t.Fatalf("title = %q, want the whole string oEmbed sent", track.Title)
	}
	if track.Uploader != "Forss" || track.UploaderURL != "https://soundcloud.com/forss" {
		t.Fatalf("uploader = %q at %q", track.Uploader, track.UploaderURL)
	}
	if track.ArtworkURL != "https://i1.sndcdn.com/artworks-000004732343-cq2xpn-t500x500.jpg" {
		t.Fatalf("artwork = %q", track.ArtworkURL)
	}
}

// The suffix oEmbed adds is oEmbed's own, so taking it off is not reading the
// title. It comes off only where it matches the uploader exactly.
func TestTheTitleLosesOnlyTheSuffixOEmbedAdded(t *testing.T) {
	cases := []struct {
		title    string
		uploader string
		want     string
	}{
		{"Flickermood by Forss", "Forss", "Flickermood"},
		{"PinkPantheress - Illegal (Abo Edit) by Abo", "Abo", "PinkPantheress - Illegal (Abo Edit)"},
		// The words after "by" name somebody else. They are part of what the
		// uploader called the track.
		{"Illegal by Abo", "Someone Else", "Illegal by Abo"},
		// A near miss is a miss: the suffix is compared character for character.
		{"Flickermood by forss", "Forss", "Flickermood by forss"},
		{"by Forss", "Forss", "by Forss"},
	}
	for _, testCase := range cases {
		track := Track{Title: testCase.title, Uploader: testCase.uploader}
		if got := track.TrackTitle(); got != testCase.want {
			t.Errorf("TrackTitle(%q, by %q) = %q, want %q",
				testCase.title, testCase.uploader, got, testCase.want)
		}
	}
}

// oEmbed answers for a playlist and for a profile as readily as for a track,
// and neither answer embeds a player pointed at a track. That player is what
// admits the address.
func TestAnAnswerWithNoTrackInItIsNotATrack(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"title":"Demos by Abo","author_name":"Abo",
		  "html":"<iframe src=\"https://w.soundcloud.com/player/?url=https%3A%2F%2Fapi.soundcloud.com%2Fplaylists%2F41\"></iframe>"}`))
	})

	if _, err := client.Lookup(context.Background(), "https://soundcloud.com/abo/demos"); err != ErrNotATrack {
		t.Fatalf("Lookup() error = %v, want ErrNotATrack", err)
	}
}

func TestATrackSoundCloudNoLongerPublishesIsGone(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(status)
		})
		_, err := client.Lookup(context.Background(), "https://soundcloud.com/abo/illegal")
		if err != ErrTrackGone {
			t.Fatalf("Lookup() on %d = %v, want ErrTrackGone", status, err)
		}
	}
}

func TestOnlyOneTrackUnderTheSiteIsAnAddressSchallAccepts(t *testing.T) {
	accepted := map[string]string{
		"https://soundcloud.com/abo/illegal":            "https://soundcloud.com/abo/illegal",
		"soundcloud.com/abo/illegal":                    "https://soundcloud.com/abo/illegal",
		"https://www.soundcloud.com/abo/illegal?in=x/y": "https://soundcloud.com/abo/illegal",
		"https://m.soundcloud.com/abo/illegal/":         "https://soundcloud.com/abo/illegal",
	}
	for given, want := range accepted {
		got, err := TrackPermalink(given)
		if err != nil || got != want {
			t.Errorf("TrackPermalink(%q) = %q, %v; want %q", given, got, err, want)
		}
	}

	refused := []string{
		"",
		"https://soundcloud.com/abo",
		"https://soundcloud.com/abo/sets/demos",
		"https://example.com/abo/illegal",
		"https://on.soundcloud.com/abcd",
	}
	for _, given := range refused {
		if _, err := TrackPermalink(given); err != ErrNotATrack {
			t.Errorf("TrackPermalink(%q) error = %v, want ErrNotATrack", given, err)
		}
	}
}

func TestArtworkIsFetchedOnlyWhenItIsAPicture(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/page" {
			response.Header().Set("Content-Type", "text/html")
			_, _ = response.Write([]byte("<html>not a picture</html>"))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a square"))
	})

	image, contentType, err := client.Artwork(context.Background(), client.baseURL+"/art.jpg")
	if err != nil || string(image) != "a square" || contentType != "image/jpeg" {
		t.Fatalf("Artwork() = %q, %q, %v", image, contentType, err)
	}
	if _, _, err := client.Artwork(context.Background(), client.baseURL+"/page"); err == nil {
		t.Fatal("a page served where a picture was asked for was accepted as artwork")
	}
	if image, _, err := client.Artwork(context.Background(), ""); err != nil || image != nil {
		t.Fatalf("a track with no picture = %q, %v; want nothing and no failure", image, err)
	}
}

// Every size of one picture exists; oEmbed points at whichever it likes.
func TestTheArtworkAskedForIsTheFiveHundredPixelSquare(t *testing.T) {
	cases := map[string]string{
		"https://i1.sndcdn.com/artworks-abc-large.jpg":    "https://i1.sndcdn.com/artworks-abc-t500x500.jpg",
		"https://i1.sndcdn.com/artworks-abc-t500x500.jpg": "https://i1.sndcdn.com/artworks-abc-t500x500.jpg",
		"https://i1.sndcdn.com/artworks-abc-t67x67.png":   "https://i1.sndcdn.com/artworks-abc-t500x500.png",
		"https://i1.sndcdn.com/nothing-recognisable":      "https://i1.sndcdn.com/nothing-recognisable",
		"": "",
	}
	for given, want := range cases {
		if got := largeArtwork(given); got != want {
			t.Errorf("largeArtwork(%q) = %q, want %q", given, got, want)
		}
	}
}
