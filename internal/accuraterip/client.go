package accuraterip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotListed reports that nobody submitted a rip of a disc with that table
// of contents. It is an answer about the database, and it is silence about
// the copy.
var ErrNotListed = errors.New("AccurateRip holds no rip of that disc")

const (
	// Rippers ask over plain HTTP. Schall asks over HTTPS, which AccurateRip
	// also answers, because a checksum changed on the way could admit a copy.
	defaultBaseURL  = "https://www.accuraterip.com"
	defaultTimeout  = 20 * time.Second
	maxResponseBody = 1 << 20
	headerSize      = 13
	trackSize       = 9
)

// Pressing is one set of submissions for a disc: the checksum of each track
// and how many people submitted it. A disc pressed more than once with the
// same table of contents has one per pressing.
type Pressing struct {
	Tracks []Submission
}

// Submission is the checksum submitted for one track and how many rips agreed
// on it.
type Submission struct {
	Confidence int
	Checksum   uint32
}

type Options struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Client reads AccurateRip's database. It needs no key.
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

// Pressings reads what was submitted for a disc with this table of contents.
// A file listing another disc's numbers is not read: it would be checksums of
// other audio.
func (client *Client) Pressings(ctx context.Context, ids IDs) ([]Pressing, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+ids.path(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Schall")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not reach AccurateRip: %w", err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, ErrNotListed
	case response.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("AccurateRip answered %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("read the AccurateRip answer: %w", err)
	}
	if len(body) > maxResponseBody {
		return nil, errors.New("the AccurateRip answer is larger than any disc's")
	}
	return parse(body, ids)
}

func parse(body []byte, ids IDs) ([]Pressing, error) {
	var pressings []Pressing
	for len(body) > 0 {
		if len(body) < headerSize {
			return nil, errors.New("the AccurateRip answer ends inside a disc")
		}
		tracks := int(body[0])
		size := headerSize + tracks*trackSize
		if tracks == 0 || len(body) < size {
			return nil, errors.New("the AccurateRip answer ends inside a disc")
		}
		listed := IDs{
			Tracks: tracks,
			ID1:    binary.LittleEndian.Uint32(body[1:5]),
			ID2:    binary.LittleEndian.Uint32(body[5:9]),
			CDDB:   binary.LittleEndian.Uint32(body[9:13]),
		}
		if listed == ids {
			pressing := Pressing{Tracks: make([]Submission, tracks)}
			for index := range tracks {
				at := headerSize + index*trackSize
				pressing.Tracks[index] = Submission{
					Confidence: int(body[at]),
					Checksum:   binary.LittleEndian.Uint32(body[at+1 : at+5]),
				}
			}
			pressings = append(pressings, pressing)
		}
		body = body[size:]
	}
	if len(pressings) == 0 {
		return nil, ErrNotListed
	}
	return pressings, nil
}
