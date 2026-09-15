package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSearchArtists(t *testing.T) {
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		gotUserAgent = request.Header.Get("User-Agent")
		if request.URL.Query().Get("query") != "Portishead" {
			t.Errorf("query = %q, want Portishead", request.URL.Query().Get("query"))
		}
		if request.URL.Query().Get("fmt") != "json" || request.URL.Query().Get("limit") != "10" {
			t.Errorf("query parameters = %q", request.URL.RawQuery)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"artists": [{
				"id": "8e40d6bd-8c3c-419f-9f0d-aa15f22b4b8c",
				"name": "Portishead",
				"sort-name": "Portishead",
				"type": "Group",
				"country": "GB",
				"area": {"name": "United Kingdom"},
				"disambiguation": "English band",
				"score": "100"
			}]
		}`))
	}))
	defer server.Close()

	client, err := NewClient(Options{
		BaseURL:   server.URL,
		UserAgent: "Schall/test (test@example.com)",
		Interval:  time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	artists, err := client.SearchArtists(context.Background(), " Portishead ", 10)
	if err != nil {
		t.Fatalf("SearchArtists() error = %v", err)
	}
	if gotUserAgent != "Schall/test (test@example.com)" {
		t.Errorf("User-Agent = %q", gotUserAgent)
	}
	if len(artists) != 1 {
		t.Fatalf("len(artists) = %d, want 1", len(artists))
	}
	artist := artists[0]
	if artist.Name != "Portishead" || artist.SortName != "Portishead" || artist.Score != 100 {
		t.Errorf("artist = %#v", artist)
	}
}

func TestSearchArtistsSkipsInvalidResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"artists":[{"id":"not-a-uuid","name":"Unknown","sort-name":"Unknown","score":50}]}`))
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	artists, err := client.SearchArtists(context.Background(), "Unknown", 10)
	if err != nil {
		t.Fatalf("SearchArtists() error = %v", err)
	}
	if len(artists) != 0 {
		t.Errorf("artists = %#v, want none", artists)
	}
}

func TestSearchArtistsReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.SearchArtists(context.Background(), "Björk", 10)
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("SearchArtists() error = %v, want HTTP 503 error", err)
	}
}

// A refusal for load is not an answer, so it is not the first one taken. The
// provider is asked again, and what it says then is the answer.
func TestARefusalForLoadIsAskedAgain(t *testing.T) {
	refused := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if !refused {
			refused = true
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"artists":[{
			"id": "8e40d6bd-8c3c-419f-9f0d-aa15f22b4b8c",
			"name": "Portishead", "sort-name": "Portishead", "score": "100"
		}]}`))
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	artists, err := client.SearchArtists(context.Background(), "Portishead", 10)
	if err != nil {
		t.Fatalf("SearchArtists() error = %v, want the answer the provider gave once it would answer", err)
	}
	if len(artists) != 1 {
		t.Fatalf("len(artists) = %d, want 1", len(artists))
	}
}

// A provider that holds its refusal is reported as backpressure, so that nothing
// above writes it down as something it learned about the music.
func TestAProviderThatKeepsRefusingIsBackpressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.SearchArtists(context.Background(), "Björk", 10)
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("SearchArtists() error = %v, want it to read as backpressure", err)
	}
}

// Backpressure is the two statuses that mean "not now" and nothing else. A
// provider that broke is an ordinary failure: asking it again would not help, and
// reading it as patience would defer the caller forever.
func TestAProviderThatBrokeIsNotBackpressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.SearchArtists(context.Background(), "Björk", 10)
	if errors.Is(err, ErrThrottled) {
		t.Fatalf("SearchArtists() error = %v, want HTTP 500 to stay an ordinary failure", err)
	}
	var httpError *HTTPError
	if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusInternalServerError {
		t.Fatalf("SearchArtists() error = %v, want HTTP 500 error", err)
	}
}

// What the provider asked to be left alone for is what it gets, up to the point
// where waiting it out would cost more than coming back later.
func TestTheProviderSetsHowLongToStandBack(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		attempt int
		asked   time.Duration
		want    time.Duration
	}{
		{name: "what the provider asked for", asked: 2 * time.Second, want: 2 * time.Second},
		{name: "the client's own pace when it did not say", want: time.Second},
		{name: "backing off when it keeps not saying", attempt: 2, want: 4 * time.Second},
		{name: "capped however long it asks for", asked: time.Hour, want: maxThrottleWait},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := throttleWait(time.Second, testCase.attempt, testCase.asked)
			if got != testCase.want {
				t.Errorf("throttleWait() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Retry-After is read as seconds, which is what MusicBrainz sends. The HTTP-date
// form needs the provider's clock to agree with ours, so it is left unread — a
// header nobody can read is silence rather than a reason to wait wrongly.
func TestRetryAfterIsReadOnlyAsSeconds(t *testing.T) {
	for header, want := range map[string]time.Duration{
		"3":                             3 * time.Second,
		" 3 ":                           3 * time.Second,
		"":                              0,
		"Wed, 21 Oct 2026 07:28:00 GMT": 0,
		"-1":                            0,
	} {
		if got := retryAfter(header); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestArtistCatalogue(t *testing.T) {
	artistID := uuid.MustParse("8e40d6bd-8c3c-419f-9f0d-aa15f22b4b8c")
	releaseGroupID := uuid.MustParse("15e0f8d9-9d8f-36ce-b41c-4a5785ba3e9e")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/artist/" + artistID.String():
			if request.URL.Query().Get("fmt") != "json" {
				t.Errorf("artist query = %q", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{
				"id":"` + artistID.String() + `",
				"name":"Portishead",
				"sort-name":"Portishead",
				"type":"Group",
				"country":"GB",
				"area":{"name":"United Kingdom"}
			}`))
		case "/release-group":
			if request.URL.Query().Get("artist") != artistID.String() ||
				request.URL.Query().Get("limit") != "100" ||
				request.URL.Query().Get("offset") != "0" {
				t.Errorf("release group query = %q", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{
				"release-group-count":1,
				"release-groups":[{
					"id":"` + releaseGroupID.String() + `",
					"title":"Dummy",
					"first-release-date":"1994-08-22",
					"primary-type":"Album",
					"secondary-types":[]
				}]
			}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(Options{
		BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	catalogue, err := client.ArtistCatalogue(context.Background(), artistID)
	if err != nil {
		t.Fatalf("ArtistCatalogue() error = %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
	if catalogue.Artist.Name != "Portishead" || catalogue.Artist.ID != artistID {
		t.Errorf("artist = %#v", catalogue.Artist)
	}
	if len(catalogue.ReleaseGroups) != 1 ||
		catalogue.ReleaseGroups[0].ID != releaseGroupID ||
		catalogue.ReleaseGroups[0].PrimaryType != "Album" {
		t.Errorf("release groups = %#v", catalogue.ReleaseGroups)
	}
	if len(catalogue.Metadata) == 0 || len(catalogue.ReleaseGroups[0].Metadata) == 0 {
		t.Error("provider metadata was not retained")
	}
}

func TestReleaseCatalogueSelectsEarliestOfficialEditionAndIngestsTracks(t *testing.T) {
	releaseGroupID := uuid.New()
	earlyReleaseID := uuid.New()
	laterReleaseID := uuid.New()
	recordingID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/release":
			if request.URL.Query().Get("release-group") != releaseGroupID.String() {
				t.Errorf("release query = %q", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{
				"release-count":2,
				"releases":[
					{"id":"` + laterReleaseID.String() + `","title":"Dummy","status":"Official","date":"1995-01-01","country":"US"},
					{"id":"` + earlyReleaseID.String() + `","title":"Dummy","status":"Official","date":"1994-08-22","country":"GB"}
				]
			}`))
		case "/release/" + earlyReleaseID.String():
			if request.URL.Query().Get("inc") != "recordings+artist-credits+isrcs" {
				t.Errorf("lookup query = %q", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{
				"id":"` + earlyReleaseID.String() + `",
				"title":"Dummy",
				"status":"Official",
				"date":"1994-08-22",
				"country":"GB",
				"barcode":"731451892224",
				"media":[{
					"position":1,
					"tracks":[{
						"position":1,
						"title":"Mysterons",
						"length":305000,
						"recording":{"id":"` + recordingID.String() + `","title":"Mysterons","isrcs":["GBBBN9400001"]}
					}]
				}]
			}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	catalogue, err := client.ReleaseCatalogue(context.Background(), releaseGroupID, uuid.Nil)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	if catalogue.Edition.ID != earlyReleaseID || catalogue.Edition.TrackCount != 1 {
		t.Errorf("edition = %#v", catalogue.Edition)
	}
	if len(catalogue.Tracks) != 1 || catalogue.Tracks[0].RecordingID != recordingID ||
		catalogue.Tracks[0].ISRC != "GBBBN9400001" {
		t.Errorf("tracks = %#v", catalogue.Tracks)
	}
}

// One track of an album can be credited to somebody the album is not, so the
// credit kept for a track is the one this release printed against it rather
// than the release's own.
func TestReleaseCatalogueKeepsTheCreditOfTheReleaseAndOfEachTrack(t *testing.T) {
	releaseGroupID := uuid.New()
	releaseID := uuid.New()
	recordingID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/release/"+releaseID.String() {
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write([]byte(`{
			"id":"` + releaseID.String() + `",
			"title":"Nurture",
			"status":"Official",
			"artist-credit":[
				{"name":"Porter Robinson","joinphrase":"",
				 "artist":{"id":"a0e7b4d9-0a06-45e5-9ba0-4b0b2fa3c9a8","name":"Porter Robinson"}}
			],
			"media":[{
				"position":1,
				"tracks":[{
					"position":1,
					"title":"Musician",
					"artist-credit":[
						{"name":"Porter Robinson","joinphrase":", ",
						 "artist":{"id":"a0e7b4d9-0a06-45e5-9ba0-4b0b2fa3c9a8","name":"Porter Robinson"}},
						{"name":"Ninajirachi","joinphrase":"",
						 "artist":{"id":"7f0a5ae7-1e2c-4a6f-9d4c-b5c1e0ab7fbc","name":"Ninajirachi"}}
					],
					"recording":{"id":"` + recordingID.String() + `","title":"Musician"}
				}]
			}]
		}`))
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	catalogue, err := client.ReleaseCatalogue(context.Background(), releaseGroupID, releaseID)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	wantEdition := []Credit{
		{Name: "Porter Robinson", ArtistID: mustUUID("a0e7b4d9-0a06-45e5-9ba0-4b0b2fa3c9a8"),
			ArtistName: "Porter Robinson"},
	}
	if !reflect.DeepEqual(catalogue.Edition.Credits, wantEdition) {
		t.Errorf("edition credits = %#v, want %#v", catalogue.Edition.Credits, wantEdition)
	}
	if len(catalogue.Tracks) != 1 {
		t.Fatalf("tracks = %#v, want one", catalogue.Tracks)
	}
	wantTrack := []Credit{
		{Name: "Porter Robinson", ArtistID: mustUUID("a0e7b4d9-0a06-45e5-9ba0-4b0b2fa3c9a8"),
			JoinPhrase: ", ", ArtistName: "Porter Robinson"},
		{Name: "Ninajirachi", ArtistID: mustUUID("7f0a5ae7-1e2c-4a6f-9d4c-b5c1e0ab7fbc"),
			ArtistName: "Ninajirachi"},
	}
	if len(catalogue.Tracks[0].Credits) != len(wantTrack) {
		t.Fatalf("track credits = %#v, want both artists", catalogue.Tracks[0].Credits)
	}
	for index, credit := range wantTrack {
		if !reflect.DeepEqual(catalogue.Tracks[0].Credits[index], credit) {
			t.Errorf("track credit %d = %#v, want %#v", index, catalogue.Tracks[0].Credits[index], credit)
		}
	}
}

func TestNewClientRequiresUserAgent(t *testing.T) {
	_, err := NewClient(Options{})
	if err == nil {
		t.Fatal("NewClient() error = nil, want user agent error")
	}
}

func TestNewClientRejectsSettingsItCannotUse(t *testing.T) {
	for name, options := range map[string]Options{
		"a base URL that is not one": {BaseURL: ":4533", UserAgent: "Schall/test"},
		"a negative pace":            {UserAgent: "Schall/test", Interval: -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(options); err == nil {
				t.Error("a client was built from settings it cannot use")
			}
		})
	}
}

// MusicBrainz asks to be left a second between requests, and a client that was
// given no pace of its own takes that one rather than none.
func TestAClientWithNoPaceOfItsOwnTakesTheDefault(t *testing.T) {
	client, err := NewClient(Options{UserAgent: "Schall/test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.interval != defaultInterval {
		t.Errorf("interval = %v, want the default of %v", client.interval, defaultInterval)
	}
}

// A slow answer from MusicBrainz is still an answer. Waiting only ten seconds
// failed every look-up of a large release group, so a client that was given no
// deadline of its own waits half a minute.
func TestAClientWithNoDeadlineOfItsOwnWaitsHalfAMinute(t *testing.T) {
	client, err := NewClient(Options{UserAgent: "Schall/test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want %v", client.httpClient.Timeout, 30*time.Second)
	}
}

// The pace is what keeps MusicBrainz answering at all, so it is kept between
// requests rather than only before the first.
func TestRequestsAreSpacedByTheClientsPace(t *testing.T) {
	client := pacedTestClient(t, 40*time.Millisecond, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"artists":[]}`))
	})

	start := time.Now()
	for range 2 {
		if _, err := client.SearchArtists(context.Background(), "Portishead", 10); err != nil {
			t.Fatalf("SearchArtists() error = %v", err)
		}
	}

	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("two requests took %v, want the second to wait out the pace", elapsed)
	}
}

// Waiting out the pace is still waiting, and a caller that has stopped waiting
// is not held in it.
func TestACallerThatGaveUpWhileBeingPacedIsNotHeldInIt(t *testing.T) {
	client := pacedTestClient(t, time.Hour, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"artists":[]}`))
	})
	if _, err := client.SearchArtists(context.Background(), "Portishead", 10); err != nil {
		t.Fatalf("SearchArtists() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.SearchArtists(ctx, "Portishead", 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("SearchArtists() error = %v, want the caller's deadline honoured", err)
	}
}

// Standing back from a refusal is waiting too, and a provider that asked for
// half a minute must not spend a caller's whole deadline sleeping.
func TestACallerThatGaveUpWhileStandingBackIsNotHeldInIt(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Retry-After", "30")
		response.WriteHeader(http.StatusServiceUnavailable)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.SearchArtists(ctx, "Portishead", 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("SearchArtists() error = %v, want the caller's deadline honoured", err)
	}
}

func TestSearchArtistsRefusesAQuestionItCannotAsk(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked a question that should not have been put")
	})
	for name, testCase := range map[string]struct {
		query string
		limit int
	}{
		"nothing to search for": {query: "   ", limit: 10},
		"no results asked for":  {query: "Portishead", limit: 0},
		"more than one page":    {query: "Portishead", limit: 101},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.SearchArtists(context.Background(), testCase.query, testCase.limit); err == nil {
				t.Error("the provider was asked anyway")
			}
		})
	}
}

// A body that is not the JSON the provider promised is a failure, not an empty
// answer that would read as "this artist has released nothing".
func TestAnAnswerThatIsNotJSONIsReported(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("<html>a proxy page</html>"))
	})

	_, err := client.SearchArtists(context.Background(), "Portishead", 10)

	if err == nil || !strings.Contains(err.Error(), "decode MusicBrainz response") {
		t.Errorf("error = %v, want the unreadable answer reported", err)
	}
}

// The provider prints a search score as a number on one endpoint and as a string
// on another. Anything that is neither is not a score, and is reported rather
// than read as zero.
func TestAScoreThatIsNeitherANumberNorADigitStringIsReported(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"artists":[{
			"id": "8e40d6bd-8c3c-419f-9f0d-aa15f22b4b8c",
			"name": "Portishead", "sort-name": "Portishead", "score": "very high"
		}]}`))
	})

	if _, err := client.SearchArtists(context.Background(), "Portishead", 10); err == nil {
		t.Error("a score nobody could read was accepted")
	}
}

// A body larger than anything MusicBrainz sends is refused rather than read into
// memory, because a provider answering with a gigabyte is not answering.
func TestAnAnswerTooLargeToBeOneIsRefused(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"artists":[`))
		filler := strings.Repeat(" ", 1<<16)
		for written := 0; written <= maxResponseBody; written += len(filler) {
			_, _ = response.Write([]byte(filler))
		}
		_, _ = response.Write([]byte(`]}`))
	})

	_, err := client.SearchArtists(context.Background(), "Portishead", 10)

	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %v, want the oversized answer refused", err)
	}
}

// A body that stopped halfway is not a short answer; it is no answer, and
// reading it as one would report an artist's catalogue as whatever arrived
// before the connection dropped.
func TestAnAnswerThatStoppedHalfwayIsReported(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Length", "4096")
		_, _ = response.Write([]byte(`{"artists":[`))
		if flusher, ok := response.(http.Flusher); ok {
			flusher.Flush()
		}
		if hijacker, ok := response.(http.Hijacker); ok {
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
			}
		}
	})

	if _, err := client.SearchArtists(context.Background(), "Portishead", 10); err == nil {
		t.Error("half an answer was read as a whole one")
	}
}

func TestAProviderThatCouldNotBeReachedIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()
	client, err := NewClient(Options{BaseURL: address, UserAgent: "Schall/test", Interval: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if _, err := client.SearchArtists(context.Background(), "Portishead", 10); err == nil {
		t.Error("an unreachable provider answered")
	}
}

func TestArtistCatalogueRefusesAnArtistNobodyNamed(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked about no artist at all")
	})

	if _, err := client.ArtistCatalogue(context.Background(), uuid.Nil); err == nil {
		t.Error("the provider was asked anyway")
	}
}

func TestArtistCatalogueReportsAProviderThatWouldNotAnswer(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := client.ArtistCatalogue(context.Background(), uuid.New()); err == nil {
		t.Error("a provider that would not answer was read as an artist with nothing")
	}
}

// An artist row with no name cannot be filed under one, and inventing a name for
// it would put music in the catalogue under something nobody proved.
func TestAnArtistTheProviderDidNotNameIsRefused(t *testing.T) {
	artistID := uuid.New()
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"id":"` + artistID.String() + `","name":"","sort-name":""}`))
	})

	_, err := client.ArtistCatalogue(context.Background(), artistID)

	if err == nil || !strings.Contains(err.Error(), "missing a name") {
		t.Errorf("error = %v, want the nameless artist refused", err)
	}
}

// Half a catalogue is worse than none: it would read as an artist who released
// only the albums that happened to arrive before the provider stopped answering.
func TestACatalogueThatLostAPageIsReportedRatherThanTruncated(t *testing.T) {
	artistID := uuid.New()
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/release-group" {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = response.Write([]byte(`{"id":"` + artistID.String() +
			`","name":"Portishead","sort-name":"Portishead"}`))
	})

	if _, err := client.ArtistCatalogue(context.Background(), artistID); err == nil {
		t.Error("a catalogue that lost a page was reported as complete")
	}
}

// The provider browses a hundred release groups at a time and says how many
// there are, so a prolific artist's catalogue is read to the end.
func TestACatalogueIsReadToTheEndOfItsPages(t *testing.T) {
	artistID := uuid.New()
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/release-group" {
			_, _ = response.Write([]byte(`{"id":"` + artistID.String() +
				`","name":"Portishead","sort-name":"Portishead"}`))
			return
		}
		offset := request.URL.Query().Get("offset")
		groups := make([]string, 0, 100)
		size := 100
		if offset == "100" {
			size = 20
		}
		for index := 0; index < size; index++ {
			groups = append(groups, `{"id":"`+uuid.New().String()+`","title":"Album `+offset+
				strconv.Itoa(index)+`","primary-type":"Album"}`)
		}
		_, _ = response.Write([]byte(`{"release-group-count":120,"release-groups":[` +
			strings.Join(groups, ",") + `]}`))
	})

	catalogue, err := client.ArtistCatalogue(context.Background(), artistID)
	if err != nil {
		t.Fatalf("ArtistCatalogue() error = %v", err)
	}
	if len(catalogue.ReleaseGroups) != 120 {
		t.Errorf("release groups = %d, want every page read", len(catalogue.ReleaseGroups))
	}
}

// A release group Schall cannot identify or name is not one it can file, and it
// is left out rather than filed under whatever the row happened to carry.
func TestReleaseGroupsTheProviderCouldNotIdentifyAreLeftOut(t *testing.T) {
	artistID := uuid.New()
	good := uuid.New()
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/release-group" {
			_, _ = response.Write([]byte(`{"id":"` + artistID.String() +
				`","name":"Portishead","sort-name":"Portishead"}`))
			return
		}
		_, _ = response.Write([]byte(`{"release-group-count":3,"release-groups":[
			{"id":"not-a-uuid","title":"Nameless"},
			{"id":"` + uuid.New().String() + `","title":"   "},
			{"id":"` + good.String() + `","title":"Dummy","primary-type":"Album"}
		]}`))
	})

	catalogue, err := client.ArtistCatalogue(context.Background(), artistID)
	if err != nil {
		t.Fatalf("ArtistCatalogue() error = %v", err)
	}
	if len(catalogue.ReleaseGroups) != 1 || catalogue.ReleaseGroups[0].ID != good {
		t.Errorf("release groups = %#v, want only the one that identified itself",
			catalogue.ReleaseGroups)
	}
}

func TestReleaseCatalogueRefusesAReleaseGroupNobodyNamed(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked about no release group at all")
	})

	if _, err := client.ReleaseCatalogue(context.Background(), uuid.Nil, uuid.Nil); err == nil {
		t.Error("the provider was asked anyway")
	}
}

// An edition somebody already settled on is looked up directly. Browsing the
// group again could pick a different one and quietly move the release.
func TestAnEditionAlreadySettledOnIsNotChosenAgain(t *testing.T) {
	chosen := uuid.New()
	browsed := false
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/release" {
			browsed = true
		}
		_, _ = response.Write([]byte(`{"id":"` + chosen.String() +
			`","title":"Dummy","status":"Official","media":[]}`))
	})

	catalogue, err := client.ReleaseCatalogue(context.Background(), uuid.New(), chosen)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	if browsed {
		t.Error("the release group was browsed although an edition was already chosen")
	}
	if catalogue.Edition.ID != chosen {
		t.Errorf("edition = %s, want the one already settled on", catalogue.Edition.ID)
	}
	if catalogue.Edition.SelectionReason != "Previously selected representative edition" {
		t.Errorf("reason = %q", catalogue.Edition.SelectionReason)
	}
}

// A release group whose editions none of them identify is one Schall cannot
// represent, and picking one anyway would mean inventing an edition.
func TestAReleaseGroupWithNoUsableEditionIsRefused(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"release-count":1,"releases":[{"id":"not-a-uuid","title":"Dummy"}]}`))
	})

	_, err := client.ReleaseCatalogue(context.Background(), uuid.New(), uuid.Nil)

	if err == nil || !strings.Contains(err.Error(), "no usable editions") {
		t.Errorf("error = %v, want the unrepresentable release group refused", err)
	}
}

func TestReleaseCatalogueReportsAFailureBrowsingEditions(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := client.ReleaseCatalogue(context.Background(), uuid.New(), uuid.Nil); err == nil {
		t.Error("a provider that would not answer was read as a release group with no editions")
	}
}

func TestReleaseCatalogueReportsAFailureLookingUpTheEdition(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/release" {
			_, _ = response.Write([]byte(`{"release-count":1,"releases":[{"id":"` +
				uuid.New().String() + `","title":"Dummy","status":"Official"}]}`))
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := client.ReleaseCatalogue(context.Background(), uuid.New(), uuid.Nil); err == nil {
		t.Error("an edition nobody could look up was read as one with no tracks")
	}
}

// The browse says what the edition is called even when the lookup does not, and
// a release with no title is one nobody can find in their own catalogue.
func TestAnEditionKeepsTheTitleItWasBrowsedUnder(t *testing.T) {
	releaseID := uuid.New()
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/release" {
			_, _ = response.Write([]byte(`{"release-count":1,"releases":[{"id":"` +
				releaseID.String() + `","title":"Dummy","status":"Official"}]}`))
			return
		}
		_, _ = response.Write([]byte(`{"id":"` + releaseID.String() + `","title":"","media":[]}`))
	})

	catalogue, err := client.ReleaseCatalogue(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	if catalogue.Edition.Title != "Dummy" {
		t.Errorf("title = %q, want the browsed title kept", catalogue.Edition.Title)
	}
}

// The provider omits a position on media and tracks often enough that reading it
// as zero would file a whole edition under disc 0, track 0.
func TestTracksTheProviderDidNotNumberAreNumberedByTheirOrder(t *testing.T) {
	releaseID := uuid.New()
	first, second := uuid.New(), uuid.New()
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"id":"` + releaseID.String() + `","title":"Dummy","media":[{
			"tracks":[
				{"title":"Mysterons","recording":{"id":"` + first.String() + `"}},
				{"title":"Sour Times","recording":{"id":"` + second.String() + `"}}
			]}]}`))
	})

	catalogue, err := client.ReleaseCatalogue(context.Background(), uuid.New(), releaseID)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	if len(catalogue.Tracks) != 2 {
		t.Fatalf("tracks = %#v", catalogue.Tracks)
	}
	for index, track := range catalogue.Tracks {
		if track.DiscNumber != 1 || track.TrackNumber != index+1 {
			t.Errorf("track %d sits at disc %d track %d", index, track.DiscNumber, track.TrackNumber)
		}
	}
}

// The provider hangs a track's title and length off the recording when the track
// row carries neither, and a track with no title anywhere is one nothing could
// be matched against.
func TestATrackFallsBackToWhatItsRecordingSays(t *testing.T) {
	releaseID := uuid.New()
	named, nameless := uuid.New(), uuid.New()
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"id":"` + releaseID.String() + `","title":"Dummy","media":[{
			"position":1,
			"tracks":[
				{"position":1,"title":"","recording":{"id":"` + named.String() +
			`","title":"Mysterons","length":305000}},
				{"position":2,"title":"","recording":{"id":"` + nameless.String() + `","title":"  "}},
				{"position":3,"title":"Roads","recording":{"id":"not-a-uuid"}}
			]}]}`))
	})

	catalogue, err := client.ReleaseCatalogue(context.Background(), uuid.New(), releaseID)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	if len(catalogue.Tracks) != 1 {
		t.Fatalf("tracks = %#v, want only the one that could be named and identified", catalogue.Tracks)
	}
	track := catalogue.Tracks[0]
	if track.Title != "Mysterons" || track.DurationMS == nil || *track.DurationMS != 305000 {
		t.Errorf("track = %#v, want the recording's title and length", track)
	}
}

// A release group with nothing official in it still has to be representable, and
// the reason has to say that is what happened.
func TestTheEarliestAvailableEditionStandsInWhenNoneIsOfficial(t *testing.T) {
	early, late := uuid.New(), uuid.New()
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/release" {
			_, _ = response.Write([]byte(`{"release-count":2,"releases":[
				{"id":"` + late.String() + `","title":"Dummy","status":"Bootleg","date":"2001-01-01"},
				{"id":"` + early.String() + `","title":"Dummy","status":"Promotion","date":"1994-08-22"}
			]}`))
			return
		}
		_, _ = response.Write([]byte(`{"id":"` + early.String() + `","title":"Dummy","media":[]}`))
	})

	catalogue, err := client.ReleaseCatalogue(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	if catalogue.Edition.ID != early {
		t.Errorf("edition = %s, want the earliest one there is", catalogue.Edition.ID)
	}
	if catalogue.Edition.SelectionReason != "Earliest available edition, with a stable MusicBrainz ID tie-break" {
		t.Errorf("reason = %q", catalogue.Edition.SelectionReason)
	}
}

// The list is what somebody picks an edition out of, so two editions of the
// same day are ordered by where they came out and then by an identifier that
// cannot change between runs.
func TestEditionsOfTheSameDayAreOrderedByCountryThenIdentifier(t *testing.T) {
	editions := listedEditions(t, `{"release-count":4,"releases":[
		{"id":"11111111-1111-4111-8111-111111111111","title":"Dummy","date":"1994-08-22"},
		{"id":"22222222-2222-4222-8222-222222222222","title":"Dummy","date":"1994-08-22","country":"GB"},
		{"id":"00000000-0000-4000-8000-000000000000","title":"Dummy","date":"1994-08-22"},
		{"id":"33333333-3333-4333-8333-333333333333","title":"Dummy"}
	]}`)

	want := []string{
		"00000000-0000-4000-8000-000000000000",
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	for index, id := range want {
		if editions[index].ID.String() != id {
			t.Fatalf("edition %d = %s, want %s", index, editions[index].ID, id)
		}
	}
}

// An edition nobody dated sorts after every dated one rather than before them,
// because a missing date is not the year zero.
func TestAnUndatedEditionSortsAfterTheDatedOnes(t *testing.T) {
	editions := listedEditions(t, `{"release-count":2,"releases":[
		{"id":"00000000-0000-4000-8000-000000000000","title":"Dummy"},
		{"id":"11111111-1111-4111-8111-111111111111","title":"Dummy","date":"2020"}
	]}`)

	if editions[0].Date != "2020" {
		t.Fatalf("first edition = %+v, want the dated one", editions[0])
	}
}

func TestListReleaseEditionsRefusesAReleaseGroupNobodyNamed(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked about no release group at all")
	})

	if _, err := client.ListReleaseEditions(context.Background(), uuid.Nil); err == nil {
		t.Error("the provider was asked anyway")
	}
}

func TestListReleaseEditionsReportsAProviderThatWouldNotAnswer(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := client.ListReleaseEditions(context.Background(), uuid.New()); err == nil {
		t.Error("a provider that would not answer was read as a group with no editions")
	}
}

// An edition that does not identify or name itself is not one somebody could
// choose, so it is left out of the list rather than shown as a blank row.
func TestEditionsThatIdentifyOrNameNothingAreLeftOut(t *testing.T) {
	editions := listedEditions(t, `{"release-count":3,"releases":[
		{"id":"not-a-uuid","title":"Dummy"},
		{"id":"00000000-0000-4000-8000-000000000000","title":"   "},
		{"id":"11111111-1111-4111-8111-111111111111","title":"Dummy"}
	]}`)

	if len(editions) != 1 || editions[0].Title != "Dummy" {
		t.Fatalf("editions = %#v, want only the one that named itself", editions)
	}
}

// The provider browses a hundred editions at a time. A release with more than
// that would otherwise be listed as holding only its first hundred.
func TestEveryPageOfEditionsIsRead(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		size := 100
		if request.URL.Query().Get("offset") == "100" {
			size = 5
		}
		releases := make([]string, 0, size)
		for index := 0; index < size; index++ {
			releases = append(releases, `{"id":"`+uuid.New().String()+`","title":"Dummy"}`)
		}
		_, _ = response.Write([]byte(`{"release-count":105,"releases":[` +
			strings.Join(releases, ",") + `]}`))
	})

	editions, err := client.ListReleaseEditions(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ListReleaseEditions() error = %v", err)
	}
	if len(editions) != 105 {
		t.Errorf("editions = %d, want every page read", len(editions))
	}
}

func TestArtistURLRelationsRefusesAnArtistNobodyNamed(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked about no artist at all")
	})

	if _, err := client.ArtistURLRelations(context.Background(), uuid.Nil); err == nil {
		t.Error("the provider was asked anyway")
	}
}

// MusicBrainz stores no images; it stores relations. What comes back is what it
// said the artist is linked to, and a relation pointing at nothing is nothing.
func TestArtistURLRelationsReportWhatTheProviderSaidTheArtistIsLinkedTo(t *testing.T) {
	var gotInclude string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotInclude = request.URL.Query().Get("inc")
		_, _ = response.Write([]byte(`{"relations":[
			{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q189991"}},
			{"type":"image","url":{"resource":"   "}},
			{"type":"free streaming","url":{"resource":"https://www.deezer.com/artist/1"}}
		]}`))
	})

	found, err := client.ArtistURLRelations(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ArtistURLRelations() error = %v", err)
	}
	if gotInclude != "url-rels" {
		t.Errorf("inc = %q, want url-rels", gotInclude)
	}
	if len(found) != 2 || found[0].Type != "wikidata" || found[1].Type != "free streaming" {
		t.Errorf("relations = %#v, want the two that point somewhere", found)
	}
}

func TestArtistURLRelationsReportsAnArtistTheProviderHasNoRecordOf(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := client.ArtistURLRelations(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// A provider having a bad afternoon is not an artist with no picture, and saying
// so would have the sweep write down an outage as a permanent absence.
func TestArtistURLRelationsReportsAFailureRatherThanNoRelations(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	_, err := client.ArtistURLRelations(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want a failure that is not an absence", err)
	}
}

// The official edition is what a person would name, whenever it came out. A
// bootleg that happens to predate it is not the release.
func TestAnOfficialEditionIsPreferredToAnEarlierUnofficialOne(t *testing.T) {
	official := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	edition := chosenEdition(t, `{"release-count":2,"releases":[
		{"id":"11111111-1111-4111-8111-111111111111","title":"Dummy","status":"Bootleg","date":"1990-01-01"},
		{"id":"22222222-2222-4222-8222-222222222222","title":"Dummy","status":"Official","date":"1994-08-22"}
	]}`)

	if edition.ID != official {
		t.Fatalf("edition = %s, want the official one", edition.ID)
	}
}

// Two editions of the same day are told apart by whether anybody said where they
// came out: an edition with a country is a documented one.
func TestAnEditionSayingWhereItCameOutIsPreferredToOneThatDoesNot(t *testing.T) {
	documented := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	edition := chosenEdition(t, `{"release-count":2,"releases":[
		{"id":"11111111-1111-4111-8111-111111111111","title":"Dummy","status":"Official","date":"1994-08-22"},
		{"id":"22222222-2222-4222-8222-222222222222","title":"Dummy","status":"Official","date":"1994-08-22","country":"GB"}
	]}`)

	if edition.ID != documented {
		t.Fatalf("edition = %s, want the edition that says where it came out", edition.ID)
	}
}

// Two editions alike in every way still have to produce the same choice on every
// ingest, or a release would move between editions for no reason.
func TestEditionsAlikeInEveryWayAreChosenBetweenByIdentifier(t *testing.T) {
	edition := chosenEdition(t, `{"release-count":2,"releases":[
		{"id":"22222222-2222-4222-8222-222222222222","title":"Dummy","status":"Official","date":"1994-08-22"},
		{"id":"11111111-1111-4111-8111-111111111111","title":"Dummy","status":"Official","date":"1994-08-22"}
	]}`)

	if edition.ID != uuid.MustParse("11111111-1111-4111-8111-111111111111") {
		t.Fatalf("edition = %s, want the stable identifier tie-break", edition.ID)
	}
}

// chosenEdition browses a release group answering with body and returns the
// edition the client settled on.
func chosenEdition(t *testing.T, body string) ReleaseEdition {
	t.Helper()
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/release" {
			_, _ = response.Write([]byte(body))
			return
		}
		_, _ = response.Write([]byte(`{"id":"` + strings.TrimPrefix(request.URL.Path, "/release/") +
			`","title":"Dummy","media":[]}`))
	})
	catalogue, err := client.ReleaseCatalogue(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatalf("ReleaseCatalogue() error = %v", err)
	}
	return catalogue.Edition
}

// listedEditions asks for one page of editions and returns them in the order the
// client put them in.
func listedEditions(t *testing.T, body string) []ReleaseEdition {
	t.Helper()
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(body))
	})
	editions, err := client.ListReleaseEditions(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("ListReleaseEditions() error = %v", err)
	}
	return editions
}

func pacedTestClient(t *testing.T, interval time.Duration, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL: server.URL, UserAgent: "Schall/test", Interval: interval,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestTheVariousArtistsPlaceholderIsNotAskedAbout(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := NewClient(Options{
		BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.ArtistCatalogue(context.Background(), VariousArtistsID); !errors.Is(err, ErrNotAnArtist) {
		t.Fatalf("ArtistCatalogue() error = %v, want ErrNotAnArtist", err)
	}
	if requests != 0 {
		t.Errorf("requests = %d, want none", requests)
	}
}

// Various Artists is the placeholder that shows up in a real library, and it is
// not the only one MusicBrainz has. Each of these was looked up on 2026-08-29
// and is a special-purpose row rather than somebody who records.
func TestEverySpecialPurposeArtistIsRefused(t *testing.T) {
	placeholders := map[string]string{
		"Various Artists": "89ad4ac3-39f7-470e-963a-56509c546377",
		"[unknown]":       "125ec42a-7229-4250-afc5-e057484327fe",
		"[anonymous]":     "f731ccc4-e22a-43af-a747-64213329e088",
		"[traditional]":   "9be7f096-97ec-4615-8957-8d40b5dcbc41",
		"[no artist]":     "eec63d3c-3b81-4ad4-b1e4-7c147d4d2b61",
		"[data]":          "33cf029c-63b0-41a0-9855-be2a3665fb3b",
		"[dialogue]":      "314e1c25-dde7-4e4d-b2f4-0a7b9f7c56dc",
	}
	for name, id := range placeholders {
		if !IsPlaceholderArtist(uuid.MustParse(id)) {
			t.Errorf("IsPlaceholderArtist(%s) = false, want %s refused", id, name)
		}
	}
	if IsPlaceholderArtist(uuid.MustParse("83d91898-7763-47d7-b03b-b92132375c47")) {
		t.Error("IsPlaceholderArtist() refused Pink Floyd, want a real artist allowed")
	}
}

func TestACatalogueTooLargeToReadIsRefusedAfterOneBrowse(t *testing.T) {
	artistID := uuid.MustParse("2c5b4f0b-1ec9-4b6b-9c1c-3a3f8e1a44dd")
	var browses int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/artist/" + artistID.String():
			_, _ = response.Write([]byte(`{
				"id":"` + artistID.String() + `",
				"name":"Crowded",
				"sort-name":"Crowded"
			}`))
		case "/release-group":
			browses++
			_, _ = response.Write([]byte(`{
				"release-group-count":290302,
				"release-groups":[{"id":"15e0f8d9-9d8f-36ce-b41c-4a5785ba3e9e","title":"One"}]
			}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(Options{
		BaseURL: server.URL, UserAgent: "Schall/test", Interval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.ArtistCatalogue(context.Background(), artistID); !errors.Is(err, ErrCatalogueTooLarge) {
		t.Fatalf("ArtistCatalogue() error = %v, want ErrCatalogueTooLarge", err)
	}
	if browses != 1 {
		t.Errorf("browses = %d, want 1", browses)
	}
}
