// Package listenbrainz reads an account's listening history and what
// ListenBrainz has built from it.
//
// It answers in MusicBrainz recording IDs and nothing else, which is the whole
// reason it is the source (ADR 0015): every one of the five
// suppression rules is a promise phrased in identifiers, and a promise kept by a
// name resolver is kept at the resolver's accuracy rather than absolutely.
//
// Nothing here knows what a suppression rule is. The client returns
// identifiers, scores and the material a reason is written from; what may be
// shown is decided above it, against Schall's own tables. It persists nothing
// either — a recommendation is not a permanent fact the way a merge is, so
// there is no cache here and the candidate store is the only one.
package listenbrainz

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
	"sync"
	"time"
)

const (
	// Name identifies the source in API responses and logs.
	Name = "listenbrainz"

	// DefaultBaseURL and DefaultLabsURL are the hosted service. Unlike slskd or
	// Navidrome this address has a right answer, so it is a default rather than
	// something every installation has to be told.
	DefaultBaseURL = "https://api.listenbrainz.org"
	DefaultLabsURL = "https://labs.api.listenbrainz.org"

	// DefaultSimilarityAlgorithm is what an installation that has never chosen
	// one starts from. It is a suggestion and not the setting: the labs
	// algorithm names are a closed, unversioned enum the dataset host may rename
	// between deployments, which is why the chosen one is stored on the settings
	// row and read from there on every call. Nothing consults this constant once
	// a row exists, so a rename upstream stays a settings edit rather than a
	// release.
	DefaultSimilarityAlgorithm = "session_based_days_7500_session_300_" +
		"contribution_5_threshold_15_limit_50_skip_30"

	maxResponseBody    = 4 << 20
	defaultHTTPTimeout = 10 * time.Second
)

// Options configures a client. UserAgent is required; MetaBrainz asks every
// caller to identify itself and refuses the ones that do not.
//
// UserToken is optional and buys nothing but a higher rate limit: every call
// this client makes is a public read, and Schall never writes to ListenBrainz.
type Options struct {
	BaseURL    string
	LabsURL    string
	UserAgent  string
	UserToken  string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    string
	labsURL    string
	userAgent  string
	userToken  string
	httpClient *http.Client

	// mu guards the published budgets.
	//
	// One budget per host, because the labs dataset host is a separate rate-limit
	// domain: a labs reply saying 49 requests remain would otherwise erase a
	// just-observed zero on the API host, and the next call would go out inside a
	// window that host has already refused.
	mu      sync.Mutex
	budgets map[string]*budget
}

// budget is what one host last said was left of the current window.
type budget struct {
	remaining int
	resetAt   time.Time
}

func NewClient(options Options) (*Client, error) {
	baseURL, err := endpointURL(options.BaseURL, DefaultBaseURL, "base")
	if err != nil {
		return nil, err
	}
	labsURL, err := endpointURL(options.LabsURL, DefaultLabsURL, "labs")
	if err != nil {
		return nil, err
	}

	userAgent := strings.TrimSpace(options.UserAgent)
	if userAgent == "" {
		return nil, errors.New("listenbrainz user agent is required")
	}

	client := &Client{
		baseURL:    baseURL,
		labsURL:    labsURL,
		userAgent:  userAgent,
		userToken:  strings.TrimSpace(options.UserToken),
		httpClient: options.HTTPClient,
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return client, nil
}

func endpointURL(value, fallback, what string) (string, error) {
	address := strings.TrimRight(strings.TrimSpace(value), "/")
	if address == "" {
		address = fallback
	}
	parsed, err := url.ParseRequestURI(address)
	if err != nil {
		return "", fmt.Errorf("parse listenbrainz %s URL: %w", what, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("listenbrainz %s URL must use http or https", what)
	}
	return address, nil
}

func (client *Client) Name() string { return Name }

// Status is the outcome of a bounded connection check.
type Status struct {
	Detail string
}

// CheckConnection reports whether ListenBrainz is reachable and knows the
// account. Failures carry a message the settings page can show without anybody
// reading the server log.
//
// An account that exists but has no computed model is a success, not a failure.
// The service is saying it has not got round to this user yet, which is a thing
// to tell them rather than a thing to retry — and it is exactly the case a
// freshly connected account is in.
func (client *Client) CheckConnection(ctx context.Context, username string) (Status, error) {
	recommendations, err := client.Recommendations(ctx, username, 1, 0)
	if errors.Is(err, ErrNoRecommendations) {
		return Status{Detail: fmt.Sprintf(
			"%s exists; ListenBrainz has not built a recommendation model for it yet", username,
		)}, nil
	}
	if err != nil {
		return Status{}, client.describe(ctx, username, err)
	}

	detail := fmt.Sprintf("%d recommendations for %s", recommendations.Total, username)
	if recommendations.ModelID != "" {
		detail += ", model " + recommendations.ModelID
	}
	if !recommendations.LastUpdated.IsZero() {
		detail += ", computed " + recommendations.LastUpdated.UTC().Format("2 Jan 2006")
	}
	return Status{Detail: detail}, nil
}

// Recommendation is one entry of the collaborative-filtered list.
type Recommendation struct {
	RecordingMBID string
	Score         float64
	// LatestListenedAt is when the user last scrobbled this recording, and is
	// frequently set: the raw list includes music they have already heard. That
	// is not a sixth suppression rule — PRODUCT.md's rule 1 is about the
	// library, and a recording heard but not owned is what this feature is for
	// — but it is the reason the list must never be called "music you have not
	// heard". Zero when the user has never listened to it.
	LatestListenedAt time.Time
}

// Recommendations is one page of the collaborative-filtered list, with the
// provenance of the batch it came from.
//
// ModelID and LastUpdated say which batch this is. They belong on the candidate
// row rather than in a log line: "suggested by model X computed on date Y" is
// the difference between an explainable list and a magic one.
type Recommendations struct {
	Recordings  []Recommendation
	ModelID     string
	LastUpdated time.Time
	Total       int
	Offset      int
}

// Recommendations asks for the account's collaborative-filtered recordings.
//
// The set is precomputed in batch and capped by the service, so a count larger
// than it holds is clamped rather than refused, and Total is what is actually
// there.
func (client *Client) Recommendations(
	ctx context.Context, username string, count, offset int,
) (Recommendations, error) {
	if strings.TrimSpace(username) == "" {
		return Recommendations{}, errors.New("listenbrainz username is required")
	}
	query := url.Values{}
	if count > 0 {
		query.Set("count", strconv.Itoa(count))
	}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}

	var payload struct {
		Payload struct {
			MBIDs []struct {
				RecordingMBID    string       `json:"recording_mbid"`
				Score            float64      `json:"score"`
				LatestListenedAt nullableTime `json:"latest_listened_at"`
			} `json:"mbids"`
			LastUpdated    int64  `json:"last_updated"`
			ModelID        string `json:"model_id"`
			TotalMBIDCount int    `json:"total_mbid_count"`
			Offset         int    `json:"offset"`
		} `json:"payload"`
	}
	path := "/1/cf/recommendation/user/" + url.PathEscape(username) + "/recording"
	if err := client.get(ctx, client.baseURL, path, query, ErrNoRecommendations, &payload); err != nil {
		return Recommendations{}, err
	}

	result := Recommendations{
		Recordings: make([]Recommendation, 0, len(payload.Payload.MBIDs)),
		ModelID:    strings.TrimSpace(payload.Payload.ModelID),
		Total:      payload.Payload.TotalMBIDCount,
		Offset:     payload.Payload.Offset,
	}
	if payload.Payload.LastUpdated > 0 {
		result.LastUpdated = time.Unix(payload.Payload.LastUpdated, 0).UTC()
	}
	for _, entry := range payload.Payload.MBIDs {
		if strings.TrimSpace(entry.RecordingMBID) == "" {
			continue
		}
		result.Recordings = append(result.Recordings, Recommendation{
			RecordingMBID:    strings.TrimSpace(entry.RecordingMBID),
			Score:            entry.Score,
			LatestListenedAt: entry.LatestListenedAt.Time,
		})
	}
	return result, nil
}

// TopArtist is one entry of the account's own most-listened artists.
type TopArtist struct {
	ArtistMBID  string
	Name        string
	ListenCount int
}

// TopArtists asks for the account's most-listened artists over a range.
//
// The statistics endpoints are documented and non-experimental, which is why
// they are the seed half of a refresh: they answer the same "no data yet" way
// the recommendation endpoint does, and never with a name Schall would have to
// resolve.
func (client *Client) TopArtists(ctx context.Context, username, statsRange string) ([]TopArtist, error) {
	var payload struct {
		Payload struct {
			Artists []struct {
				ArtistMBID  string `json:"artist_mbid"`
				ArtistName  string `json:"artist_name"`
				ListenCount int    `json:"listen_count"`
			} `json:"artists"`
		} `json:"payload"`
	}
	if err := client.stats(ctx, username, "artists", statsRange, &payload); err != nil {
		return nil, err
	}

	artists := make([]TopArtist, 0, len(payload.Payload.Artists))
	for _, entry := range payload.Payload.Artists {
		if strings.TrimSpace(entry.ArtistMBID) == "" {
			continue
		}
		artists = append(artists, TopArtist{
			ArtistMBID:  strings.TrimSpace(entry.ArtistMBID),
			Name:        strings.TrimSpace(entry.ArtistName),
			ListenCount: entry.ListenCount,
		})
	}
	return artists, nil
}

// TopRecording is one entry of the account's own most-listened recordings.
type TopRecording struct {
	RecordingMBID string
	Name          string
	ArtistName    string
	ListenCount   int
}

// TopRecordings asks for the account's most-listened recordings over a range.
func (client *Client) TopRecordings(ctx context.Context, username, statsRange string) ([]TopRecording, error) {
	var payload struct {
		Payload struct {
			Recordings []struct {
				RecordingMBID string `json:"recording_mbid"`
				TrackName     string `json:"track_name"`
				ArtistName    string `json:"artist_name"`
				ListenCount   int    `json:"listen_count"`
			} `json:"recordings"`
		} `json:"payload"`
	}
	if err := client.stats(ctx, username, "recordings", statsRange, &payload); err != nil {
		return nil, err
	}

	recordings := make([]TopRecording, 0, len(payload.Payload.Recordings))
	for _, entry := range payload.Payload.Recordings {
		if strings.TrimSpace(entry.RecordingMBID) == "" {
			continue
		}
		recordings = append(recordings, TopRecording{
			RecordingMBID: strings.TrimSpace(entry.RecordingMBID),
			Name:          strings.TrimSpace(entry.TrackName),
			ArtistName:    strings.TrimSpace(entry.ArtistName),
			ListenCount:   entry.ListenCount,
		})
	}
	return recordings, nil
}

func (client *Client) stats(ctx context.Context, username, statistic, statsRange string, into any) error {
	if strings.TrimSpace(username) == "" {
		return errors.New("listenbrainz username is required")
	}
	query := url.Values{}
	if trimmed := strings.TrimSpace(statsRange); trimmed != "" {
		query.Set("range", trimmed)
	}
	path := "/1/stats/user/" + url.PathEscape(username) + "/" + statistic
	return client.get(ctx, client.baseURL, path, query, ErrNoRecommendations, into)
}

// SimilarRecording is one hop away from a seed recording.
//
// It deliberately carries no artist identifier. The labs host returned
// artist_credit_mbids as null on a well-known recording, so its answer is
// authoritative for recording_mbid and for nothing else: a suppression decision
// must not rest on a field the source leaves empty at its own discretion, and
// artist and release-group identifiers are resolved through internal/musicbrainz
// instead. Leaving the field off the struct is what stops that being a matter of
// care.
type SimilarRecording struct {
	RecordingMBID string
	Name          string
	// ArtistCreditName is display text and is never an identifier. It exists so
	// a reason can be written in words, not so anything can be looked up by it.
	ArtistCreditName string
	ReleaseMBID      string
	ReleaseName      string
	Score            float64
	// ReferenceMBID is the seed this recording was reached from, which is the
	// reason code for free: "similar to <recording you listen to>".
	ReferenceMBID string
}

// SimilarRecordings asks the labs dataset host for one similarity hop from the
// given seeds.
//
// algorithm is a closed, unversioned enum: an unlisted value is refused with the
// permitted list in the body, and the permitted list is a deployment detail of
// the labs host rather than an API contract. It is therefore passed in from
// settings rather than fixed here, so a rename upstream is a settings edit.
func (client *Client) SimilarRecordings(
	ctx context.Context, recordingMBIDs []string, algorithm string,
) ([]SimilarRecording, error) {
	seeds := make([]string, 0, len(recordingMBIDs))
	for _, mbid := range recordingMBIDs {
		if trimmed := strings.TrimSpace(mbid); trimmed != "" {
			seeds = append(seeds, trimmed)
		}
	}
	if len(seeds) == 0 {
		return nil, errors.New("at least one recording MBID is required")
	}
	if strings.TrimSpace(algorithm) == "" {
		return nil, errors.New("listenbrainz similarity algorithm is required")
	}

	query := url.Values{}
	query.Set("recording_mbids", strings.Join(seeds, ","))
	query.Set("algorithm", strings.TrimSpace(algorithm))

	// The labs host answers with a flat array rather than the payload envelope
	// the main API uses; it is a different service that happens to share a name.
	var payload []struct {
		RecordingMBID    string  `json:"recording_mbid"`
		RecordingName    string  `json:"recording_name"`
		ArtistCreditName string  `json:"artist_credit_name"`
		ReleaseMBID      string  `json:"release_mbid"`
		ReleaseName      string  `json:"release_name"`
		Score            float64 `json:"score"`
		ReferenceMBID    string  `json:"reference_mbid"`
	}
	if err := client.get(ctx, client.labsURL, "/similar-recordings/json", query, ErrNoSimilarRecordings, &payload); err != nil {
		return nil, err
	}

	similar := make([]SimilarRecording, 0, len(payload))
	for _, entry := range payload {
		if strings.TrimSpace(entry.RecordingMBID) == "" {
			continue
		}
		similar = append(similar, SimilarRecording{
			RecordingMBID:    strings.TrimSpace(entry.RecordingMBID),
			Name:             strings.TrimSpace(entry.RecordingName),
			ArtistCreditName: strings.TrimSpace(entry.ArtistCreditName),
			ReleaseMBID:      strings.TrimSpace(entry.ReleaseMBID),
			ReleaseName:      strings.TrimSpace(entry.ReleaseName),
			Score:            entry.Score,
			ReferenceMBID:    strings.TrimSpace(entry.ReferenceMBID),
		})
	}
	return similar, nil
}

// get performs one paced request and decodes the answer.
//
// empty is what a 204 from this host means. The API host answers 204 for an
// account it has computed nothing for; the labs host answers it about the seed
// it was asked for, which is not a statement about the account at all.
func (client *Client) get(
	ctx context.Context, host, path string, query url.Values, empty error, into any,
) error {
	if err := client.wait(ctx, host); err != nil {
		return err
	}

	requestURL, err := url.Parse(host + path)
	if err != nil {
		return fmt.Errorf("build listenbrainz URL: %w", err)
	}
	requestURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create listenbrainz request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)
	// The token goes to the API host and nowhere else. The labs dataset host
	// asks for no authentication and every call this client makes is a public
	// read, so sending it there would be a second host seeing a credential that
	// only one of them has any use for.
	if client.userToken != "" && host == client.baseURL {
		request.Header.Set("Authorization", "Token "+client.userToken)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	client.observe(host, response.Header)

	switch {
	case response.StatusCode == http.StatusNoContent:
		// Not an error condition dressed as one: the service is saying it has
		// nothing computed for what was asked about.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return empty
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return &HTTPError{
			StatusCode: response.StatusCode,
			Message:    refusalMessage(response.Body),
		}
	}

	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(into); err != nil {
		return fmt.Errorf("decode listenbrainz response: %w", err)
	}
	return nil
}

// refusalMessage reads the reason out of a refusal.
//
// ListenBrainz describes what it refused in the body — {"code":…,"error":"…"} —
// and the labs host answers a rejected algorithm name with the permitted list.
// The whole point of the connection check is a message somebody can act on, and
// "listenbrainz returned HTTP 400" is not one. A body that says nothing useful
// leaves the status to speak for itself.
func refusalMessage(body io.Reader) string {
	var refusal struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 4096)).Decode(&refusal); err != nil {
		return ""
	}
	return strings.TrimSpace(refusal.Error)
}

// wait holds the next request to this host until its published budget allows it.
//
// MusicBrainz is paced to a constant one per second because it publishes no
// budget (internal/musicbrainz/client.go); ListenBrainz answers every request
// with what is left of the current window and how long that window still has to
// run, so the pace is read off the wire rather than guessed. A spent budget is
// the only reason to hold back at all — the window turns over on its own, and
// waiting before it is spent would be slower than the service asks for.
//
// The budget is spent here rather than left for the response to correct, because
// mu is released the moment this returns and the request has not gone out yet:
// several callers reading a remaining of one would all be let through, and the
// host would refuse all but the first.
func (client *Client) wait(ctx context.Context, host string) error {
	client.mu.Lock()
	defer client.mu.Unlock()

	window, published := client.budgets[host]
	if !published {
		return nil
	}
	if window.remaining > 0 {
		window.remaining--
		return nil
	}
	if delay := time.Until(window.resetAt); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	// The window has turned over, and what the new one holds is not known until
	// the next response says so.
	delete(client.budgets, host)
	return nil
}

// observe records the budget this host published. A response without the
// headers leaves the previous reading alone rather than resetting it, because
// "this host did not say" is not "the budget is full".
func (client *Client) observe(host string, header http.Header) {
	remaining, err := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
	if err != nil {
		return
	}
	client.mu.Lock()
	defer client.mu.Unlock()

	window := &budget{remaining: remaining}
	if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Reset-In"))); err == nil {
		window.resetAt = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	if client.budgets == nil {
		client.budgets = map[string]*budget{}
	}
	client.budgets[host] = window
}

// ErrThrottled reports that the service would not answer this time because it
// is being asked too often or is shedding load.
var ErrThrottled = errors.New("listenbrainz is being asked too often")

// ErrNotFound is the account the settings name not existing. It is a settings
// problem — a username somebody typed wrong — rather than a fault to retry.
var ErrNotFound = errors.New("listenbrainz does not know that account")

// ErrNoRecommendations is the service saying it has not computed anything for
// this account yet. The account exists; there is simply no model for it, which
// is what a freshly connected one looks like and is never a failure.
var ErrNoRecommendations = errors.New("listenbrainz has not built a model for this account yet")

// ErrNoSimilarRecordings is the labs host saying it has nothing for the seed it
// was asked about. It is kept apart from ErrNoRecommendations because that one
// makes a claim about the *account*, and a caller that logged it here would be
// repeating a claim about the user the labs host never made.
var ErrNoSimilarRecordings = errors.New("listenbrainz has nothing similar to that recording")

// HTTPError is a status the request was refused with, and the reason the
// service gave for it when it gave one.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (err *HTTPError) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("listenbrainz returned HTTP %d", err.StatusCode)
	}
	return fmt.Sprintf("listenbrainz refused the request: %s", err.Message)
}

// Unwrap names the statuses that mean "not now" as backpressure, and the one
// that means "no such account" as an answer, without losing which they were.
// Every other status is an ordinary failure and stays one.
func (err *HTTPError) Unwrap() error {
	switch err.StatusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return ErrThrottled
	case http.StatusNotFound:
		return ErrNotFound
	}
	return nil
}

// describe turns a transport or status failure into a message a user can act
// on. A caller that gave up is propagated unchanged.
func (client *Client) describe(ctx context.Context, username string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return errors.New("listenbrainz did not respond in time; check the address and try again")
	}
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("listenbrainz does not know the account %q; check the username", username)
	}
	if errors.Is(err, ErrThrottled) {
		return errors.New("listenbrainz is being asked too often; try again in a moment")
	}
	var httpError *HTTPError
	if errors.As(err, &httpError) {
		switch httpError.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("listenbrainz rejected the user token; clear it or replace it")
		case http.StatusBadGateway, http.StatusGatewayTimeout:
			return errors.New("listenbrainz is reachable but not answering; try again later")
		default:
			// The service's own words when it gave any, because it knows what it
			// refused and this does not.
			return err
		}
	}
	return fmt.Errorf("could not reach listenbrainz: %w", err)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// nullableTime reads a timestamp the service may send as null, as an ISO
// timestamp, or as epoch seconds.
//
// ADR 0015 observed latest_listened_at present and frequently non-null but not
// which of those encodings it uses, and a field nothing depends on must not be
// able to fail the whole decode. What cannot be read is read as "never", which
// is the same thing the field being null means.
type nullableTime struct {
	Time time.Time
}

func (value *nullableTime) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		return nil
	}
	if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
		value.Time = time.Unix(seconds, 0).UTC()
		return nil
	}
	var moment string
	if err := json.Unmarshal(data, &moment); err != nil {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, moment); err == nil {
			value.Time = parsed.UTC()
			return nil
		}
	}
	return nil
}

// Listen is one listen the account's history holds, as ListenBrainz keeps it:
// the moment, the names the player sent, and the MusicBrainz identifiers the
// service's own mapping added. Every identifier may be blank; the names never
// are.
type Listen struct {
	ListenedAt     time.Time
	ArtistName     string
	TrackName      string
	ReleaseName    string
	RecordingMBID  string
	ReleaseMBID    string
	CAAReleaseMBID string
	ArtistMBIDs    []string
}

// ErrNoListens is the service saying the account has no listens in the range.
var ErrNoListens = errors.New("listenbrainz has no listens for this account")

// Listens reads one page of the account's history, newest first, at most
// count entries (the service caps a page at 1000). A zero minTS or maxTS is
// not sent, which is how the caller asks from the beginning or up to now.
func (client *Client) Listens(
	ctx context.Context, username string, minTS, maxTS time.Time, count int,
) ([]Listen, error) {
	if strings.TrimSpace(username) == "" {
		return nil, errors.New("listenbrainz username is required")
	}
	query := url.Values{}
	if !minTS.IsZero() {
		query.Set("min_ts", strconv.FormatInt(minTS.Unix(), 10))
	}
	if !maxTS.IsZero() {
		query.Set("max_ts", strconv.FormatInt(maxTS.Unix(), 10))
	}
	if count > 0 {
		query.Set("count", strconv.Itoa(count))
	}
	var payload struct {
		Payload struct {
			Listens []struct {
				ListenedAt int64 `json:"listened_at"`
				Metadata   struct {
					ArtistName  string `json:"artist_name"`
					TrackName   string `json:"track_name"`
					ReleaseName string `json:"release_name"`
					Mapping     *struct {
						RecordingMBID  string   `json:"recording_mbid"`
						ReleaseMBID    string   `json:"release_mbid"`
						CAAReleaseMBID string   `json:"caa_release_mbid"`
						ArtistMBIDs    []string `json:"artist_mbids"`
					} `json:"mbid_mapping"`
				} `json:"track_metadata"`
			} `json:"listens"`
		} `json:"payload"`
	}
	path := "/1/user/" + url.PathEscape(username) + "/listens"
	err := client.get(ctx, client.baseURL, path, query, ErrNoListens, &payload)
	if errors.Is(err, ErrNoListens) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	listens := make([]Listen, 0, len(payload.Payload.Listens))
	for _, entry := range payload.Payload.Listens {
		artist := strings.TrimSpace(entry.Metadata.ArtistName)
		track := strings.TrimSpace(entry.Metadata.TrackName)
		if entry.ListenedAt <= 0 || artist == "" || track == "" {
			continue
		}
		listen := Listen{
			ListenedAt:  time.Unix(entry.ListenedAt, 0).UTC(),
			ArtistName:  artist,
			TrackName:   track,
			ReleaseName: strings.TrimSpace(entry.Metadata.ReleaseName),
		}
		if mapping := entry.Metadata.Mapping; mapping != nil {
			listen.RecordingMBID = strings.TrimSpace(mapping.RecordingMBID)
			listen.ReleaseMBID = strings.TrimSpace(mapping.ReleaseMBID)
			listen.CAAReleaseMBID = strings.TrimSpace(mapping.CAAReleaseMBID)
			for _, id := range mapping.ArtistMBIDs {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					listen.ArtistMBIDs = append(listen.ArtistMBIDs, trimmed)
				}
			}
		}
		listens = append(listens, listen)
	}
	return listens, nil
}
