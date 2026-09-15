package peerchallenge

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/slskd"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

const proveIt = `To prove you are a human downloading these files, please type "open sesame" in this chat to be added to my whitelist.`

// A peer that will not send a file until a word is typed back gets the word,
// and then gets asked for the files it refused.
func TestAnswerSendsTheWordAPeerAsksForAndAsksAgain(t *testing.T) {
	requestID := uuid.New()
	store := &fakeStore{
		peers:  []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}},
		failed: map[string][]uuid.UUID{"PSXDupe": {requestID}},
	}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "PSXDupe", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"PSXDupe": {{ID: 7, Incoming: true, Text: proveIt}},
		},
	}
	retries := &fakeRetrier{}
	service := testService(store, chat, retries)

	open, err := service.Answer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !open {
		t.Error("a peer with an open request read as nothing waiting")
	}
	if len(chat.sent) != 1 || chat.sent[0].username != "PSXDupe" || chat.sent[0].message != "open sesame" {
		t.Fatalf("sent = %#v", chat.sent)
	}
	if len(store.recorded) != 1 || store.recorded[0].Outcome != OutcomeSent ||
		store.recorded[0].Reply.String != "open sesame" || store.recorded[0].Challenge != proveIt {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	// Marked read, or the same message is answered again on the next pass.
	if len(chat.acknowledged["PSXDupe"]) != 1 || chat.acknowledged["PSXDupe"][0] != 7 {
		t.Fatalf("acknowledged = %#v", chat.acknowledged)
	}
	if len(retries.requests) != 1 || retries.requests[0] != requestID {
		t.Fatalf("retried = %#v, want the request the peer refused", retries.requests)
	}
}

// Marking the messages read is the step most likely to fail: the route it uses
// is the one nobody has confirmed against a running slskd. The word has already
// gone out, and the record of it stops it ever being sent again, so a peer whose
// messages could not be marked read must still get its files asked for.
func TestAnswerAsksAgainEvenWhenTheMessagesCannotBeMarkedRead(t *testing.T) {
	requestID := uuid.New()
	store := &fakeStore{
		peers:  []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}},
		failed: map[string][]uuid.UUID{"PSXDupe": {requestID}},
	}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "PSXDupe", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"PSXDupe": {{ID: 7, Incoming: true, Text: proveIt}},
		},
		acknowledgeErr: errors.New("slskd did not recognise the API path"),
	}
	retries := &fakeRetrier{}
	service := testService(store, chat, retries)

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 1 {
		t.Fatalf("sent = %#v", chat.sent)
	}
	if len(retries.requests) != 1 || retries.requests[0] != requestID {
		t.Fatalf("retried = %#v, want the files asked for again anyway", retries.requests)
	}
}

// The bot that takes a word answers with a note, and that note counts a queue
// that is a different number every time. Shown as a question, it put a reply box
// on the page after every word that worked.
func TestAnswerShowsNoQuestionForAPeerConfirmingAWord(t *testing.T) {
	correct := "Correct. Downloads are unlocked for 7 days. Please retry any files that are " +
		"not already queued; your requested paths remain recorded. For permanent priority " +
		"access, send !friend. The upload queue is currently busy (300 users waiting). " +
		"Uploads are scheduled round-robin, so the displayed file position is not an exact " +
		"waiting position. Please keep your client online and allow time."
	store := &fakeStore{peers: []db.PeersAwaitingTransfersRow{{SourceUsername: "paradox1977", Open: true}}}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "paradox1977", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"paradox1977": {{ID: 21, Incoming: true, Text: correct}},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v", chat.sent)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded = %#v, want a note to leave no question behind", store.recorded)
	}
}

// One peer is one line on the Downloads page. What it said before goes when it
// says something new.
func TestAnswerKeepsOnlyTheLastThingAPeerSaid(t *testing.T) {
	store := &fakeStore{peers: []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}}}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "PSXDupe", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"PSXDupe": {{ID: 5, Incoming: true, Text: "something nobody here can read"}},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.forgotten) != 1 || store.forgotten[0] != "PSXDupe" {
		t.Fatalf("forgotten = %#v", store.forgotten)
	}
	if len(store.recorded) != 1 || store.recorded[0].Outcome != OutcomeUnanswered {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

// The Soulseek server's announcements are not a peer's question, and its ban
// notice says in its own words not to answer it.
func TestAnswerNeverWritesToTheServer(t *testing.T) {
	store := &fakeStore{peers: []db.PeersAwaitingTransfersRow{{SourceUsername: "server", Open: true}}}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "server", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"server": {{ID: 1, Incoming: true, Text: `please type "hello" in this chat`}},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v, want nothing sent to the server", chat.sent)
	}
}

// A peer nobody is waiting on is not somebody to write to, whatever it said.
func TestAnswerIgnoresAPeerWithNothingWaiting(t *testing.T) {
	store := &fakeStore{peers: []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}}}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "delightful", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"delightful": {{ID: 1, Incoming: true, Text: proveIt}},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v", chat.sent)
	}
}

// A message that is not a challenge is kept for a person to read, and nothing
// is sent. This is the whole rule: an unreadable message is a question, not a
// prompt to guess.
func TestAnswerKeepsWhatItCannotReadAndSendsNothing(t *testing.T) {
	prose := "Just in case you couldn't download, just type rockstar in lowercase " +
		"without any punctuation, it's a plugin that I use to stop bots from spamming me."
	store := &fakeStore{peers: []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}}}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "PSXDupe", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"PSXDupe": {{ID: 3, Incoming: true, Text: prose}},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v", chat.sent)
	}
	if len(store.recorded) != 1 || store.recorded[0].Outcome != OutcomeUnanswered ||
		store.recorded[0].Challenge != prose || store.recorded[0].Reply.Valid {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

// The peers that challenge repeat the same message on every request. One answer
// stands for a day: sending it every time is the chat flood the Soulseek server
// bans this account for.
func TestAnswerSendsNothingToAPeerAlreadyAnswered(t *testing.T) {
	store := &fakeStore{
		peers:    []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}},
		answered: true,
	}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "PSXDupe", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"PSXDupe": {{ID: 9, Incoming: true, Text: proveIt}},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v", chat.sent)
	}
	// Still marked read: an answered question is not a new one.
	if len(chat.acknowledged["PSXDupe"]) != 1 {
		t.Fatalf("acknowledged = %#v", chat.acknowledged)
	}
}

func TestAnswerStopsAtTheDailyCap(t *testing.T) {
	store := &fakeStore{
		peers:   []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}},
		replies: dailyReplyCap,
	}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "PSXDupe", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"PSXDupe": {{ID: 9, Incoming: true, Text: proveIt}},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v, want the cap to hold", chat.sent)
	}
}

// The bot that sets a deadline says what to do when it passes. Asking for a new
// challenge is not an answer, so nothing is retried until the word that follows
// it has been sent.
func TestAnswerAsksForANewChallengeWhenTheOldOneExpired(t *testing.T) {
	expired := "The download challenge expired. Request a file again for a new challenge."
	requestID := uuid.New()
	store := &fakeStore{
		peers:  []db.PeersAwaitingTransfersRow{{SourceUsername: "paradox1977", Open: true}},
		failed: map[string][]uuid.UUID{"paradox1977": {requestID}},
	}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "paradox1977", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"paradox1977": {{ID: 11, Incoming: true, Text: expired}},
		},
	}
	retries := &fakeRetrier{}
	service := testService(store, chat, retries)

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 1 || chat.sent[0].message != RequestNew {
		t.Fatalf("sent = %#v", chat.sent)
	}
	if len(store.recorded) != 1 || store.recorded[0].Outcome != OutcomeRequestNew {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	if len(retries.requests) != 0 {
		t.Fatalf("retried = %#v, want nothing until the word is sent", retries.requests)
	}
}

// What Schall already sent, and what it has already read, are not questions.
func TestAnswerReadsOnlyUnreadIncomingMessages(t *testing.T) {
	store := &fakeStore{peers: []db.PeersAwaitingTransfersRow{{SourceUsername: "PSXDupe", Open: true}}}
	chat := &fakeChat{
		list: []slskd.Conversation{{Username: "PSXDupe", Unacknowledged: 1}},
		messages: map[string][]slskd.Message{
			"PSXDupe": {
				{ID: 1, Incoming: true, Text: proveIt, Acknowledged: true},
				{ID: 0, Incoming: false, Text: proveIt},
			},
		},
	}
	service := testService(store, chat, &fakeRetrier{})

	if _, err := service.Answer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v", chat.sent)
	}
}

// Nothing open means nothing to answer, and the pass stops rescheduling.
func TestAnswerReadsNoChatWithNoPeersWaiting(t *testing.T) {
	chat := &fakeChat{}
	service := testService(&fakeStore{}, chat, &fakeRetrier{})

	open, err := service.Answer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if open {
		t.Error("no peers waiting read as something waiting")
	}
	if chat.listed {
		t.Error("private chat was read with nothing waiting on a peer")
	}
}

func TestReplySendsWhatAPersonTypedAndAsksAgain(t *testing.T) {
	requestID := uuid.New()
	store := &fakeStore{
		questions: []db.UnansweredPeerChallengesRow{{Username: "delightful", Challenge: "say > x < in this chat"}},
		failed:    map[string][]uuid.UUID{"delightful": {requestID}},
	}
	chat := &fakeChat{}
	retries := &fakeRetrier{}
	service := testService(store, chat, retries)

	if err := service.Reply(context.Background(), "delightful", "please daddy give it to me"); err != nil {
		t.Fatal(err)
	}
	if len(chat.sent) != 1 || chat.sent[0].message != "please daddy give it to me" {
		t.Fatalf("sent = %#v", chat.sent)
	}
	if len(store.recorded) != 1 || store.recorded[0].Outcome != OutcomeByPerson ||
		store.recorded[0].Challenge != "say > x < in this chat" {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	if len(retries.requests) != 1 || retries.requests[0] != requestID {
		t.Fatalf("retried = %#v", retries.requests)
	}
}

// The button exists because a peer asked something. A peer that asked nothing is
// a stranger being written to for no reason.
func TestReplyRefusesAPeerThatAskedNothing(t *testing.T) {
	chat := &fakeChat{}
	service := testService(&fakeStore{}, chat, &fakeRetrier{})

	err := service.Reply(context.Background(), "delightful", "hello")
	if !errors.Is(err, ErrNoQuestion) {
		t.Fatalf("error = %v, want ErrNoQuestion", err)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("sent = %#v", chat.sent)
	}
}

func testService(store *fakeStore, chat *fakeChat, retries Retrier) *Service {
	service := NewService(store, fakeBuilder{chat: chat}, retries, zerolog.New(io.Discard))
	// Tests do not wait half a minute for the peer's plugin to catch up.
	service.settle = 0
	return service
}

type sentMessage struct{ username, message string }

type fakeChat struct {
	list           []slskd.Conversation
	messages       map[string][]slskd.Message
	sent           []sentMessage
	acknowledged   map[string][]int64
	acknowledgeErr error
	listed         bool
}

func (chat *fakeChat) Name() string { return slskd.Name }

func (chat *fakeChat) CheckConnection(context.Context) (sources.Status, error) {
	return sources.Status{}, nil
}

func (chat *fakeChat) SearchBudget() time.Duration { return time.Second }

func (chat *fakeChat) Search(context.Context, sources.Query) ([]sources.Candidate, error) {
	return nil, nil
}

func (chat *fakeChat) Conversations(context.Context) ([]slskd.Conversation, error) {
	chat.listed = true
	return chat.list, nil
}

func (chat *fakeChat) Conversation(_ context.Context, username string) (slskd.Conversation, error) {
	return slskd.Conversation{Username: username, Messages: chat.messages[username]}, nil
}

func (chat *fakeChat) SendPrivateMessage(_ context.Context, username, message string) error {
	chat.sent = append(chat.sent, sentMessage{username: username, message: message})
	return nil
}

func (chat *fakeChat) AcknowledgeMessages(_ context.Context, username string, ids []int64) error {
	if chat.acknowledged == nil {
		chat.acknowledged = make(map[string][]int64)
	}
	chat.acknowledged[username] = append(chat.acknowledged[username], ids...)
	return chat.acknowledgeErr
}

type fakeBuilder struct{ chat *fakeChat }

func (builder fakeBuilder) Build(sources.Settings) (sources.Provider, error) {
	return builder.chat, nil
}

type fakeStore struct {
	peers     []db.PeersAwaitingTransfersRow
	questions []db.UnansweredPeerChallengesRow
	failed    map[string][]uuid.UUID
	answered  bool
	seen      bool
	replies   int64
	recorded  []db.RecordPeerChallengeParams
	forgotten []string
}

func (store *fakeStore) PeersAwaitingTransfers(
	context.Context, db.PeersAwaitingTransfersParams,
) ([]db.PeersAwaitingTransfersRow, error) {
	return store.peers, nil
}

func (store *fakeStore) PeerChallengeAnswered(
	context.Context, db.PeerChallengeAnsweredParams,
) (bool, error) {
	return store.answered, nil
}

func (store *fakeStore) PeerChallengeSeen(context.Context, db.PeerChallengeSeenParams) (bool, error) {
	return store.seen, nil
}

func (store *fakeStore) PeerRepliesSince(context.Context, pgtype.Timestamptz) (int64, error) {
	return store.replies, nil
}

func (store *fakeStore) RecordPeerChallenge(
	_ context.Context, params db.RecordPeerChallengeParams,
) (db.PeerChallenge, error) {
	store.recorded = append(store.recorded, params)
	return db.PeerChallenge{Username: params.Username, Challenge: params.Challenge}, nil
}

func (store *fakeStore) UnansweredPeerChallenges(
	context.Context, []string,
) ([]db.UnansweredPeerChallengesRow, error) {
	return store.questions, nil
}

func (store *fakeStore) ForgetPeerQuestions(_ context.Context, username string) error {
	store.forgotten = append(store.forgotten, username)
	return nil
}

func (store *fakeStore) FailedDownloadRequestsForPeer(
	_ context.Context, params db.FailedDownloadRequestsForPeerParams,
) ([]uuid.UUID, error) {
	return store.failed[params.Username], nil
}

func (store *fakeStore) SlskdSettings(context.Context) (db.SlskdSettingsRow, error) {
	return db.SlskdSettingsRow{Enabled: true, BaseURL: "http://slskd", APIKey: "key"}, nil
}

type fakeRetrier struct{ requests []uuid.UUID }

func (retrier *fakeRetrier) Retry(
	_ context.Context, requestID uuid.UUID, _ []string,
) (db.DownloadRequestRow, error) {
	retrier.requests = append(retrier.requests, requestID)
	return db.DownloadRequestRow{}, nil
}
