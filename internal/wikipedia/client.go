// Package wikipedia reads the opening summary of one Wikipedia article.
//
// It is reached by article title and never by search. The title comes from the
// Wikidata entity MusicBrainz says an artist is, so the article is the article
// about that artist and not an article whose name happens to look like theirs.
// Asking Wikipedia "who is called this?" would be the fuzzy text agreement the
// rest of Schall exists to refuse, and a biography under the wrong name is
// worse than no biography at all.
//
// Nothing here decides anything. A biography is display, like a picture: no
// matching, identity or import grader can reach it.
//
// Wikipedia text is CC BY-SA. Every summary carries the address of the article
// it came from, because whatever shows the words has to name Wikipedia and link
// them.
package wikipedia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNoArticle reports that Wikipedia has no article under this title, or has
// one with nothing to say. It is an answer about the artist rather than a
// failure of the request, so a caller can record it and stop asking.
var ErrNoArticle = errors.New("Wikipedia has no article under this title")

const (
	defaultBaseURL = "https://en.wikipedia.org"
	defaultTimeout = 10 * time.Second
	// summaryPath is Wikipedia's own short description of one article: the
	// opening paragraphs as plain text, with no markup to strip and no page to
	// read.
	summaryPath = "/api/rest_v1/page/summary/"
	// maxBody is what one summary may weigh. A summary is a paragraph or two;
	// anything approaching this is not the answer that was asked for.
	maxBody = 1 << 20
)

// Summary is the opening of one Wikipedia article: the words, and the address
// of the article they came from.
type Summary struct {
	Text string
	// URL is the article a reader can open. It is part of the licence and not
	// decoration, so a summary without one is not usable and is not returned.
	URL string
}

type Options struct {
	BaseURL string
	// UserAgent identifies this installation, which Wikipedia asks for the same
	// way MusicBrainz does.
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

// Summary reads the opening of the article under this exact title.
//
// A 404 is Wikipedia saying there is no such article, which is an answer. A
// disambiguation page is Wikipedia saying the title names several things, which
// is no answer about this artist and is refused rather than shown. Anything
// else that is not a summary — the service failing, or asking to be left alone
// — is reported as an error, so a caller writes nothing down and asks again
// later.
func (client *Client) Summary(ctx context.Context, title string) (Summary, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Summary{}, ErrNoArticle
	}
	address := client.baseURL + summaryPath + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return Summary{}, fmt.Errorf("ask Wikipedia about %s: %w", title, err)
	}
	request.Header.Set("Accept", "application/json")
	if client.userAgent != "" {
		request.Header.Set("User-Agent", client.userAgent)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return Summary{}, fmt.Errorf("ask Wikipedia about %s: %w", title, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBody))
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotFound {
		return Summary{}, ErrNoArticle
	}
	if response.StatusCode != http.StatusOK {
		return Summary{}, fmt.Errorf("Wikipedia returned %s for %s", response.Status, title)
	}

	var payload summaryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxBody)).Decode(&payload); err != nil {
		return Summary{}, fmt.Errorf("read Wikipedia's answer about %s: %w", title, err)
	}
	// A disambiguation page lists everything the title could mean. It says
	// nothing about this artist, so it is the same answer as no article.
	if strings.EqualFold(strings.TrimSpace(payload.Type), "disambiguation") {
		return Summary{}, ErrNoArticle
	}
	text := strings.TrimSpace(payload.Extract)
	if text == "" {
		return Summary{}, ErrNoArticle
	}
	article := strings.TrimSpace(payload.ContentURLs.Desktop.Page)
	if article == "" {
		article = strings.TrimSpace(payload.ContentURLs.Mobile.Page)
	}
	if article == "" {
		// The words cannot be shown without the article behind them, because the
		// licence they come under requires the link.
		return Summary{}, ErrNoArticle
	}
	return Summary{Text: text, URL: article}, nil
}

// summaryResponse is the part of Wikipedia's answer that is read. Extract is
// the opening of the article as plain text; content_urls names the article a
// reader can open, which the licence makes as necessary as the words.
type summaryResponse struct {
	Type        string `json:"type"`
	Extract     string `json:"extract"`
	ContentURLs struct {
		Desktop struct {
			Page string `json:"page"`
		} `json:"desktop"`
		Mobile struct {
			Page string `json:"page"`
		} `json:"mobile"`
	} `json:"content_urls"`
}
