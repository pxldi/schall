package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/notify"
	"github.com/rs/zerolog"
)

// Where Schall is told to send word that the review queue has a question. What
// is pinned here is that the token never comes back out, and that switching the
// feature on without an address is refused rather than stored.

type notificationStore struct {
	*fakeStore
	settings db.NotificationSettingsRow
	readErr  error
	saved    db.SaveNotificationSettingsParams
}

func (store *notificationStore) NotificationSettings(
	context.Context,
) (db.NotificationSettingsRow, error) {
	return store.settings, store.readErr
}

func (store *notificationStore) SaveNotificationSettings(
	_ context.Context, params db.SaveNotificationSettingsParams,
) (db.NotificationSettingsRow, error) {
	store.saved = params
	store.settings = db.NotificationSettingsRow{
		Kind: params.Kind, Endpoint: params.Endpoint, Enabled: params.Enabled,
		ConnectionStatus: "unknown",
	}
	if params.Token != "" {
		store.settings.Token = pgtype.Text{String: params.Token, Valid: true}
	}
	return store.settings, nil
}

type fakeSender struct {
	err  error
	sent []notify.Message
}

func (sender *fakeSender) Send(_ context.Context, message notify.Message) error {
	sender.sent = append(sender.sent, message)
	return sender.err
}

func notificationHandler(store *notificationStore, options ...Option) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), options...)
}

func call(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
	return response
}

// An installation that has never opened this section is shown the row it would
// have: ntfy, no address, switched off.
func TestUnconfiguredNotificationsAreOff(t *testing.T) {
	store := &notificationStore{fakeStore: &fakeStore{}, readErr: pgx.ErrNoRows}
	handler := notificationHandler(store)

	response := call(handler, http.MethodGet, "/api/v1/settings/notifications", "")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var answer notificationSettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Configured || answer.Enabled || answer.Endpoint != "" {
		t.Fatalf("answer = %+v; want an installation that sends nothing", answer)
	}
	if answer.Kind != db.NotifyNtfy {
		t.Fatalf("kind = %q", answer.Kind)
	}
}

// The token is never returned. A caller learns only that one is stored.
func TestTheTokenIsNeverReturned(t *testing.T) {
	store := &notificationStore{
		fakeStore: &fakeStore{},
		settings: db.NotificationSettingsRow{
			Kind: db.NotifyNtfy, Endpoint: "https://ntfy.sh/topic", Enabled: true,
			Token: pgtype.Text{String: "tk_secret", Valid: true},
		},
	}
	handler := notificationHandler(store)

	response := call(handler, http.MethodGet, "/api/v1/settings/notifications", "")

	if strings.Contains(response.Body.String(), "tk_secret") {
		t.Fatalf("the token came back out: %s", response.Body)
	}
	var answer notificationSettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if !answer.TokenSet {
		t.Fatal("a stored token was not reported as stored")
	}
}

// Switched on with nowhere to send to is a setting that cannot do what it says.
func TestSwitchingNotificationsOnWithNoAddressIsRefused(t *testing.T) {
	store := &notificationStore{fakeStore: &fakeStore{}, readErr: pgx.ErrNoRows}
	handler := notificationHandler(store)

	response := call(handler, http.MethodPut, "/api/v1/settings/notifications",
		`{"kind":"ntfy","endpoint":"","token":"","clearToken":false,"enabled":true}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.Kind != "" {
		t.Fatal("a setting that cannot send was stored")
	}
}

// Off with no address is the row every installation starts with, so saving it
// is allowed.
func TestSwitchingNotificationsOffWithNoAddressIsAllowed(t *testing.T) {
	store := &notificationStore{fakeStore: &fakeStore{}, readErr: pgx.ErrNoRows}
	handler := notificationHandler(store)

	response := call(handler, http.MethodPut, "/api/v1/settings/notifications",
		`{"kind":"ntfy","endpoint":"","token":"","clearToken":false,"enabled":false}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestAnAddressThatIsNotAWebAddressIsRefused(t *testing.T) {
	for _, address := range []string{"ntfy.sh/topic", "ftp://ntfy.sh/topic", "https://"} {
		t.Run(address, func(t *testing.T) {
			store := &notificationStore{fakeStore: &fakeStore{}, readErr: pgx.ErrNoRows}
			handler := notificationHandler(store)

			response := call(handler, http.MethodPut, "/api/v1/settings/notifications",
				`{"kind":"ntfy","endpoint":"`+address+`","token":"","clearToken":false,"enabled":true}`)

			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d; body = %s", response.Code, response.Body)
			}
		})
	}
}

// A test sends a real message, and it says it is a test: somebody pressing it is
// waiting for it, and one that looked like a real question would send them
// looking for a question that is not there.
func TestTheTestButtonSendsAMessageThatSaysItIsATest(t *testing.T) {
	store := &notificationStore{
		fakeStore: &fakeStore{},
		settings: db.NotificationSettingsRow{
			Kind: db.NotifyNtfy, Endpoint: "https://ntfy.sh/topic", Enabled: true,
		},
	}
	sender := &fakeSender{}
	handler := notificationHandler(store, WithNotifier(sender))

	response := call(handler, http.MethodPost, "/api/v1/settings/notifications/test", "")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d messages", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0].Body, "test message") {
		t.Fatalf("body = %q", sender.sent[0].Body)
	}
}

// The endpoint's own answer is passed on. The address is the user's and so is
// the server at the end of it; what it said is the only thing that tells them
// which of the two is wrong.
func TestATestThatFailedSaysWhatTheEndpointSaid(t *testing.T) {
	store := &notificationStore{
		fakeStore: &fakeStore{},
		settings: db.NotificationSettingsRow{
			Kind: db.NotifyNtfy, Endpoint: "https://ntfy.sh/topic", Enabled: true,
		},
	}
	sender := &fakeSender{err: errors.New("send to https://ntfy.sh/topic: HTTP 403: forbidden")}
	handler := notificationHandler(store, WithNotifier(sender))

	response := call(handler, http.MethodPost, "/api/v1/settings/notifications/test", "")

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "403") {
		t.Fatalf("body = %s; want the endpoint's own answer in it", response.Body)
	}
}

// Testing with nowhere to send to says what to do rather than reporting a
// failure the reader can only press retry on.
func TestTestingWithNoAddressSaysWhatToDo(t *testing.T) {
	store := &notificationStore{fakeStore: &fakeStore{}, readErr: pgx.ErrNoRows}
	sender := &fakeSender{err: notify.ErrNotConfigured}
	handler := notificationHandler(store, WithNotifier(sender))

	response := call(handler, http.MethodPost, "/api/v1/settings/notifications/test", "")

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "Enter the address") {
		t.Fatalf("body = %s; want a sentence naming what to do", response.Body)
	}
}
