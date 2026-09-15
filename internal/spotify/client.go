// Package spotify is the Spotify Web API client, in the shape
// internal/musicbrainz set: net/http and encoding/json, options with test
// seams, and no dependency.
//
// One wire fact this package encodes on purpose, against the published
// documentation: the playlist object serves its first page of rows under a
// top-level "items" paging object, each row carries its track under "item",
// and the documented /tracks subresource answers 403. Later pages come from
// /playlists/{id}/items. The docs still describe the old shape; the wire wins.
package spotify

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

const (
	defaultAPIURL      = "https://api.spotify.com/v1"
	defaultAccountsURL = "https://accounts.spotify.com"
	maxResponseBody    = 8 << 20
	// retryAttempts bounds how long a 429 is obeyed before it becomes an
	// error. Spotify's Retry-After is seconds and usually single digits.
	retryAttempts = 4
)

// ErrNotFound reports that Spotify has no such entity — a fact about the
// playlist, not a failure of Schall.
var ErrNotFound = errors.New("Spotify has no such playlist")

// ErrUnauthorized reports credentials Spotify rejected. It covers an expired
// or revoked authorization, which the caller repairs by re-connecting the
// account, never by retrying.
var ErrUnauthorized = errors.New("Spotify rejected the stored credentials")

type Client struct {
	apiURL       string
	accountsURL  string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

type Options struct {
	APIURL       string
	AccountsURL  string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

func NewClient(options Options) (*Client, error) {
	apiURL := strings.TrimRight(strings.TrimSpace(options.APIURL), "/")
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	accountsURL := strings.TrimRight(strings.TrimSpace(options.AccountsURL), "/")
	if accountsURL == "" {
		accountsURL = defaultAccountsURL
	}
	for _, address := range []string{apiURL, accountsURL} {
		if _, err := url.ParseRequestURI(address); err != nil {
			return nil, fmt.Errorf("parse Spotify base URL: %w", err)
		}
	}
	if strings.TrimSpace(options.ClientID) == "" || strings.TrimSpace(options.ClientSecret) == "" {
		return nil, errors.New("Spotify client ID and secret are required")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		apiURL:       apiURL,
		accountsURL:  accountsURL,
		clientID:     strings.TrimSpace(options.ClientID),
		clientSecret: strings.TrimSpace(options.ClientSecret),
		httpClient:   httpClient,
	}, nil
}

// Scopes are what playlist ingestion needs and nothing more: the user's own
// playlists, including private and collaborative ones, and Liked Songs.
const Scopes = "playlist-read-private playlist-read-collaborative user-library-read"

// AuthorizeURL is where the user's browser goes to approve access. state is
// the CSRF token the callback must echo.
func (client *Client) AuthorizeURL(redirectURI, state string) string {
	return client.accountsURL + "/authorize?" + url.Values{
		"client_id":     {client.clientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"scope":         {Scopes},
		"state":         {state},
	}.Encode()
}

// Token is what the accounts service hands back. RefreshToken is empty on a
// refresh grant when Spotify chooses to keep the old one.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Exchange turns the authorization code from the OAuth callback into tokens.
func (client *Client) Exchange(ctx context.Context, code, redirectURI string) (Token, error) {
	return client.token(ctx, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	})
}

// Refresh trades the stored refresh token for a fresh access token.
func (client *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Token{}, ErrUnauthorized
	}
	return client.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
}

func (client *Client) token(ctx context.Context, form url.Values) (Token, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.accountsURL+"/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("create Spotify token request: %w", err)
	}
	request.SetBasicAuth(client.clientID, client.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return Token{}, fmt.Errorf("request Spotify token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Token{}, ErrUnauthorized
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Token{}, &HTTPError{StatusCode: response.StatusCode}
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return Token{}, fmt.Errorf("decode Spotify token response: %w", err)
	}
	if payload.AccessToken == "" {
		return Token{}, errors.New("Spotify token response held no access token")
	}
	return Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

// Account is whose authorization this is, for display only.
type Account struct {
	ID          string
	DisplayName string
}

func (client *Client) Me(ctx context.Context, accessToken string) (Account, error) {
	var payload struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if err := client.getJSON(ctx, accessToken, client.apiURL+"/me", &payload); err != nil {
		return Account{}, err
	}
	if payload.ID == "" {
		return Account{}, errors.New("Spotify profile response held no account")
	}
	return Account{ID: payload.ID, DisplayName: payload.DisplayName}, nil
}

// Playlist is one list as Spotify describes it, entries in playlist order.
type Playlist struct {
	ID          string
	Name        string
	Description string
	OwnerName   string
	SnapshotID  string
	Entries     []Entry
}

// Entry is one track row. Artists keeps Spotify's order — the first is the
// primary credit. A local file or an episode row comes back with Unusable set
// rather than being silently dropped, so the caller can say what it skipped.
type Entry struct {
	TrackID    string
	Artists    []string
	Title      string
	Album      string
	DurationMS int
	ISRC       string
	// Explicit is Spotify's own flag for the track. It is the one thing the
	// source says that separates the explicit recording from the clean edition
	// MusicBrainz holds beside it (docs/decisions/0032).
	Explicit bool
	Unusable bool
}

// PlaylistRevision asks only what the idempotency check needs: the name and
// the snapshot identifier, one request.
func (client *Client) PlaylistRevision(ctx context.Context, accessToken, playlistID string) (name, snapshotID string, err error) {
	var payload struct {
		Name       string `json:"name"`
		SnapshotID string `json:"snapshot_id"`
	}
	address := client.apiURL + "/playlists/" + url.PathEscape(playlistID) +
		"?fields=" + url.QueryEscape("name,snapshot_id")
	if err := client.getJSON(ctx, accessToken, address, &payload); err != nil {
		return "", "", err
	}
	return payload.Name, payload.SnapshotID, nil
}

// Playlist reads one playlist whole: metadata and every row, following the
// items paging until it runs out.
func (client *Client) Playlist(ctx context.Context, accessToken, playlistID string) (Playlist, error) {
	var head struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		SnapshotID  string `json:"snapshot_id"`
		Owner       struct {
			DisplayName string `json:"display_name"`
		} `json:"owner"`
		Items paging `json:"items"`
	}
	address := client.apiURL + "/playlists/" + url.PathEscape(playlistID)
	if err := client.getJSON(ctx, accessToken, address, &head); err != nil {
		return Playlist{}, err
	}
	playlist := Playlist{
		ID:          playlistID,
		Name:        strings.TrimSpace(head.Name),
		Description: strings.TrimSpace(head.Description),
		OwnerName:   strings.TrimSpace(head.Owner.DisplayName),
		SnapshotID:  head.SnapshotID,
	}
	if playlist.Name == "" {
		return Playlist{}, errors.New("Spotify playlist response held no name")
	}

	total := head.Items.Total
	playlist.Entries = appendRows(playlist.Entries, head.Items.Items)
	for offset := len(head.Items.Items); offset < total && len(head.Items.Items) > 0; {
		var page paging
		pageURL := fmt.Sprintf("%s/playlists/%s/items?limit=100&offset=%d",
			client.apiURL, url.PathEscape(playlistID), offset)
		if err := client.getJSON(ctx, accessToken, pageURL, &page); err != nil {
			return Playlist{}, err
		}
		if len(page.Items) == 0 {
			break
		}
		playlist.Entries = appendRows(playlist.Entries, page.Items)
		offset += len(page.Items)
		if page.Total > 0 {
			total = page.Total
		}
	}
	return playlist, nil
}

type paging struct {
	Total int   `json:"total"`
	Items []row `json:"items"`
}

// row is one playlist row as the wire serves it now: the track under "item".
type row struct {
	IsLocal bool `json:"is_local"`
	Item    *struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DurationMS  int    `json:"duration_ms"`
		Type        string `json:"type"`
		IsLocal     bool   `json:"is_local"`
		Explicit    bool   `json:"explicit"`
		ExternalIDs struct {
			ISRC string `json:"isrc"`
		} `json:"external_ids"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		Album struct {
			Name string `json:"name"`
		} `json:"album"`
	} `json:"item"`
}

func appendRows(entries []Entry, rows []row) []Entry {
	for _, item := range rows {
		track := item.Item
		if track == nil || track.Type == "episode" || item.IsLocal || track.IsLocal ||
			strings.TrimSpace(track.ID) == "" {
			entries = append(entries, Entry{Unusable: true})
			continue
		}
		artists := make([]string, 0, len(track.Artists))
		for _, artist := range track.Artists {
			if name := strings.TrimSpace(artist.Name); name != "" {
				artists = append(artists, name)
			}
		}
		entries = append(entries, Entry{
			TrackID:    strings.TrimSpace(track.ID),
			Artists:    artists,
			Title:      strings.TrimSpace(track.Name),
			Album:      strings.TrimSpace(track.Album.Name),
			DurationMS: track.DurationMS,
			ISRC:       strings.ToUpper(strings.TrimSpace(track.ExternalIDs.ISRC)),
			Explicit:   track.Explicit,
		})
	}
	return entries
}

func (client *Client) getJSON(ctx context.Context, accessToken, address string, destination any) error {
	for attempt := 0; attempt < retryAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return fmt.Errorf("create Spotify request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+accessToken)
		request.Header.Set("Accept", "application/json")

		response, err := client.httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("request Spotify: %w", err)
		}
		retry, err := decodeResponse(response, destination)
		if !retry {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay(response)):
		}
	}
	return errors.New("Spotify kept asking to slow down")
}

// decodeResponse reads one response; retry reports a 429 whose Retry-After
// should be obeyed.
func decodeResponse(response *http.Response, destination any) (retry bool, err error) {
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(destination); err != nil {
			return false, fmt.Errorf("decode Spotify response: %w", err)
		}
		return false, nil
	case http.StatusTooManyRequests:
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return false, ErrUnauthorized
	case http.StatusNotFound:
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return false, ErrNotFound
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return false, &HTTPError{StatusCode: response.StatusCode}
}

func retryDelay(response *http.Response) time.Duration {
	seconds, _ := strconv.Atoi(response.Header.Get("Retry-After"))
	if seconds < 1 {
		seconds = 2
	}
	return time.Duration(seconds) * time.Second
}

type HTTPError struct {
	StatusCode int
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("Spotify returned HTTP %d", err.StatusCode)
}

// PlaylistID digs the playlist identifier out of whatever the user pasted: a
// share URL, a spotify: URI, or the bare ID.
func PlaylistID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("a playlist link or ID is required")
	}
	if index := strings.Index(value, "playlist/"); index >= 0 {
		value = value[index+len("playlist/"):]
	} else if index := strings.Index(value, "playlist:"); index >= 0 {
		value = value[index+len("playlist:"):]
	}
	if cut := strings.IndexAny(value, "?#"); cut >= 0 {
		value = value[:cut]
	}
	if value == "" || strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		return "", fmt.Errorf("%q does not name a Spotify playlist", value)
	}
	return value, nil
}
