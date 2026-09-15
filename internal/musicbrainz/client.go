package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultBaseURL  = "https://musicbrainz.org/ws/2"
	defaultInterval = time.Second
	// defaultTimeout is how long one request to MusicBrainz may take. Ten
	// seconds was not enough: browsing the releases of a large release group
	// regularly took longer, and every one of those look-ups failed as a
	// time-out. A request that slow is still worth waiting for — the answer is
	// asked for once a day at most.
	defaultTimeout  = 30 * time.Second
	maxResponseBody = 2 << 20
)

const (
	// throttleAttempts is how many times one question is put again when the
	// provider refuses it for load. Three is enough for a limiter that has just
	// been overtaken and few enough that a provider genuinely down is not held
	// against for long.
	throttleAttempts = 3
	// maxThrottleWait caps how long one request stands back for. MusicBrainz can
	// ask to be left alone for far longer than a caller's deadline; waiting it out
	// would spend that deadline sleeping and return nothing, where standing back
	// this far and letting the caller reschedule returns the same nothing sooner.
	maxThrottleWait = 5 * time.Second
)

type Artist struct {
	ID             uuid.UUID
	Name           string
	SortName       string
	Type           string
	Country        string
	Area           string
	Disambiguation string
	Score          int
	// Genres is what the MusicBrainz community voted this artist is, most voted
	// first. A search never asks for genres, so an artist that came from one has
	// none — absent is silence about the votes, not a claim there are none.
	Genres []string
}

type ArtistCatalogue struct {
	Artist        Artist
	ReleaseGroups []ReleaseGroup
	Metadata      json.RawMessage
}

type ReleaseGroup struct {
	ID               uuid.UUID
	Title            string
	FirstReleaseDate string
	PrimaryType      string
	SecondaryTypes   []string
	// Genres is what the MusicBrainz community voted this release group is, most
	// voted first, on the same terms as an artist's.
	Genres   []string
	Metadata json.RawMessage
}

type ReleaseCatalogue struct {
	Edition ReleaseEdition
	Tracks  []Track
	// Genres is what the MusicBrainz community voted the release group is, on
	// the same terms as an artist's. It is read here as well as on the artist
	// catalogue because a release group credited to somebody else is not in its
	// Schall artist's catalogue, and nothing else would ever ask about it.
	//
	// Empty and nil are different answers. Empty means MusicBrainz was asked and
	// holds no votes; nil means the lookup could not be made, and the column is
	// left alone.
	Genres   []string
	Metadata json.RawMessage
}

type ReleaseEdition struct {
	ID              uuid.UUID
	Title           string
	Status          string
	Country         string
	Barcode         string
	Date            string
	MediaCount      int
	TrackCount      int
	SelectionReason string
	// Credits is the artists this edition is credited to, in the order
	// MusicBrainz credits them. A browse never asks for credits, so an edition
	// that came from one has none — absent is silence about the credit, not a
	// claim that the release has none.
	Credits []Credit
}

func (client *Client) ListReleaseEditions(ctx context.Context, releaseGroupID uuid.UUID) ([]ReleaseEdition, error) {
	if releaseGroupID == uuid.Nil {
		return nil, errors.New("release group MusicBrainz ID is required")
	}
	const pageSize = 100
	result := make([]ReleaseEdition, 0)
	for offset := 0; ; offset += pageSize {
		var page releaseBrowseResponse
		_, err := client.getJSON(ctx, "/release", url.Values{
			"release-group": []string{releaseGroupID.String()},
			"fmt":           []string{"json"},
			"limit":         []string{strconv.Itoa(pageSize)},
			"offset":        []string{strconv.Itoa(offset)},
		}, &page)
		if err != nil {
			return nil, fmt.Errorf("browse MusicBrainz releases: %w", err)
		}
		for _, item := range page.Releases {
			id, err := uuid.Parse(item.ID)
			if err != nil || strings.TrimSpace(item.Title) == "" {
				continue
			}
			result = append(result, ReleaseEdition{
				ID: id, Title: strings.TrimSpace(item.Title), Status: item.Status,
				Country: item.Country, Date: item.Date,
			})
		}
		if len(page.Releases) == 0 || offset+len(page.Releases) >= page.Count {
			break
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		a, b := result[left], result[right]
		if normalizedReleaseDate(a.Date) != normalizedReleaseDate(b.Date) {
			return normalizedReleaseDate(a.Date) < normalizedReleaseDate(b.Date)
		}
		if a.Country != b.Country {
			return a.Country < b.Country
		}
		return a.ID.String() < b.ID.String()
	})
	return result, nil
}

type Track struct {
	RecordingID uuid.UUID
	Title       string
	DiscNumber  int
	TrackNumber int
	DurationMS  *int
	ISRC        string
	// Credits is the artists this release credits the track to, read off the
	// track rather than off the recording: a recording appears on many releases
	// and each prints its own credit, and the one that belongs here is the one
	// this release printed.
	Credits []Credit
}

type Client struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
	interval   time.Duration

	mu          sync.Mutex
	lastRequest time.Time

	// knownMu guards known, which is what this process has learnt about merges.
	// Its own lock rather than the one above: the pacer is held while a request is
	// being waited out, and a cache read must never queue behind that.
	knownMu sync.Mutex
	known   map[uuid.UUID]resolution
	// now is the clock those answers age by. A field so a test can move it, and
	// never anything a caller passes in.
	now func() time.Time
}

// resolution is one answer about one ID: what the provider said it names, and
// when this process stops taking its own word for it.
type resolution struct {
	// id is the recording the provider arrived at, or uuid.Nil for an ID it has
	// never heard of.
	id      uuid.UUID
	expires time.Time
}

type Options struct {
	BaseURL    string
	UserAgent  string
	HTTPClient *http.Client
	Interval   time.Duration
}

func NewClient(options Options) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("parse MusicBrainz base URL: %w", err)
	}

	userAgent := strings.TrimSpace(options.UserAgent)
	if userAgent == "" {
		return nil, errors.New("MusicBrainz user agent is required")
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}

	interval := options.Interval
	if interval == 0 {
		interval = defaultInterval
	}
	if interval < 0 {
		return nil, errors.New("MusicBrainz request interval cannot be negative")
	}

	return &Client{
		baseURL:    baseURL,
		userAgent:  userAgent,
		httpClient: httpClient,
		interval:   interval,
		now:        time.Now,
	}, nil
}

func (client *Client) SearchArtists(ctx context.Context, query string, limit int) ([]Artist, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("artist search query is required")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("artist search limit must be between 1 and 100")
	}

	// Through getJSON like every other question, so the artist search is paced and
	// stands back from a refusal on the same terms as the rest. It shares the one
	// limiter, so it is as able as anything else to be the request that overtakes
	// it.
	var payload artistSearchResponse
	if _, err := client.getJSON(ctx, "/artist/", url.Values{
		"query": []string{query},
		"limit": []string{strconv.Itoa(limit)},
		"fmt":   []string{"json"},
	}, &payload); err != nil {
		return nil, fmt.Errorf("search MusicBrainz artists: %w", err)
	}

	artists := make([]Artist, 0, len(payload.Artists))
	for _, item := range payload.Artists {
		id, err := uuid.Parse(item.ID)
		if err != nil || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.SortName) == "" {
			continue
		}
		artists = append(artists, Artist{
			ID:             id,
			Name:           item.Name,
			SortName:       item.SortName,
			Type:           item.Type,
			Country:        item.Country,
			Area:           item.Area.Name,
			Disambiguation: item.Disambiguation,
			Score:          item.Score.Int(),
		})
	}
	return artists, nil
}

// VariousArtistsID is the artist MusicBrainz credits a compilation to when the
// tracks are by different people. It is a placeholder, not a person or a band:
// nearly every compilation ever released is filed under it, so its catalogue is
// hundreds of thousands of releases and belongs to nobody.
var VariousArtistsID = uuid.MustParse("89ad4ac3-39f7-470e-963a-56509c546377")

// placeholderArtistIDs are MusicBrainz's special-purpose artists: the rows a
// release is credited to when there is nobody to credit. Each one was looked up
// against MusicBrainz on 2026-08-29 and carries the name in the comment beside
// it. None of them has a discography anybody would want, and none of them has
// genres, so Schall asks about none of them.
var placeholderArtistIDs = map[uuid.UUID]struct{}{
	VariousArtistsID: {},
	uuid.MustParse("125ec42a-7229-4250-afc5-e057484327fe"): {}, // [unknown]
	uuid.MustParse("f731ccc4-e22a-43af-a747-64213329e088"): {}, // [anonymous]
	uuid.MustParse("9be7f096-97ec-4615-8957-8d40b5dcbc41"): {}, // [traditional]
	uuid.MustParse("eec63d3c-3b81-4ad4-b1e4-7c147d4d2b61"): {}, // [no artist]
	uuid.MustParse("33cf029c-63b0-41a0-9855-be2a3665fb3b"): {}, // [data]
	uuid.MustParse("314e1c25-dde7-4e4d-b2f4-0a7b9f7c56dc"): {}, // [dialogue]
}

// IsPlaceholderArtist reports that the ID names one of those rows.
func IsPlaceholderArtist(id uuid.UUID) bool {
	_, placeholder := placeholderArtistIDs[id]
	return placeholder
}

// ErrNotAnArtist says the ID names one of MusicBrainz's placeholders rather
// than somebody who records. There is no catalogue to read.
var ErrNotAnArtist = errors.New("this is a MusicBrainz placeholder, not an artist")

// ErrNoUsableEditions says the release group has no release Schall can read a
// track list off. It is an answer about the release group, so asking again gets
// the same one.
var ErrNoUsableEditions = errors.New("MusicBrainz release group has no usable editions")

// ErrCatalogueTooLarge says the artist has more releases than one pass will
// read. It is not a fact about MusicBrainz being slow — it is a refusal to
// spend an hour of requests and then write a catalogue that large into a
// library that holds one album by them.
var ErrCatalogueTooLarge = errors.New("this artist has more releases than one pass reads")

// maxReleaseGroups is where that refusal starts. Five thousand is far above any
// real discography — the most prolific people in MusicBrainz are in the low
// hundreds — and far below the placeholders, which run to six figures. Reading
// them at the pace MusicBrainz asks for took over three quarters of an hour,
// during which the one worker that scans, tags and moves files did nothing
// else.
const maxReleaseGroups = 5000

func (client *Client) ArtistCatalogue(ctx context.Context, id uuid.UUID) (ArtistCatalogue, error) {
	if id == uuid.Nil {
		return ArtistCatalogue{}, errors.New("artist MusicBrainz ID is required")
	}
	if IsPlaceholderArtist(id) {
		return ArtistCatalogue{}, ErrNotAnArtist
	}

	var artistPayload artistLookupResponse
	artistMetadata, err := client.getJSON(ctx, "/artist/"+id.String(), url.Values{
		"fmt": []string{"json"},
		// Genres arrive on the lookup this pass already makes. They are one more
		// field on one response, not one more request, so the pace MusicBrainz
		// asks for is untouched.
		"inc": []string{"genres"},
	}, &artistPayload)
	if err != nil {
		return ArtistCatalogue{}, fmt.Errorf("look up MusicBrainz artist: %w", err)
	}
	if strings.TrimSpace(artistPayload.Name) == "" || strings.TrimSpace(artistPayload.SortName) == "" {
		return ArtistCatalogue{}, errors.New("MusicBrainz artist response is missing a name")
	}

	catalogue := ArtistCatalogue{
		Artist: Artist{
			ID:             id,
			Name:           artistPayload.Name,
			SortName:       artistPayload.SortName,
			Type:           artistPayload.Type,
			Country:        artistPayload.Country,
			Area:           artistPayload.Area.Name,
			Disambiguation: artistPayload.Disambiguation,
			Genres:         topGenres(artistPayload.Genres),
		},
		Metadata: artistMetadata,
	}

	const pageSize = 100
	for offset := 0; ; offset += pageSize {
		var page releaseGroupBrowseResponse
		_, err := client.getJSON(ctx, "/release-group", url.Values{
			"artist": []string{id.String()},
			"fmt":    []string{"json"},
			// The same one more field, on the browse this pass already pages
			// through. Every release group an artist has is genred by the pass
			// that lists them.
			"inc":    []string{"genres"},
			"limit":  []string{strconv.Itoa(pageSize)},
			"offset": []string{strconv.Itoa(offset)},
		}, &page)
		if err != nil {
			return ArtistCatalogue{}, fmt.Errorf("browse MusicBrainz release groups: %w", err)
		}
		// The count arrives on the first page, so a catalogue too large to read is
		// refused after one request rather than after thousands.
		if page.Count > maxReleaseGroups {
			return ArtistCatalogue{}, fmt.Errorf("%w: %d", ErrCatalogueTooLarge, page.Count)
		}

		for _, item := range page.ReleaseGroups {
			releaseGroupID, err := uuid.Parse(item.ID)
			if err != nil || strings.TrimSpace(item.Title) == "" {
				continue
			}
			metadata, err := json.Marshal(item)
			if err != nil {
				return ArtistCatalogue{}, fmt.Errorf("encode MusicBrainz release group metadata: %w", err)
			}
			catalogue.ReleaseGroups = append(catalogue.ReleaseGroups, ReleaseGroup{
				ID:               releaseGroupID,
				Title:            item.Title,
				FirstReleaseDate: item.FirstReleaseDate,
				PrimaryType:      item.PrimaryType,
				SecondaryTypes:   item.SecondaryTypes,
				Genres:           topGenres(item.Genres),
				Metadata:         metadata,
			})
		}

		if len(page.ReleaseGroups) == 0 || offset+len(page.ReleaseGroups) >= page.Count {
			break
		}
	}
	return catalogue, nil
}

func (client *Client) ReleaseCatalogue(ctx context.Context, releaseGroupID, selectedReleaseID uuid.UUID) (ReleaseCatalogue, error) {
	if releaseGroupID == uuid.Nil {
		return ReleaseCatalogue{}, errors.New("release group MusicBrainz ID is required")
	}

	var selected releaseItem
	reason := "Previously selected representative edition"
	if selectedReleaseID != uuid.Nil {
		selected.ID = selectedReleaseID.String()
	} else {
		const pageSize = 100
		candidates := make([]releaseItem, 0)
		for offset := 0; ; offset += pageSize {
			var page releaseBrowseResponse
			_, err := client.getJSON(ctx, "/release", url.Values{
				"release-group": []string{releaseGroupID.String()},
				"fmt":           []string{"json"},
				"limit":         []string{strconv.Itoa(pageSize)},
				"offset":        []string{strconv.Itoa(offset)},
			}, &page)
			if err != nil {
				return ReleaseCatalogue{}, fmt.Errorf("browse MusicBrainz releases: %w", err)
			}
			candidates = append(candidates, page.Releases...)
			if len(page.Releases) == 0 || offset+len(page.Releases) >= page.Count {
				break
			}
		}
		var err error
		selected, reason, err = selectRepresentativeRelease(candidates)
		if err != nil {
			return ReleaseCatalogue{}, err
		}
	}

	releaseID, _ := uuid.Parse(selected.ID)

	var payload releaseLookupResponse
	metadata, err := client.getJSON(ctx, "/release/"+releaseID.String(), url.Values{
		"fmt": []string{"json"},
		// artist-credits costs no extra request: this lookup is already made
		// once per album, and asking for credits returns the release's own and
		// every track's in the same response.
		"inc": []string{"recordings+artist-credits+isrcs"},
	}, &payload)
	if err != nil {
		return ReleaseCatalogue{}, fmt.Errorf("look up MusicBrainz release: %w", err)
	}

	// The release lookup above carries no release-group genres, so they are read
	// from the release group itself. A release group MusicBrainz no longer holds
	// leaves them alone rather than failing a refresh that has its tracks.
	var genres []string
	switch group, groupErr := client.ReleaseGroup(ctx, releaseGroupID); {
	case groupErr == nil:
		genres = group.ReleaseGroup.Genres
	case errors.Is(groupErr, ErrNotFound):
	default:
		return ReleaseCatalogue{}, groupErr
	}

	catalogue := ReleaseCatalogue{
		Genres: genres,
		Edition: ReleaseEdition{
			ID:              releaseID,
			Title:           strings.TrimSpace(payload.Title),
			Status:          payload.Status,
			Country:         payload.Country,
			Barcode:         payload.Barcode,
			Date:            payload.Date,
			MediaCount:      len(payload.Media),
			SelectionReason: reason,
			Credits:         artistCredits(payload.ArtistCredit),
		},
		Metadata: metadata,
	}
	if catalogue.Edition.Title == "" {
		catalogue.Edition.Title = strings.TrimSpace(selected.Title)
	}
	for mediaIndex, medium := range payload.Media {
		discNumber := medium.Position
		if discNumber < 1 {
			discNumber = mediaIndex + 1
		}
		for trackIndex, item := range medium.Tracks {
			recordingID, parseErr := uuid.Parse(item.Recording.ID)
			if parseErr != nil {
				continue
			}
			trackNumber := item.Position
			if trackNumber < 1 {
				trackNumber = trackIndex + 1
			}
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = strings.TrimSpace(item.Recording.Title)
			}
			if title == "" {
				continue
			}
			duration := item.Length
			if duration == nil {
				duration = item.Recording.Length
			}
			isrcs := append([]string(nil), item.Recording.ISRCs...)
			sort.Strings(isrcs)
			isrc := ""
			if len(isrcs) > 0 {
				isrc = strings.TrimSpace(isrcs[0])
			}
			catalogue.Tracks = append(catalogue.Tracks, Track{
				RecordingID: recordingID,
				Title:       title,
				DiscNumber:  discNumber,
				TrackNumber: trackNumber,
				DurationMS:  duration,
				ISRC:        isrc,
				Credits:     artistCredits(item.ArtistCredit),
			})
		}
	}
	catalogue.Edition.TrackCount = len(catalogue.Tracks)
	return catalogue, nil
}

func selectRepresentativeRelease(candidates []releaseItem) (releaseItem, string, error) {
	valid := make([]releaseItem, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := uuid.Parse(candidate.ID); err == nil && strings.TrimSpace(candidate.Title) != "" {
			valid = append(valid, candidate)
		}
	}
	if len(valid) == 0 {
		return releaseItem{}, "", ErrNoUsableEditions
	}
	sort.SliceStable(valid, func(left, right int) bool {
		a, b := valid[left], valid[right]
		aOfficial := strings.EqualFold(a.Status, "official")
		bOfficial := strings.EqualFold(b.Status, "official")
		if aOfficial != bOfficial {
			return aOfficial
		}
		aDate, bDate := normalizedReleaseDate(a.Date), normalizedReleaseDate(b.Date)
		if aDate != bDate {
			return aDate < bDate
		}
		if (a.Country != "") != (b.Country != "") {
			return a.Country != ""
		}
		return a.ID < b.ID
	})
	reason := "Earliest official edition, with a stable MusicBrainz ID tie-break"
	if !strings.EqualFold(valid[0].Status, "official") {
		reason = "Earliest available edition, with a stable MusicBrainz ID tie-break"
	}
	return valid[0], reason, nil
}

func normalizedReleaseDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "9999-99-99"
	}
	return value + strings.Repeat("-99", 3-strings.Count(value, "-")-1)
}

// getJSON asks the provider one question and decodes the answer.
//
// A refusal for load is not an answer, so it is not the first one taken.
// MusicBrainz replies 503 when it is shedding load and 429 when it is being
// asked faster than it will serve, and both mean "not now" rather than anything
// about the music. The question is put again a bounded number of times, at
// whatever pace the provider asked for, and only a provider that held that
// position hands the refusal back.
func (client *Client) getJSON(ctx context.Context, path string, parameters url.Values, destination any) (json.RawMessage, error) {
	requestURL, err := url.Parse(client.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("build MusicBrainz URL: %w", err)
	}
	requestURL.RawQuery = parameters.Encode()

	for attempt := 0; ; attempt++ {
		metadata, asked, err := client.fetch(ctx, requestURL.String())
		if errors.Is(err, ErrThrottled) && attempt < throttleAttempts-1 {
			if waitErr := stand(ctx, throttleWait(client.interval, attempt, asked)); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(metadata, destination); err != nil {
			return nil, fmt.Errorf("decode MusicBrainz response: %w", err)
		}
		return metadata, nil
	}
}

// fetch puts one request. A refusal carries how long the provider asked to be
// left alone, when it said.
func (client *Client) fetch(
	ctx context.Context, requestURL string,
) (json.RawMessage, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create MusicBrainz request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)

	if err := client.wait(ctx); err != nil {
		return nil, 0, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, retryAfter(response.Header.Get("Retry-After")),
			&HTTPError{StatusCode: response.StatusCode}
	}

	metadata, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read MusicBrainz response: %w", err)
	}
	if len(metadata) > maxResponseBody {
		return nil, 0, errors.New("MusicBrainz response is too large")
	}
	return json.RawMessage(metadata), 0, nil
}

// throttleWait is how long to stand back before asking again. What the provider
// asked for wins where it said so; otherwise the client's own politeness
// interval, doubling, so a provider under load is not met again at the pace that
// found it under load. Either way it is capped: a wait longer than that is
// better spent letting the caller reschedule.
func throttleWait(interval time.Duration, attempt int, asked time.Duration) time.Duration {
	wait := asked
	if wait <= 0 {
		wait = interval << attempt
	}
	if wait > maxThrottleWait {
		return maxThrottleWait
	}
	return wait
}

// retryAfter reads the header a refusal carries. Only the delay-seconds form is
// read: the HTTP-date form needs the provider's clock to agree with ours, and a
// header that cannot be read is silence rather than a reason to wait wrongly.
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// stand waits, unless the caller has stopped waiting.
func stand(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (client *Client) wait(ctx context.Context) error {
	client.mu.Lock()
	defer client.mu.Unlock()

	delay := time.Until(client.lastRequest.Add(client.interval))
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	client.lastRequest = time.Now()
	return nil
}

// ErrThrottled reports that the provider would not answer this time because it
// is being asked too often or is shedding load.
//
// It is a fact about how hard MusicBrainz is being pushed and never about the
// music: nothing is known after it that was not known before it. A caller must
// not write it down as what an entry or a file turned out to be, and must not
// spend anything it counts on it — it is a reason to come back, not an answer.
var ErrThrottled = errors.New("MusicBrainz is refusing requests for now")

type HTTPError struct {
	StatusCode int
}

func (err *HTTPError) Error() string {
	return fmt.Sprintf("MusicBrainz returned HTTP %d", err.StatusCode)
}

// Unwrap names the two statuses that mean "not now" as backpressure without
// losing which one it was: 503 is the provider shedding load and 429 is its rate
// limiter. Every other status is an ordinary failure and stays one.
func (err *HTTPError) Unwrap() error {
	switch err.StatusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return ErrThrottled
	}
	return nil
}

type artistSearchResponse struct {
	Artists []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		SortName       string `json:"sort-name"`
		Type           string `json:"type"`
		Country        string `json:"country"`
		Disambiguation string `json:"disambiguation"`
		Score          score  `json:"score"`
		Area           struct {
			Name string `json:"name"`
		} `json:"area"`
	} `json:"artists"`
}

type artistLookupResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SortName       string `json:"sort-name"`
	Type           string `json:"type"`
	Country        string `json:"country"`
	Disambiguation string `json:"disambiguation"`
	Area           struct {
		Name string `json:"name"`
	} `json:"area"`
	Genres []genreItem `json:"genres"`
}

type releaseGroupBrowseResponse struct {
	Count         int                `json:"release-group-count"`
	ReleaseGroups []releaseGroupItem `json:"release-groups"`
}

type releaseGroupItem struct {
	ID               string      `json:"id"`
	Title            string      `json:"title"`
	FirstReleaseDate string      `json:"first-release-date"`
	PrimaryType      string      `json:"primary-type"`
	SecondaryTypes   []string    `json:"secondary-types"`
	Genres           []genreItem `json:"genres"`
}

type releaseBrowseResponse struct {
	Count    int           `json:"release-count"`
	Releases []releaseItem `json:"releases"`
}

type releaseItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Date    string `json:"date"`
	Country string `json:"country"`
}

type releaseLookupResponse struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	Status       string             `json:"status"`
	Date         string             `json:"date"`
	Country      string             `json:"country"`
	Barcode      string             `json:"barcode"`
	ArtistCredit []artistCreditItem `json:"artist-credit"`
	Media        []struct {
		Position int `json:"position"`
		Tracks   []struct {
			Position     int                `json:"position"`
			Title        string             `json:"title"`
			Length       *int               `json:"length"`
			ArtistCredit []artistCreditItem `json:"artist-credit"`
			Recording    struct {
				ID     string   `json:"id"`
				Title  string   `json:"title"`
				Length *int     `json:"length"`
				ISRCs  []string `json:"isrcs"`
			} `json:"recording"`
		} `json:"tracks"`
	} `json:"media"`
}

type score int

func (value *score) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(string(data), `"`)
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("decode score: %w", err)
	}
	*value = score(parsed)
	return nil
}

func (value score) Int() int {
	return int(value)
}

// URLRelation is something MusicBrainz says this entity is linked to: what kind
// of link it is, and where it points.
type URLRelation struct {
	Type     string
	Resource string
}

// ArtistURLRelations returns what MusicBrainz says this artist is linked to.
//
// MusicBrainz stores no images itself. It stores relations: sometimes an "image"
// pointing at a Wikimedia Commons file page, far more often a "wikidata" pointing
// at the entity that holds the picture. Both are statements about the artist
// whose ID the catalogue already holds, and turning either into bytes is the
// caller's business because that is a different service at a different address.
//
// This is display and never evidence. Nothing about a file, a recording or a
// release is decided from it; it exists so an artist has a face beside their
// name.
func (client *Client) ArtistURLRelations(ctx context.Context, id uuid.UUID) ([]URLRelation, error) {
	if id == uuid.Nil {
		return nil, errors.New("artist MusicBrainz ID is required")
	}
	var payload artistRelationsResponse
	_, err := client.getJSON(ctx, "/artist/"+id.String(), url.Values{
		"fmt": []string{"json"},
		"inc": []string{"url-rels"},
	}, &payload)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("look up MusicBrainz artist relations: %w", err)
	}
	var found []URLRelation
	for _, relation := range payload.Relations {
		if resource := strings.TrimSpace(relation.URL.Resource); resource != "" {
			found = append(found, URLRelation{Type: relation.Type, Resource: resource})
		}
	}
	return found, nil
}

type artistRelationsResponse struct {
	Relations []struct {
		Type string `json:"type"`
		URL  struct {
			Resource string `json:"resource"`
		} `json:"url"`
	} `json:"relations"`
}
