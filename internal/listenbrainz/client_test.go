package listenbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient points both hosts at the same test server. No test here reaches
// the real service.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(Options{
		BaseURL:   server.URL,
		LabsURL:   server.URL,
		UserAgent: "schall-test/0.0 (test)",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestNewClientRequiresAUserAgent(t *testing.T) {
	if _, err := NewClient(Options{UserAgent: "  "}); err == nil {
		t.Fatal("NewClient() accepted an empty user agent")
	}
}

// The addresses have a right answer, so an installation that never sets them
// gets the hosted service rather than a validation failure.
func TestNewClientDefaultsBothHosts(t *testing.T) {
	client, err := NewClient(Options{UserAgent: "schall/0.0"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.baseURL != DefaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", client.baseURL, DefaultBaseURL)
	}
	if client.labsURL != DefaultLabsURL {
		t.Fatalf("labsURL = %q, want %q", client.labsURL, DefaultLabsURL)
	}
}

func TestNewClientRefusesANonHTTPHost(t *testing.T) {
	if _, err := NewClient(Options{UserAgent: "schall/0.0", BaseURL: "ftp://example.test"}); err == nil {
		t.Fatal("NewClient() accepted an ftp base URL")
	}
}

func TestRecommendationsReadsTheBatchAndItsProvenance(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/1/cf/recommendation/user/listener/recording" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("count"); got != "25" {
			t.Errorf("count = %q, want 25", got)
		}
		if got := request.Header.Get("User-Agent"); got != "schall-test/0.0 (test)" {
			t.Errorf("User-Agent = %q", got)
		}
		if _, ok := request.Header["Authorization"]; ok {
			t.Error("sent an Authorization header without a stored token")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"payload":{
			"mbids":[
				{"recording_mbid":"11111111-1111-1111-1111-111111111111","score":0.9,
				 "latest_listened_at":"2026-08-01T12:00:00+00:00"},
				{"recording_mbid":"22222222-2222-2222-2222-222222222222","score":0.4,
				 "latest_listened_at":null},
				{"recording_mbid":"  ","score":0.1}
			],
			"last_updated":1754870400,
			"model_id":"model-2026-08",
			"total_mbid_count":1000,
			"offset":0
		}}`))
	})

	result, err := client.Recommendations(context.Background(), "listener", 25, 0)
	if err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	// The blank identifier is dropped: a candidate with no recording MBID
	// cannot be put through a single suppression rule.
	if len(result.Recordings) != 2 {
		t.Fatalf("read %d recordings, want 2", len(result.Recordings))
	}
	if result.Recordings[0].RecordingMBID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("first recording = %q", result.Recordings[0].RecordingMBID)
	}
	if result.Recordings[0].LatestListenedAt.IsZero() {
		t.Error("a scrobbled recommendation came back as never listened to")
	}
	if !result.Recordings[1].LatestListenedAt.IsZero() {
		t.Error("a null latest_listened_at should read as never")
	}
	if result.ModelID != "model-2026-08" {
		t.Errorf("model = %q", result.ModelID)
	}
	if result.LastUpdated.IsZero() {
		t.Error("the batch came back without a computed-at time")
	}
	if result.Total != 1000 {
		t.Errorf("total = %d, want 1000", result.Total)
	}
}

// A recommendation carries a timestamp whose encoding the ADR did not pin down,
// and nothing depends on it. An encoding this client did not expect must cost
// that one field, never the whole answer.
func TestRecommendationsSurviveAnUnreadableListenTime(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"payload":{"mbids":[
			{"recording_mbid":"11111111-1111-1111-1111-111111111111","score":0.9,
			 "latest_listened_at":"the day before yesterday"}
		]}}`))
	})

	result, err := client.Recommendations(context.Background(), "listener", 1, 0)
	if err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if len(result.Recordings) != 1 {
		t.Fatalf("read %d recordings, want 1", len(result.Recordings))
	}
	if !result.Recordings[0].LatestListenedAt.IsZero() {
		t.Error("an unreadable timestamp should read as never")
	}
}

func TestRecommendationsSendTheStoredToken(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen = request.Header.Get("Authorization")
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Options{BaseURL: server.URL, UserAgent: "schall/0.0", UserToken: "secret"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if seen != "Token secret" {
		t.Fatalf("Authorization = %q, want %q", seen, "Token secret")
	}
}

func TestRecommendationsRequireAUsername(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("asked the service without a username")
	})
	if _, err := client.Recommendations(context.Background(), " ", 1, 0); err == nil {
		t.Fatal("Recommendations() accepted an empty username")
	}
}

// 204 is the service saying the model is not computed yet. It is a distinct
// answer from an empty list, and reading it as one would tell the user their
// taste has nothing in it.
func TestNoComputedModelIsItsOwnAnswer(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	_, err := client.Recommendations(context.Background(), "listener", 1, 0)
	if !errors.Is(err, ErrNoRecommendations) {
		t.Fatalf("Recommendations() error = %v, want ErrNoRecommendations", err)
	}
}

func TestAnUnknownAccountIsNotFound(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
		_, _ = response.Write([]byte(`{"code":404,"error":"Cannot find user: nobody"}`))
	})
	_, err := client.Recommendations(context.Background(), "nobody", 1, 0)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Recommendations() error = %v, want ErrNotFound", err)
	}
}

func TestBackpressureIsNamedWithoutLosingTheStatus(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(status)
		})
		_, err := client.Recommendations(context.Background(), "listener", 1, 0)
		if !errors.Is(err, ErrThrottled) {
			t.Fatalf("HTTP %d error = %v, want ErrThrottled", status, err)
		}
		var httpError *HTTPError
		if !errors.As(err, &httpError) || httpError.StatusCode != status {
			t.Fatalf("HTTP %d lost its status: %v", status, err)
		}
	}
}

// A failure is an error, never an empty answer: could-not-ask is not
// nothing-there (ADR 0002).
func TestAServerFaultIsAnErrorRatherThanAnEmptyList(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})
	result, err := client.Recommendations(context.Background(), "listener", 1, 0)
	if err == nil {
		t.Fatal("Recommendations() reported success on HTTP 500")
	}
	if len(result.Recordings) != 0 {
		t.Fatalf("a failed call returned %d recordings", len(result.Recordings))
	}
}

func TestTopArtistsDropEntriesWithoutAnIdentifier(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/1/stats/user/listener/artists" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("range"); got != "month" {
			t.Errorf("range = %q, want month", got)
		}
		_, _ = response.Write([]byte(`{"payload":{"artists":[
			{"artist_mbid":"33333333-3333-3333-3333-333333333333","artist_name":"A","listen_count":40},
			{"artist_mbid":null,"artist_name":"Unidentified","listen_count":9}
		]}}`))
	})

	artists, err := client.TopArtists(context.Background(), "listener", "month")
	if err != nil {
		t.Fatalf("TopArtists() error = %v", err)
	}
	if len(artists) != 1 {
		t.Fatalf("read %d artists, want 1", len(artists))
	}
	if artists[0].ListenCount != 40 {
		t.Errorf("listen count = %d, want 40", artists[0].ListenCount)
	}
}

func TestTopRecordingsReadTheStatistic(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/1/stats/user/listener/recordings" {
			t.Errorf("path = %q", request.URL.Path)
		}
		_, _ = response.Write([]byte(`{"payload":{"recordings":[
			{"recording_mbid":"44444444-4444-4444-4444-444444444444",
			 "track_name":"A Song","artist_name":"A","listen_count":12}
		]}}`))
	})

	recordings, err := client.TopRecordings(context.Background(), "listener", "year")
	if err != nil {
		t.Fatalf("TopRecordings() error = %v", err)
	}
	if len(recordings) != 1 || recordings[0].Name != "A Song" {
		t.Fatalf("read %+v", recordings)
	}
}

// The statistics endpoints answer 204 when the statistic has not been computed
// for that account and range — the same shape as the recommendation endpoint.
func TestAnUncomputedStatisticIsTheSameAnswer(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	if _, err := client.TopArtists(context.Background(), "listener", "month"); !errors.Is(err, ErrNoRecommendations) {
		t.Fatalf("TopArtists() error = %v, want ErrNoRecommendations", err)
	}
}

func TestSimilarRecordingsCarryTheSeedTheyCameFrom(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/similar-recordings/json" {
			t.Errorf("path = %q", request.URL.Path)
		}
		query := request.URL.Query()
		if got := query.Get("recording_mbids"); got != "55555555-5555-5555-5555-555555555555" {
			t.Errorf("recording_mbids = %q", got)
		}
		if got := query.Get("algorithm"); got != "session_based" {
			t.Errorf("algorithm = %q", got)
		}
		_, _ = response.Write([]byte(`[
			{"recording_mbid":"66666666-6666-6666-6666-666666666666","recording_name":"Nearby",
			 "artist_credit_name":"Somebody","artist_credit_mbids":null,
			 "release_mbid":"77777777-7777-7777-7777-777777777777","release_name":"A Record",
			 "score":8,"reference_mbid":"55555555-5555-5555-5555-555555555555"}
		]`))
	})

	similar, err := client.SimilarRecordings(context.Background(),
		[]string{"55555555-5555-5555-5555-555555555555", "  "}, "session_based")
	if err != nil {
		t.Fatalf("SimilarRecordings() error = %v", err)
	}
	if len(similar) != 1 {
		t.Fatalf("read %d recordings, want 1", len(similar))
	}
	if similar[0].ReferenceMBID != "55555555-5555-5555-5555-555555555555" {
		t.Errorf("reference = %q", similar[0].ReferenceMBID)
	}
	if similar[0].ArtistCreditName != "Somebody" {
		t.Errorf("artist credit = %q", similar[0].ArtistCreditName)
	}
}

func TestSimilarRecordingsRefuseWithoutASeed(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("asked the labs host without a seed")
	})
	if _, err := client.SimilarRecordings(context.Background(), []string{" "}, "session_based"); err == nil {
		t.Fatal("SimilarRecordings() accepted an empty seed list")
	}
}

// The algorithm is a closed enum the labs host refuses an unlisted value of, so
// asking with none would spend a request to be told what this can say for free.
func TestSimilarRecordingsRefuseWithoutAnAlgorithm(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("asked the labs host without an algorithm")
	})
	if _, err := client.SimilarRecordings(context.Background(), []string{"abc"}, " "); err == nil {
		t.Fatal("SimilarRecordings() accepted an empty algorithm")
	}
}

func TestCheckConnectionDescribesTheBatch(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(
			`{"payload":{"mbids":[],"total_mbid_count":1000,"model_id":"model-2026-08","last_updated":1754870400}}`))
	})

	status, err := client.CheckConnection(context.Background(), "listener")
	if err != nil {
		t.Fatalf("CheckConnection() error = %v", err)
	}
	for _, want := range []string{"1000", "listener", "model-2026-08"} {
		if !strings.Contains(status.Detail, want) {
			t.Errorf("detail %q does not mention %q", status.Detail, want)
		}
	}
}

// An account with no computed model is connected. Reporting it as a failure
// would tell the user their settings are wrong when they are right.
func TestCheckConnectionTreatsAnUncomputedModelAsConnected(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})

	status, err := client.CheckConnection(context.Background(), "listener")
	if err != nil {
		t.Fatalf("CheckConnection() error = %v, want success", err)
	}
	if !strings.Contains(status.Detail, "not built a recommendation model") {
		t.Fatalf("detail = %q", status.Detail)
	}
}

func TestCheckConnectionSaysWhichUsernameWasRefused(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	_, err := client.CheckConnection(context.Background(), "nobody")
	if err == nil {
		t.Fatal("CheckConnection() reported success for an unknown account")
	}
	if !strings.Contains(err.Error(), "nobody") {
		t.Fatalf("error %q does not name the account", err)
	}
}

// The published budget is spent as requests go out, not only when a response
// says so: mu is released before the request leaves, so several callers reading
// the same remaining of one would otherwise all be let through.
//
// The budget is published once and never again, so what governs the third call
// is what this client counted down. Without the decrement it would still read
// one remaining and go out at once.
func TestTheBudgetIsSpentAsRequestsGoOut(t *testing.T) {
	var calls int
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			response.Header().Set("X-RateLimit-Remaining", "1")
			response.Header().Set("X-RateLimit-Reset-In", "1")
		}
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	})

	// The first goes out with no budget known and learns that one is left; the
	// second spends it.
	for range 2 {
		if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
			t.Fatalf("Recommendations() error = %v", err)
		}
	}

	start := time.Now()
	if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("the third call went out after %s, want it held until the window turned over", elapsed)
	}
}

// The labs host is a separate rate-limit domain. A generous budget there must
// not erase a spent one on the API host, or the next call goes out inside a
// window that host has already refused.
func TestEachHostKeepsItsOwnBudget(t *testing.T) {
	labs := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-RateLimit-Remaining", "49")
		response.Header().Set("X-RateLimit-Reset-In", "10")
		_, _ = response.Write([]byte(`[]`))
	}))
	t.Cleanup(labs.Close)

	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		apiCalls++
		if apiCalls == 1 {
			response.Header().Set("X-RateLimit-Remaining", "0")
			response.Header().Set("X-RateLimit-Reset-In", "1")
		}
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	}))
	t.Cleanup(api.Close)

	client, err := NewClient(Options{BaseURL: api.URL, LabsURL: labs.URL, UserAgent: "schall/0.0"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	// The API host says its window is spent; the labs host then says it has
	// plenty, which is a statement about the labs host alone.
	if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if _, err := client.SimilarRecordings(context.Background(), []string{"abc"}, "session_based"); err != nil {
		t.Fatalf("SimilarRecordings() error = %v", err)
	}

	start := time.Now()
	if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("the API call went out after %s, want the labs budget to have left it spent", elapsed)
	}
}

// The pace is read off the wire: a response saying the window is spent holds the
// next request until that window turns over, rather than sending it to be
// refused.
func TestASpentBudgetHoldsTheNextRequest(t *testing.T) {
	var calls int
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			response.Header().Set("X-RateLimit-Remaining", "0")
			response.Header().Set("X-RateLimit-Reset-In", "1")
		}
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	})

	if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}

	start := time.Now()
	if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("the second call went out after %s, want it held to the reset window", elapsed)
	}
}

func TestABudgetStillLeftDoesNotWait(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-RateLimit-Remaining", "29")
		response.Header().Set("X-RateLimit-Reset-In", "10")
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	})

	start := time.Now()
	for range 3 {
		if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
			t.Fatalf("Recommendations() error = %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("three calls inside the budget took %s", elapsed)
	}
}

// A host that publishes no budget is not a host with an empty one.
func TestAHostWithoutRateHeadersIsNotHeldBack(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`[]`))
	})

	for range 3 {
		if _, err := client.SimilarRecordings(context.Background(), []string{"abc"}, "session_based"); err != nil {
			t.Fatalf("SimilarRecordings() error = %v", err)
		}
	}
}

// The labs host answers 204 about the seed it was asked for, not about the
// account. Reusing the account's sentinel would have a caller repeating a claim
// about the user that the labs host never made.
func TestAnEmptyLabsAnswerIsNotAClaimAboutTheAccount(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})

	_, err := client.SimilarRecordings(context.Background(), []string{"abc"}, "session_based")
	if !errors.Is(err, ErrNoSimilarRecordings) {
		t.Fatalf("SimilarRecordings() error = %v, want ErrNoSimilarRecordings", err)
	}
	if errors.Is(err, ErrNoRecommendations) {
		t.Error("a labs answer was reported as a statement about the account")
	}
}

// The point of the connection check is a message somebody can act on, and the
// service describes what it refused in the body.
func TestARefusalCarriesTheServiceOwnWords(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`{"code":400,"error":"Invalid algorithm. Must be one of session_based"}`))
	})

	_, err := client.Recommendations(context.Background(), "listener", 1, 0)
	if err == nil {
		t.Fatal("Recommendations() reported success on HTTP 400")
	}
	if !strings.Contains(err.Error(), "Invalid algorithm") {
		t.Fatalf("error = %q, want the service's own reason", err)
	}
}

// A refusal that says nothing useful leaves the status to speak for itself
// rather than becoming an empty sentence.
func TestARefusalWithoutAReasonStillNamesTheStatus(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`<html>gateway</html>`))
	})

	_, err := client.Recommendations(context.Background(), "listener", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v, want the status named", err)
	}
}

// The token is for the API host. The labs dataset host asks for no
// authentication, so a credential it has no use for never reaches it.
func TestTheTokenNeverReachesTheLabsHost(t *testing.T) {
	var labsAuth, apiAuth string
	labs := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		labsAuth = request.Header.Get("Authorization")
		_, _ = response.Write([]byte(`[]`))
	}))
	t.Cleanup(labs.Close)
	api := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		apiAuth = request.Header.Get("Authorization")
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	}))
	t.Cleanup(api.Close)

	client, err := NewClient(Options{
		BaseURL: api.URL, LabsURL: labs.URL, UserAgent: "schall/0.0", UserToken: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Recommendations(context.Background(), "listener", 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if _, err := client.SimilarRecordings(context.Background(), []string{"abc"}, "session_based"); err != nil {
		t.Fatalf("SimilarRecordings() error = %v", err)
	}

	if apiAuth != "Token secret" {
		t.Errorf("the API host was sent %q", apiAuth)
	}
	if labsAuth != "" {
		t.Errorf("the labs host was sent %q, want no credential", labsAuth)
	}
}

func TestListensReadsAPageWithItsMapping(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/1/user/listener/listens" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("max_ts"); got != "1788627200" {
			t.Errorf("max_ts = %q, want 1788627200", got)
		}
		if got := request.URL.Query().Get("count"); got != "2" {
			t.Errorf("count = %q, want 2", got)
		}
		if got := request.URL.Query().Get("min_ts"); got != "" {
			t.Errorf("min_ts = %q, want none", got)
		}
		_, _ = response.Write([]byte(`{"payload":{"count":2,"listens":[
			{"listened_at":1788627181,"track_metadata":{"artist_name":"Pashanim","track_name":"RondoNumbaNine","release_name":"traence",
			 "mbid_mapping":{"recording_mbid":"8bc0f5f1-d052-429b-9123-1040f7e2b4b6","release_mbid":"11111111-1111-1111-1111-111111111111","caa_release_mbid":"22222222-2222-2222-2222-222222222222","artist_mbids":["33333333-3333-3333-3333-333333333333"]}}},
			{"listened_at":1788627052,"track_metadata":{"artist_name":"","track_name":"Nameless"}}
		]}}`))
	})

	listens, err := client.Listens(context.Background(), "listener", time.Time{}, time.Unix(1788627200, 0), 2)
	if err != nil {
		t.Fatalf("Listens() error = %v", err)
	}
	if len(listens) != 1 {
		t.Fatalf("read %d listens, want 1 (the one with no artist name is dropped)", len(listens))
	}
	got := listens[0]
	if !got.ListenedAt.Equal(time.Unix(1788627181, 0)) {
		t.Errorf("listened at %v", got.ListenedAt)
	}
	if got.TrackName != "RondoNumbaNine" || got.ReleaseName != "traence" {
		t.Errorf("names = %q / %q", got.TrackName, got.ReleaseName)
	}
	if got.RecordingMBID != "8bc0f5f1-d052-429b-9123-1040f7e2b4b6" || got.CAAReleaseMBID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("mapping = %+v", got)
	}
	if len(got.ArtistMBIDs) != 1 {
		t.Errorf("artist mbids = %v", got.ArtistMBIDs)
	}
}

func TestListensTreatsNoContentAsAnEmptyPage(t *testing.T) {
	client := newTestClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	listens, err := client.Listens(context.Background(), "listener", time.Time{}, time.Time{}, 10)
	if err != nil || len(listens) != 0 {
		t.Fatalf("Listens() = %v, %v; want an empty page and no error", listens, err)
	}
}
