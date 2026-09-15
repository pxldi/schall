// Package acoustid identifies audio by what it sounds like rather than by what
// it claims to be.
//
// Every other identity signal in Schall comes from something a file or a peer
// says: a filename, a tag, a folder name, a duration a stranger typed. All of
// those can be wrong, and the ones that are wrong are exactly the ones worth
// catching. An acoustic fingerprint is computed from the decoded audio, so it
// cannot be mistyped, mislabelled, or copied from a different release.
//
// It resolves to MusicBrainz recording IDs, which is the identifier the rest of
// Schall is keyed on, so an answer from here lands on the same identity path as
// everything else rather than beside it.
package acoustid

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrNotConfigured reports that no AcoustID application key is stored. The
// service requires one per application and only the operator can obtain it.
var ErrNotConfigured = errors.New("no AcoustID API key is configured")

// ErrNoFingerprint reports that the file could not be decoded into audio. It is
// a fact about the bytes, not about the identity, and retrying will not change
// it.
var ErrNoFingerprint = errors.New("the file could not be fingerprinted")

// ErrRateLimited reports that AcoustID refused because it is being asked too
// fast. It is a fact about the minute, not about the file: nothing was learned
// about this audio, and the same question a minute later is answerable.
//
// It exists so that a caller can tell "asked too fast" from "failed". Without
// it, a plain error was indistinguishable from a real refusal, and a burst of
// resolutions — which is what the first scan of a new library is — spent every
// attempt on the same 429 and then recorded the throttle against the file.
// internal/sources carries the same sentinel for the same reason.
var ErrRateLimited = errors.New("AcoustID is rate limiting requests")

const (
	defaultBaseURL     = "https://api.acoustid.org/v2"
	defaultFpcalc      = "fpcalc"
	defaultTimeout     = 30 * time.Second
	defaultFpcalcLimit = 120 * time.Second
	maxResponseBody    = 2 << 20
)

// How much audio a compressed fingerprint stands for. These describe the
// fingerprint format rather than anything Schall chose, which is why they are
// arithmetic on the algorithm's own parameters and why the algorithm byte is
// checked before they are applied: chromaprint resamples to 11025 Hz and reads
// it in 4096-sample frames overlapping by two thirds, so every item after the
// first stands for a third of a frame, and the first only appears once the
// filter delay has filled.
const (
	fingerprintAlgorithm = 1
	secondsPerItem       = 4096.0 / 3.0 / 11025.0
	itemDelaySeconds     = 2.64
	// fpcalc reads only the opening of a file by default, so a fingerprint of a
	// long track is complete at this length rather than short.
	fpcalcWindowSeconds = 120
	// How far the audio a fingerprint was computed from may fall short of the
	// audio in the file before the fingerprint stops describing that file. It is
	// the five seconds every other duration in Schall is compared within.
	decodeShortfallSeconds = 5
)

// Fingerprint is what fpcalc computed from the decoded audio.
type Fingerprint struct {
	// DurationSeconds is the decoded length, which AcoustID needs alongside the
	// fingerprint. It is the audio's own length, not a tag's claim about it.
	DurationSeconds int
	Value           string
	// DecodedSeconds is how much audio the fingerprint was actually computed
	// from, read out of the fingerprint's own header. It is normally the whole
	// file, or the opening two minutes of a longer one; it falls short when the
	// decoder gave up part way.
	DecodedSeconds int
	// Damaged is what the decoder complained about while reading a file it
	// nevertheless got through. Empty when the decode was clean. It says
	// something about the copy and nothing about its identity, so it is carried
	// as evidence and decides nothing.
	Damaged string
}

// Identification is everything one file's audio came to: what AcoustID answered,
// and how long the audio it was computed from actually ran.
//
// The length travels with the answer because it is a measurement and the only
// alternative is a claim. fpcalc decodes the file in order to fingerprint it and
// therefore knows exactly how long it played; dropping that left a match to be
// decided on the duration a peer had typed into a tag, which is the one number
// in the comparison the peer controls.
type Identification struct {
	DurationSeconds int
	Clusters        []Cluster
	// Damaged is what the decoder complained about while reading a file it got
	// through anyway. It travels with the answer so that a copy identified from
	// audio with a bad frame in it can say so, and decides nothing on its own —
	// the fingerprint either named the recording or it did not.
	Damaged string
}

// Cluster is one of the fingerprint clusters AcoustID matched the audio to, with
// every MusicBrainz recording that cluster is linked to.
//
// The clusters are kept apart because they are different statements. Within one
// cluster AcoustID is saying these recordings are the same piece of audio —
// MusicBrainz routinely holds one performance as several rows, and this is where
// that shows up. Between clusters it is saying nothing of the kind: the second
// cluster is other audio that resembled the fingerprint, and its recordings have
// no more to do with the file than any stranger's. Flattening the two together
// made those cases indistinguishable.
type Cluster struct {
	// ID is AcoustID's identifier for the result that answered the lookup.
	ID string
	// Score is AcoustID's confidence that the submitted audio is this cluster,
	// between 0 and 1. It is the service's number, passed through unchanged:
	// Schall decides what is good enough, and does so where the decision is
	// visible rather than in here.
	Score      float64
	Recordings []uuid.UUID
}

// Options configures a Client. APIKey is required to look anything up;
// fingerprinting works without one.
type Options struct {
	APIKey     string
	BaseURL    string
	FpcalcPath string
	HTTPClient *http.Client
	// Run executes fpcalc. Tests replace it; nothing else should.
	Run func(ctx context.Context, path string, args ...string) ([]byte, error)
}

type Client struct {
	apiKey     string
	baseURL    string
	fpcalcPath string
	httpClient *http.Client
	run        func(ctx context.Context, path string, args ...string) ([]byte, error)
}

func NewClient(options Options) *Client {
	client := &Client{
		apiKey:     strings.TrimSpace(options.APIKey),
		baseURL:    strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"),
		fpcalcPath: strings.TrimSpace(options.FpcalcPath),
		httpClient: options.HTTPClient,
		run:        options.Run,
	}
	if client.baseURL == "" {
		client.baseURL = defaultBaseURL
	}
	if client.fpcalcPath == "" {
		client.fpcalcPath = defaultFpcalc
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if client.run == nil {
		client.run = runCommand
	}
	return client
}

// Configured reports whether lookups are possible. Fingerprinting a file does
// not need a key, so the two are asked separately.
func (client *Client) Configured() bool { return client.apiKey != "" }

// Fingerprint decodes one file and computes its acoustic fingerprint.
//
// A file that cannot be decoded returns ErrNoFingerprint rather than a failure
// worth retrying: the bytes on disk will not decode differently next time.
//
// A file with a damaged frame in otherwise sound audio is a different case and
// the common one. fpcalc reads past the damage, prints the fingerprint it
// computed, and exits non-zero to report the complaint — so an exit code alone
// cannot tell a file that would not decode from one that did. What decides is
// whether the fingerprint covers the audio: it is accepted only when it was
// computed from the whole file, or from the opening fpcalc reads of a long one.
// Anything shorter is a fingerprint of part of a file being offered as a
// description of all of it, which is exactly the guess this package exists to
// refuse.
func (client *Client) Fingerprint(ctx context.Context, path string) (Fingerprint, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultFpcalcLimit)
	defer cancel()

	output, runErr := client.run(ctx, client.fpcalcPath, "-json", path)
	if runErr != nil && ctx.Err() != nil {
		return Fingerprint{}, ctx.Err()
	}
	refuse := func(format string, values ...any) (Fingerprint, error) {
		if runErr != nil {
			return Fingerprint{}, fmt.Errorf("%w: %s", ErrNoFingerprint, firstLine(runErr.Error()))
		}
		return Fingerprint{}, fmt.Errorf("%w: "+format, append([]any{ErrNoFingerprint}, values...)...)
	}

	var payload struct {
		Duration    float64 `json:"duration"`
		Fingerprint string  `json:"fingerprint"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return refuse("fpcalc returned unreadable output")
	}
	if payload.Fingerprint == "" || payload.Duration <= 0 {
		return refuse("no audio was decoded")
	}
	print := Fingerprint{
		DurationSeconds: int(payload.Duration + 0.5),
		Value:           payload.Fingerprint,
		DecodedSeconds:  decodedSeconds(payload.Fingerprint),
	}
	if runErr == nil {
		return print, nil
	}
	if wanted := min(print.DurationSeconds, fpcalcWindowSeconds); print.DecodedSeconds <= 0 ||
		wanted-print.DecodedSeconds > decodeShortfallSeconds {
		return Fingerprint{}, fmt.Errorf("%w: only %ds of %ds decoded before %s",
			ErrNoFingerprint, print.DecodedSeconds, wanted, firstLine(runErr.Error()))
	}
	print.Damaged = firstLine(runErr.Error())
	return print, nil
}

// decodedSeconds reports how much audio a compressed fingerprint was computed
// from, by reading the item count out of its own four-byte header rather than
// believing anything about the file. Zero means the header could not be read,
// which is treated the same as a fingerprint that covers nothing.
func decodedSeconds(value string) int {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil || len(raw) < 4 || raw[0] != fingerprintAlgorithm {
		return 0
	}
	items := int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	if items <= 0 {
		return 0
	}
	return int(float64(items)*secondsPerItem + itemDelaySeconds + 0.5)
}

// Identify asks AcoustID which fingerprint clusters the audio belongs to, best
// first, with the recordings of each.
//
// An empty result is an answer: the audio is not in AcoustID's database, which
// is common for small labels and self-released music. It is never evidence that
// the file is the wrong track.
func (client *Client) Identify(ctx context.Context, print Fingerprint) ([]Cluster, error) {
	if !client.Configured() {
		return nil, ErrNotConfigured
	}
	if print.Value == "" || print.DurationSeconds <= 0 {
		return nil, ErrNoFingerprint
	}

	query := url.Values{}
	query.Set("client", client.apiKey)
	query.Set("format", "json")
	query.Set("meta", "recordingids")
	query.Set("duration", strconv.Itoa(print.DurationSeconds))
	query.Set("fingerprint", print.Value)

	// The fingerprint is long enough to trouble some proxies as a query string,
	// and AcoustID accepts the same parameters as a form body.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.baseURL+"/lookup", strings.NewReader(query.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not reach AcoustID: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("AcoustID returned HTTP %d", response.StatusCode)
	}

	var payload struct {
		Status  string                   `json:"status"`
		Error   struct{ Message string } `json:"error"`
		Results []struct {
			ID         string  `json:"id"`
			Score      float64 `json:"score"`
			Recordings []struct {
				ID string `json:"id"`
			} `json:"recordings"`
		} `json:"results"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, maxResponseBody)).
		Decode(&payload); err != nil {
		return nil, fmt.Errorf("could not read the AcoustID response: %w", err)
	}
	if payload.Status != "ok" {
		detail := strings.TrimSpace(payload.Error.Message)
		if detail == "" {
			detail = payload.Status
		}
		return nil, fmt.Errorf("AcoustID refused the lookup: %s", detail)
	}

	// The results are passed on with their boundaries intact, in the order
	// AcoustID ranked them. One cluster naming several recordings is that service
	// saying they are one piece of audio, which is ordinary — MusicBrainz holds a
	// single performance as several rows all the time. Two clusters are two
	// pieces of audio, one of which merely resembled the fingerprint. Collapsing
	// them into one list, as this did, made a resemblance read exactly like a
	// duplicated row, and deciding which the catalogue meant was then impossible
	// downstream because the distinction had already been thrown away here.
	//
	// Duplicates are dropped within a cluster and never across one: the same ID
	// twice in one cluster is nothing, while the same ID in two clusters is two
	// different statements about it and both belong to whoever reads them.
	clusters := make([]Cluster, 0, len(payload.Results))
	for _, result := range payload.Results {
		cluster := Cluster{
			ID: result.ID, Score: result.Score,
			Recordings: make([]uuid.UUID, 0, len(result.Recordings)),
		}
		seen := make(map[uuid.UUID]bool, len(result.Recordings))
		for _, recording := range result.Recordings {
			id, err := uuid.Parse(strings.TrimSpace(recording.ID))
			if err != nil || seen[id] {
				continue
			}
			seen[id] = true
			cluster.Recordings = append(cluster.Recordings, id)
		}
		clusters = append(clusters, cluster)
	}
	return clusters, nil
}

// runCommand returns what the command printed as well as how it ended. The two
// travel together because fpcalc reports both at once: a file with damaged audio
// gets a complete fingerprint onto stdout and a complaint onto stderr, and
// dropping the output whenever the exit code was non-zero threw away answers
// that had already been computed.
func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return output, errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return output, err
	}
	return output, nil
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return strings.TrimSpace(value)
}
