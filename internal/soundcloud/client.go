// Package soundcloud asks SoundCloud what one of its tracks is called.
//
// SoundCloud is a website people upload music to. A great deal of the music a
// Schall user wants — edits, bootlegs, remixes nobody released — exists there
// and nowhere else, which means MusicBrainz, the catalogue Schall names music
// from, holds no row for it. Such a file arrives in the library as a path with
// a filename for a name.
//
// This package closes that gap with the one thing SoundCloud publishes without
// a key: **oEmbed**. oEmbed is a small convention a website can implement to
// describe one of its pages to another program. Asking for a track's page
// returns its title, who uploaded it, and a link to its artwork. One HTTP
// request, no registration, no credentials.
//
// **Schall never fetches the audio.** SoundCloud's terms of use forbid taking
// the stream, and nothing here does: the person uploads the file themselves,
// through the ordinary upload path, and tells Schall which track it is. What
// this package fetches is the name of the thing and its picture.
package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// defaultBaseURL is where SoundCloud answers oEmbed requests.
const defaultBaseURL = "https://soundcloud.com"

// Source is what a source identity records as the service it names. It is the
// value stored in library_file_identities.source and in artists.source, and it
// is the prefix of every external identifier this package builds.
const Source = "soundcloud"

// maxArtworkBytes bounds the picture a track's page points at. Artwork is a
// square JPEG a few hundred kilobytes at most; anything far past that is not
// the thing that was asked for, and reading it into memory is not something an
// address on somebody else's server gets to decide the size of.
const maxArtworkBytes = 8 << 20

// ErrNotATrack reports an address that is not one SoundCloud track.
//
// A profile page, a playlist, a link to the site itself: SoundCloud answers
// oEmbed for all of them, and none of them is a piece of music a file can be.
var ErrNotATrack = errors.New("that address is not a single SoundCloud track")

// ErrTrackGone reports a track SoundCloud no longer publishes. An upload the
// uploader deleted, or made private, answers this way.
var ErrTrackGone = errors.New("SoundCloud does not have that track any more")

// Track is what SoundCloud says about one track.
//
// Title is the oEmbed title exactly as it arrived, and it is stored exactly as
// it arrived. SoundCloud writes it as "<the uploader's own title> by
// <uploader>", and the uploader's own title very often holds an artist, a
// dash, and a remixer in brackets — "PinkPantheress - Illegal (Abo Edit)".
// Nothing here cuts that string up. Which half of it is an artist is a guess,
// and a guess written into a file is a wrong name that outlives the guess.
type Track struct {
	// ID is SoundCloud's own number for the track, read out of the player the
	// oEmbed response embeds.
	ID string
	// ExternalID is that number as a source identity stores it: "soundcloud:293".
	ExternalID string
	// Title is oEmbed's title, verbatim.
	Title string
	// Uploader is oEmbed's author_name: the account that published the track.
	Uploader string
	// UploaderURL is that account's page.
	UploaderURL string
	// ArtworkURL is the track's picture at 500 pixels square. Empty where the
	// track has no picture of its own.
	ArtworkURL string
	// Permalink is the address the track was looked up by.
	Permalink string
}

// TrackTitle is the title with oEmbed's own suffix taken off.
//
// oEmbed builds its title by joining the uploader's title to the uploader's
// name with the word "by". That join is SoundCloud's, not the uploader's, and
// undoing it is not reading the title: the suffix is removed only when the
// words after the last " by " are exactly the uploader's name, character for
// character. A track genuinely called "Illegal by Abo" uploaded by anybody else
// keeps its whole title, and so does one whose uploader renamed themselves
// between the upload and the question.
func (track Track) TrackTitle() string {
	suffix := " by " + track.Uploader
	if track.Uploader == "" || !strings.HasSuffix(track.Title, suffix) {
		return track.Title
	}
	stripped := strings.TrimSuffix(track.Title, suffix)
	if stripped == "" {
		return track.Title
	}
	return stripped
}

// Options configures the client.
type Options struct {
	// BaseURL is where oEmbed is asked. Empty means SoundCloud itself; a test
	// points it at its own server.
	BaseURL string
	// UserAgent identifies Schall to SoundCloud. Required, the same way
	// MusicBrainz requires one: a program asking anonymously is a program
	// nobody can ask to stop.
	UserAgent  string
	HTTPClient *http.Client
}

// Client asks SoundCloud's oEmbed endpoint about a track.
type Client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
}

func NewClient(options Options) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("parse SoundCloud base URL: %w", err)
	}
	userAgent := strings.TrimSpace(options.UserAgent)
	if userAgent == "" {
		return nil, errors.New("SoundCloud user agent is required")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: baseURL, userAgent: userAgent, httpClient: httpClient}, nil
}

// oembed is the answer SoundCloud sends. Only the fields Schall keeps are read;
// the rest of the document — the player's width, its height, the description —
// describes an embed nobody here is building.
type oembed struct {
	Title        string `json:"title"`
	AuthorName   string `json:"author_name"`
	AuthorURL    string `json:"author_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	HTML         string `json:"html"`
}

// Lookup asks SoundCloud what the track at one address is.
//
// The address is checked for shape first, because a profile page is a common
// thing to paste and refusing it before a request is a faster answer than
// refusing it after one. That check is an early refusal and not the admission:
// what admits an address is the track number in the player SoundCloud sends
// back. oEmbed answers for playlists and profiles too, and only a track's
// answer embeds a player pointed at api.soundcloud.com/tracks/<number>.
func (client *Client) Lookup(ctx context.Context, permalink string) (Track, error) {
	canonical, err := TrackPermalink(permalink)
	if err != nil {
		return Track{}, err
	}

	endpoint := client.baseURL + "/oembed?format=json&url=" + url.QueryEscape(canonical)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Track{}, fmt.Errorf("build the SoundCloud request for %s: %w", canonical, err)
	}
	request.Header.Set("User-Agent", client.userAgent)
	request.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return Track{}, fmt.Errorf("ask SoundCloud about %s: %w", canonical, err)
	}
	defer response.Body.Close()

	switch {
	// SoundCloud answers 404 for a track it never had and 403 for one that was
	// made private. Both are the same thing to a person holding the link: the
	// track is not there to be asked about.
	case response.StatusCode == http.StatusNotFound,
		response.StatusCode == http.StatusForbidden,
		response.StatusCode == http.StatusGone:
		return Track{}, ErrTrackGone
	case response.StatusCode != http.StatusOK:
		return Track{}, fmt.Errorf("ask SoundCloud about %s: status %d", canonical, response.StatusCode)
	}

	var answer oembed
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&answer); err != nil {
		return Track{}, fmt.Errorf("read SoundCloud's answer about %s: %w", canonical, err)
	}

	id, found := trackID(answer.HTML)
	if !found {
		return Track{}, ErrNotATrack
	}

	return Track{
		ID:          id,
		ExternalID:  Source + ":" + id,
		Title:       strings.TrimSpace(answer.Title),
		Uploader:    strings.TrimSpace(answer.AuthorName),
		UploaderURL: strings.TrimSpace(answer.AuthorURL),
		ArtworkURL:  largeArtwork(strings.TrimSpace(answer.ThumbnailURL)),
		Permalink:   canonical,
	}, nil
}

// Artwork fetches the picture a track's page points at.
//
// The answer has to be a picture. A page served as image/jpeg would be written
// beside the music as cover.jpg, and the folder would then hold a cover nobody
// can open — the same bargain internal/coverart makes for the same reason.
func (client *Client) Artwork(ctx context.Context, artworkURL string) ([]byte, string, error) {
	if strings.TrimSpace(artworkURL) == "" {
		return nil, "", nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artworkURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build the artwork request for %s: %w", artworkURL, err)
	}
	request.Header.Set("User-Agent", client.userAgent)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("fetch the artwork at %s: %w", artworkURL, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("fetch the artwork at %s: status %d", artworkURL, response.StatusCode)
	}
	contentType := strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0])
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("fetch the artwork at %s: it is %s, not a picture", artworkURL, contentType)
	}
	image, err := io.ReadAll(io.LimitReader(response.Body, maxArtworkBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read the artwork at %s: %w", artworkURL, err)
	}
	if len(image) == 0 {
		return nil, "", fmt.Errorf("read the artwork at %s: it is empty", artworkURL)
	}
	return image, contentType, nil
}

// playerTrack finds the track SoundCloud's embedded player is pointed at. The
// player's address is a query parameter inside the iframe, so the number
// arrives percent-encoded: api.soundcloud.com%2Ftracks%2F293.
var playerTrack = regexp.MustCompile(`api\.soundcloud\.com(?:/|%2F)tracks(?:/|%2F)([0-9]+)`)

func trackID(html string) (string, bool) {
	match := playerTrack.FindStringSubmatch(html)
	if match == nil {
		// The encoding is not guaranteed to be the one above — a response that
		// escaped the address differently is still an answer about a track.
		if unescaped, err := url.QueryUnescape(html); err == nil {
			match = playerTrack.FindStringSubmatch(unescaped)
		}
	}
	if match == nil {
		return "", false
	}
	return match[1], true
}

// artworkSize is the size token SoundCloud puts at the end of an artwork
// address: artworks-000004732343-cq2xpn-large.jpg. Every stored picture exists
// at every size, so the token is simply rewritten.
var artworkSize = regexp.MustCompile(`-(t\d+x\d+|large|original|badge|small|tiny|mini|crop)(\.[a-zA-Z]+)$`)

// largeArtwork asks for the 500-pixel square rather than whatever size the
// answer happened to name. oEmbed usually points at the large thumbnail, which
// is 100 pixels across and looks like a smudge on a phone held full-screen.
func largeArtwork(artworkURL string) string {
	if artworkURL == "" {
		return ""
	}
	if !artworkSize.MatchString(artworkURL) {
		return artworkURL
	}
	return artworkSize.ReplaceAllString(artworkURL, "-t500x500$2")
}

// TrackPermalink is the address of one track, or a refusal.
//
// A track's address is exactly two segments under the site: the uploader, then
// the track. A profile is one segment, a playlist has "sets" in the middle, and
// neither is a piece of music. Anything the address carries beyond that — a
// secret token, a share campaign, a play position — is dropped, so that the
// same track pasted from two places is one address.
func TrackPermalink(permalink string) (string, error) {
	trimmed := strings.TrimSpace(permalink)
	if trimmed == "" {
		return "", ErrNotATrack
	}
	if !strings.Contains(trimmed, "//") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", ErrNotATrack
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	// on.soundcloud.com and the mobile host serve shortened links whose path is
	// an opaque token rather than uploader and track. They are not refused
	// here; they are simply not the two-segment shape, so they never reach the
	// admission below. Somebody holding one opens it and pastes where it lands.
	if host != "soundcloud.com" && host != "m.soundcloud.com" {
		return "", ErrNotATrack
	}
	segments := make([]string, 0, 2)
	for _, segment := range strings.Split(parsed.EscapedPath(), "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	if len(segments) != 2 || segments[0] == "sets" || segments[1] == "sets" {
		return "", ErrNotATrack
	}
	return "https://soundcloud.com/" + segments[0] + "/" + segments[1], nil
}
