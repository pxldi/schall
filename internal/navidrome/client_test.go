package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// subsonic answers like a Subsonic server: always HTTP 200, with the outcome
// inside the envelope. Each request's query is kept so a test can read what was
// actually asked.
func subsonic(t *testing.T, body string) (*httptest.Server, *[]url.Values) {
	t.Helper()
	var asked []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.ReplaceAll(body, "PATH", r.URL.Path)))
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(Options{BaseURL: baseURL, Username: "schall", Password: "hunter2"})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

const okPing = `{"subsonic-response":{"status":"ok","version":"1.16.1",` +
	`"type":"navidrome","serverVersion":"0.63.2"}}`

func TestARescanAsksTheServerToStartOne(t *testing.T) {
	server, asked := subsonic(t, `{"subsonic-response":{"status":"ok","version":"1.16.1",`+
		`"path":"PATH","scanStatus":{"scanning":true,"count":0}}}`)

	if err := testClient(t, server.URL).Rescan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	if len(*asked) != 1 {
		t.Fatalf("requests = %d, want one", len(*asked))
	}
	if got := (*asked)[0].Get("c"); got != clientName {
		t.Errorf("client name = %q, want %q", got, clientName)
	}
	if got := (*asked)[0].Get("v"); got != protocolVersion {
		t.Errorf("protocol version = %q, want %q", got, protocolVersion)
	}
}

// The whole point of the client boundary: Subsonic reports a refusal with a 200
// and describes it in the body, so a client reading only the status code would
// call every failure a success.
func TestARefusalArrivingAsSuccessIsStillARefusal(t *testing.T) {
	server, _ := subsonic(t, `{"subsonic-response":{"status":"failed","version":"1.16.1",`+
		`"error":{"code":40,"message":"Wrong username or password"}}}`)

	err := testClient(t, server.URL).Rescan(context.Background())

	if err == nil {
		t.Fatal("rescan reported success on a refusal")
	}
	if !strings.Contains(err.Error(), "username and password") {
		t.Errorf("error = %q, want it to name the credentials", err)
	}
}

// An account that may play but may not scan is a configuration mistake somebody
// can fix, and only worth fixing if the message says so.
func TestAnAccountThatMayNotScanSaysSo(t *testing.T) {
	server, _ := subsonic(t, `{"subsonic-response":{"status":"failed","version":"1.16.1",`+
		`"error":{"code":50,"message":"Forbidden"}}}`)

	err := testClient(t, server.URL).Rescan(context.Background())

	if err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Errorf("error = %v, want it to name the account's permissions", err)
	}
}

func TestTheCredentialSentIsAHashAndNeverThePassword(t *testing.T) {
	server, asked := subsonic(t, okPing)

	if _, err := testClient(t, server.URL).CheckConnection(context.Background()); err != nil {
		t.Fatalf("check connection: %v", err)
	}

	query := (*asked)[0]
	if strings.Contains(query.Encode(), "hunter2") {
		t.Fatal("the password was sent in the request")
	}
	salt := query.Get("s")
	if salt == "" {
		t.Fatal("no salt was sent")
	}
	expected := md5.Sum([]byte("hunter2" + salt))
	if got := query.Get("t"); got != hex.EncodeToString(expected[:]) {
		t.Errorf("token = %q, want the password salted with %q", got, salt)
	}
}

// A salt reused across requests is a password hash somebody can replay.
func TestEachRequestIsSaltedAfresh(t *testing.T) {
	server, asked := subsonic(t, okPing)
	client := testClient(t, server.URL)

	for range 2 {
		if _, err := client.CheckConnection(context.Background()); err != nil {
			t.Fatalf("check connection: %v", err)
		}
	}

	if first, second := (*asked)[0].Get("s"), (*asked)[1].Get("s"); first == second {
		t.Errorf("salt = %q twice, want a new one each request", first)
	}
}

func TestAConnectionCheckReportsTheServerItReached(t *testing.T) {
	server, _ := subsonic(t, okPing)

	status, err := testClient(t, server.URL).CheckConnection(context.Background())

	if err != nil {
		t.Fatalf("check connection: %v", err)
	}
	for _, want := range []string{"navidrome", "0.63.2", "schall"} {
		if !strings.Contains(status.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", status.Detail, want)
		}
	}
}

// A player that is starting up says nothing about the library, so the message
// has to invite trying again rather than read as a misconfiguration.
func TestAServerThatIsNotReadyIsReportedAsNotReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := testClient(t, server.URL).Rescan(context.Background())

	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("error = %v, want it to report a server that is not ready yet", err)
	}
}

func TestACallerThatGaveUpIsReportedUnchanged(t *testing.T) {
	server, _ := subsonic(t, okPing)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := testClient(t, server.URL).Rescan(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation", err)
	}
}

// The player is named in API responses and in the log, so the name is part of
// what this client is rather than a detail of it.
func TestTheClientNamesThePlayerItSpeaksTo(t *testing.T) {
	if got := testClient(t, "http://navidrome:4533").Name(); got != "navidrome" {
		t.Errorf("Name() = %q, want navidrome", got)
	}
}

func TestAClientWithoutAnAddressIsRefused(t *testing.T) {
	for name, baseURL := range map[string]string{
		"no base URL":            "",
		"a port with no host":    ":4533",
		"not a URL at all":       "navidrome:4533",
		"a scheme nobody speaks": "ftp://navidrome:4533",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(Options{
				BaseURL: baseURL, Username: "schall", Password: "hunter2",
			}); err == nil {
				t.Error("a client was built with an address it cannot reach")
			}
		})
	}
}

// Subsonic sends a refusal with a 200 and describes it in the body, so the body
// is the only place a reason exists — and each reason is a different thing for
// somebody to go and fix.
func TestARefusalTheServerDescribedIsTurnedIntoSomethingActionable(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		code    int
		message string
		want    string
	}{
		{"token authentication refused", codeTokenRefused, "Token authentication not supported",
			"external directory"},
		{"the client is too old", codeClientTooOld, "Incompatible Subsonic REST protocol version",
			"do not share a Subsonic version"},
		{"the server is too old", codeServerTooOld, "Incompatible Subsonic REST protocol version",
			"do not share a Subsonic version"},
		{"a code describe has nothing to add to", codeNotFound, "The requested data was not found",
			"navidrome refused the request: The requested data was not found"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server, _ := subsonic(t, fmt.Sprintf(
				`{"subsonic-response":{"status":"failed","version":"1.16.1",`+
					`"error":{"code":%d,"message":%q}}}`, testCase.code, testCase.message))

			err := testClient(t, server.URL).Rescan(context.Background())

			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

// A server that refused without saying why still has to read as a refusal rather
// than as an empty success.
func TestARefusalWithNoReasonIsStillARefusal(t *testing.T) {
	server, _ := subsonic(t, `{"subsonic-response":{"status":"failed","version":"1.16.1"}}`)

	err := testClient(t, server.URL).Rescan(context.Background())

	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("error = %v, want a refusal nobody explained still reported", err)
	}
}

// A refusal carrying a code and no words is still worth reporting by its code:
// it is what somebody would look up.
func TestARefusalWithNoWordsIsReportedByItsCode(t *testing.T) {
	err := &Error{Code: 70}

	if got := err.Error(); !strings.Contains(got, "code 70") {
		t.Errorf("error = %q, want the code named", got)
	}
}

// A status the server answered with instead of an envelope is not a refusal it
// described, so it is reported as the status it was.
func TestAStatusTheServerAnsweredWithIsTurnedIntoSomethingActionable(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
		want   string
	}{
		{"credentials rejected", http.StatusUnauthorized, "username and password"},
		{"account forbidden", http.StatusForbidden, "username and password"},
		{"api path not found", http.StatusNotFound, "exposes /rest"},
		{"gateway timeout", http.StatusGatewayTimeout, "not ready"},
		{"a status nobody here knows", http.StatusInternalServerError, "navidrome returned HTTP 500"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
			}))
			t.Cleanup(server.Close)

			err := testClient(t, server.URL).Rescan(context.Background())

			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

// A player that is not there at all is a base URL somebody typed wrong, and the
// transport error alone says nothing they could act on.
func TestAPlayerThatCouldNotBeReachedSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	err := testClient(t, address).Rescan(context.Background())

	if err == nil || !strings.Contains(err.Error(), "could not reach navidrome") {
		t.Errorf("error = %v, want the unreachable player reported", err)
	}
}

func TestAPlayerThatDidNotRespondInTimeSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	client, err := NewClient(Options{
		BaseURL: server.URL, Username: "schall", Password: "hunter2",
		HTTPClient: &http.Client{Timeout: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	if err := client.Rescan(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "did not respond in time") {
		t.Errorf("error = %v, want an actionable timeout message", err)
	}
}

// The connection check reads the body, so a body that is not an envelope has to
// be reported rather than read as an empty success.
func TestAnAnswerThatIsNotAnEnvelopeIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>a proxy login page</html>"))
	}))
	t.Cleanup(server.Close)

	_, err := testClient(t, server.URL).CheckConnection(context.Background())

	if err == nil || !strings.Contains(err.Error(), "decode navidrome response") {
		t.Errorf("error = %v, want the unreadable answer reported", err)
	}
}

// A server that answered without naming itself is still a server that answered,
// and the check has to say what it found rather than nothing.
func TestAServerThatDidNotNameItselfIsStillReported(t *testing.T) {
	server, _ := subsonic(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	status, err := testClient(t, server.URL).CheckConnection(context.Background())

	if err != nil {
		t.Fatalf("check connection: %v", err)
	}
	if !strings.Contains(status.Detail, "subsonic server") {
		t.Errorf("detail = %q, want the server it reached still described", status.Detail)
	}
}

// Signing a request must not lose what the request was asking for.
func TestSigningARequestKeepsTheParametersItCarries(t *testing.T) {
	signed, err := testClient(t, "http://navidrome:4533").
		authenticated(url.Values{"id": {"an-album"}})
	if err != nil {
		t.Fatalf("sign the request: %v", err)
	}

	if got := signed.Get("id"); got != "an-album" {
		t.Errorf("id = %q, want the parameter kept alongside the credentials", got)
	}
}

func TestAClientWithoutAnAccountIsRefused(t *testing.T) {
	if _, err := NewClient(Options{BaseURL: "http://navidrome:4533", Password: "hunter2"}); err == nil {
		t.Error("a client was built with no username")
	}
	if _, err := NewClient(Options{BaseURL: "http://navidrome:4533", Username: "schall"}); err == nil {
		t.Error("a client was built with no password")
	}
}
