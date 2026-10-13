// Package applemusic reads the preview Apple publishes for a track, by Apple's
// own identifier for it.
//
// Apple's public lookup service answers for a song or an album by number, with
// no key, and gives each song a thirty-second preview. Schall asks it only for
// the numbers MusicBrainz links a recording or a release to, so the audio that
// comes back belongs to a track an editor tied to the recording. A text search
// is not offered here, for the reason internal/deezer gives.
package applemusic

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

// ErrNotFound reports that Apple has nothing under that number in that
// storefront. It is an answer about the catalogue.
var ErrNotFound = errors.New("Apple has nothing under that number")

// ErrNoPreview reports a song Apple holds and publishes no preview of.
var ErrNoPreview = errors.New("Apple publishes no preview of that song")

// ErrRateLimited reports Apple refusing because it is asked too fast. It is a
// fact about the minute.
var ErrRateLimited = errors.New("Apple is rate limiting requests")

const (
	defaultBaseURL  = "https://itunes.apple.com"
	defaultTimeout  = 20 * time.Second
	maxResponseBody = 2 << 20
	maxPreviewBytes = 8 << 20
)

// Song is one song as Apple lists it.
type Song struct {
	ID           int64
	CollectionID int64
	Title        string
	Disc         int
	Track        int
	DurationMS   int
	PreviewURL   string
}

// Album is one album as Apple lists it: how many tracks it holds and the songs
// the lookup returned, in Apple's order.
type Album struct {
	Tracks int
	Songs  []Song
}

type Options struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Client talks to Apple's public lookup service. It needs no key.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(options Options) *Client {
	client := &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
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

// Song reads one song by Apple's number for it, in one storefront. An empty
// storefront is Apple's default.
func (client *Client) Song(ctx context.Context, id int64, storefront string) (Song, error) {
	results, err := client.lookup(ctx, id, storefront, false)
	if err != nil {
		return Song{}, err
	}
	for _, item := range results {
		if item.WrapperType == "track" && item.TrackID == id {
			return item.song(), nil
		}
	}
	return Song{}, ErrNotFound
}

// Album reads one album and its songs by Apple's number for the album.
func (client *Client) Album(ctx context.Context, id int64, storefront string) (Album, error) {
	results, err := client.lookup(ctx, id, storefront, true)
	if err != nil {
		return Album{}, err
	}
	var album Album
	found := false
	for _, item := range results {
		switch {
		case item.WrapperType == "collection" && item.CollectionID == id:
			album.Tracks, found = item.TrackCount, true
		case item.WrapperType == "track" && item.CollectionID == id:
			album.Songs = append(album.Songs, item.song())
		}
	}
	if !found {
		return Album{}, ErrNotFound
	}
	return album, nil
}

// FetchPreview downloads a song's preview.
func (client *Client) FetchPreview(ctx context.Context, previewURL string) ([]byte, error) {
	if strings.TrimSpace(previewURL) == "" {
		return nil, ErrNoPreview
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, previewURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not reach Apple: %w", err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return nil, ErrRateLimited
	case response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: the preview address is no longer valid", ErrNoPreview)
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, fmt.Errorf("Apple returned HTTP %d for the preview", response.StatusCode)
	}
	audio, err := io.ReadAll(io.LimitReader(response.Body, maxPreviewBytes))
	if err != nil {
		return nil, fmt.Errorf("could not read the preview: %w", err)
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("%w: the preview was empty", ErrNoPreview)
	}
	return audio, nil
}

type item struct {
	WrapperType     string `json:"wrapperType"`
	CollectionID    int64  `json:"collectionId"`
	TrackID         int64  `json:"trackId"`
	TrackName       string `json:"trackName"`
	TrackCount      int    `json:"trackCount"`
	DiscNumber      int    `json:"discNumber"`
	TrackNumber     int    `json:"trackNumber"`
	TrackTimeMillis int    `json:"trackTimeMillis"`
	PreviewURL      string `json:"previewUrl"`
}

func (value item) song() Song {
	return Song{
		ID: value.TrackID, CollectionID: value.CollectionID,
		Title: strings.TrimSpace(value.TrackName), Disc: value.DiscNumber, Track: value.TrackNumber,
		DurationMS: value.TrackTimeMillis, PreviewURL: strings.TrimSpace(value.PreviewURL),
	}
}

func (client *Client) lookup(ctx context.Context, id int64, storefront string, songs bool) ([]item, error) {
	if id <= 0 {
		return nil, ErrNotFound
	}
	parameters := url.Values{"id": []string{strconv.FormatInt(id, 10)}}
	if storefront = strings.ToLower(strings.TrimSpace(storefront)); storefront != "" {
		parameters.Set("country", storefront)
	}
	if songs {
		parameters.Set("entity", "song")
		parameters.Set("limit", "200")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		client.baseURL+"/lookup?"+parameters.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not reach Apple: %w", err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden:
		return nil, ErrRateLimited
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, fmt.Errorf("Apple returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("could not read Apple's answer: %w", err)
	}
	var payload struct {
		Results []item `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("could not read Apple's answer: %w", err)
	}
	if len(payload.Results) == 0 {
		return nil, ErrNotFound
	}
	return payload.Results, nil
}
