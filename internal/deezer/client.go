// Package deezer fetches the thirty-second preview a distributor publishes for
// a recording.
//
// Deezer is a music streaming service. Alongside the tracks it sells it
// publishes, for most of them, a thirty-second excerpt as a plain audio file on
// a public address with no key needed. Schall wants that excerpt for one reason
// only: it is audio of a known recording, so a copy fetched from a stranger can
// be compared against it.
//
// The lookup is by ISRC — the registration code a recording is issued when it
// is published, twelve characters, one recording. It is an exact key and not a
// text search, which is what makes the returned audio trustworthy as a
// reference. A text search is not offered here on purpose: an earlier
// experiment searched Apple's catalogue for "Ivy" and was answered with "White
// Ferrari", and a wrong reference makes every judgement after it confidently
// wrong.
//
// Nothing here identifies anything. It returns a track and its preview audio;
// internal/chromaprint measures, and the caller applies the rule in
// docs/decisions/0024-a-distributors-preview-is-an-audio-anchor.md.
package deezer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNoTrack reports that Deezer has nothing registered under that ISRC. It is
// an answer, not a failure: the recording is not in their catalogue, and asking
// again will give the same answer.
var ErrNoTrack = errors.New("Deezer has no track registered under that ISRC")

// ErrNoPreview reports that Deezer holds the track but publishes no excerpt of
// it. There is no audio to compare against, so nothing can be proven and
// nothing can be refused. It is silence, never disagreement.
var ErrNoPreview = errors.New("Deezer publishes no preview of that track")

// ErrRateLimited reports that Deezer refused because it is being asked too
// fast. It is a fact about the minute, not about the recording: nothing was
// learned, and the same question later is answerable. It is separate so that a
// caller can wait rather than record a preview as absent.
var ErrRateLimited = errors.New("Deezer is rate limiting requests")

// ErrNotAnISRC reports that the code handed in is not shaped like an ISRC. It
// is checked before the request is built because the code is put in the URL
// path.
var ErrNotAnISRC = errors.New("the value is not an ISRC")

const (
	defaultBaseURL = "https://api.deezer.com/2.0"
	defaultTimeout = 20 * time.Second
	// A preview is thirty seconds of MP3, which is well under a megabyte. The
	// cap is generous enough never to cut a real one short and small enough
	// that a wrong address cannot fill memory.
	maxPreviewBytes = 8 << 20
	maxResponseBody = 1 << 20
	// The code Deezer answers with when it holds nothing for the key asked
	// about. It arrives inside a body with HTTP 200, which is why the body is
	// read before the status is believed.
	dataExceptionCode = 800
	quotaCode         = 4
)

// Track is what Deezer holds for one recording. Only the fields Schall reads
// are kept: enough to fetch the excerpt and enough to see, in a log or in the
// review queue, which recording the reference came from.
type Track struct {
	ID              int64
	Title           string
	DurationSeconds int
	// PreviewURL is signed and expires within days. It is fetched once and
	// never stored, which is why the anchor kept on a want is the fingerprint
	// of the audio rather than the address it came from.
	PreviewURL string
}

// Options configures a Client. The zero value is usable.
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Client talks to Deezer's public API. It needs no key.
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

// TrackByISRC asks Deezer which track carries that registration code.
func (client *Client) TrackByISRC(ctx context.Context, isrc string) (Track, error) {
	code, err := normalizeISRC(isrc)
	if err != nil {
		return Track{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		client.baseURL+"/track/isrc:"+code, nil)
	if err != nil {
		return Track{}, err
	}
	request.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return Track{}, fmt.Errorf("could not reach Deezer: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests {
		return Track{}, ErrRateLimited
	}
	if response.StatusCode == http.StatusNotFound {
		return Track{}, ErrNoTrack
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Track{}, fmt.Errorf("Deezer returned HTTP %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return Track{}, fmt.Errorf("could not read Deezer's answer: %w", err)
	}

	var payload struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Duration int    `json:"duration"`
		Preview  string `json:"preview"`
		Error    *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Track{}, fmt.Errorf("could not read Deezer's answer: %w", err)
	}

	// Deezer reports a missing key and an exceeded quota the same way, inside a
	// body sent with HTTP 200, so the two are told apart by the code.
	if payload.Error != nil {
		switch payload.Error.Code {
		case dataExceptionCode:
			return Track{}, ErrNoTrack
		case quotaCode:
			return Track{}, ErrRateLimited
		default:
			return Track{}, fmt.Errorf("Deezer refused: %s", strings.TrimSpace(payload.Error.Message))
		}
	}
	if payload.ID == 0 {
		return Track{}, ErrNoTrack
	}
	if strings.TrimSpace(payload.Preview) == "" {
		return Track{}, ErrNoPreview
	}

	return Track{
		ID:              payload.ID,
		Title:           strings.TrimSpace(payload.Title),
		DurationSeconds: payload.Duration,
		PreviewURL:      strings.TrimSpace(payload.Preview),
	}, nil
}

// FetchPreview downloads the excerpt itself. The address comes from
// TrackByISRC and is not built here, because it is signed.
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
		return nil, fmt.Errorf("could not reach Deezer: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
		// The address is signed and expires, so a refusal here means it went
		// stale rather than that the preview never existed.
		return nil, fmt.Errorf("%w: the preview address is no longer valid", ErrNoPreview)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Deezer returned HTTP %d for the preview", response.StatusCode)
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

// normalizeISRC checks the shape of a registration code and upper-cases it. An
// ISRC is twelve characters: two for the country, three for the registrant, two
// digits for the year and five for the recording. Deezer accepts it with or
// without the hyphens people write it with, so they are removed here.
func normalizeISRC(isrc string) (string, error) {
	var built strings.Builder
	for _, letter := range strings.TrimSpace(isrc) {
		switch {
		case letter >= 'a' && letter <= 'z':
			built.WriteRune(letter - 'a' + 'A')
		case letter >= 'A' && letter <= 'Z', letter >= '0' && letter <= '9':
			built.WriteRune(letter)
		case letter == '-':
		default:
			return "", fmt.Errorf("%w: %q holds a character an ISRC cannot", ErrNotAnISRC, isrc)
		}
	}
	code := built.String()
	if len(code) != 12 {
		return "", fmt.Errorf("%w: %q is %d characters, not 12", ErrNotAnISRC, isrc, len(code))
	}
	return code, nil
}
