package tracksource

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func fakeClient(stdout, stderr string, runErr error) (*Client, *[]string) {
	client := NewClient(Options{Path: "/fake/yt-dlp"})
	client.real = false
	var gotArgs []string
	client.run = func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
		gotArgs = args
		return []byte(stdout), []byte(stderr), runErr
	}
	return client, &gotArgs
}

func TestLookupReadsASoundCloudTrackAsTheUploaderNamedIt(t *testing.T) {
	client, gotArgs := fakeClient(`{"_type":"video","extractor":"soundcloud","id":"293",
		"title":"PinkPantheress - Illegal (Abo Edit)","uploader":"Abo","uploader_id":"1234",
		"uploader_url":"https://soundcloud.com/abo/","duration":187.4,
		"thumbnail":"https://i1.sndcdn.com/artworks-x-original.jpg",
		"webpage_url":"https://soundcloud.com/abo/illegal-edit"}`, "", nil)

	track, err := client.Lookup(context.Background(), "soundcloud.com/abo/illegal-edit?si=share")
	if err != nil {
		t.Fatal(err)
	}
	want := Track{
		Source: SoundCloud, ID: "293", ExternalID: "soundcloud:293",
		URL: "https://soundcloud.com/abo/illegal-edit",
		// The title is kept whole. Which half of it is an artist is a guess.
		Title: "PinkPantheress - Illegal (Abo Edit)", Artist: "Abo", Uploader: "Abo",
		AccountID:  "soundcloud:https://soundcloud.com/abo",
		DurationMS: 187400, ArtworkURL: "https://i1.sndcdn.com/artworks-x-original.jpg",
	}
	if track != want {
		t.Fatalf("track = %#v\nwant    %#v", track, want)
	}
	wantArgs := []string{"--dump-single-json", "--flat-playlist", "--no-playlist", "--skip-download",
		"--no-warnings", "https://soundcloud.com/abo/illegal-edit?si=share"}
	if strings.Join(*gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %#v, want %#v", *gotArgs, wantArgs)
	}
}

func TestLookupUsesTheArtistAndTrackAServicePublishes(t *testing.T) {
	client, _ := fakeClient(`{"extractor":"youtube","id":"dQw4w9WgXcQ","title":"Official Audio",
		"track":"Illegal","artists":["PinkPantheress"," "],"uploader":"PinkPantheress - Topic",
		"uploader_id":"@pp","channel_id":"UCabc","duration":120,
		"webpage_url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RD"}`, "", nil)

	track, err := client.Lookup(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if err != nil {
		t.Fatal(err)
	}
	if track.Title != "Illegal" || track.Artist != "PinkPantheress" {
		t.Fatalf("named %q by %q, want the service's own track and artist", track.Title, track.Artist)
	}
	if track.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" || track.ExternalID != "youtube:dQw4w9WgXcQ" {
		t.Fatalf("key = %q at %q", track.ExternalID, track.URL)
	}
	if track.AccountID != "youtube:UCabc" {
		t.Fatalf("account = %q, want the channel id", track.AccountID)
	}
}

func TestLookupFallsBackToTheUploaderWhenOnlyOneFieldIsPublished(t *testing.T) {
	client, _ := fakeClient(`{"extractor":"Bandcamp","id":"77","title":"Artist - Song",
		"track":"Song","uploader":"Label","uploader_id":"label","duration":200,
		"webpage_url":"https://label.bandcamp.com/track/song"}`, "", nil)

	track, err := client.Lookup(context.Background(), "https://label.bandcamp.com/track/song")
	if err != nil {
		t.Fatal(err)
	}
	if track.Source != Bandcamp || track.ExternalID != "bandcamp:77" {
		t.Fatalf("key = %q %q", track.Source, track.ExternalID)
	}
	if track.Title != "Artist - Song" || track.Artist != "Label" {
		t.Fatalf("named %q by %q, want the title as uploaded and the uploader", track.Title, track.Artist)
	}
	if track.AccountID != "bandcamp:label" {
		t.Fatalf("account = %q", track.AccountID)
	}
}

func TestLookupRefusesAnythingThatIsNotOneTrack(t *testing.T) {
	answers := map[string]string{
		"a SoundCloud set":   `{"_type":"playlist","extractor":"soundcloud:set","id":"1","webpage_url":"https://soundcloud.com/a/sets/b"}`,
		"a YouTube playlist": `{"_type":"playlist","extractor":"youtube:tab","id":"PL1","webpage_url":"https://www.youtube.com/playlist?list=PL1"}`,
		"a Bandcamp album":   `{"_type":"playlist","extractor":"Bandcamp:album","id":"9","webpage_url":"https://a.bandcamp.com/album/b"}`,
		"a flat pointer":     `{"_type":"url","extractor":"youtube","id":"x","webpage_url":"https://www.youtube.com/watch?v=x"}`,
		"another site":       `{"extractor":"vimeo","id":"5","webpage_url":"https://vimeo.com/5"}`,
		"a user page":        `{"extractor":"soundcloud:user","id":"abo","webpage_url":"https://soundcloud.com/abo"}`,
		"no id":              `{"extractor":"soundcloud","id":"","webpage_url":"https://soundcloud.com/a/b"}`,
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			client, _ := fakeClient(answer, "", nil)
			if _, err := client.Lookup(context.Background(), "https://example.com/x"); !errors.Is(err, ErrNotATrack) {
				t.Fatalf("err = %v, want ErrNotATrack", err)
			}
		})
	}
}

func TestLookupClassifiesYtdlpFailures(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"ERROR: Unsupported URL: https://example.com", ErrNotATrack},
		{"ERROR: [soundcloud] 293: HTTP Error 404: Not Found", ErrGone},
		{"ERROR: [youtube] x: Video unavailable", ErrGone},
		{"ERROR: HTTP Error 429: Too Many Requests", ErrRateLimited},
	}
	for _, test := range cases {
		client, _ := fakeClient("", test.stderr, errors.New("exit status 1"))
		if _, err := client.Lookup(context.Background(), "https://soundcloud.com/a/b"); !errors.Is(err, test.want) {
			t.Fatalf("%q gave %v, want %v", test.stderr, err, test.want)
		}
	}
	client := NewClient(Options{Path: "/nonexistent/yt-dlp"})
	if _, err := client.Lookup(context.Background(), "https://soundcloud.com/a/b"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing binary = %v", err)
	}
}

func TestExcerptTakesThirtySecondsFromTheMiddle(t *testing.T) {
	cases := []struct {
		name       string
		durationMS int
		section    string
	}{
		{"middle", 200_000, "*85-115"},
		{"short", 20_000, "*0-30"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(Options{Path: "/fake/yt-dlp"})
			client.real = false
			var gotArgs []string
			client.run = func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
				gotArgs = args
				output := args[len(args)-2]
				path := strings.TrimSuffix(output, "%(id)s.%(ext)s") + "293.mp3"
				if err := os.WriteFile(path, []byte("mp3"), 0o600); err != nil {
					t.Fatal(err)
				}
				return nil, nil, nil
			}
			data, err := client.Excerpt(context.Background(), Track{
				URL: "https://soundcloud.com/abo/illegal-edit", DurationMS: test.durationMS,
			})
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "mp3" {
				t.Fatalf("data = %q", data)
			}
			if gotArgs[len(gotArgs)-1] != "https://soundcloud.com/abo/illegal-edit" {
				t.Fatalf("fetched %q", gotArgs[len(gotArgs)-1])
			}
			for index, arg := range gotArgs {
				if arg == "--download-sections" && gotArgs[index+1] != test.section {
					t.Fatalf("section = %q, want %q", gotArgs[index+1], test.section)
				}
			}
		})
	}
}

func TestExcerptWithNoAudioWrittenIsGone(t *testing.T) {
	client, _ := fakeClient("", "", nil)
	if _, err := client.Excerpt(context.Background(), Track{URL: "https://soundcloud.com/a/b"}); !errors.Is(err, ErrGone) {
		t.Fatalf("err = %v", err)
	}
}
