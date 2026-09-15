// Package lrclib fetches the words of a song from LRCLIB.
//
// LRCLIB is a free, open, keyless public database of song lyrics, timed and
// untimed. It is asked one question — "what are the words to this exact
// recording" — and answers only when the recording it holds runs to the same
// length as the one being asked about, within two seconds.
//
// That tolerance is the reason this service fits here. Schall refuses fuzzy
// agreement everywhere it decides anything, and a lyrics service that answered
// on a title match alone would hand back the words of a different song with the
// same name. LRCLIB independently arrived at duration-as-evidence, and its two
// seconds are stricter than this project's own five.
//
// Nothing here is evidence all the same. Lyrics are display: they are read by a
// phone and by a person, never by matching, by identity, or by any import
// grader, and no answer from this package reaches a decision store.
package lrclib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNoLyrics reports that LRCLIB has no words for this recording. It is an
// answer about the song rather than a failure, so a caller may act on it and
// move on.
var ErrNoLyrics = errors.New("LRCLIB has no lyrics for this recording")

const (
	defaultBaseURL = "https://lrclib.net"
	defaultTimeout = 10 * time.Second
	// maxBodyBytes bounds one answer. A synced lyric file for a long song is a
	// few tens of kilobytes; anything approaching this is not lyrics.
	maxBodyBytes = 1 << 20
)

// Song is what LRCLIB is asked about: the catalogue's own account of a
// recording, never the file's tags.
//
// The distinction matters. A file's tags are whatever a peer typed, and a
// misspelt artist would either find nothing or find somebody else's song. The
// catalogue's account came from MusicBrainz through an identity Schall already
// proved, so it is the one description of this recording that is known to be
// right.
type Song struct {
	Artist   string
	Title    string
	Album    string
	Duration time.Duration
}

// Lyrics are the words, in the two forms LRCLIB keeps them.
//
// Instrumental is an answer, not an absence: it says this recording has no words
// at all. A caller that treats it as a miss will ask again forever about a song
// that will never have any.
type Lyrics struct {
	Synced       string
	Plain        string
	Instrumental bool
}

// Timed reports whether these lyrics carry timing a player can follow along
// with. Plain words are worth writing too, but only a synced file scrolls.
func (lyrics Lyrics) Timed() bool { return strings.TrimSpace(lyrics.Synced) != "" }

type Options struct {
	BaseURL string
	// UserAgent identifies this installation, which LRCLIB's own documentation
	// asks clients to send so it can tell software apart from scrapers.
	UserAgent  string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
}

func NewClient(options Options) *Client {
	client := &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
		userAgent:  strings.TrimSpace(options.UserAgent),
		httpClient: options.HTTPClient,
	}
	if client.baseURL == "" {
		client.baseURL = defaultBaseURL
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return client
}

// answer is LRCLIB's reply, in its own words.
type answer struct {
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// Get asks for the words to one recording.
//
// The duration is sent as well as the names, because it is what LRCLIB checks
// the answer against. Without it the service falls back to matching on the names
// alone, which is the fuzzy agreement this codebase refuses — so a song whose
// length Schall does not know is not asked about at all.
func (client *Client) Get(ctx context.Context, song Song) (Lyrics, error) {
	if strings.TrimSpace(song.Artist) == "" || strings.TrimSpace(song.Title) == "" {
		return Lyrics{}, ErrNoLyrics
	}
	if song.Duration <= 0 {
		return Lyrics{}, ErrNoLyrics
	}

	query := url.Values{}
	query.Set("artist_name", song.Artist)
	query.Set("track_name", song.Title)
	if album := strings.TrimSpace(song.Album); album != "" {
		query.Set("album_name", album)
	}
	query.Set("duration", strconv.Itoa(int(song.Duration.Round(time.Second)/time.Second)))

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		client.baseURL+"/api/get?"+query.Encode(), nil)
	if err != nil {
		return Lyrics{}, fmt.Errorf("build lyrics request: %w", err)
	}
	if client.userAgent != "" {
		request.Header.Set("User-Agent", client.userAgent)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return Lyrics{}, fmt.Errorf("ask LRCLIB about %s - %s: %w", song.Artist, song.Title, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBodyBytes))
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotFound {
		return Lyrics{}, ErrNoLyrics
	}
	if response.StatusCode != http.StatusOK {
		return Lyrics{}, fmt.Errorf("ask LRCLIB about %s - %s: HTTP %d",
			song.Artist, song.Title, response.StatusCode)
	}

	var reply answer
	if err := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes)).Decode(&reply); err != nil {
		return Lyrics{}, fmt.Errorf("read LRCLIB's answer about %s - %s: %w",
			song.Artist, song.Title, err)
	}
	lyrics := Lyrics{
		Synced:       strings.TrimSpace(reply.SyncedLyrics),
		Plain:        strings.TrimSpace(reply.PlainLyrics),
		Instrumental: reply.Instrumental,
	}
	if lyrics.Instrumental {
		return lyrics, nil
	}
	if lyrics.Synced == "" && lyrics.Plain == "" {
		// A row with neither kind of words is the same as no row: there is
		// nothing to write beside the music.
		return Lyrics{}, ErrNoLyrics
	}
	return lyrics, nil
}
