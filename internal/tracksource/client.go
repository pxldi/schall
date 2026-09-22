// Package tracksource reads the address of one track on SoundCloud, YouTube or
// Bandcamp through yt-dlp (ADR 0038 §2).
//
// A person keys a want to such an address when MusicBrainz has no recording
// for it. Lookup says what the address holds, in the service's own words, and
// Excerpt takes thirty seconds of its audio, which becomes the want's anchor.
// Fetch takes the whole track, which becomes one more copy of the want
// (ADR 0038 §6). Nothing here decides anything about a copy.
package tracksource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The services an address may name. Each is the value a key stores in
// acquisition_targets.source and library_file_identities.source.
const (
	SoundCloud = "soundcloud"
	YouTube    = "youtube"
	Bandcamp   = "bandcamp"
)

var (
	ErrUnavailable = errors.New("yt-dlp is unavailable")
	// ErrNotATrack reports an address yt-dlp does not read as one track on one
	// of the three services: a playlist, a set, an album, a profile, or a site
	// Schall does not take addresses from.
	ErrNotATrack   = errors.New("that address is not a single track on SoundCloud, YouTube or Bandcamp")
	ErrGone        = errors.New("the service no longer has that track")
	ErrRateLimited = errors.New("yt-dlp was rate limited")
)

const (
	defaultPath           = "yt-dlp"
	defaultLookupTimeout  = 45 * time.Second
	defaultExcerptTimeout = 120 * time.Second
	// A whole track can be an hour-long mix, served at the service's own pace.
	defaultFetchTimeout = 15 * time.Minute
	excerptSeconds      = 30
)

// Serves reports whether a copy's provider is one of the services Fetch takes
// a track from. A fetched copy records its service as its provider, so this is
// how the importer and the review player tell it from a peer's copy.
func Serves(provider string) bool {
	switch provider {
	case SoundCloud, YouTube, Bandcamp:
		return true
	}
	return false
}

// Track is what yt-dlp said about one address.
type Track struct {
	// Source is the service, one of the constants above.
	Source string
	// ID is the service's own identifier for the track, and ExternalID is that
	// identifier as a key stores it: "soundcloud:293".
	ID         string
	ExternalID string
	// URL is the track's page.
	URL string
	// Title and Artist are the service's own fields: its artist and track name
	// where it publishes both, and otherwise the uploader and the title as
	// uploaded. Nothing here splits a title to find an artist (ADR 0026 §4).
	Title  string
	Artist string
	// Uploader is the account that published the track, and AccountID the key
	// its artist row is found by (ADR 0026 §5).
	Uploader   string
	AccountID  string
	DurationMS int
	ArtworkURL string
}

type Options struct {
	// Path is the yt-dlp binary. Empty means yt-dlp on PATH.
	Path           string
	LookupTimeout  time.Duration
	ExcerptTimeout time.Duration
	FetchTimeout   time.Duration
}

type Client struct {
	path           string
	lookupTimeout  time.Duration
	excerptTimeout time.Duration
	fetchTimeout   time.Duration
	run            func(ctx context.Context, path string, args ...string) ([]byte, []byte, error)
	real           bool
}

func NewClient(options Options) *Client {
	lookupTimeout := options.LookupTimeout
	if lookupTimeout <= 0 {
		lookupTimeout = defaultLookupTimeout
	}
	excerptTimeout := options.ExcerptTimeout
	if excerptTimeout <= 0 {
		excerptTimeout = defaultExcerptTimeout
	}
	fetchTimeout := options.FetchTimeout
	if fetchTimeout <= 0 {
		fetchTimeout = defaultFetchTimeout
	}
	path := strings.TrimSpace(options.Path)
	if path == "" {
		path = defaultPath
	}
	return &Client{
		path:           path,
		lookupTimeout:  lookupTimeout,
		excerptTimeout: excerptTimeout,
		fetchTimeout:   fetchTimeout,
		run:            runCommand,
		real:           true,
	}
}

// Available reports whether yt-dlp can be run at all.
func (client *Client) Available() bool {
	return client.path != "" && client.real && lookPath(client.path)
}

// Lookup asks yt-dlp what one address holds.
//
// The address is admitted when yt-dlp's extractor is exactly soundcloud,
// youtube or bandcamp and the answer is one item. Those extractors read one
// track; a set, a channel tab or an album is a different extractor
// ("soundcloud:set", "youtube:tab", "Bandcamp:album") or a playlist answer, and
// is refused.
func (client *Client) Lookup(ctx context.Context, address string) (Track, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return Track{}, ErrNotATrack
	}
	if !strings.Contains(address, "//") {
		address = "https://" + address
	}
	if !client.enabled() {
		return Track{}, fmt.Errorf("%w: no binary configured", ErrUnavailable)
	}

	ctx, cancel := context.WithTimeout(ctx, client.lookupTimeout)
	defer cancel()
	stdout, stderr, runErr := client.run(ctx, client.path,
		"--dump-single-json", "--flat-playlist", "--no-playlist", "--skip-download",
		"--no-warnings", address)
	if err := client.commandError(ctx, stderr, runErr); err != nil {
		return Track{}, err
	}
	return parseTrack(stdout)
}

// Excerpt returns thirty seconds from the middle of the track as MP3 bytes. It
// takes the same slice the YouTube anchor takes (internal/youtube), so an
// excerpt from any of the services is measured the same way.
func (client *Client) Excerpt(ctx context.Context, track Track) ([]byte, error) {
	if !client.enabled() {
		return nil, fmt.Errorf("%w: no binary configured", ErrUnavailable)
	}
	if strings.TrimSpace(track.URL) == "" {
		return nil, ErrNotATrack
	}
	start := track.DurationMS/2000 - excerptSeconds/2
	if start < 0 {
		start = 0
	}

	directory, err := os.MkdirTemp("", "schall-tracksource-")
	if err != nil {
		return nil, fmt.Errorf("create yt-dlp temp directory: %w", err)
	}
	defer os.RemoveAll(directory)

	ctx, cancel := context.WithTimeout(ctx, client.excerptTimeout)
	defer cancel()
	output := filepath.Join(directory, "%(id)s.%(ext)s")
	_, stderr, runErr := client.run(ctx, client.path,
		"-f", "bestaudio", "--download-sections", fmt.Sprintf("*%d-%d", start, start+excerptSeconds),
		"--force-keyframes-at-cuts", "-x", "--audio-format", "mp3", "--audio-quality", "0",
		"--no-warnings", "--no-playlist", "-o", output, track.URL)
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
		return nil, fmt.Errorf("%w: yt-dlp wrote no audio for %s", ErrGone, track.URL)
	}
	data, err := os.ReadFile(mp3)
	if err != nil {
		return nil, fmt.Errorf("read yt-dlp excerpt: %w", err)
	}
	return data, nil
}

// Fetched is one whole track Fetch wrote to disk, as the service served it.
type Fetched struct {
	Path string
	// Name is the file's name inside the folder Fetch was given, and Extension
	// its extension, lower case and without the dot.
	Name      string
	Extension string
	SizeBytes int64
}

// Fetch takes the whole track from its address and writes it into directory
// (ADR 0038 §6). It takes the best audio the service serves and keeps it as
// served: nothing is re-encoded. A WebM stream is copied into an Ogg container
// because the library does not hold WebM files, and the Opus audio inside is
// the same bytes either way.
//
// yt-dlp writes into a folder of its own inside directory, and the finished
// file is moved out of it, so directory never holds half a track.
func (client *Client) Fetch(ctx context.Context, track Track, directory string) (Fetched, error) {
	if !client.enabled() {
		return Fetched{}, fmt.Errorf("%w: no binary configured", ErrUnavailable)
	}
	if strings.TrimSpace(track.URL) == "" {
		return Fetched{}, ErrNotATrack
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return Fetched{}, fmt.Errorf("create the fetch folder %s: %w", directory, err)
	}
	staging, err := os.MkdirTemp(directory, ".fetching-")
	if err != nil {
		return Fetched{}, fmt.Errorf("create a yt-dlp folder in %s: %w", directory, err)
	}
	defer os.RemoveAll(staging)

	ctx, cancel := context.WithTimeout(ctx, client.fetchTimeout)
	defer cancel()
	output := filepath.Join(staging, "%(id)s.%(ext)s")
	_, stderr, runErr := client.run(ctx, client.path,
		"-f", "bestaudio", "--remux-video", "webm>opus",
		"--no-warnings", "--no-playlist", "--no-mtime", "-o", output, track.URL)
	if err := client.commandError(ctx, stderr, runErr); err != nil {
		return Fetched{}, err
	}

	entries, err := os.ReadDir(staging)
	if err != nil {
		return Fetched{}, fmt.Errorf("read yt-dlp output: %w", err)
	}
	var name string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if name != "" {
			return Fetched{}, fmt.Errorf("yt-dlp wrote more than one file for %s", track.URL)
		}
		name = entry.Name()
	}
	if name == "" {
		return Fetched{}, fmt.Errorf("yt-dlp wrote no audio for %s", track.URL)
	}
	destination := filepath.Join(directory, name)
	if err := os.Rename(filepath.Join(staging, name), destination); err != nil {
		return Fetched{}, fmt.Errorf("move the fetched track into %s: %w", directory, err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		return Fetched{}, fmt.Errorf("read the fetched track %s: %w", destination, err)
	}
	return Fetched{
		Path: destination, Name: name, SizeBytes: info.Size(),
		Extension: strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")),
	}, nil
}

// answer is the part of yt-dlp's info document Schall reads.
type answer struct {
	Type        string   `json:"_type"`
	Extractor   string   `json:"extractor"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Track       string   `json:"track"`
	Artist      string   `json:"artist"`
	Artists     []string `json:"artists"`
	Uploader    string   `json:"uploader"`
	UploaderID  string   `json:"uploader_id"`
	UploaderURL string   `json:"uploader_url"`
	ChannelID   string   `json:"channel_id"`
	Duration    *float64 `json:"duration"`
	Thumbnail   string   `json:"thumbnail"`
	WebpageURL  string   `json:"webpage_url"`
}

func parseTrack(data []byte) (Track, error) {
	var value answer
	if err := json.Unmarshal(data, &value); err != nil {
		return Track{}, fmt.Errorf("parse yt-dlp answer: %w", err)
	}
	// A single item comes back as "video" or with no type at all. Anything else
	// is a list of items, or a pointer to one yt-dlp did not follow.
	if value.Type != "" && value.Type != "video" {
		return Track{}, ErrNotATrack
	}
	source := strings.ToLower(strings.TrimSpace(value.Extractor))
	switch source {
	case SoundCloud, YouTube, Bandcamp:
	default:
		return Track{}, ErrNotATrack
	}
	id := strings.TrimSpace(value.ID)
	if id == "" {
		return Track{}, ErrNotATrack
	}

	track := Track{
		Source:     source,
		ID:         id,
		ExternalID: source + ":" + id,
		URL:        strings.TrimSpace(value.WebpageURL),
		Uploader:   strings.TrimSpace(value.Uploader),
		ArtworkURL: strings.TrimSpace(value.Thumbnail),
	}
	// One video has one address, whatever list or share parameters the pasted
	// one carried, and it is the address 0037's Topic key uses.
	if source == YouTube {
		track.URL = "https://www.youtube.com/watch?v=" + id
	}
	if track.URL == "" {
		return Track{}, ErrNotATrack
	}
	if value.Duration != nil && *value.Duration > 0 {
		track.DurationMS = int(*value.Duration*1000 + 0.5)
	}

	artist := strings.TrimSpace(value.Artist)
	if artist == "" {
		named := make([]string, 0, len(value.Artists))
		for _, one := range value.Artists {
			if one = strings.TrimSpace(one); one != "" {
				named = append(named, one)
			}
		}
		artist = strings.Join(named, ", ")
	}
	title := strings.TrimSpace(value.Track)
	if artist != "" && title != "" {
		track.Artist, track.Title = artist, title
	} else {
		track.Artist, track.Title = track.Uploader, strings.TrimSpace(value.Title)
	}
	track.AccountID = accountID(source, value)
	return track, nil
}

// accountID is the key an uploader's artist row is found by, never the name.
//
// SoundCloud's is the account's page, because that is the key the oEmbed path
// already stores (internal/soundcloud), and one uploader must be one row
// whichever path named them. YouTube's is the channel id, which survives a
// renamed handle. Bandcamp's is the band's own id.
func accountID(source string, value answer) string {
	var id string
	switch source {
	case SoundCloud:
		id = strings.TrimSuffix(strings.TrimSpace(value.UploaderURL), "/")
		if id == "" && strings.TrimSpace(value.UploaderID) != "" {
			id = "user:" + strings.TrimSpace(value.UploaderID)
		}
	case YouTube:
		id = strings.TrimSpace(value.ChannelID)
		if id == "" {
			id = strings.TrimSpace(value.UploaderID)
		}
	default:
		id = strings.TrimSpace(value.UploaderID)
		if id == "" {
			id = strings.TrimSuffix(strings.TrimSpace(value.UploaderURL), "/")
		}
	}
	if id == "" {
		return ""
	}
	return source + ":" + id
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
	message := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(message, "http error 429"),
		strings.Contains(message, "too many requests"),
		strings.Contains(message, "sign in to confirm you're not a bot"):
		return fmt.Errorf("%w: %s", ErrRateLimited, firstLine(string(stderr)))
	case errors.Is(runErr, exec.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrUnavailable, client.path)
	case strings.Contains(message, "unsupported url"):
		return fmt.Errorf("%w: %s", ErrNotATrack, firstLine(string(stderr)))
	case strings.Contains(message, "video unavailable"),
		strings.Contains(message, "this video is unavailable"),
		strings.Contains(message, "private video"),
		strings.Contains(message, "video does not exist"),
		strings.Contains(message, "http error 404"),
		strings.Contains(message, "http error 410"):
		return fmt.Errorf("%w: %s", ErrGone, firstLine(string(stderr)))
	}
	return fmt.Errorf("yt-dlp failed: %w", runErr)
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
