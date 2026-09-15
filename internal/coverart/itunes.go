package coverart

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// NameITunes says which archive answered. It is recorded rather than assumed,
// because a cover from here was found by name and one from the Cover Art Archive
// was found by identifier, and an interface showing them must be able to say so.
const NameITunes = "itunes"

const (
	defaultITunesBaseURL = "https://itunes.apple.com"
	// itunesLimit is how many albums are considered. It is generous on purpose:
	// the answer is chosen by exact agreement rather than by rank, so a short list
	// would only hide the disagreement that makes an answer ambiguous.
	itunesLimit = 25
	// itunesArtworkSize replaces the 100×100 thumbnail the search returns. The URL
	// is the same file with the dimensions in its name, which is how the service
	// has always exposed the larger renditions.
	itunesArtworkSize = "600x600"
)

// ITunesClient finds artwork by name, for releases MusicBrainz has no picture of.
//
// It is the one artwork source that is a guess, and it is treated as one. iTunes
// is keyed by text, so asking it "who has an album called this by someone called
// that" is exactly the fuzzy agreement Schall refuses everywhere a decision is
// made. It is allowed here, and only here, because the consequence is a picture:
// nothing about a file's identity depends on it, no grader reads it, and the
// worst case is the wrong sleeve beside the right music.
//
// Even so it does not guess. An answer is taken only when the artist and the
// album agree exactly once the two are normalized, and only when every exact
// agreement points at the same picture. Two different albums that both match, or
// one that matches approximately, are no answer at all — a cover is cheap to go
// without and expensive to be quietly wrong about.
type ITunesClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewITunesClient(options Options) *ITunesClient {
	client := &ITunesClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
		httpClient: options.HTTPClient,
	}
	if client.baseURL == "" {
		client.baseURL = defaultITunesBaseURL
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return client
}

// Cover finds the artwork of one release by artist and title.
func (client *ITunesClient) Cover(ctx context.Context, artist, title string) (Artwork, error) {
	artist, title = strings.TrimSpace(artist), strings.TrimSpace(title)
	if artist == "" || title == "" {
		return Artwork{}, ErrNoArtwork
	}
	found, err := client.search(ctx, artist+" "+title)
	if err != nil {
		return Artwork{}, err
	}

	agreed := ""
	for _, album := range found {
		if !nameAgrees(album.ArtistName, artist) || !nameAgrees(album.CollectionName, title) {
			continue
		}
		artworkURL := strings.TrimSpace(album.ArtworkURL100)
		if artworkURL == "" {
			continue
		}
		if agreed != "" && agreed != artworkURL {
			// Two albums of this name by this artist, picturing different things.
			// Which one this release is, is exactly what a text search cannot say.
			return Artwork{}, ErrNoArtwork
		}
		agreed = artworkURL
	}
	if agreed == "" {
		return Artwork{}, ErrNoArtwork
	}
	return client.fetchImage(ctx, strings.Replace(agreed, "100x100", itunesArtworkSize, 1))
}

func (client *ITunesClient) search(ctx context.Context, term string) ([]itunesAlbum, error) {
	endpoint := client.baseURL + "/search?" + url.Values{
		"term":   []string{term},
		"entity": []string{"album"},
		"limit":  []string{fmt.Sprint(itunesLimit)},
	}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build iTunes request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("search iTunes for %q: %w", term, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxImageBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search iTunes for %q: HTTP %d", term, response.StatusCode)
	}
	var payload itunesSearchResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxImageBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode iTunes response: %w", err)
	}
	return payload.Results, nil
}

func (client *ITunesClient) fetchImage(ctx context.Context, artworkURL string) (Artwork, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artworkURL, nil)
	if err != nil {
		return Artwork{}, fmt.Errorf("build iTunes artwork request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return Artwork{}, fmt.Errorf("fetch iTunes artwork: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxImageBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNotFound {
		return Artwork{}, ErrNoArtwork
	}
	if response.StatusCode != http.StatusOK {
		return Artwork{}, fmt.Errorf("fetch iTunes artwork: HTTP %d", response.StatusCode)
	}
	image, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes))
	if err != nil {
		return Artwork{}, fmt.Errorf("read iTunes artwork: %w", err)
	}
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") || len(image) == 0 {
		return Artwork{}, ErrNoArtwork
	}
	return Artwork{Image: image, ContentType: contentType, Source: NameITunes}, nil
}

// nameAgrees compares two names the way a person reading them would, and no
// further. Case, punctuation and spacing differ between catalogues for the same
// album; anything beyond that is a different album with a similar name, and
// deciding between those is what this deliberately will not do.
func nameAgrees(left, right string) bool {
	return foldName(left) == foldName(right) && foldName(left) != ""
}

func foldName(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, symbol := range strings.ToLower(value) {
		if unicode.IsLetter(symbol) || unicode.IsDigit(symbol) {
			builder.WriteRune(symbol)
		}
	}
	return builder.String()
}

type itunesSearchResponse struct {
	Results []itunesAlbum `json:"results"`
}

type itunesAlbum struct {
	ArtistName     string `json:"artistName"`
	CollectionName string `json:"collectionName"`
	ArtworkURL100  string `json:"artworkUrl100"`
}
