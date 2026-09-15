package youtube

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseVideo(t *testing.T) {
	video, err := parseVideo([]byte(`{"id":"abc","title":"A title","channel":"Channel","duration":123.6,"view_count":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if video != (Video{ID: "abc", Title: "A title", Channel: "Channel", Seconds: 124, Views: 42}) {
		t.Fatalf("video = %#v", video)
	}
	video, err = parseVideo([]byte(`{"id":"def","title":"Other","uploader":"Uploader","duration":null,"view_count":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if video != (Video{ID: "def", Title: "Other", Channel: "Uploader"}) {
		t.Fatalf("video with null fields = %#v", video)
	}
}

func TestSearchCommandAndResults(t *testing.T) {
	client := NewClient(Options{Path: "/fake/yt-dlp"})
	client.real = false
	var gotArgs []string
	client.run = func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
		gotArgs = args
		return []byte(`{"id":"one","title":"One","channel":"A","duration":20,"view_count":1}
{"id":"two","title":"Two","uploader":"B","duration":null,"view_count":null}`), nil, nil
	}
	videos, err := client.Search(context.Background(), "two words", 2)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--dump-json", "--flat-playlist", "--skip-download", "--no-warnings", "--no-playlist", "ytsearch2:two words"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if len(videos) != 2 || videos[1].Channel != "B" {
		t.Fatalf("videos = %#v", videos)
	}
}

func TestExcerptCommandRange(t *testing.T) {
	tests := []struct {
		name    string
		seconds int
		start   string
	}{
		{name: "middle", seconds: 200, start: "*85-115"},
		{name: "short", seconds: 20, start: "*0-30"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(Options{Path: "/fake/yt-dlp"})
			client.real = false
			var gotArgs []string
			client.run = func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
				gotArgs = args
				output := args[len(args)-2]
				path := strings.TrimSuffix(output, "%(id)s.%(ext)s") + "video.mp3"
				if err := os.WriteFile(path, []byte("mp3"), 0o600); err != nil {
					t.Fatal(err)
				}
				return nil, nil, nil
			}
			data, err := client.Excerpt(context.Background(), "video", test.seconds)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "mp3" {
				t.Fatalf("data = %q", data)
			}
			for index, arg := range gotArgs {
				if arg == "--download-sections" && gotArgs[index+1] != test.start {
					t.Fatalf("range = %q, want %q", gotArgs[index+1], test.start)
				}
			}
		})
	}
}

func TestErrors(t *testing.T) {
	client := NewClient(Options{Path: "/fake/yt-dlp"})
	client.real = false
	client.run = func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, []byte("ERROR: HTTP Error 429: Too Many Requests"), errors.New("exit status 1")
	}
	_, err := client.Search(context.Background(), "query", 1)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429 error = %v", err)
	}
	client = NewClient(Options{Path: "/nonexistent/yt-dlp"})
	if _, err := client.Search(context.Background(), "query", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing binary error = %v", err)
	}
}

func TestExcerptReturnsNoVideoWhenNoMP3WasWritten(t *testing.T) {
	client := NewClient(Options{Path: "/fake/yt-dlp"})
	client.real = false
	client.run = func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, nil }
	_, err := client.Excerpt(context.Background(), "missing", 20)
	if !errors.Is(err, ErrNoVideo) {
		t.Fatalf("no mp3 error = %v", err)
	}
}
