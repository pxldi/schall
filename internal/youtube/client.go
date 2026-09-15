package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnavailable = errors.New("yt-dlp is unavailable")
	ErrNoVideo     = errors.New("yt-dlp returned no video")
	ErrRateLimited = errors.New("yt-dlp was rate limited")
)

const (
	// defaultPath is what runs when no path is configured, found on PATH the
	// way fpcalc is; the image puts it in /usr/local/bin.
	defaultPath           = "yt-dlp"
	defaultSearchTimeout  = 45 * time.Second
	defaultExcerptTimeout = 120 * time.Second
)

// Video is one upload a search returned.
type Video struct {
	ID      string
	Title   string
	Channel string
	Seconds int
	Views   int64
}

type Options struct {
	// Path is the yt-dlp binary. Empty means yt-dlp on PATH.
	Path           string
	SearchTimeout  time.Duration
	ExcerptTimeout time.Duration
}

type Client struct {
	path           string
	searchTimeout  time.Duration
	excerptTimeout time.Duration
	run            func(ctx context.Context, path string, args ...string) ([]byte, []byte, error)
	real           bool
}

func NewClient(options Options) *Client {
	searchTimeout := options.SearchTimeout
	if searchTimeout <= 0 {
		searchTimeout = defaultSearchTimeout
	}
	excerptTimeout := options.ExcerptTimeout
	if excerptTimeout <= 0 {
		excerptTimeout = defaultExcerptTimeout
	}
	path := strings.TrimSpace(options.Path)
	if path == "" {
		path = defaultPath
	}
	return &Client{
		path:           path,
		searchTimeout:  searchTimeout,
		excerptTimeout: excerptTimeout,
		run:            runCommand,
		real:           true,
	}
}

func (client *Client) Available() bool {
	return client.path != "" && client.real && lookPath(client.path)
}

// Search returns up to limit uploads for a free-text query, in YouTube's order.
func (client *Client) Search(ctx context.Context, query string, limit int) ([]Video, error) {
	if !client.enabled() {
		return nil, fmt.Errorf("%w: no binary configured", ErrUnavailable)
	}
	if limit <= 0 {
		return []Video{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, client.searchTimeout)
	defer cancel()
	stdout, stderr, runErr := client.run(ctx, client.path,
		"--dump-json", "--flat-playlist", "--skip-download", "--no-warnings", "--no-playlist",
		"ytsearch"+strconv.Itoa(limit)+":"+query)
	if err := client.commandError(ctx, stderr, runErr); err != nil {
		return nil, err
	}

	videos := make([]Video, 0, limit)
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		video, err := parseVideo([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("parse yt-dlp search result: %w", err)
		}
		videos = append(videos, video)
	}
	return videos, nil
}

// Excerpt returns thirty seconds from the middle of an upload as MP3 bytes.
func (client *Client) Excerpt(ctx context.Context, id string, durationSeconds int) ([]byte, error) {
	if !client.enabled() {
		return nil, fmt.Errorf("%w: no binary configured", ErrUnavailable)
	}
	start := durationSeconds/2 - 15
	if start < 0 {
		start = 0
	}

	directory, err := os.MkdirTemp("", "schall-youtube-")
	if err != nil {
		return nil, fmt.Errorf("create yt-dlp temp directory: %w", err)
	}
	defer os.RemoveAll(directory)

	ctx, cancel := context.WithTimeout(ctx, client.excerptTimeout)
	defer cancel()
	output := filepath.Join(directory, "%(id)s.%(ext)s")
	_, stderr, runErr := client.run(ctx, client.path,
		"-f", "bestaudio", "--download-sections", fmt.Sprintf("*%d-%d", start, start+30),
		"--force-keyframes-at-cuts", "-x", "--audio-format", "mp3", "--audio-quality", "0",
		"--no-warnings", "--no-playlist", "-o", output,
		"https://www.youtube.com/watch?v="+id)
	if err := client.commandError(ctx, stderr, runErr); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read yt-dlp output: %w", err)
	}
	var mp3 string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".mp3") {
			if mp3 != "" {
				return nil, fmt.Errorf("yt-dlp returned more than one audio file")
			}
			mp3 = filepath.Join(directory, entry.Name())
		}
	}
	if mp3 == "" {
		return nil, ErrNoVideo
	}
	data, err := os.ReadFile(mp3)
	if err != nil {
		return nil, fmt.Errorf("read yt-dlp excerpt: %w", err)
	}
	return data, nil
}

func (client *Client) enabled() bool {
	return client.path != "" && client.run != nil && (!client.real || client.Available())
}

func (client *Client) commandError(ctx context.Context, stderr []byte, runErr error) error {
	if runErr == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if rateLimited(string(stderr)) {
		return fmt.Errorf("%w: %s", ErrRateLimited, firstLine(string(stderr)))
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrUnavailable, client.path)
	}
	if noVideo(string(stderr)) {
		return fmt.Errorf("%w: %s", ErrNoVideo, firstLine(string(stderr)))
	}
	return fmt.Errorf("yt-dlp failed: %w", runErr)
}

func parseVideo(data []byte) (Video, error) {
	var value struct {
		ID       string   `json:"id"`
		Title    string   `json:"title"`
		Channel  string   `json:"channel"`
		Uploader string   `json:"uploader"`
		Duration *float64 `json:"duration"`
		Views    *int64   `json:"view_count"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return Video{}, err
	}
	channel := value.Channel
	if channel == "" {
		channel = value.Uploader
	}
	video := Video{ID: value.ID, Title: value.Title, Channel: channel}
	if value.Duration != nil {
		video.Seconds = int(*value.Duration + 0.5)
	}
	if value.Views != nil {
		video.Views = *value.Views
	}
	return video, nil
}

func rateLimited(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "http error 429") ||
		strings.Contains(lower, "sign in to confirm you're not a bot") ||
		strings.Contains(lower, "too many requests")
}

func noVideo(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "video unavailable") ||
		strings.Contains(lower, "this video is unavailable") ||
		strings.Contains(lower, "private video") ||
		strings.Contains(lower, "video does not exist")
}

func lookPath(path string) bool {
	_, err := exec.LookPath(path)
	return err == nil
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	stdout, err := command.Output()
	if err == nil {
		return stdout, nil, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout, exit.Stderr, err
	}
	return stdout, nil, err
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}
