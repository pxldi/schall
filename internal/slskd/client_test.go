package slskd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(Options{
		BaseURL:       server.URL,
		APIKey:        "test-key",
		SearchTimeout: 2 * time.Second,
		PollInterval:  time.Millisecond,
		// The pace between creations is what the tests about pacing set for
		// themselves; everywhere else it is time the suite would only spend
		// waiting.
		Gate: NewGate(defaultSearchConcurrency, time.Millisecond, time.Millisecond),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, server
}

// knownLoggedIn puts the Soulseek connection on the gate as a search a moment
// ago would have left it. A test about what a search comes back with needs the
// search to be created at all, and creation now asks first whether there is a
// connection to create it on.
func knownLoggedIn(client *Client) {
	client.gate.observeConnection(true, `slskd reports "Connected, LoggedIn"`)
}

// eventually waits for something a search handed to a goroutine on its way out,
// so a test about the cleanup does not have to guess how long it takes.
func eventually(t *testing.T, message string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

func TestNewClientRejectsIncompleteSettings(t *testing.T) {
	for name, options := range map[string]Options{
		"no base URL": {APIKey: "key"},
		"no API key":  {BaseURL: "http://localhost:5030"},
		"bad scheme":  {BaseURL: "ftp://localhost:5030", APIKey: "key"},
		"not a URL":   {BaseURL: "localhost:5030", APIKey: "key"},
		"no host":     {BaseURL: ":5030", APIKey: "key"},
	} {
		if _, err := NewClient(options); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestCheckConnectionReportsVersionAndLogin(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("missing API key header")
		}
		if request.URL.Path != "/api/v0/application" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"version": "0.22.1",
			"server":  map[string]string{"state": "Connected, LoggedIn", "username": "schall"},
		})
	}))

	status, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("check connection: %v", err)
	}
	// The detail is a sentence for the settings page, not the provider's flag
	// list: plain words, and the username, never "LoggedIn".
	for _, want := range []string{"0.22.1", "connected to Soulseek", "schall"} {
		if !strings.Contains(status.Detail, want) {
			t.Fatalf("detail %q does not mention %q", status.Detail, want)
		}
	}
}

// Real slskd returns version as an object, not the bare string earlier builds
// used. Both shapes have to decode or the connection check fails against a
// live server.
func TestCheckConnectionAcceptsObjectVersion(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"version": {
				"full": "0.26.0",
				"current": "0.26.0",
				"latest": "0.26.0",
				"isUpdateAvailable": false,
				"isCanary": false
			},
			"server": {"state": "Connected, LoggedIn", "username": "schall", "address": "vps.slsknet.org"}
		}`))
	}))

	status, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("check connection: %v", err)
	}
	if !strings.Contains(status.Detail, "0.26.0") {
		t.Fatalf("detail %q does not report the version", status.Detail)
	}
}

func TestCheckConnectionFallsBackToCurrentVersion(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"version": {"full": "", "current": "0.27.1"},
			"server": {"state": "Connected, LoggedIn"}
		}`))
	}))

	status, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("check connection: %v", err)
	}
	if !strings.Contains(status.Detail, "0.27.1") {
		t.Fatalf("detail %q does not fall back to the current version", status.Detail)
	}
}

func TestCheckConnectionSurvivesUnknownVersionShape(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"version": {"unexpected": true},
			"server": {"state": "Connected, LoggedIn"}
		}`))
	}))

	status, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("an unreadable version must not fail the check: %v", err)
	}
	if !strings.Contains(status.Detail, "unknown version") {
		t.Fatalf("detail %q should admit the version is unknown", status.Detail)
	}
}

func TestCheckConnectionExplainsRejectedKey(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
	}))

	_, err := client.CheckConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("error = %v, want an actionable API key failure", err)
	}
}

func TestCheckConnectionReportsDisconnectedSoulseek(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"version": "0.22.1",
			"server":  map[string]string{"state": "Disconnected"},
		})
	}))

	status, err := client.CheckConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("error = %v, want a login failure", err)
	}
	// The provider's own word for the state stays in the error, where somebody
	// debugging wants it; the stored detail says it plainly instead.
	if !strings.Contains(err.Error(), "Disconnected") {
		t.Fatalf("error %v should carry slskd's own state", err)
	}
	if !strings.Contains(status.Detail, "not signed in") {
		t.Fatalf("detail %q should say the login is missing", status.Detail)
	}
}

// A refusal for asking too often is backpressure, and the layers above have to
// be able to tell it apart from a failure so they wait instead of recording an
// answer about a release that was never searched.
func TestSearchReportsRateLimitingAsBackpressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if !errors.Is(err, sources.ErrRateLimited) {
		t.Fatalf("error = %v, want it to report rate limiting", err)
	}
}

// slskd answers 409 when its own search call throws, and a Soulseek client that
// is not connected or logged in is the one thing that throws it. Read as a
// failure it cost a real installation 236 attempts across 156 wants in one
// afternoon, every one of them spent on a question nobody was ever asked.
func TestSearchReportsADisconnectedSoulseekAsBackpressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want it to report the source as unable to search", err)
	}
	if !strings.Contains(err.Error(), "Soulseek network") {
		t.Errorf("error = %q, want it to name what is not connected", err)
	}
}

// An slskd that is up but not ready has asked nobody anything either, so it is
// the same backpressure and must not settle a release.
func TestSearchReportsAnUnreadySlskdAsBackpressure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want it to report the source as unable to search", err)
	}
}

// The Soulseek server bans this account for thirty minutes at a time, and while
// it does, searches still "work": slskd accepts the creation, opens the reply
// window, and collects nothing. Read as an answer, that is a confident "nobody
// is sharing this" about music plenty of peers are sharing — the false negative
// this whole path exists to refuse.
func TestASearchThatFoundNothingWhileNotLoggedInIsBackpressure(t *testing.T) {
	stub := &searchStub{serverState: "Disconnected"}
	client, _ := newTestClient(t, stub.handler(t, nil))
	// The connection was there when the search was created and gone by the time
	// it came back, which is the whole of what this check is for.
	knownLoggedIn(client)

	_, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want the empty search reported as nobody having been asked", err)
	}
	if !strings.Contains(err.Error(), "Disconnected") {
		t.Errorf("error = %q, want it to carry what slskd said the connection was doing", err)
	}
}

// The check may only ever object. A search that went out over a connection that
// was there and heard nobody is the ordinary quiet search Soulseek is full of,
// and it stays an answer.
func TestASearchThatFoundNothingWhileLoggedInIsStillAnAnswer(t *testing.T) {
	stub := &searchStub{serverState: "Connected, LoggedIn"}
	client, _ := newTestClient(t, stub.handler(t, nil))

	candidates, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want the empty answer kept", candidates)
	}
}

// Nothing is claimed from a question that was not answered either. An slskd
// that will not say what its connection was doing — an older build without the
// endpoint among them — leaves the empty search exactly as it was before this
// was ever asked.
func TestAnEmptySearchIsBelievedWhenTheConnectionStateCannotBeRead(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost:
			response.WriteHeader(http.StatusCreated)
		case request.URL.Path == "/api/v0/server":
			response.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(request.URL.Path, "/responses"):
			_ = json.NewEncoder(response).Encode([]map[string]any{})
		default:
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Completed, TimedOut"})
		}
	}))

	candidates, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want the empty answer kept", candidates)
	}
}

// Not every build spells the connection out in words. Where it only sets the
// flags, they are what the state is read from — and where it sets neither, the
// question went unanswered and the empty search is left exactly as it was.
func TestAConnectionStateWithoutWordsIsReadFromItsFlags(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload string
		asked   bool
	}{
		{"logged in", `{"isConnected":true,"isLoggedIn":true}`, true},
		{"logged out", `{"isConnected":true,"isLoggedIn":false}`, false},
		{"connected, and not saying more", `{"isConnected":true}`, true},
		{"not connected at all", `{"isConnected":false}`, false},
		{"saying nothing", `{}`, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch {
				case request.Method == http.MethodPost:
					response.WriteHeader(http.StatusCreated)
				case request.URL.Path == "/api/v0/server":
					_, _ = response.Write([]byte(testCase.payload))
				case strings.HasSuffix(request.URL.Path, "/responses"):
					_ = json.NewEncoder(response).Encode([]map[string]any{})
				default:
					_ = json.NewEncoder(response).Encode(map[string]string{"state": "Completed, TimedOut"})
				}
			}))

			_, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

			if testCase.asked && err != nil {
				t.Fatalf("search: %v, want the empty answer kept", err)
			}
			if !testCase.asked && !errors.Is(err, sources.ErrProviderUnavailable) {
				t.Fatalf("error = %v, want the empty search reported as nobody having been asked", err)
			}
		})
	}
}

// Read only after a search, the connection state saves an answer from being
// believed. Read before one, it saves the search from being made at all — which
// is what matters while the Soulseek server is refusing this account: slskd
// logged eleven connection attempts in one minute and threw dozens of searches
// straight back, every one of them a knock on a door just shut in our face.
func TestASearchIsNotCreatedWhileSlskdIsNotLoggedIn(t *testing.T) {
	stub := &searchStub{serverState: "Disconnected"}
	client, _ := newTestClient(t, stub.handler(t, nil))

	_, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want a search into a connection that is not there refused", err)
	}
	// Learned before the search, not from an empty reply, so it carries
	// ErrSignedOut rather than ErrNotLoggedIn: the two keep acquisition's
	// different waits for a fact learned early versus one learned late (#495).
	if !errors.Is(err, sources.ErrSignedOut) {
		t.Errorf("error = %v, want it classified as slskd refusing before a search was made", err)
	}
	if errors.Is(err, sources.ErrNotLoggedIn) {
		t.Error("error satisfies ErrNotLoggedIn, want it kept apart from the empty-search refusal")
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.created) != 0 {
		t.Errorf("creations = %d, want the search never to have reached slskd", len(stub.created))
	}
}

// slskd queues outgoing searches in memory after accepting them. Schall used
// to add one every five seconds even when that queue was already growing,
// eventually leaving every acquisition worker behind work whose caller had
// moved on. A full Schall-sized set waiting there is admission backpressure,
// not another search to append to it.
func TestASearchIsNotCreatedWhileSlskdOutgoingQueueIsFull(t *testing.T) {
	stub := &searchStub{queueDepth: maxQueuedSearches}
	client, _ := newTestClient(t, stub.handler(t, nil))

	for _, text := range []string{"pashanim florenz", "pashanim shababs"} {
		_, err := client.Search(context.Background(), sources.Query{Text: text})
		if !errors.Is(err, sources.ErrProviderUnavailable) {
			t.Fatalf("search %q error = %v, want the full outgoing queue reported as backpressure",
				text, err)
		}
		if !strings.Contains(err.Error(), fmt.Sprint(maxQueuedSearches)) {
			t.Errorf("error = %q, want the queue depth carried to the caller", err)
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.created) != 0 {
		t.Errorf("creations = %d, want no search appended to the full queue", len(stub.created))
	}
	if stub.queueReads != 1 {
		t.Errorf("queue reads = %d, want one recent reading shared by the refused searches",
			stub.queueReads)
	}
}

// Refusing before the search costs no request per search: what slskd said last
// is kept on the gate, so a run of searches asks between them rather than each.
func TestTheConnectionIsNotAskedAboutOncePerSearch(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@peer\Music\01.flac`, "extension": "flac", "size": 30 << 20},
		},
	}}))

	for _, text := range []string{"pashanim florenz", "pashanim shababs"} {
		if _, err := client.Search(context.Background(), sources.Query{Text: text}); err != nil {
			t.Fatalf("search %q: %v", text, err)
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.serverReads > 1 {
		t.Errorf("connection reads = %d, want the second search to go on what the first learned",
			stub.serverReads)
	}
}

// It may only ever object, before a search as after one. An slskd that will not
// say what its connection is doing has said nothing, and a search that would
// have gone out before this existed still goes out.
func TestASearchIsStillCreatedWhenTheConnectionStateCannotBeRead(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v0/server" {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		stub.handler(t, nil).ServeHTTP(response, request)
	}))

	if _, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.created) != 1 {
		t.Errorf("creations = %d, want the search made anyway", len(stub.created))
	}
}

// A cancellation Schall requested while settling a window is not an abandoned
// search. A cancellation with no such request still is.
func TestASchallRequestedCancellationIsNotAbandoned(t *testing.T) {
	if gaveUp, ended := abandoned("Completed, Cancelled", true); ended || gaveUp != "" {
		t.Errorf("abandoned = (%q, %t), want Schall-requested cancellation left out", gaveUp, ended)
	}
}

// slskd reports how a search ended in the same compound state it reports
// completion in, and a search it gave up on is not a search anybody answered.
// Seventy consecutive searches ended this way in one evening and every one of
// them was written down as nothing on offer.
func TestASearchSlskdGaveUpOnIsNotAnAnswer(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		state string
		want  string
	}{
		{"errored", "Completed, Errored", "failed"},
		{"cancelled", "Completed, Cancelled", "cancelled"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := &searchStub{state: testCase.state}
			client, _ := newTestClient(t, stub.handler(t, nil))

			_, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

			if !errors.Is(err, sources.ErrProviderUnavailable) {
				t.Fatalf("error = %v, want an abandoned search reported as no answer", err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %q, want it to say slskd %s the search", err, testCase.want)
			}
		})
	}
}

// A reply-window that ran out having heard nobody is what the ladder and the
// second walk exist for. It is not slskd giving up, and it stays an answer.
func TestASearchThatOnlyRanOutOfTimeIsStillAnAnswer(t *testing.T) {
	stub := &searchStub{state: "Completed, TimedOut"}
	client, _ := newTestClient(t, stub.handler(t, nil))

	if _, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"}); err != nil {
		t.Fatalf("search: %v", err)
	}
}

// One peer answering settles it: the search reached the network whatever slskd
// says about its connection a moment later, and what it found is not thrown
// away over a reconnection that happened after the replies did.
func TestASearchPeersAnsweredIsNotSecondGuessed(t *testing.T) {
	stub := &searchStub{serverState: "Disconnected"}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@peer\Music\01.flac`, "extension": "flac", "size": 30 << 20},
		},
	}}))
	// The connection was there when the search was created; what slskd says
	// about it a moment later is what this refuses to read backwards.
	knownLoggedIn(client)

	candidates, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want what the peer offered", candidates)
	}
}

func TestCheckConnectionExplainsATimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key",
		HTTPClient: &http.Client{Timeout: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.CheckConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not respond in time") {
		t.Fatalf("error = %v, want an actionable timeout message", err)
	}
}

func TestCheckConnectionPropagatesCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.CheckConnection(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the caller's deadline propagated", err)
	}
}

// The provider is named in API responses and in the log.
func TestTheClientNamesTheProviderItSpeaksTo(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if got := client.Name(); got != "slskd" {
		t.Errorf("Name() = %q, want slskd", got)
	}
}

// Searches created back to back are what the Soulseek server bans this account
// for, and no concurrency bound can see it: one search at a time, as fast as
// slskd will take them, satisfies every bound there was before this one.
func TestOneSearchCreationWaitsOutTheGatesIntervalAfterTheLast(t *testing.T) {
	const interval = 80 * time.Millisecond
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	client.gate = NewGate(4, interval, time.Millisecond)

	for range 2 {
		if _, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"}); err != nil {
			t.Fatalf("search: %v", err)
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.created) != 2 {
		t.Fatalf("creations = %d, want both searches created", len(stub.created))
	}
	if spacing := stub.created[1].Sub(stub.created[0]); spacing < interval {
		t.Fatalf("the second search was created %s after the first, want at least %s",
			spacing, interval)
	}
}

// Waiting out the pace is waiting like waiting for a slot is, and a caller that
// gave up during it must not leave the door shut behind it.
func TestACallerThatGaveUpWhileTheGateWasPacingIsNotHeldInIt(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	gate := NewGate(4, time.Minute, time.Millisecond)
	client.gate = gate
	if _, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"}); err != nil {
		t.Fatalf("first search: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Search(ctx, sources.Query{Text: "pashanim florenz"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the caller's deadline honoured", err)
	}

	select {
	case gate.creation <- struct{}{}:
	default:
		t.Fatal("the creation slot is still held by a search nobody is waiting for")
	}
}

// A gate nobody paced still paces, for the same reason a gate nobody sized
// still bounds: the default is what the process runs with.
func TestAGateNobodyPacedKeepsTheDefaultInterval(t *testing.T) {
	if got := NewGate(0, 0, 0).interval; got != defaultCreationInterval {
		t.Errorf("interval = %s, want the default of %s", got, defaultCreationInterval)
	}
}

// A gate nobody gave a window still refuses repeats, for the same reason.
func TestAGateNobodyGaveAWindowKeepsTheDefaultOne(t *testing.T) {
	if got := NewGate(0, 0, 0).repeat; got != defaultRepeatWindow {
		t.Errorf("window = %s, want the default of %s", got, defaultRepeatWindow)
	}
}

// "Do not repeat the same text several times in a row" is the ban's own first
// sentence. One phrase went to Soulseek fifteen times in six minutes on the
// afternoon this was written, and nothing in the gate counted that.
func TestAPhraseAlreadyAskedTwiceIsNotAskedAgainInsideTheWindow(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	client.gate = NewGate(4, time.Millisecond, time.Minute)

	for range repeatAllowance {
		if _, err := client.Search(context.Background(), sources.Query{Text: "souly teufel fallen"}); err != nil {
			t.Fatalf("search: %v", err)
		}
	}
	_, err := client.Search(context.Background(), sources.Query{Text: "souly teufel fallen"})

	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want a repeated phrase refused", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.created) != repeatAllowance {
		t.Errorf("creations = %d, want the repeat never to have reached slskd", len(stub.created))
	}
}

// The second ask is deliberate: a want whose ladder came back empty asks its
// broadest phrase once more, and that echo is how an unreliable network's
// silence is told from an answer. Refusing it would leave the want deferred
// forever rather than settling, which is more searching than it saves.
func TestAPhraseAskedOnceIsStillAskedItsSecondTime(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	client.gate = NewGate(4, time.Millisecond, time.Minute)

	for ask := range repeatAllowance {
		if _, err := client.Search(context.Background(), sources.Query{Text: "souly traence"}); err != nil {
			t.Fatalf("ask %d: %v", ask+1, err)
		}
	}
}

// The window is per phrase, not a pause on searching. A want behind the one
// that repeated itself is asking about different music and is asked about it.
func TestAnotherPhraseIsNotRefusedByThePhraseBeforeIt(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	client.gate = NewGate(4, time.Millisecond, time.Minute)

	for range repeatAllowance {
		if _, err := client.Search(context.Background(), sources.Query{Text: "souly teufel fallen"}); err != nil {
			t.Fatalf("search: %v", err)
		}
	}
	if _, err := client.Search(context.Background(), sources.Query{Text: "souly engel fliegen"}); err != nil {
		t.Fatalf("a phrase nobody had asked was refused: %v", err)
	}
}

// A refused repeat is backpressure and never an answer. Recorded as an empty
// result it would be Schall writing "nobody is sharing this" about music it had
// declined to ask about, which is issue #193 arriving by a new road.
func TestAPhraseRefusedAsARepeatIsNotAnEmptyResult(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	client.gate = NewGate(4, time.Millisecond, time.Minute)
	for range repeatAllowance {
		if _, err := client.Search(context.Background(), sources.Query{Text: "souly teufel fallen"}); err != nil {
			t.Fatalf("search: %v", err)
		}
	}

	candidates, err := client.Search(context.Background(), sources.Query{Text: "souly teufel fallen"})

	if candidates != nil {
		t.Errorf("candidates = %#v, want nothing that could be read as an answer", candidates)
	}
	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want the refusal reported as the source being unable", err)
	}
	// The sentence a want carries into the interface has to say what happened
	// and when it will be asked again, never invite somebody to press retry.
	for _, want := range []string{"souly teufel fallen", "asked again in"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// The window counts what Soulseek heard. A caller that gave up before its
// search reached slskd asked nobody anything, and holding that against the
// phrase would spend the allowance the deliberate second ask needs.
func TestAPhraseNobodyGotAsFarAsAskingIsNotCountedAsAsked(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	gate := NewGate(1, time.Millisecond, time.Minute)
	client.gate = gate
	if err := gate.enter(context.Background()); err != nil {
		t.Fatalf("take the only slot: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Search(ctx, sources.Query{Text: "souly teufel fallen"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the caller's deadline honoured", err)
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	if remembered := len(gate.asked); remembered != 0 {
		t.Errorf("phrases remembered = %d, want a phrase nobody asked forgotten", remembered)
	}
}

// slskd refusing the creation is the same thing: a 429 is answered before it
// creates anything, so the phrase was never put to the network.
func TestAPhraseSlskdWouldNotCreateASearchForIsNotCountedAsAsked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			response.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"state": "Connected, LoggedIn"})
	}))
	t.Cleanup(server.Close)
	gate := NewGate(4, time.Millisecond, time.Minute)
	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key", Gate: gate,
		SearchTimeout: time.Second, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := client.Search(context.Background(), sources.Query{Text: "souly traence"}); !errors.Is(err, sources.ErrRateLimited) {
		t.Fatalf("error = %v, want the refused creation reported as backpressure", err)
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	if remembered := len(gate.asked); remembered != 0 {
		t.Errorf("phrases remembered = %d, want a phrase slskd never created forgotten", remembered)
	}
}

// The lanes share the gate and press it at once. Two wants from one release ask
// the release rung in the same words, and which of them arrives first is a race
// — what may not happen is that the race lets a third through.
func TestOnePhraseAskedFromEveryLaneAtOnceIsAdmittedExactlyItsAllowance(t *testing.T) {
	gate := NewGate(4, time.Millisecond, time.Minute)
	var lanes sync.WaitGroup
	var mutex sync.Mutex
	admitted := 0
	for range 12 {
		lanes.Add(1)
		go func() {
			defer lanes.Done()
			if gate.reserve("souly teufel fallen").repeated {
				return
			}
			mutex.Lock()
			admitted++
			mutex.Unlock()
		}()
	}
	lanes.Wait()

	if admitted != repeatAllowance {
		t.Errorf("admitted = %d, want exactly the allowance of %d", admitted, repeatAllowance)
	}
}

// A search can wait for a slot longer than a window lasts. What it hands back
// when it gives up is its own ask, not one taken since in a window of its own —
// giving that one back would leave the gate believing nobody had asked, which is
// the state the window exists to prevent.
func TestASearchGivingUpAfterItsWindowDoesNotTakeBackALaterAsk(t *testing.T) {
	gate := NewGate(4, time.Millisecond, 20*time.Millisecond)
	stale := gate.reserve("souly teufel fallen")

	time.Sleep(25 * time.Millisecond)
	fresh := gate.reserve("souly teufel fallen")
	gate.unreserve(stale)

	gate.mu.Lock()
	defer gate.mu.Unlock()
	asked, seen := gate.asked["souly teufel fallen"]
	if !seen || asked.asks != 1 || !asked.first.Equal(fresh.window) {
		t.Errorf("the gate remembers %+v, want the one ask taken in the window after it", asked)
	}
}

// Two searches share one window, and one of them giving up gives back its own
// ask and nothing else: the phrase has been asked once, and may be asked once
// more.
func TestOneOfTwoSearchesSharingAWindowGivesBackOnlyItsOwnAsk(t *testing.T) {
	gate := NewGate(4, time.Millisecond, time.Minute)
	first := gate.reserve("souly teufel fallen")
	second := gate.reserve("souly teufel fallen")
	if second.repeated {
		t.Fatalf("the second ask of the allowance was refused")
	}

	gate.unreserve(second)

	third := gate.reserve("souly teufel fallen")
	if third.repeated {
		t.Errorf("the ask handed back was not given to the phrase")
	}
	if !third.window.Equal(first.window) {
		t.Errorf("window = %s, want the one the first ask opened at %s", third.window, first.window)
	}
	if !gate.reserve("souly teufel fallen").repeated {
		t.Errorf("a fourth ask was admitted, want the allowance spent again")
	}
}

// A phrase is asked again once its window has passed — the window is a pause,
// not a ban of our own — and what the gate remembers of it goes with it, which
// is the whole of what keeps that memory from growing forever.
func TestAPhraseIsAskableAgainOnceItsWindowHasPassed(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))
	gate := NewGate(4, time.Millisecond, 20*time.Millisecond)
	client.gate = gate
	for range repeatAllowance {
		if _, err := client.Search(context.Background(), sources.Query{Text: "souly teufel fallen"}); err != nil {
			t.Fatalf("search: %v", err)
		}
	}

	time.Sleep(25 * time.Millisecond)
	if _, err := client.Search(context.Background(), sources.Query{Text: "souly teufel fallen"}); err != nil {
		t.Fatalf("a phrase whose window had passed was refused: %v", err)
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	if remembered := len(gate.asked); remembered != 1 {
		t.Errorf("phrases remembered = %d, want only the one still inside its window", remembered)
	}
}

// A gate nobody sized bounds searching at the default rather than not at all.
func TestAGateNobodySizedAdmitsTheDefaultNumberOfSearches(t *testing.T) {
	gate := NewGate(0, time.Millisecond, time.Millisecond)

	for admitted := range defaultSearchConcurrency {
		if err := gate.enter(context.Background()); err != nil {
			t.Fatalf("the gate refused search %d of the default %d: %v",
				admitted+1, defaultSearchConcurrency, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.enter(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want one more than the default made to wait", err)
	}
}

// Waiting for a slot is waiting, and a caller that gave up waiting is not held
// in it — the slot is what keeps slskd answering, not a queue nobody watches.
func TestACallerThatGaveUpWaitingForASlotIsNotHeldInIt(t *testing.T) {
	gate := NewGate(1, time.Millisecond, time.Millisecond)
	if err := gate.enter(context.Background()); err != nil {
		t.Fatalf("take the only slot: %v", err)
	}
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a search that never got a slot reached slskd")
	}))
	client.gate = gate

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Search(ctx, sources.Query{Text: "portishead dummy"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the caller's deadline honoured", err)
	}
}

// Waiting for a shared slot spends the ladder's outer budget. Once the slot is
// ours, a rung that can no longer fit must not be created and allowed to expire
// halfway through its reply window.
func TestAContendedGateIsChargedBeforeSearchAffordability(t *testing.T) {
	gate := NewGate(1, time.Millisecond, time.Millisecond)
	if err := gate.enter(context.Background()); err != nil {
		t.Fatalf("take the only slot: %v", err)
	}
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an underfunded search reached slskd")
	}))
	client.gate = gate

	ctx, cancel := context.WithTimeout(
		context.Background(), client.SearchBudget()+50*time.Millisecond)
	defer cancel()
	released := time.AfterFunc(100*time.Millisecond, gate.leave)
	defer released.Stop()

	if _, err := client.Search(
		ctx, sources.Query{Text: "portishead dummy"},
	); !errors.Is(err, sources.ErrSearchBudgetExhausted) {
		t.Fatalf("error = %v, want budget exhaustion after gate admission", err)
	}
}

// Creations are serialized because slskd admits exactly one at a time, and
// waiting for that turn is waiting like any other.
func TestACallerThatGaveUpWaitingToCreateASearchIsNotHeldInIt(t *testing.T) {
	gate := NewGate(4, time.Millisecond, time.Millisecond)
	gate.creation <- struct{}{}
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a search that never got its turn to be created reached slskd")
	}))
	client.gate = gate

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Search(ctx, sources.Query{Text: "portishead dummy"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the caller's deadline honoured", err)
	}
}

// A refused creation is retried after a short wait, and that wait is still a
// wait the caller can walk away from.
func TestACallerThatGaveUpBeforeARefusedCreationWasRetriedIsNotHeldInIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	giveUp := time.AfterFunc(50*time.Millisecond, cancel)
	defer giveUp.Stop()
	if _, err := client.Search(ctx, sources.Query{Text: "portishead dummy"}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the caller's cancellation honoured", err)
	}
}

// A search slskd would not report the state of is a search nothing is known
// about, and a caller must hear that rather than an empty answer.
func TestASearchSlskdWouldNotReportOnIsReported(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			response.WriteHeader(http.StatusCreated)
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
	}))

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err == nil {
		t.Fatal("a search nobody could follow was reported as finding nothing")
	}
}

// A search that completed and then would not hand over its replies has found
// nothing anybody can read, and that is a failure rather than an empty result.
func TestRepliesSlskdWouldNotHandOverAreReported(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost:
			response.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(request.URL.Path, "/responses"):
			response.WriteHeader(http.StatusInternalServerError)
		default:
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Completed"})
		}
	}))

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err == nil {
		t.Fatal("replies nobody could read were reported as no replies")
	}
}

// Polling for completion is waiting too, and a caller that gave up during it
// hears its own cancellation rather than an empty search.
func TestACallerThatGaveUpWhilePollingIsNotHeldInIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost:
			response.WriteHeader(http.StatusCreated)
		case request.URL.Path == "/api/v0/server":
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Connected, LoggedIn"})
		default:
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "InProgress"})
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key",
		SearchTimeout: 10 * time.Second, PollInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	giveUp := time.AfterFunc(50*time.Millisecond, cancel)
	defer giveUp.Stop()
	if _, err := client.Search(ctx, sources.Query{Text: "portishead dummy"}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the caller's cancellation honoured", err)
	}
}

// Removing the finished search is tidying up, not part of the answer. A slskd
// that would not take the removal still found what it found.
func TestASearchThatCouldNotBeRemovedStillReturnsWhatItFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodDelete:
			// The connection drops rather than answering, which is what a slskd
			// restarting between the search and its cleanup looks like.
			if hijacker, ok := response.(http.Hijacker); ok {
				if connection, _, err := hijacker.Hijack(); err == nil {
					_ = connection.Close()
				}
			}
		case request.Method == http.MethodPost:
			response.WriteHeader(http.StatusCreated)
		case request.URL.Path == "/api/v0/server":
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Connected, LoggedIn"})
		case strings.HasSuffix(request.URL.Path, "/responses"):
			_ = json.NewEncoder(response).Encode([]map[string]any{{
				"username": "peer", "hasFreeUploadSlot": true,
				"files": []map[string]any{
					{"filename": `@@peer\Music\01.flac`, "extension": "flac", "size": 30 << 20},
				},
			}})
		default:
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Completed"})
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key",
		SearchTimeout: 2 * time.Second, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the folder that was found", candidates)
	}
}

// The removal is housekeeping, and housekeeping never accumulates: a search
// slskd has finished with is taken back out of it.
func TestASearchSlskdHasFinishedWithIsRemoved(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if !stub.deleted {
		t.Error("a finished search was left behind in slskd")
	}
}

// A search that outran its reply window is still running, and deleting one that
// is takes the row out from under slskd's own writes: it logs "Failed to
// execute search" with a DbUpdateConcurrencyException, the search dies
// mid-flight, and the want comes back to ask the same phrase again. A record
// left behind is the far smaller problem, and slskd ages them out.
func TestASearchThatOutranItsBudgetIsNotDeletedUnderSlskd(t *testing.T) {
	stub := &searchStub{completeAfter: 1 << 20}
	server := httptest.NewServer(stub.handler(t, nil))
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key",
		SearchTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond,
		Gate: NewGate(4, time.Millisecond, time.Millisecond),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := client.Search(context.Background(), sources.Query{Text: "never completes"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	// Long enough for the cleanup to have made its mind up several times over.
	time.Sleep(50 * time.Millisecond)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.deleted {
		t.Error("a search slskd was still running was deleted under it")
	}
}

// A caller that gave up is the other way a live search used to be deleted. The
// caller has stopped waiting; the search has not stopped running.
func TestASearchTheCallerGaveUpOnIsNotDeletedUnderSlskd(t *testing.T) {
	stub := &searchStub{completeAfter: 1 << 20}
	server := httptest.NewServer(stub.handler(t, nil))
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key",
		SearchTimeout: 10 * time.Second, PollInterval: time.Millisecond,
		Gate: NewGate(4, time.Millisecond, time.Millisecond),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _ = client.Search(ctx, sources.Query{Text: "never completes"})

	time.Sleep(50 * time.Millisecond)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.deleted {
		t.Error("a search the caller stopped watching was deleted while slskd ran it")
	}
}

// Waiting rather than deleting is a delay, not a leak: the search is removed as
// soon as slskd says it is over, on nobody's time but the cleanup's own.
func TestASearchLeftRunningIsRemovedOnceSlskdHasFinishedWithIt(t *testing.T) {
	stub := &searchStub{completeAfter: 40}
	server := httptest.NewServer(stub.handler(t, nil))
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key",
		SearchTimeout: 10 * time.Millisecond, PollInterval: time.Millisecond,
		Gate: NewGate(4, time.Millisecond, time.Millisecond),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := client.Search(context.Background(), sources.Query{Text: "finishes late"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	eventually(t, "the search slskd finished was never removed", func() bool {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return stub.deleted
	})
}

// A peer who did not say who they are cannot be asked for anything, so nothing
// they offered is a candidate.
func TestAPeerWhoDidNotSayWhoTheyAreOffersNothing(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "  ", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@a\Music\01.flac`, "extension": "flac", "size": 30 << 20},
		},
	}}))

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want nothing from a peer nobody could ask", candidates)
	}
}

// Peers do not always fill in the extension field, and the name says what the
// file is well enough to tell music from a cover image.
func TestAFileWithNoExtensionFieldIsReadFromItsName(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@a\Music\01 Mysterons.flac`, "size": 30 << 20},
			{"filename": `@@a\Music\folder.jpg`, "size": 1 << 20},
		},
	}}))

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 || candidates[0].TrackCount() != 1 {
		t.Fatalf("candidates = %#v, want the audio file kept and the image left out", candidates)
	}
	if candidates[0].Format != "flac" {
		t.Errorf("format = %q, want it read from the name", candidates[0].Format)
	}
}

// A peer sharing a whole discography in one folder is not handed on unbounded:
// a candidate is something somebody reads, and a folder is not a database.
func TestOneFoldersFilesAreBounded(t *testing.T) {
	files := make([]map[string]any, 0, maxFilesPerFolder+10)
	for index := range maxFilesPerFolder + 10 {
		files = append(files, map[string]any{
			"filename":  fmt.Sprintf(`@@a\Discography\%03d.flac`, index),
			"extension": "flac", "size": 30 << 20,
		})
	}
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true, "files": files,
	}}))

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if candidates[0].TrackCount() != maxFilesPerFolder {
		t.Fatalf("track count = %d, want the cap of %d",
			candidates[0].TrackCount(), maxFilesPerFolder)
	}
}

// A peer that advertised a path with nothing after the last separator still has
// to give the file something to be called, or it appears in the list as nothing.
func TestAFileWhosePathEndsInASeparatorIsStillNamed(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@a\Music\`, "extension": "flac", "size": 30 << 20},
		},
	}}))

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Files[0].Name == "" {
		t.Fatalf("candidates = %#v, want the file still named", candidates)
	}
}

// A body that is not the JSON it claims to be is a failure, not a slskd that
// answered with nothing.
func TestAnAnswerThatIsNotJSONIsReported(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("<html>a proxy login page</html>"))
	}))

	_, err := client.CheckConnection(context.Background())

	if err == nil || !strings.Contains(err.Error(), "decode slskd response") {
		t.Fatalf("error = %v, want the unreadable answer reported", err)
	}
}

// slskd that answered without saying what the Soulseek connection is doing is
// still described, so the settings page shows what was reached.
func TestASlskdThatDidNotSayItsStateIsStillDescribed(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"version":"0.22.1","server":{}}`))
	}))

	status, err := client.CheckConnection(context.Background())

	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("error = %v, want a state nobody reported treated as not logged in", err)
	}
	if !strings.Contains(status.Detail, "unknown") {
		t.Errorf("detail = %q, want the unknown state admitted", status.Detail)
	}
}

// A status has to arrive as something somebody can act on without reading the
// server log.
func TestAStatusSlskdAnsweredWithIsTurnedIntoSomethingActionable(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
		want   string
	}{
		{"api path not found", http.StatusNotFound, "exposes /api/v0"},
		{"not ready yet", http.StatusServiceUnavailable, "not ready"},
		{"behind a broken gateway", http.StatusBadGateway, "not ready"},
		{"a status nobody here knows", http.StatusInternalServerError, "slskd returned HTTP 500"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(testCase.status)
			}))

			_, err := client.CheckConnection(context.Background())

			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

// slskd not being there at all is a base URL somebody typed wrong, and the
// transport error alone says nothing they could act on.
func TestASlskdThatCouldNotBeReachedSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()
	client, err := NewClient(Options{BaseURL: address, APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if _, err := client.CheckConnection(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "could not reach slskd") {
		t.Errorf("error = %v, want the unreachable server reported", err)
	}
}

type searchStub struct {
	mu       sync.Mutex
	polls    int
	deleted  bool
	searched string
	// created is when each creation reached slskd, which is what the pace
	// between two searches is read from.
	created []time.Time
	// replies is the responseCount slskd answers each poll with, so a test can
	// script when peers replied. The last entry stands for every poll after it,
	// and a stub that scripted none had nobody answer.
	replies []int
	// completeAfter is the poll at which slskd reports the search finished.
	// Zero keeps the second-poll default the searches here were written against.
	completeAfter int
	// dequeueAfter is how many polls report "Queued" before the search leaves
	// slskd's queue and the ordinary state machine above (completeAfter, state)
	// takes over. Zero — the default — never queues, so every test written
	// before the dequeue wait existed still runs at the same speed.
	dequeueAfter int
	// state is what slskd reports the finished search as. Empty is the ordinary
	// ending, which the default below spells out.
	state string
	// serverState is what slskd says its Soulseek connection is doing when it is
	// asked. Empty is a connection that was there to ask over.
	serverState string
	// serverReads is how many times it was asked, which is what "without a
	// request per search" is measured in.
	serverReads int
	// responseReads counts GET /responses calls. emptyResponses makes the first
	// few completed reads empty, which is the race this client waits out.
	responseReads  int
	emptyResponses int
	// queueDepth is how many outgoing searches slskd says it has accepted but
	// not yet sent. queueReads measures that one reading is shared by the gate.
	queueDepth int
	queueReads int
	// searchTimeout is the reply window the creation carried, in milliseconds.
	searchTimeout int64
	// stopped is whether the search was asked to finish, and stops is how many
	// times. A stopped search reports a completed state from then on, which is
	// what slskd does with one.
	stopped bool
	stops   int
	// refuseStop answers the stop with an error, and ignoreStop takes it and
	// leaves the search running. Both are how a search whose replies were never
	// stored is written.
	refuseStop bool
	ignoreStop bool
}

// reportedState is what slskd says the search is doing after the polls it has
// answered so far. It is what decides whether /responses hands anything over:
// slskd stores a search's responses only once the search has completed.
func (stub *searchStub) reportedState() string {
	if stub.stopped {
		return "Completed, Cancelled"
	}
	if stub.polls <= stub.dequeueAfter {
		return "Queued"
	}
	completeAfter := stub.completeAfter
	if completeAfter == 0 {
		completeAfter = 1
	}
	if stub.polls <= completeAfter {
		return "InProgress"
	}
	if stub.state != "" {
		return stub.state
	}
	return "Completed, TimedOut"
}

func (stub *searchStub) repliesAt(poll int) int {
	if len(stub.replies) == 0 {
		return 0
	}
	if poll > len(stub.replies) {
		poll = len(stub.replies)
	}
	return stub.replies[poll-1]
}

func (stub *searchStub) handler(t *testing.T, responses []map[string]any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()

		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v0/searches":
			var payload searchRequest
			_ = json.NewDecoder(request.Body).Decode(&payload)
			stub.searched = payload.SearchText
			stub.searchTimeout = payload.SearchTimeout
			stub.created = append(stub.created, time.Now())
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v0/searches":
			stub.queueReads++
			searches := make([]map[string]any, stub.queueDepth)
			for i := range searches {
				state := "Queued"
				if i%2 == 1 {
					state = "Requested"
				}
				searches[i] = map[string]any{"id": fmt.Sprint(i), "state": state}
			}
			_ = json.NewEncoder(response).Encode(searches)
		case request.Method == http.MethodPut:
			stub.stops++
			if stub.refuseStop {
				response.WriteHeader(http.StatusInternalServerError)
				return
			}
			stub.stopped = !stub.ignoreStop
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodDelete:
			stub.deleted = true
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/api/v0/server":
			stub.serverReads++
			state := stub.serverState
			if state == "" {
				state = "Connected, LoggedIn"
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"state": state, "username": "schall", "address": "vps.slsknet.org",
			})
		case strings.HasSuffix(request.URL.Path, "/responses"):
			stub.responseReads++
			// slskd 0.26.0 stores a search's responses only when the search
			// completes. Until then it answers an empty list, however many
			// replies its own responseCount has counted — which is the whole
			// reason a window closed early is finished before it is read.
			if !isComplete(stub.reportedState()) || stub.responseReads <= stub.emptyResponses {
				_ = json.NewEncoder(response).Encode([]map[string]any{})
				return
			}
			_ = json.NewEncoder(response).Encode(responses)
		default:
			stub.polls++
			state := stub.reportedState()
			replies := stub.repliesAt(stub.polls)
			if state == "Queued" && !stub.stopped {
				// A search still in slskd's own queue has reached nobody.
				replies = 0
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"state": state, "responseCount": replies,
			})
		}
	})
}

func TestSearchGroupsFilesByFolderAndRanks(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{
		{
			"username": "peer-one", "hasFreeUploadSlot": true, "queueLength": 0, "uploadSpeed": 2 << 20,
			"files": []map[string]any{
				{"filename": `@@a\Music\Dummy\01 Mysterons.flac`, "extension": "flac", "size": 30 << 20},
				{"filename": `@@a\Music\Dummy\02 Sour Times.flac`, "extension": "flac", "size": 30 << 20},
				{"filename": `@@a\Music\Dummy\cover.jpg`, "extension": "jpg", "size": 1 << 20},
				{"filename": `@@a\Other\bonus.flac`, "extension": "flac", "size": 30 << 20},
			},
		},
		{
			"username": "peer-two", "hasFreeUploadSlot": false, "queueLength": 25, "uploadSpeed": 1024,
			"files": []map[string]any{
				{"filename": `@@b\Dummy\01.mp3`, "extension": "mp3", "size": 8 << 20, "bitRate": 128},
				{"filename": `@@b\Dummy\02.mp3`, "extension": "mp3", "size": 8 << 20, "bitRate": 128},
			},
		},
	}))

	candidates, err := client.Search(context.Background(), sources.Query{
		Text: "portishead dummy", ExpectedTrackCount: 2,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3 folders", len(candidates))
	}

	best := candidates[0]
	if best.Username != "peer-one" || best.Directory != "@@a/Music/Dummy" {
		t.Fatalf("best candidate = %s %s", best.Username, best.Directory)
	}
	if best.TrackCount() != 2 {
		t.Fatalf("track count = %d, want 2 audio files with the image skipped", best.TrackCount())
	}
	if best.Format != "flac" || best.Provider != Name {
		t.Fatalf("candidate = %#v", best)
	}
	if len(best.Reasons) == 0 {
		t.Fatal("candidate has no ranking explanation")
	}
	if candidates[len(candidates)-1].Username != "peer-two" {
		t.Fatalf("order = %#v, want the queued low-bitrate peer last", candidates)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.searched != "portishead dummy" {
		t.Fatalf("search text = %q", stub.searched)
	}
	if stub.polls < 2 {
		t.Fatalf("polled %d times, want polling until completion", stub.polls)
	}
	if !stub.deleted {
		t.Fatal("search was not removed from slskd")
	}
}

// A popular release answers with more folders than the cap allows. Cutting
// them in the order slskd returned them discarded candidates before anything
// had looked at them, so the best source could be thrown away unseen.
func TestSearchRanksBeforeCappingTheResults(t *testing.T) {
	responses := make([]map[string]any, 0, maxCandidates+10)
	for index := 0; index < maxCandidates+9; index++ {
		responses = append(responses, map[string]any{
			"username": fmt.Sprintf("filler-%02d", index), "hasFreeUploadSlot": false,
			"queueLength": 30, "uploadSpeed": 1024,
			"files": []map[string]any{
				{"filename": fmt.Sprintf(`@@f%02d\Misc\unrelated.mp3`, index),
					"extension": "mp3", "size": 3 << 20, "bitRate": 128, "length": 90},
			},
		})
	}
	// The one folder that actually holds the release answers last.
	responses = append(responses, map[string]any{
		"username": "the-right-one", "hasFreeUploadSlot": true, "queueLength": 0,
		"uploadSpeed": 4 << 20,
		"files": []map[string]any{
			{"filename": `@@z\Music\01 Mysterons.flac`, "extension": "flac",
				"size": 30 << 20, "length": 205},
		},
	})

	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, responses))

	candidates, err := client.Search(context.Background(), sources.Query{
		Text: "portishead mysterons", ExpectedTrackCount: 1,
		Tracks: []sources.QueryTrack{{Title: "Mysterons", DurationSeconds: 205}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(candidates) != maxCandidates {
		t.Fatalf("got %d candidates, want the cap of %d", len(candidates), maxCandidates)
	}
	if candidates[0].Username != "the-right-one" {
		t.Fatalf("best candidate = %q, want the corroborated folder that answered last",
			candidates[0].Username)
	}
}

func TestSearchRequiresText(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("slskd should not be called for an empty query")
	}))

	if _, err := client.Search(context.Background(), sources.Query{Text: "  "}); err == nil {
		t.Fatal("expected an error for an empty search")
	}
}

func TestSearchStopsAtTheSearchBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/responses"):
			_ = json.NewEncoder(response).Encode([]map[string]any{})
		case request.URL.Path == "/api/v0/server":
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Connected, LoggedIn"})
		case request.Method == http.MethodGet:
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "InProgress"})
		default:
			response.WriteHeader(http.StatusCreated)
		}
	}))
	defer server.Close()

	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "key",
		SearchTimeout: 50 * time.Millisecond, PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	start := time.Now()
	candidates, err := client.Search(context.Background(), sources.Query{Text: "never completes"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("got %d candidates, want none", len(candidates))
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("search took %s; the budget was not enforced", elapsed)
	}
}

// slskd answers 429 when several searches arrive at once, and a throttled search
// is a want that has to be asked about again rather than an answer. So searching
// is bounded, and bounded across every client built from one gate: a client is
// constructed afresh for each search, so a limit kept inside one would be no
// limit at all.
func TestSearchesAreBoundedAcrossEveryClientSharingAGate(t *testing.T) {
	var mutex sync.Mutex
	inFlight, highest := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			mutex.Lock()
			inFlight++
			if inFlight > highest {
				highest = inFlight
			}
			mutex.Unlock()
			defer func() {
				mutex.Lock()
				inFlight--
				mutex.Unlock()
			}()
			time.Sleep(20 * time.Millisecond)
			response.WriteHeader(http.StatusOK)
			return
		}
		if request.URL.Path == "/api/v0/server" {
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Connected, LoggedIn"})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/responses") {
			_ = json.NewEncoder(response).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"state": "Completed"})
	}))
	t.Cleanup(server.Close)

	gate := NewGate(2, time.Millisecond, time.Millisecond)
	var searches sync.WaitGroup
	// A phrase each, because six wants pressing at once are six different
	// wants; the same phrase six times is what the repeat window is for.
	for phrase := range 6 {
		searches.Add(1)
		go func() {
			defer searches.Done()
			client, err := NewClient(Options{
				BaseURL: server.URL, APIKey: "test-key", Gate: gate,
				SearchTimeout: 2 * time.Second, PollInterval: time.Millisecond,
			})
			if err != nil {
				t.Errorf("new client: %v", err)
				return
			}
			text := fmt.Sprintf("portishead dummy %d", phrase)
			if _, err := client.Search(context.Background(), sources.Query{Text: text}); err != nil {
				t.Errorf("Search() error = %v", err)
			}
		}()
	}
	searches.Wait()

	mutex.Lock()
	defer mutex.Unlock()
	if highest > 2 {
		t.Fatalf("%d searches were in flight at once, want no more than the gate admits", highest)
	}
}

// slskd admits exactly one search creation at a time — a second POST arriving
// while one is being started gets 429, not a queue. Reply windows may overlap;
// creations may not, however many clients share the gate.
func TestSearchCreationsAreSerializedAcrossTheGate(t *testing.T) {
	var mutex sync.Mutex
	creating := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			mutex.Lock()
			if creating {
				mutex.Unlock()
				response.WriteHeader(http.StatusTooManyRequests)
				return
			}
			creating = true
			mutex.Unlock()
			time.Sleep(20 * time.Millisecond)
			mutex.Lock()
			creating = false
			mutex.Unlock()
			response.WriteHeader(http.StatusOK)
			return
		}
		if request.URL.Path == "/api/v0/server" {
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Connected, LoggedIn"})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/responses") {
			_ = json.NewEncoder(response).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"state": "Completed"})
	}))
	t.Cleanup(server.Close)

	gate := NewGate(4, time.Millisecond, time.Millisecond)
	var searches sync.WaitGroup
	for phrase := range 6 {
		searches.Add(1)
		go func() {
			defer searches.Done()
			client, err := NewClient(Options{
				BaseURL: server.URL, APIKey: "test-key", Gate: gate,
				SearchTimeout: 2 * time.Second, PollInterval: time.Millisecond,
			})
			if err != nil {
				t.Errorf("new client: %v", err)
				return
			}
			text := fmt.Sprintf("portishead dummy %d", phrase)
			if _, err := client.Search(context.Background(), sources.Query{Text: text}); err != nil {
				t.Errorf("Search() error = %v, want serialized creations never to be refused", err)
			}
		}()
	}
	searches.Wait()
}

// A creation can still collide with one made outside this process — somebody
// searching in slskd's own interface holds the same one-slot semaphore. One
// refusal is absorbed with a retry; the search goes on to complete.
func TestARefusedCreationIsRetriedOnce(t *testing.T) {
	var mutex sync.Mutex
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			mutex.Lock()
			posts++
			first := posts == 1
			mutex.Unlock()
			if first {
				response.WriteHeader(http.StatusTooManyRequests)
				return
			}
			response.WriteHeader(http.StatusOK)
			return
		}
		if request.URL.Path == "/api/v0/server" {
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "Connected, LoggedIn"})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/responses") {
			_ = json.NewEncoder(response).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"state": "Completed"})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "test-key",
		SearchTimeout: 2 * time.Second, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("Search() error = %v, want one refusal absorbed", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if posts != 2 {
		t.Errorf("posts = %d, want the refused creation tried exactly once more", posts)
	}
}

// The window a search waits out is a setting nobody has measured. What follows
// pins the measurement, not the setting: the polls that wait for a search also
// record when its replies arrived, so shortening the window can later be argued
// from what peers did rather than from how long the waiting feels.

// newMeasuredClient builds a client that reports its search windows where the
// test can read them.
func newMeasuredClient(
	t *testing.T, handler http.Handler, budget, poll time.Duration,
) (*Client, *bytes.Buffer) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	reported := &bytes.Buffer{}
	client, err := NewClient(Options{
		BaseURL:       server.URL,
		APIKey:        "test-key",
		SearchTimeout: budget,
		PollInterval:  poll,
		Logger:        zerolog.New(reported),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, reported
}

// reportedWindow is the line a finished search leaves behind. Durations are
// milliseconds, which is how zerolog writes them everywhere in this project.
type reportedWindow struct {
	Query             string  `json:"query"`
	State             string  `json:"slskd_state"`
	ClosedBy          string  `json:"closed_by"`
	Stopped           bool    `json:"stopped"`
	WaitForCompleteMs int64   `json:"wait_for_complete_ms"`
	ResponsesAfterMs  int64   `json:"responses_after_ms"`
	Replies           int     `json:"replies"`
	Candidates        int     `json:"candidates"`
	Polls             int     `json:"polls"`
	Elapsed           float64 `json:"elapsed"`
	LastReplyAt       float64 `json:"last_reply_at"`
	BySecond          []int   `json:"replies_by_second"`
}

func readWindow(t *testing.T, reported *bytes.Buffer) reportedWindow {
	t.Helper()
	line := strings.TrimSpace(reported.String())
	if line == "" {
		t.Fatal("the search reported no window at all")
	}
	var window reportedWindow
	if err := json.Unmarshal([]byte(line), &window); err != nil {
		t.Fatalf("decode reported window %q: %v", line, err)
	}
	return window
}

func TestASearchReportsHowManyPeersRepliedToIt(t *testing.T) {
	stub := &searchStub{replies: []int{0, 2, 7}, completeAfter: 3}
	client, reported := newMeasuredClient(t, stub.handler(t, offeredByOnePeer()), 2*time.Second, time.Millisecond)

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	if replies := readWindow(t, reported).Replies; replies != 7 {
		t.Errorf("replies = %d, want the 7 slskd counted", replies)
	}
}

// The window question is about late replies, so the moment the last one landed
// is the number it turns on: a window whose replies all arrived early is one
// that could be shortened for nothing.
func TestASearchReportsWhenItsLastReplyLanded(t *testing.T) {
	stub := &searchStub{replies: []int{4, 4, 4, 4}, completeAfter: 4}
	client, reported := newMeasuredClient(t, stub.handler(t, offeredByOnePeer()), 2*time.Second, 10*time.Millisecond)

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	window := readWindow(t, reported)
	if window.LastReplyAt >= window.Elapsed {
		t.Errorf("last reply at %.1fms of %.1fms, want the waiting after it accounted for",
			window.LastReplyAt, window.Elapsed)
	}
}

func TestASearchNobodyAnsweredReportsNoReplies(t *testing.T) {
	stub := &searchStub{}
	client, reported := newMeasuredClient(t, stub.handler(t, nil), 2*time.Second, time.Millisecond)

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	window := readWindow(t, reported)
	if window.Replies != 0 || window.LastReplyAt != 0 {
		t.Errorf("window = %+v, want a silence recorded as one", window)
	}
}

// A window the budget cut short is the case shortening it would change, so it
// has to be told apart from one slskd itself declared finished.
func TestAWindowTheBudgetCutShortIsReportedUnfinished(t *testing.T) {
	stub := &searchStub{completeAfter: 1 << 20}
	client, reported := newMeasuredClient(t, stub.handler(t, nil), 50*time.Millisecond, 5*time.Millisecond)

	if _, err := client.Search(context.Background(), sources.Query{Text: "never completes"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	if state := readWindow(t, reported).State; state != "InProgress" {
		t.Errorf("state = %q, want the search still running when its budget ran out", state)
	}
}

// Replies are peers; candidates are folders worth reading. Both are reported,
// because a window full of replies that yielded nothing says something else
// about the window than one that yielded a copy.
func TestASearchReportsTheCandidatesItsWindowYielded(t *testing.T) {
	stub := &searchStub{}
	client, reported := newMeasuredClient(t, stub.handler(t, []map[string]any{
		{
			"username": "peer-one", "hasFreeUploadSlot": true,
			"files": []map[string]any{
				{"filename": `@@a\Dummy\01.flac`, "extension": "flac", "size": 30 << 20},
			},
		},
	}), 2*time.Second, time.Millisecond)

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if reported := readWindow(t, reported).Candidates; reported != len(candidates) {
		t.Errorf("reported %d candidates, want the %d the search returned", reported, len(candidates))
	}
}

// A window is only worth reading against what the search yielded, and a search
// whose replies slskd would not hand over yielded nothing. It is left out of
// the measurement rather than counted as a window that found no music.
func TestASearchThatCouldNotCollectItsRepliesReportsNoWindow(t *testing.T) {
	stub := &searchStub{replies: []int{3}}
	client, reported := newMeasuredClient(t, http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/responses") {
				response.WriteHeader(http.StatusInternalServerError)
				return
			}
			stub.handler(t, nil).ServeHTTP(response, request)
		}), 2*time.Second, time.Millisecond)

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err == nil {
		t.Fatal("Search() error = nil, want the replies nobody handed over reported")
	}

	if line := strings.TrimSpace(reported.String()); line != "" {
		t.Errorf("reported %q, want no window for a search that produced no answer", line)
	}
}

// The curve is per second and the polls are not, so a second no poll landed in
// carries the count from before it: what is recorded is what was known to have
// arrived by then, never a gap that reads as nobody answering.
func TestASecondNoPollLandedInCarriesTheCountBeforeIt(t *testing.T) {
	var window searchWindow
	window.observe(1, "InProgress", 0)
	window.observe(6, "Completed, TimedOut", 3*time.Second)

	if got := window.bySecond; len(got) != 4 || got[0] != 1 || got[1] != 1 || got[2] != 1 || got[3] != 6 {
		t.Errorf("curve = %v, want the two seconds nobody polled in holding the one reply known then", got)
	}
}

// The window is a setting, and a curve is one entry per second of it. A setting
// somebody typed an extra zero into must not grow the line without limit.
func TestACurveIsBoundedHoweverLongTheWindowWasSetTo(t *testing.T) {
	var window searchWindow
	window.observe(3, "InProgress", time.Hour)

	if length := len(window.bySecond); length != maxCurveSeconds {
		t.Errorf("curve has %d entries, want it bounded at %d", length, maxCurveSeconds)
	}
}

// The measurement above is what shortened the window, and what follows pins the
// shortening itself.
//
// The real numbers are seconds: a search that peers answer at one second and at
// two seconds, and then nobody else, closes eight seconds after that second
// reply — at about ten seconds of a twenty-second budget, rather than at twenty.
// The tests below scale all of those numbers down by the same factor, so that
// the suite spends milliseconds where production spends seconds. Nothing about
// the plateau depends on how long a second is.
//
// The timing is not a race. Replies are scripted by poll and not by the clock,
// so after the second poll no new reply ever arrives however the polls are
// spaced; a poll the scheduler delayed can only find the window quieter, never
// louder.

// A window closes once the replies stop, which is the whole change: the search
// is still running at slskd, and Schall stops watching it rather than spending
// the rest of a budget on silence.
func TestAWindowClosesOnceTheRepliesStopArriving(t *testing.T) {
	// completeAfter keeps slskd reporting the search as running, so that the
	// plateau closes the window and not slskd finishing with it.
	stub := &searchStub{replies: []int{1, 2}, completeAfter: 1 << 20}
	client, reported := newMeasuredClient(
		t, stub.handler(t, offeredByOnePeer()), 2*time.Second, 10*time.Millisecond)
	client.replyPlateau = 80 * time.Millisecond

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	window := readWindow(t, reported)
	if window.ClosedBy != "plateau" {
		t.Errorf("closed by %q, want the window closed by its replies stopping", window.ClosedBy)
	}
	if window.Replies != 2 {
		t.Errorf("replies = %d, want the 2 that arrived before the plateau", window.Replies)
	}
	// Long enough to have waited the plateau out, and nowhere near the
	// two-second budget the window would have spent in full before.
	if window.Elapsed < 80 || window.Elapsed > 1000 {
		t.Errorf("elapsed %.1fms, want a window closed after the plateau and well inside the budget",
			window.Elapsed)
	}
}

// A search nobody answered has no plateau to reach. Silence is not an answer
// here, so such a search waits out all of its budget exactly as it did before.
func TestASearchNobodyAnsweredStillWaitsOutItsWholeBudget(t *testing.T) {
	stub := &searchStub{completeAfter: 1 << 20}
	client, reported := newMeasuredClient(
		t, stub.handler(t, nil), 200*time.Millisecond, 10*time.Millisecond)
	client.replyPlateau = 20 * time.Millisecond

	if _, err := client.Search(context.Background(), sources.Query{Text: "nobody has this"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	window := readWindow(t, reported)
	if window.ClosedBy != "budget" {
		t.Errorf("closed by %q, want a silent search closed by its budget", window.ClosedBy)
	}
	if window.Replies != 0 {
		t.Errorf("replies = %d, want the silence the plateau must not read as an answer", window.Replies)
	}
	if window.Elapsed < 200 {
		t.Errorf("elapsed %.1fms, want the whole 200ms budget spent", window.Elapsed)
	}
}

// slskd finishing with a search is read before the plateau is. The state slskd
// reports is the real ending, so a window that reached both on one poll is
// recorded as the ending slskd declared.
func TestASearchSlskdFinishedIsNotRecordedAsAPlateau(t *testing.T) {
	stub := &searchStub{replies: []int{3}, completeAfter: 1}
	client, reported := newMeasuredClient(t, stub.handler(t, offeredByOnePeer()), 2*time.Second, time.Millisecond)
	client.replyPlateau = time.Nanosecond

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	if closedBy := readWindow(t, reported).ClosedBy; closedBy != "completed" {
		t.Errorf("closed by %q, want slskd's own ending preferred to the plateau", closedBy)
	}
}

// offeredByOnePeer is one peer answering with one folder, which is all a window
// needs to yield a candidate.
func offeredByOnePeer() []map[string]any {
	return []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@peer\Music\01.flac`, "extension": "flac", "size": 30 << 20},
		},
	}}
}

// The bug this whole path exists for. slskd stores a search's responses only
// when the search completes, so a window closed on its plateau used to read the
// empty list slskd answers a running search with and report no candidates at
// all: 491 of 759 windows in a 2h22m sample on 2026-09-04, 29,204 replies, not
// one candidate between them.
func TestAWindowClosedByThePlateauStillReadsTheRepliesItCollected(t *testing.T) {
	stub := &searchStub{replies: []int{1, 2}, completeAfter: 1 << 20, emptyResponses: 1}
	client, reported := newMeasuredClient(
		t, stub.handler(t, offeredByOnePeer()), 2*time.Second, 10*time.Millisecond)
	client.replyPlateau = 30 * time.Millisecond

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want what the peers who answered were offering", candidates)
	}
	window := readWindow(t, reported)
	if window.ClosedBy != "plateau" {
		t.Errorf("closed by %q, want the window still closed by its replies stopping", window.ClosedBy)
	}
	if !window.Stopped {
		t.Error("the search was read without slskd being asked to finish it")
	}
}

// The stop is what makes the wait short: slskd finishes the search, stores what
// it collected, and answers the next poll with a completed state.
func TestAWindowClosedByThePlateauAsksSlskdToFinishTheSearch(t *testing.T) {
	stub := &searchStub{replies: []int{1}, completeAfter: 1 << 20}
	client, _ := newMeasuredClient(
		t, stub.handler(t, offeredByOnePeer()), 2*time.Second, 10*time.Millisecond)
	client.replyPlateau = 30 * time.Millisecond

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.stops != 1 {
		t.Errorf("stops = %d, want the one stop that finishes the search", stub.stops)
	}
	// A search slskd has finished with is a search that may be removed, which
	// is the reaper's whole condition.
	if !stub.deleted {
		t.Error("the finished search was left in slskd")
	}
}

// A plateau can finish with an empty response list even after Schall asked slskd
// to stop. The reply count proves the search reached peers, so this is a retryable
// unread response and does not enter the abandoned or silent-search paths.
func TestAPlateauWithEmptyResponsesReportsRepliesUnread(t *testing.T) {
	stub := &searchStub{replies: []int{1, 2}, completeAfter: 1 << 20}
	client, reported := newMeasuredClient(
		t, stub.handler(t, nil), 2*time.Second, 10*time.Millisecond)
	client.replyPlateau = 30 * time.Millisecond
	client.completionGrace = 20 * time.Millisecond

	_, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if !errors.Is(err, sources.ErrRepliesUnread) {
		t.Fatalf("error = %v, want the answered search with no stored replies reported", err)
	}
	if !strings.Contains(err.Error(), "2 peers answered") ||
		!strings.Contains(err.Error(), "slskd stored none of their replies") {
		t.Errorf("error = %q, want the reply count and missing stored replies", err)
	}
	line := reported.String()
	if strings.Contains(line, "abandoned") {
		t.Errorf("log = %q, want no abandoned-search warning", line)
	}
	for _, field := range []string{`"search_id"`, `"query":"portishead dummy"`, `"replies":2`, `"state":"Completed, Cancelled"`} {
		if !strings.Contains(line, field) {
			t.Errorf("log = %q, want %s", line, field)
		}
	}

	client.gate.mu.Lock()
	run := client.gate.silence.run
	client.gate.mu.Unlock()
	if run != 0 {
		t.Errorf("silent search run = %d, want the answered search excluded", run)
	}
}

// A stop slskd would not take is not a failure: the wait for the search to end
// on its own is the same wait, one poll longer.
func TestASearchSlskdWouldNotStopIsStillWaitedOutForItsReplies(t *testing.T) {
	stub := &searchStub{replies: []int{1}, completeAfter: 3, refuseStop: true}
	client, reported := newMeasuredClient(
		t, stub.handler(t, offeredByOnePeer()), 2*time.Second, time.Millisecond)
	client.replyPlateau = 5 * time.Millisecond

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the replies read once slskd finished on its own", candidates)
	}
	if window := readWindow(t, reported); window.Stopped {
		t.Error("a stop slskd refused was reported as taken")
	}
}

// A search still running when the grace runs out has replies nobody can read.
// Reading the empty list slskd answers with would record a false absence about
// music peers are plainly sharing, so it is backpressure and spends no attempt.
func TestASearchSlskdNeverFinishedIsNotReadAsAnAbsence(t *testing.T) {
	stub := &searchStub{replies: []int{4}, completeAfter: 1 << 20, ignoreStop: true}
	client, reported := newMeasuredClient(
		t, stub.handler(t, offeredByOnePeer()), 2*time.Second, time.Millisecond)
	client.replyPlateau = 5 * time.Millisecond
	client.completionGrace = 20 * time.Millisecond

	_, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})

	if !errors.Is(err, sources.ErrRepliesUnread) {
		t.Fatalf("error = %v, want replies nobody could read reported as no answer", err)
	}
	// Every caller that only asks whether this was backpressure rather than an
	// answer must keep the answer it had.
	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Errorf("error = %v, want it to still read as the source being unable to search", err)
	}
	window := readWindow(t, reported)
	if window.Replies != 4 || window.Candidates != 0 {
		t.Errorf("window = %+v, want the replies recorded against the candidates they did not become", window)
	}
	if window.WaitForCompleteMs < 20 {
		t.Errorf("waited %dms, want the whole grace spent on a search that never finished",
			window.WaitForCompleteMs)
	}
}

// A search nobody answered is left exactly where it was. It has no replies to
// lose, and stopping it would rewrite the state confirmAsked reads: a search
// slskd cancelled says something quite different about the music from a search
// that ran its window out in silence.
func TestASearchNobodyAnsweredIsNotStopped(t *testing.T) {
	stub := &searchStub{completeAfter: 1 << 20}
	client, _ := newMeasuredClient(t, stub.handler(t, nil), 50*time.Millisecond, 5*time.Millisecond)

	if _, err := client.Search(context.Background(), sources.Query{Text: "nobody has this"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.stops != 0 {
		t.Errorf("stops = %d, want a silent window left where it was", stub.stops)
	}
}

// slskd is told the window Schall watches for, so it finishes the search while
// Schall is still there to read it rather than on its own longer default.
func TestASearchIsCreatedWithTheReplyWindowItIsWatchedFor(t *testing.T) {
	stub := &searchStub{}
	client, _ := newTestClient(t, stub.handler(t, nil))

	if _, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if want := client.searchTimeout.Milliseconds(); stub.searchTimeout != want {
		t.Errorf("searchTimeout = %dms, want the %dms window Schall watches for", stub.searchTimeout, want)
	}
}

// A search Schall could not finish is still running at slskd, so it is left
// where the budget leaves one: handed to the reaper, never deleted from under
// slskd while it works.
func TestAWindowClosedByThePlateauIsNotDeletedWhileItRuns(t *testing.T) {
	stub := &searchStub{replies: []int{1}, completeAfter: 1 << 20, ignoreStop: true}
	client, _ := newMeasuredClient(t, stub.handler(t, nil), 2*time.Second, time.Millisecond)
	client.replyPlateau = 5 * time.Millisecond
	client.completionGrace = 20 * time.Millisecond

	_, _ = client.Search(context.Background(), sources.Query{Text: "portishead dummy"})

	// Long enough for the cleanup to have made its mind up several times over.
	time.Sleep(50 * time.Millisecond)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.deleted {
		t.Error("the search was deleted while slskd was still running it")
	}
}

// A search still waiting to go out when its window closed was put to nobody.
// slskd sets "Queued" while a search waits for a slot inside its own client and
// "InProgress" only once the request has been written to the Soulseek server, so
// the empty list beside either of the earlier states is not an answer about who
// is sharing the music — it is the 195 windows in seventy-two hours that spent a
// want's attempt and climbed its ladder for a question nobody was asked.
func TestASearchThatNeverWentOutIsNotAnAnswer(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		state string
	}{
		{"queued", "Queued"},
		{"requested", "Requested"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := &searchStub{state: testCase.state}
			client, _ := newTestClient(t, stub.handler(t, nil))

			_, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

			if !errors.Is(err, sources.ErrNobodyWasAsked) {
				t.Fatalf("error = %v, want a search that never went out reported as no answer", err)
			}
			// Every caller that only asks whether this was backpressure rather
			// than an answer must keep the answer it had.
			if !errors.Is(err, sources.ErrProviderUnavailable) {
				t.Errorf("error = %v, want it to still read as the source being unable to search", err)
			}
			if !strings.Contains(err.Error(), testCase.state) {
				t.Errorf("error = %q, want it to carry slskd's own word %q", err, testCase.state)
			}
		})
	}
}

// Before the dequeue wait had its own budget, a search still queued when the
// fixed reply window closed was read as nobody having been asked, whatever the
// reason it was still queued. Sitting in slskd's queue for longer than that
// window used to be, and then reaching the network and finding a peer, must
// read as the answer it is.
func TestASearchThatDequeuesLateStillCollectsAnAnswer(t *testing.T) {
	// dequeueAfter outlasts the searchTimeout below several times over, which is
	// what the old code counted the queue wait against.
	stub := &searchStub{dequeueAfter: 30, replies: []int{1}}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@peer\Music\01.flac`, "extension": "flac", "size": 30 << 20},
		},
	}}))
	client.searchTimeout = 5 * time.Millisecond
	client.dequeueBudget = 100 * time.Millisecond

	candidates, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want what the peer offered once the search left the queue", candidates)
	}
}

// The dequeue wait is real backpressure too, and it must still end the search
// as sources.ErrNobodyWasAsked rather than let a search that never left slskd's
// queue run forever.
func TestASearchStillQueuedWhenTheDequeueBudgetRunsOutIsNotAnAnswer(t *testing.T) {
	stub := &searchStub{dequeueAfter: 1 << 20}
	client, _ := newTestClient(t, stub.handler(t, nil))
	client.dequeueBudget = 20 * time.Millisecond

	_, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if !errors.Is(err, sources.ErrNobodyWasAsked) {
		t.Fatalf("error = %v, want a search still queued when the dequeue wait ran out reported as no answer", err)
	}
	if !strings.Contains(err.Error(), "Queued") {
		t.Errorf("error = %q, want it to carry slskd's own word %q", err, "Queued")
	}
}

// SearchBudget is what internal/downloads sizes a want's whole ladder walk
// from, so it has to give the search room for the dequeue wait as well as the
// reply window and the HTTP allowance — a caller given exactly that much room
// must be able to sit out the whole dequeue wait and be read as backpressure,
// rather than have its own deadline cut the wait off first.
func TestSearchBudgetCoversTheWholeDequeueWait(t *testing.T) {
	stub := &searchStub{dequeueAfter: 1 << 20}
	client, _ := newTestClient(t, stub.handler(t, nil))
	client.dequeueBudget = 30 * time.Millisecond
	client.searchTimeout = 10 * time.Millisecond

	// A little over SearchBudget() itself, the way TestAContendedGateIsChargedBeforeSearchAffordability
	// does: canFinish rechecks what remains of the caller's own deadline once the
	// gate is entered, and some of it is always spent getting there.
	ctx, cancel := context.WithTimeout(context.Background(), client.SearchBudget()+20*time.Millisecond)
	defer cancel()
	_, err := client.Search(ctx, sources.Query{Text: "pashanim florenz"})

	if !errors.Is(err, sources.ErrNobodyWasAsked) {
		t.Fatalf("error = %v, want SearchBudget() to give the dequeue wait room to run out on its own "+
			"rather than the caller's deadline cutting it short", err)
	}
}

// The reading may only ever object. A search that was still queued and that
// peers answered anyway reached somebody, whatever slskd called it, and what
// they offered is not thrown away over the name of a state.
//
// It is also where the two readings of a window meet, and they cannot collide.
// A window closes early on a plateau only once a peer has answered, and the
// state is only read when nobody did, so the search below closes on its
// plateau — with "Queued" still its state — and is never asked the question at
// all.
func TestASearchThatNeverWentOutButFoundSomethingIsStillAnAnswer(t *testing.T) {
	stub := &searchStub{state: "Queued", replies: []int{1}}
	client, _ := newTestClient(t, stub.handler(t, []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@peer\Music\01.flac`, "extension": "flac", "size": 30 << 20},
		},
	}}))
	client.replyPlateau = 20 * time.Millisecond

	started := time.Now()
	candidates, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})
	if closed := time.Since(started); closed >= client.searchTimeout {
		t.Errorf("the window ran %v, want it closed on its plateau well inside the %v budget",
			closed, client.searchTimeout)
	}

	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want what the peer offered", candidates)
	}
}

// A word this does not know is silence, not evidence. Only "requested" and
// "queued" say the search never went out; anything else leaves an empty search
// the ordinary empty search it has always been.
func TestASearchInAStateNobodyKnowsIsStillAnAnswer(t *testing.T) {
	stub := &searchStub{state: "Whatever"}
	client, _ := newTestClient(t, stub.handler(t, nil))

	if _, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"}); err != nil {
		t.Fatalf("search: %v", err)
	}
}

// The phrase the gate will not repeat is refused about that phrase, not about
// the source. It still reads as backpressure to everything that only asks
// whether an answer was given.
func TestAPhraseTheGateWillNotRepeatIsRefusedAboutThePhrase(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	knownLoggedIn(client)
	for range repeatAllowance {
		client.gate.reserve("pashanim florenz")
	}

	_, err := client.Search(context.Background(), sources.Query{Text: "pashanim florenz"})

	if !errors.Is(err, sources.ErrPhraseHeldBack) {
		t.Fatalf("error = %v, want the refusal to name the phrase it is about", err)
	}
	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Errorf("error = %v, want it to still read as backpressure", err)
	}
}

// "Search timeout" on the settings page is how long to wait for peers, and it
// takes anything from five seconds to two minutes. The plateau is how long a
// window that has already heard from somebody waits for one more reply, and
// while it was a fixed eight seconds a two-minute window closed at the same
// early moment a twenty-second one did — so the setting bought nothing above
// the first eight quiet seconds. It is a share of the window now.
func TestThePlateauIsAShareOfTheWindowTheUserAskedFor(t *testing.T) {
	for _, window := range []struct {
		timeout time.Duration
		want    time.Duration
	}{
		// The measured default: eight seconds of twenty is the share every
		// other width is worked out from.
		{20 * time.Second, 8 * time.Second},
		// The widest the setting goes.
		{120 * time.Second, 48 * time.Second},
		// And the narrowest, where the floor holds: a short window is never a
		// reason to call a reply the last one sooner than the measurement did.
		{5 * time.Second, 8 * time.Second},
		{time.Minute, 24 * time.Second},
	} {
		t.Run(window.timeout.String(), func(t *testing.T) {
			if got := plateauFor(window.timeout); got != window.want {
				t.Fatalf("plateau for a %s window = %s, want %s",
					window.timeout, got, window.want)
			}
		})
	}
}

// And the client is built with it, rather than the constant, so raising the
// setting reaches the search that reads it.
func TestAClientTakesThePlateauItsOwnTimeoutEarns(t *testing.T) {
	client, err := NewClient(Options{
		BaseURL: "http://slskd.test", APIKey: "key", SearchTimeout: 120 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.replyPlateau != 48*time.Second {
		t.Fatalf("plateau = %s, want the 48s a two-minute window earns", client.replyPlateau)
	}

	// A client built without a timeout falls back to the twenty-second window,
	// and the plateau has to be worked out after that fallback rather than
	// before it, or the default window would keep a plateau of nothing.
	client, err = NewClient(Options{BaseURL: "http://slskd.test", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if client.replyPlateau != defaultReplyPlateau {
		t.Fatalf("plateau = %s, want the measured %s", client.replyPlateau, defaultReplyPlateau)
	}
}

// silentStub answers searches the way a banned account's searches are answered:
// slskd takes the creation, reports the search finished, and hands back no
// replies at all. From one search that is indistinguishable from the ordinary
// quiet search Soulseek is full of, which is why the run is what gets counted.
type silentStub struct {
	mu sync.Mutex
	// created counts the searches that reached slskd, which is how a search
	// refused before it was made is told from one that went out.
	created int
	// answered makes the searches after it come back with a peer.
	answered bool
	// state is what slskd reports the finished search as. Empty is the ordinary
	// window that ran out having heard nobody.
	state string
	// serverState is what slskd says its Soulseek connection is doing. Empty is
	// the connection being up throughout, which is what a ban served without a
	// disconnection looks like.
	serverState string
	// conversation is what the Soulseek server has said to slskd, and reads
	// counts how often the ban notice in it was looked for.
	conversation []map[string]any
	reads        int
}

func (stub *silentStub) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()

		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v0/searches":
			stub.created++
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v0/searches":
			_ = json.NewEncoder(response).Encode([]map[string]any{})
		case request.Method == http.MethodDelete:
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/api/v0/server":
			state := stub.serverState
			if state == "" {
				state = "Connected, LoggedIn"
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"state": state})
		case request.URL.Path == "/api/v0/conversations/server":
			stub.reads++
			_ = json.NewEncoder(response).Encode(map[string]any{
				"username": "server", "messages": stub.conversation,
			})
		case strings.HasSuffix(request.URL.Path, "/responses"):
			_ = json.NewEncoder(response).Encode(stub.offers())
		default:
			state := stub.state
			if state == "" {
				state = "Completed, TimedOut"
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"state": state, "responseCount": len(stub.offers()),
			})
		}
	})
}

// offers is the one peer a stub set to answering hands back, and nothing at all
// otherwise. Called with the stub already held.
func (stub *silentStub) offers() []map[string]any {
	if !stub.answered {
		return []map[string]any{}
	}
	return []map[string]any{{
		"username": "peer", "hasFreeUploadSlot": true,
		"files": []map[string]any{
			{"filename": `@@peer\Music\01.flac`, "extension": "flac", "size": 30 << 20},
		},
	}}
}

func (stub *silentStub) creations() int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.created
}

func (stub *silentStub) noticeReads() int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.reads
}

func (stub *silentStub) answer(answered bool) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.answered = answered
}

func (stub *silentStub) reportState(state string) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.state = state
}

// Every search is put in words of its own, because the gate refuses a phrase
// repeated inside its window and what these tests count is searches.
func phrase(n int) sources.Query {
	return sources.Query{Text: fmt.Sprintf("unanswered phrase %d", n)}
}

// runSilentSearches walks the run that earns the pause and fails if any of them
// was refused on the way.
func runSilentSearches(t *testing.T, client *Client, from, count int) {
	t.Helper()
	for i := range count {
		if _, err := client.Search(context.Background(), phrase(from+i)); err != nil {
			t.Fatalf("search %d of the run: %v", i+1, err)
		}
	}
}

// The Soulseek server serves some of its thirty-minute bans without
// disconnecting the account: slskd goes on taking searches, /server goes on
// saying "Connected, LoggedIn", and every search collects nothing. On
// 2026-09-04 that lasted from 09:52 until 10:28, and 72 of the 84 searches
// inside it recorded an absence and spent an attempt on music peers were
// sharing the whole time.
func TestARunOfSearchesTheNetworkDidNotAnswerPausesSearching(t *testing.T) {
	stub := &silentStub{}
	client, _ := newTestClient(t, stub.handler(t))

	runSilentSearches(t, client, 0, silentWindowsBeforePause)

	_, err := client.Search(context.Background(), phrase(100))
	if !errors.Is(err, sources.ErrSearchSilenced) {
		t.Fatalf("error = %v, want the run of unanswered searches to stop the next one", err)
	}
	// Every caller that only asks "was this backpressure rather than an answer"
	// has to keep the answer it had, or the refusal settles a want.
	if !errors.Is(err, sources.ErrProviderUnavailable) {
		t.Errorf("error = %v, want it read as backpressure as well", err)
	}
	if stub.creations() != silentWindowsBeforePause {
		t.Errorf("creations = %d, want the refused search never to reach slskd (%d)",
			stub.creations(), silentWindowsBeforePause)
	}
}

// One peer answering is the Soulseek server distributing this account's
// queries, which is the whole of what the run ever stood in for. Ordinary quiet
// runs to four searches on a normal day, so a run that never resets would pause
// searching on an unpopular record rather than on a ban.
func TestAPeerAnsweringEndsTheRunOfUnansweredSearches(t *testing.T) {
	stub := &silentStub{}
	client, _ := newTestClient(t, stub.handler(t))

	runSilentSearches(t, client, 0, silentWindowsBeforePause-1)
	stub.answer(true)
	if _, err := client.Search(context.Background(), phrase(100)); err != nil {
		t.Fatalf("the search a peer answered: %v", err)
	}
	stub.answer(false)

	// The run starts again from nothing, so as many unanswered searches as
	// before the reply pauses nothing.
	runSilentSearches(t, client, 200, silentWindowsBeforePause-1)
}

// The pause is the length of the ban behind it and ends on its own, because
// nothing tells this account when it is over.
func TestThePauseEndsOnceItsHalfHourHasPassed(t *testing.T) {
	stub := &silentStub{}
	client, _ := newTestClient(t, stub.handler(t))
	at := time.Now()
	client.gate.now = func() time.Time { return at }

	runSilentSearches(t, client, 0, silentWindowsBeforePause)
	if _, err := client.Search(context.Background(), phrase(100)); !errors.Is(err, sources.ErrSearchSilenced) {
		t.Fatalf("error = %v, want searching paused", err)
	}

	at = at.Add(silencedRest)

	if _, err := client.Search(context.Background(), phrase(200)); err != nil {
		t.Fatalf("a search after the half hour: %v", err)
	}
	if stub.creations() != silentWindowsBeforePause+1 {
		t.Errorf("creations = %d, want the search after the pause to reach slskd (%d)",
			stub.creations(), silentWindowsBeforePause+1)
	}
}

// An account that signed out and back in is on a new connection to Soulseek,
// and the Soulseek server hands a banned account a fresh half hour rather than
// the remainder of the old one. The run said nothing about the new connection,
// so the pause it earned goes with the old one.
func TestThePauseEndsWhenTheAccountSignsBackIn(t *testing.T) {
	stub := &silentStub{}
	client, _ := newTestClient(t, stub.handler(t))

	runSilentSearches(t, client, 0, silentWindowsBeforePause)
	if _, err := client.Search(context.Background(), phrase(100)); !errors.Is(err, sources.ErrSearchSilenced) {
		t.Fatalf("error = %v, want searching paused", err)
	}

	client.gate.observeConnection(false, `slskd reports "Disconnected"`)
	client.gate.observeConnection(true, `slskd reports "Connected, LoggedIn"`)

	if _, err := client.Search(context.Background(), phrase(200)); err != nil {
		t.Fatalf("a search after the account signed back in: %v", err)
	}
}

// The server's own notice says why, and it is worth one request to know whether
// this was a ban or an slskd that stopped distributing searches for some other
// reason. It is read when the pause starts and not once more: the notice
// arrives minutes after the replies stop, so nothing about the pause waits for
// it and nothing about it is asked again while searches are being refused.
func TestTheBanNoticeIsReadOnceWhenSearchingPauses(t *testing.T) {
	stub := &silentStub{conversation: []map[string]any{{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"username":  "server",
		"direction": "In",
		"message": "System Message: You have been banned for 30 minutes. This is usually the " +
			"result of doing too many operations at once. Do not quickly repeat a search.",
	}}}
	client, _ := newTestClient(t, stub.handler(t))

	runSilentSearches(t, client, 0, silentWindowsBeforePause-1)
	if reads := stub.noticeReads(); reads != 0 {
		t.Errorf("notice reads = %d, want none before searching paused", reads)
	}

	runSilentSearches(t, client, 100, 1)
	if reads := stub.noticeReads(); reads != 1 {
		t.Fatalf("notice reads = %d, want the notice read once as the pause started", reads)
	}

	if _, err := client.Search(context.Background(), phrase(200)); !errors.Is(err, sources.ErrSearchSilenced) {
		t.Fatalf("error = %v, want searching paused", err)
	}
	if reads := stub.noticeReads(); reads != 1 {
		t.Errorf("notice reads = %d, want a refused search to ask nothing", reads)
	}
}

// Only a search Soulseek actually heard says anything about whether Soulseek is
// answering. A search slskd gave up on is already reported as backpressure of
// its own, and counting it here would pause searching over an slskd fault.
func TestASearchSlskdGaveUpOnIsNotCountedTowardsThePause(t *testing.T) {
	stub := &silentStub{state: "Completed, Errored"}
	client, _ := newTestClient(t, stub.handler(t))

	for i := range silentWindowsBeforePause {
		_, err := client.Search(context.Background(), phrase(i))
		if !errors.Is(err, sources.ErrProviderUnavailable) {
			t.Fatalf("search %d: error = %v, want the abandoned search reported as no answer", i, err)
		}
		if errors.Is(err, sources.ErrSearchSilenced) {
			t.Fatalf("search %d: error = %v, want it read as slskd's fault, not the network's", i, err)
		}
	}

	stub.reportState("Completed, TimedOut")

	if _, err := client.Search(context.Background(), phrase(100)); err != nil {
		t.Fatalf("a search after eight slskd gave up on: %v", err)
	}
}

// deleteSearch is the only way a finished search ever leaves slskd. A DELETE
// slskd refuses used to fail silently, and the only symptom was a search row
// slskd never ages out, growing without a trace anywhere in the log.
func TestDeleteSearchLogsAFailureAtWarn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	reported := &bytes.Buffer{}
	client, err := NewClient(Options{
		BaseURL: server.URL, APIKey: "test-key",
		SearchTimeout: 2 * time.Second, PollInterval: time.Millisecond,
		Logger: zerolog.New(reported),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	client.deleteSearch("search-1")

	line := reported.String()
	if !strings.Contains(line, `"level":"warn"`) {
		t.Fatalf("log = %q, want a warning logged", line)
	}
	if !strings.Contains(line, `"search_id":"search-1"`) {
		t.Fatalf("log = %q, want the search ID in the warning", line)
	}
}

// slskd can report completion before it has moved counted replies into the
// response list. Search reads the list again before reporting unread replies.
func TestASearchRereadsResponsesAfterCompletion(t *testing.T) {
	stub := &searchStub{replies: []int{1}, emptyResponses: 1}
	client, reported := newMeasuredClient(
		t, stub.handler(t, offeredByOnePeer()), 2*time.Second, 10*time.Millisecond)
	client.completionGrace = 50 * time.Millisecond

	candidates, err := client.Search(context.Background(), sources.Query{Text: "portishead dummy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the response returned after the reread", candidates)
	}
	window := readWindow(t, reported)
	if window.ResponsesAfterMs <= 0 {
		t.Errorf("responses_after_ms = %d, want the reread delay", window.ResponsesAfterMs)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.responseReads != 2 {
		t.Errorf("response reads = %d, want the empty read and the reread", stub.responseReads)
	}
	if stub.stops != 0 {
		t.Errorf("stops = %d, want slskd's own completion without a stop request", stub.stops)
	}
}

// A search nobody answered has no response race. It reads /responses once and
// lets confirmAsked inspect the completed state.
func TestASearchWithNoRepliesReadsResponsesOnce(t *testing.T) {
	stub := &searchStub{completeAfter: 1}
	client, _ := newMeasuredClient(
		t, stub.handler(t, nil), 2*time.Second, time.Millisecond)

	if _, err := client.Search(context.Background(), sources.Query{Text: "nobody has this"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.responseReads != 1 {
		t.Errorf("response reads = %d, want one read for a search with no replies", stub.responseReads)
	}
}
