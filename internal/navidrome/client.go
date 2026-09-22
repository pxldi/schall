// Package navidrome talks to the player through the Subsonic API.
//
// It says two things. The first is "look again now": Schall owns the library
// directory and is its only writer, Navidrome reads the same directory and
// would find new music eventually on its own schedule, so the whole message is
// that there is no need to wait for the schedule.
//
// The second is playlists, which run the other way — Schall reads what the
// player holds so a sync pass can compare it with what Schall holds. That half
// carries ids and paths, because those are what a comparison is made of, and it
// carries them without interpreting them: a path here is the path Navidrome
// reported, not a claim about where the file is.
package navidrome

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Name identifies the player in API responses and logs.
	Name = "navidrome"

	apiPrefix       = "/rest"
	maxResponseBody = 1 << 20

	// protocolVersion is the Subsonic revision this client speaks. It is sent
	// on every request and the server refuses anything it is too old for, so it
	// is the floor Schall requires rather than a preference.
	protocolVersion = "1.16.1"

	// clientName identifies Schall to the server, which shows it in its own
	// activity log. Subsonic requires it on every request.
	clientName = "schall"

	// SyncClientName is the name playlist sync introduces itself under, and it
	// is deliberately not clientName. Navidrome copies
	// Subsonic.DefaultReportRealPath onto a player row the first time a client
	// calls under a given name and never re-reads it for that row, and the
	// rescan trigger has already created a row under "schall" — from before
	// anybody needed real paths. Sync is the half that reads paths back, so it
	// asks under a name of its own and gets a row created after the setting was
	// turned on (ADR 0010).
	SyncClientName = "schall-sync"

	defaultHTTPTimeout = 10 * time.Second
)

// Options configures a client. BaseURL, Username and Password are required.
//
// ClientName is what the server files the calls under, and an empty one means
// "schall" so that everything which only asks for a rescan keeps the identity
// it has always had.
type Options struct {
	BaseURL    string
	Username   string
	Password   string
	ClientName string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    string
	username   string
	password   string
	clientName string
	httpClient *http.Client
}

func NewClient(options Options) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("navidrome base URL is required")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse navidrome base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("navidrome base URL must use http or https")
	}
	if strings.TrimSpace(options.Username) == "" {
		return nil, errors.New("navidrome username is required")
	}
	if options.Password == "" {
		return nil, errors.New("navidrome password is required")
	}

	name := strings.TrimSpace(options.ClientName)
	if name == "" {
		name = clientName
	}

	client := &Client{
		baseURL:    baseURL,
		username:   strings.TrimSpace(options.Username),
		password:   options.Password,
		clientName: name,
		httpClient: options.HTTPClient,
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return client, nil
}

func (client *Client) Name() string { return Name }

// Status is the outcome of a bounded connection check.
type Status struct {
	Detail string
}

// CheckConnection reports whether the player is reachable and accepts the
// stored account. Failures carry a message the settings page can show without
// anybody reading the server log.
func (client *Client) CheckConnection(ctx context.Context) (Status, error) {
	payload, err := client.call(ctx, "ping", nil)
	if err != nil {
		return Status{}, client.describe(ctx, err)
	}
	server := strings.TrimSpace(payload.Type)
	if server == "" {
		server = "subsonic server"
	}
	detail := server
	if version := strings.TrimSpace(payload.ServerVersion); version != "" {
		detail += " " + version
	}
	detail += fmt.Sprintf(", Subsonic %s, signed in as %s",
		strings.TrimSpace(payload.Version), client.username)
	return Status{Detail: detail}, nil
}

// Rescan asks the player to look at the library again.
//
// It is a request, not a wait: the server answers as soon as it has started
// looking, and how long it then takes is its own business. A scan already
// running is not an error either — the server keeps the one it has, which is
// the right answer to "there is more music" arriving twice.
func (client *Client) Rescan(ctx context.Context) error {
	if _, err := client.call(ctx, "startScan", nil); err != nil {
		return client.describe(ctx, err)
	}
	return nil
}

// call performs one Subsonic request as a GET, with everything it asks for in
// the query string.
func (client *Client) call(
	ctx context.Context, method string, parameters url.Values,
) (subsonicResponse, error) {
	query, err := client.authenticated(parameters)
	if err != nil {
		return subsonicResponse{}, err
	}
	requestURL, err := client.endpoint(method)
	if err != nil {
		return subsonicResponse{}, err
	}
	requestURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return subsonicResponse{}, fmt.Errorf("create navidrome request: %w", err)
	}
	return client.send(request)
}

// post performs one Subsonic request with its parameters in a form-encoded
// body instead of the query string.
//
// Subsonic accepts either, and the playlist calls are the reason to prefer the
// body: they carry one songId per track, so a playlist of a few hundred is a
// URL long enough for a proxy — or the server's own header limit — to cut off.
// What that failure looks like afterwards is a playlist that came out short,
// which is indistinguishable from Schall having asked for the wrong thing.
// Nothing else about the call changes: same credentials, same envelope, same
// reading of a refusal.
func (client *Client) post(
	ctx context.Context, method string, parameters url.Values,
) (subsonicResponse, error) {
	form, err := client.authenticated(parameters)
	if err != nil {
		return subsonicResponse{}, err
	}
	requestURL, err := client.endpoint(method)
	if err != nil {
		return subsonicResponse{}, err
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, requestURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return subsonicResponse{}, fmt.Errorf("create navidrome request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return client.send(request)
}

func (client *Client) endpoint(method string) (*url.URL, error) {
	requestURL, err := url.Parse(client.baseURL + apiPrefix + "/" + method)
	if err != nil {
		return nil, fmt.Errorf("build navidrome URL: %w", err)
	}
	return requestURL, nil
}

// send performs one prepared request and returns the answer inside the
// envelope. A refusal arrives as HTTP 200 with a status of "failed" and an
// error object, so the body decides the outcome and the status code only
// reports whether the server answered at all.
func (client *Client) send(request *http.Request) (subsonicResponse, error) {
	request.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return subsonicResponse{}, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return subsonicResponse{}, &HTTPError{StatusCode: response.StatusCode}
	}

	var envelope struct {
		Response subsonicResponse `json:"subsonic-response"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(&envelope); err != nil {
		return subsonicResponse{}, fmt.Errorf("decode navidrome response: %w", err)
	}
	if envelope.Response.Error != nil {
		return subsonicResponse{}, &Error{
			Code:    envelope.Response.Error.Code,
			Message: strings.TrimSpace(envelope.Response.Error.Message),
		}
	}
	if envelope.Response.Status != "" && envelope.Response.Status != "ok" {
		return subsonicResponse{}, &Error{Message: "the request was refused"}
	}
	return envelope.Response, nil
}

// authenticated adds the identifying and credential parameters Subsonic wants
// on every request.
//
// The credential is a salted hash rather than the password, and the salt is new
// each time: the protocol defines the hash as MD5, so this is the shape of the
// interface being spoken to and not a choice about how to store a secret.
func (client *Client) authenticated(parameters url.Values) (url.Values, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate navidrome salt: %w", err)
	}
	saltText := hex.EncodeToString(salt)
	token := md5.Sum([]byte(client.password + saltText))

	query := url.Values{}
	for key, values := range parameters {
		query[key] = values
	}
	query.Set("u", client.username)
	query.Set("t", hex.EncodeToString(token[:]))
	query.Set("s", saltText)
	query.Set("v", protocolVersion)
	query.Set("c", client.clientName)
	query.Set("f", "json")
	return query, nil
}

// HTTPError is a response the server did not answer the request with at all.
type HTTPError struct {
	StatusCode int
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("navidrome returned HTTP %d", err.StatusCode)
}

// Error is a refusal the server described in its own words. Subsonic sends
// these with a 200, so it is the only place a reason can be read from.
type Error struct {
	Code    int
	Message string
}

func (err *Error) Error() string {
	if err.Message == "" {
		return fmt.Sprintf("navidrome refused the request (code %d)", err.Code)
	}
	return fmt.Sprintf("navidrome refused the request: %s", err.Message)
}

// ErrNotFound is the one refusal callers act on rather than report. Everything
// else the server says no to is a fault somebody has to look at; this one is an
// answer — whatever was asked for is not there.
var ErrNotFound = errors.New("navidrome does not have what was asked for")

// Is reads a not-found refusal as ErrNotFound so that a caller can tell "that
// id stopped resolving" from every other reason the request failed, without
// knowing what a Subsonic error code is.
//
// It matters because ADR 0010 reads exactly that difference as
// evidence: an id that no longer resolves is an identity that broke, which is
// never a track the user removed. Sorting the two apart is the whole reason
// this code has a name outside the switch in describe.
func (err *Error) Is(target error) bool {
	return target == ErrNotFound && err.Code == codeNotFound
}

// Subsonic error codes describe turns into something a person can act on.
const (
	codeClientTooOld = 20
	codeServerTooOld = 30
	codeBadPassword  = 40
	codeTokenRefused = 41
	codeNotAllowed   = 50
)

// codeNotFound is the one code that is not for a person at all. Nothing about
// the message needs improving — a caller acts on it instead of showing it — so
// describe leaves it as the server wrote it and ErrNotFound is how it is read.
const codeNotFound = 70

// describe turns a transport, status or protocol failure into a message a user
// can act on. A caller that gave up is propagated unchanged.
func (client *Client) describe(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return errors.New("navidrome did not respond in time; check the base URL and that it is running")
	}
	var refusal *Error
	if errors.As(err, &refusal) {
		switch refusal.Code {
		case codeBadPassword:
			return errors.New("navidrome rejected the account; check the username and password")
		case codeTokenRefused:
			return errors.New("navidrome does not accept token authentication for this account; " +
				"it is configured against an external directory")
		case codeNotAllowed:
			return errors.New("the navidrome account is not allowed to do this; " +
				"scanning requires an administrator account")
		case codeClientTooOld, codeServerTooOld:
			return fmt.Errorf("navidrome and Schall do not share a Subsonic version: %s", refusal.Message)
		default:
			return err
		}
	}
	var httpError *HTTPError
	if errors.As(err, &httpError) {
		switch httpError.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("navidrome rejected the account; check the username and password")
		case http.StatusNotFound:
			return errors.New("navidrome did not recognise the API path; check the base URL and that it exposes /rest")
		case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
			return errors.New("navidrome is reachable but not ready; try again once it has started")
		default:
			return err
		}
	}
	return fmt.Errorf("could not reach navidrome: %w", err)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// subsonicResponse is the envelope every answer arrives in. Each method fills
// in the one field it is about and leaves the rest absent, so the pointers are
// how a caller tells "the server had nothing to say" from "the server said
// nothing was there". The playlist and song payloads live in playlists.go.
type subsonicResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	Type          string `json:"type"`
	ServerVersion string `json:"serverVersion"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Playlists *struct {
		Playlist []playlistPayload `json:"playlist"`
	} `json:"playlists"`
	Playlist      *playlistPayload `json:"playlist"`
	Song          *songPayload     `json:"song"`
	SearchResult3 *struct {
		Song []songPayload `json:"song"`
	} `json:"searchResult3"`
}
