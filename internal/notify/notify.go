// Package notify tells the user that the review queue has a question.
//
// The review queue is the one screen in Schall where a person answers something
// no rule could: which of two copies of a recording to keep, or what an
// unidentified file actually is. Everything else runs on its own. The queue is
// the deliberate exception, and the product spec is blunt about why its speed
// matters: a queue nobody empties is eventually emptied by somebody accepting
// everything in it, and the promise that Schall never guesses dies there while
// the code still looks like it keeps it.
//
// A queue can only be answered quickly if somebody knows there is anything in
// it, and until now that was discovered by opening the app. This sends one
// message when the number of waiting questions goes up.
//
// Two events and no others. A fetched copy of a wanted recording that nothing
// could identify, held for an ear rather than imported or thrown away; and a
// whole-release import that stopped and asked. Both are the zero-false-positive
// machinery waiting on a human. A release the follow feed found, a job that
// failed, a transfer that finished — those are status, they are on their own
// screens, and a notification about them is a notification nobody reads.
//
// Nothing here can fail anything. A notification that could not be delivered is
// logged and forgotten: the work that produced the question is already done, the
// question is already in the queue, and chasing a courtesy at the cost of the
// job that earned it would be the wrong trade every time.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

const (
	// sendTimeout bounds one delivery. Nobody is waiting on it and the job that
	// triggered it is already finished, so this is short enough that a server
	// which has gone away cannot hold a worker goroutine for long.
	sendTimeout = 10 * time.Second
	// maxBodyBytes bounds what is read back from the endpoint. Only the first
	// line of it is ever shown, as the reason a delivery failed.
	maxBodyBytes = 8 << 10
)

// ErrNotConfigured reports that nothing has been told where to send word. It is
// an answer rather than a failure: an installation that never opens this setting
// is the ordinary one.
var ErrNotConfigured = errors.New("no notification endpoint is configured")

// Store is the persistence this needs: where to send, and how many questions
// are waiting.
type Store interface {
	NotificationSettings(context.Context) (db.NotificationSettingsRow, error)
	RecordNotificationDelivery(ctx context.Context, status, failure string) error
	// WaitingQuestions counts what the review queue is holding: copies nothing
	// could identify, and imports that stopped and asked.
	WaitingQuestions(context.Context) (db.WaitingQuestionsRow, error)
}

// Notifier sends word, and remembers what it last said so it does not say it
// twice.
type Notifier struct {
	store      Store
	httpClient *http.Client
	logger     zerolog.Logger

	// seen is how many questions were waiting when this process last looked.
	//
	// It is held in memory rather than written down, and that is a decision with
	// a cost either way. In memory, a restart forgets — and the first look after
	// one sets the baseline silently, so a question that appeared while Schall
	// was down is never announced. Written down, a restart would re-announce
	// every question already waiting, which is the same message arriving again
	// about nothing new. The second is worse: a notification that repeats itself
	// is one people turn off, and turning it off costs every future question.
	mu    sync.Mutex
	seen  counts
	known bool
}

type counts struct {
	heldCopies  int64
	pausedItems int64
}

func New(store Store, logger zerolog.Logger) *Notifier {
	return &Notifier{
		store:      store,
		httpClient: &http.Client{Timeout: sendTimeout},
		logger:     logger,
	}
}

// Check looks at what the queue is holding and sends one message if it has grown.
//
// One message per look, not one per question: a playlist import that holds ten
// copies is called after it finishes and sends a single message naming ten. That
// is the whole of the batching, and it is why this is called from the job that
// finished rather than from the place each copy is held.
//
// It is called after work that can produce a question, and it returns silently
// when the setting is off — nothing is read, nothing is sent, and no code in
// this package runs beyond the first two lines.
func (notifier *Notifier) Check(ctx context.Context) {
	settings, err := notifier.store.NotificationSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !settings.Enabled) {
		return
	}
	if err != nil {
		notifier.logger.Debug().Err(err).Msg("read notification settings")
		return
	}

	waiting, err := notifier.store.WaitingQuestions(ctx)
	if err != nil {
		notifier.logger.Debug().Err(err).Msg("count the questions waiting for a person")
		return
	}
	now := counts{heldCopies: waiting.HeldCopies, pausedItems: waiting.PausedImports}

	notifier.mu.Lock()
	first := !notifier.known
	before := notifier.seen
	notifier.seen = now
	notifier.known = true
	notifier.mu.Unlock()

	if first {
		// The first look after a start. What is waiting now was already waiting
		// before this process existed, so it is remembered and not announced.
		return
	}
	if now.heldCopies <= before.heldCopies && now.pausedItems <= before.pausedItems {
		return
	}

	notifier.deliver(ctx, settings, Message{
		Title: "Schall needs a decision",
		Body:  sentence(now),
	})
}

// Message is what gets sent: a line for the notification's own heading, and a
// line for its body.
//
// Both are interface text rather than documentation. Somebody reads them on a
// lock screen in the middle of something else, so they name the thing, say what
// happened to it, and stop.
// reviewLink opens the Review tab of the phone app (app/app/review/index.tsx).
// The message names counts, never one question, so the list is the honest
// target.
const reviewLink = "schall://review"

type Message struct {
	Title string
	Body  string
}

// sentence says what is waiting, in the fewest words that are still true.
func sentence(waiting counts) string {
	parts := make([]string, 0, 2)
	if waiting.heldCopies > 0 {
		parts = append(parts, plural(waiting.heldCopies, "copy nobody could identify",
			"copies nobody could identify"))
	}
	if waiting.pausedItems > 0 {
		parts = append(parts, plural(waiting.pausedItems, "import waiting for a decision",
			"imports waiting for a decision"))
	}
	if len(parts) == 0 {
		return "The review queue has something waiting."
	}
	return strings.Join(parts, " and ") + ". Open Review to answer."
}

func plural(count int64, one, many string) string {
	if count == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(count, 10) + " " + many
}

// Send delivers one message to the configured endpoint and records what it came
// to. It is what the Settings page's test button presses.
func (notifier *Notifier) Send(ctx context.Context, message Message) error {
	settings, err := notifier.store.NotificationSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotConfigured
	}
	if err != nil {
		return fmt.Errorf("read notification settings: %w", err)
	}
	if strings.TrimSpace(settings.Endpoint) == "" {
		return ErrNotConfigured
	}
	return notifier.post(ctx, settings, message)
}

// deliver is Send for a message nobody asked for, so a failure is written down
// and never returned: the job that produced the question has already finished
// and there is nothing here that failing would help.
func (notifier *Notifier) deliver(
	ctx context.Context, settings db.NotificationSettingsRow, message Message,
) {
	if err := notifier.post(ctx, settings, message); err != nil {
		notifier.logger.Warn().Err(err).Msg("tell the user the review queue has a question")
	}
}

func (notifier *Notifier) post(
	ctx context.Context, settings db.NotificationSettingsRow, message Message,
) error {
	bounded, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	request, err := notifier.build(bounded, settings, message)
	if err != nil {
		notifier.record(ctx, "failed", err.Error())
		return err
	}

	response, err := notifier.httpClient.Do(request)
	if err != nil {
		failure := fmt.Errorf("send to %s: %w", settings.Endpoint, err)
		notifier.record(ctx, "failed", failure.Error())
		return failure
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBodyBytes))
		_ = response.Body.Close()
	}()

	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
		failure := fmt.Errorf("send to %s: HTTP %d: %s",
			settings.Endpoint, response.StatusCode, firstLine(string(body)))
		notifier.record(ctx, "failed", failure.Error())
		return failure
	}
	notifier.record(ctx, "ok", "")
	return nil
}

// build writes the request in the shape the far end reads.
//
// ntfy takes the message as a plain text body and reads its heading out of a
// header; a webhook takes a JSON object, because anything else at the other end
// has to be able to tell the two lines apart.
func (notifier *Notifier) build(
	ctx context.Context, settings db.NotificationSettingsRow, message Message,
) (*http.Request, error) {
	var request *http.Request
	var err error

	switch settings.Kind {
	case db.NotifyWebhook:
		body, marshalErr := json.Marshal(map[string]string{
			"title": message.Title,
			"body":  message.Body,
		})
		if marshalErr != nil {
			return nil, fmt.Errorf("write the notification: %w", marshalErr)
		}
		request, err = http.NewRequestWithContext(ctx, http.MethodPost,
			settings.Endpoint, bytes.NewReader(body))
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
		}
	default:
		request, err = http.NewRequestWithContext(ctx, http.MethodPost,
			settings.Endpoint, strings.NewReader(message.Body))
		if err == nil {
			request.Header.Set("Content-Type", "text/plain; charset=utf-8")
			request.Header.Set("Title", message.Title)
			// Tags are what ntfy draws its icon from. One that says a person is
			// wanted, rather than the default bell that says anything at all.
			request.Header.Set("Tags", "mag")
			// Click is what the ntfy app opens when the notification is tapped.
			// The phone app registers the schall scheme; a phone without it
			// opens nothing, which is what a tap did before.
			request.Header.Set("Click", reviewLink)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("build the notification request: %w", err)
	}
	if token := strings.TrimSpace(settings.Token.String); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request, nil
}

// record writes down what the last delivery came to. It never fails anything
// either: the message was already sent, or already lost.
func (notifier *Notifier) record(ctx context.Context, status, failure string) {
	if err := notifier.store.RecordNotificationDelivery(ctx, status, failure); err != nil {
		notifier.logger.Debug().Err(err).Msg("record what the notification came to")
	}
}

func firstLine(body string) string {
	trimmed := strings.TrimSpace(body)
	if index := strings.IndexAny(trimmed, "\r\n"); index >= 0 {
		trimmed = trimmed[:index]
	}
	if len(trimmed) > 200 {
		trimmed = trimmed[:200]
	}
	if trimmed == "" {
		return "the endpoint gave no reason"
	}
	return trimmed
}
