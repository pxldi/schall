package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The word sent when the review queue has a question. What is pinned here is
// what it must never do: send anything while it is switched off, send the same
// question twice, or fail the work that produced it.

type fakeStore struct {
	mu         sync.Mutex
	settings   db.NotificationSettingsRow
	settingErr error
	waiting    db.WaitingQuestionsRow
	waitingErr error
	recorded   []string
}

func (store *fakeStore) NotificationSettings(context.Context) (db.NotificationSettingsRow, error) {
	return store.settings, store.settingErr
}

func (store *fakeStore) RecordNotificationDelivery(_ context.Context, status, _ string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.recorded = append(store.recorded, status)
	return nil
}

func (store *fakeStore) WaitingQuestions(context.Context) (db.WaitingQuestionsRow, error) {
	return store.waiting, store.waitingErr
}

type sent struct {
	mu     sync.Mutex
	bodies []string
	titles []string
	auths  []string
	clicks []string
}

func (record *sent) count() int {
	record.mu.Lock()
	defer record.mu.Unlock()
	return len(record.bodies)
}

// endpoint stands in for ntfy or a webhook, and records what arrived.
func endpoint(t *testing.T, record *sent, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		record.mu.Lock()
		record.bodies = append(record.bodies, string(body))
		record.titles = append(record.titles, r.Header.Get("Title"))
		record.auths = append(record.auths, r.Header.Get("Authorization"))
		record.clicks = append(record.clicks, r.Header.Get("Click"))
		record.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestNothingIsSentWhileTheSettingIsOff(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: false},
		waiting:  db.WaitingQuestionsRow{HeldCopies: 3},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 9}
	notifier.Check(context.Background())

	if record.count() != 0 {
		t.Fatalf("sent %d messages while the setting was off", record.count())
	}
}

// The first look after a start is the baseline. What is waiting now was waiting
// before this process existed, and announcing it would repeat itself on every
// restart.
func TestTheFirstLookAfterAStartAnnouncesNothing(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
		waiting:  db.WaitingQuestionsRow{HeldCopies: 4, PausedImports: 2},
	}

	New(store, zerolog.Nop()).Check(context.Background())

	if record.count() != 0 {
		t.Fatalf("sent %d messages about questions that were already waiting", record.count())
	}
}

func TestOneMessageIsSentWhenAQuestionAppears(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 1}
	notifier.Check(context.Background())

	if record.count() != 1 {
		t.Fatalf("sent %d messages", record.count())
	}
	if !strings.Contains(record.bodies[0], "1 copy nobody could identify") {
		t.Fatalf("body = %q", record.bodies[0])
	}
	if record.titles[0] != "Schall needs a decision" {
		t.Fatalf("title = %q", record.titles[0])
	}
}

// Tapping the notification opens the phone app's Review tab.
func TestTheNtfyMessageOpensTheAppsReview(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 1}
	notifier.Check(context.Background())

	if record.count() != 1 || record.clicks[0] != "schall://review" {
		t.Fatalf("clicks = %q", record.clicks)
	}
}

// A pass that held ten copies is one bad night, not ten notifications.
func TestAPassThatHeldTenCopiesSendsOneMessage(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 10}
	notifier.Check(context.Background())

	if record.count() != 1 {
		t.Fatalf("sent %d messages for one pass", record.count())
	}
	if !strings.Contains(record.bodies[0], "10 copies nobody could identify") {
		t.Fatalf("body = %q", record.bodies[0])
	}
}

// A queue that has not grown is a queue nobody needs telling about again.
func TestAQueueThatHasNotGrownSaysNothingAgain(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 2}
	notifier.Check(context.Background())
	notifier.Check(context.Background())
	notifier.Check(context.Background())

	if record.count() != 1 {
		t.Fatalf("sent %d messages about the same two questions", record.count())
	}
}

// A queue somebody answered down and that then grows again is news.
func TestAQuestionAfterTheQueueWasAnsweredIsNews(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
		waiting:  db.WaitingQuestionsRow{HeldCopies: 2},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 0}
	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 1}
	notifier.Check(context.Background())

	if record.count() != 1 {
		t.Fatalf("sent %d messages", record.count())
	}
}

// An import that stopped and asked is the other event, and it is named as one.
func TestAPausedImportIsNamedInTheMessage(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{PausedImports: 1}
	notifier.Check(context.Background())

	if record.count() != 1 || !strings.Contains(record.bodies[0], "1 import waiting for a decision") {
		t.Fatalf("bodies = %v", record.bodies)
	}
}

// A webhook is sent a JSON object, because anything at the other end has to be
// able to tell the two lines apart.
func TestAWebhookIsSentJSON(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{
			Kind: db.NotifyWebhook, Endpoint: server.URL, Enabled: true,
		},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 1}
	notifier.Check(context.Background())

	var message map[string]string
	if err := json.Unmarshal([]byte(record.bodies[0]), &message); err != nil {
		t.Fatalf("body = %q: %v", record.bodies[0], err)
	}
	if message["title"] == "" || message["body"] == "" {
		t.Fatalf("message = %v", message)
	}
}

func TestATokenIsSentAsABearer(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, 0)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{
			Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true,
			Token: pgtype.Text{String: "tk_secret", Valid: true},
		},
	}

	if err := New(store, zerolog.Nop()).Send(context.Background(), Message{Title: "a", Body: "b"}); err != nil {
		t.Fatal(err)
	}

	if record.auths[0] != "Bearer tk_secret" {
		t.Fatalf("authorization = %q", record.auths[0])
	}
}

// A delivery that failed is written down and never returned to the caller that
// did not ask for it: the question is already in the queue.
func TestADeliveryThatFailedCostsNothingButALine(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, http.StatusInternalServerError)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
	}
	notifier := New(store, zerolog.Nop())

	notifier.Check(context.Background())
	store.waiting = db.WaitingQuestionsRow{HeldCopies: 1}
	notifier.Check(context.Background())

	if len(store.recorded) == 0 || store.recorded[len(store.recorded)-1] != "failed" {
		t.Fatalf("recorded = %v; want the failure written down", store.recorded)
	}
}

// The test button gets the failure back, because somebody is standing there
// waiting to find out whether the address works.
func TestTheTestButtonIsToldWhenTheAddressDoesNotWork(t *testing.T) {
	record := &sent{}
	server := endpoint(t, record, http.StatusUnauthorized)
	store := &fakeStore{
		settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy, Endpoint: server.URL, Enabled: true},
	}

	err := New(store, zerolog.Nop()).Send(context.Background(), Message{Title: "a", Body: "b"})

	if err == nil {
		t.Fatal("a refused delivery reported success")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v; want the endpoint's own answer in it", err)
	}
}

func TestSendingWithNoAddressIsAnAnswerRatherThanAFailure(t *testing.T) {
	store := &fakeStore{settings: db.NotificationSettingsRow{Kind: db.NotifyNtfy}}

	err := New(store, zerolog.Nop()).Send(context.Background(), Message{Title: "a", Body: "b"})

	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v; want ErrNotConfigured", err)
	}
}
