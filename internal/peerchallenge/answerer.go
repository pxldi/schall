package peerchallenge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/slskd"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

const (
	// checkInterval is how often private chat is read while a peer still has
	// something of ours open. Reading is one call to slskd and nothing reaches
	// the Soulseek network, but a peer's plugin sends its challenge once per
	// request and there is no hurry to find it.
	checkInterval = time.Minute

	// settleBeforeRetry is the pause between sending the word and asking for
	// the files again. The peer's plugin has to receive the message and add us
	// to its whitelist before a new request is judged, and a request that
	// arrives first is refused exactly as the last one was.
	settleBeforeRetry = 30 * time.Second

	// dailyReplyCap is the most messages Schall will send peers in a day,
	// however many peers ask. The Soulseek server bans this account for
	// flooding private chat, and a parser that started reading challenges into
	// ordinary sentences would be a chat flood with a rule behind it. Twenty is
	// well above what the peers seen so far ask for: six challenge, and each
	// asks once per request.
	dailyReplyCap = 20

	// answerWindow is how long one answer stands for. A peer that has been sent
	// its word within the window is not sent it again, whatever it repeats:
	// the peers that challenge re-send the same message on every request.
	answerWindow = 24 * time.Hour

	// failedWindow is how far back a failed request is still worth answering
	// for. A request that failed a week ago failed on something else.
	failedWindow = 24 * time.Hour

	// maxReplyLength bounds what a person may send by hand. It is one chat
	// line, because that is what a challenge asks for.
	maxReplyLength = 500
)

// Outcomes recorded against a challenge. They are the schema's own vocabulary,
// checked by the constraint in migration 00084.
const (
	OutcomeSent       = "sent"
	OutcomeRequestNew = "requested_new"
	OutcomeByPerson   = "answered_by_person"
	OutcomeUnanswered = "unanswered"
)

const (
	slskdProviderName = slskd.Name
	serverUsername    = slskd.ServerUsername
)

// ErrNoQuestion reports a peer that has not asked anything nobody could read.
// Sending a stranger a message it did not ask for is not something a button
// does by accident.
var ErrNoQuestion = errors.New("this peer has no unread question")

// Chat is the part of a provider that reads and writes private messages. Only
// slskd has one, and a provider without it simply has no challenges to answer.
type Chat interface {
	Conversations(context.Context) ([]slskd.Conversation, error)
	Conversation(context.Context, string) (slskd.Conversation, error)
	SendPrivateMessage(ctx context.Context, username, message string) error
	AcknowledgeMessages(ctx context.Context, username string, ids []int64) error
}

// Store is what the answerer keeps and asks between passes. Every rule that
// bounds what is sent is a question to this store, so a restart cannot forget
// what was already answered.
type Store interface {
	PeersAwaitingTransfers(context.Context, db.PeersAwaitingTransfersParams) ([]db.PeersAwaitingTransfersRow, error)
	PeerChallengeAnswered(context.Context, db.PeerChallengeAnsweredParams) (bool, error)
	PeerChallengeSeen(context.Context, db.PeerChallengeSeenParams) (bool, error)
	PeerRepliesSince(context.Context, pgtype.Timestamptz) (int64, error)
	RecordPeerChallenge(context.Context, db.RecordPeerChallengeParams) (db.PeerChallenge, error)
	UnansweredPeerChallenges(context.Context, []string) ([]db.UnansweredPeerChallengesRow, error)
	ForgetPeerQuestions(context.Context, string) error
	FailedDownloadRequestsForPeer(context.Context, db.FailedDownloadRequestsForPeerParams) ([]uuid.UUID, error)
	SlskdSettings(context.Context) (db.SlskdSettingsRow, error)
}

// Retrier asks a peer for the files it refused. It is the same retry a person
// presses on the Downloads page: the same request, the same folder, and only
// the transfers that failed.
type Retrier interface {
	Retry(ctx context.Context, requestID uuid.UUID, paths []string) (db.DownloadRequestRow, error)
}

// Service answers the human checks peers send by private message.
//
// It never decides anything about music. A word sent here changes whether a
// peer will send a file at all; the file that arrives afterwards is proved by
// exactly what proved every other file.
type Service struct {
	store     Store
	providers sources.Builder
	retries   Retrier
	logger    zerolog.Logger
	now       func() time.Time
	// settle is how long to wait between answering and asking again. A field so
	// tests do not wait half a minute.
	settle time.Duration
}

func NewService(store Store, providers sources.Builder, retries Retrier, logger zerolog.Logger) *Service {
	return &Service{
		store:     store,
		providers: providers,
		retries:   retries,
		logger:    logger,
		now:       time.Now,
		settle:    settleBeforeRetry,
	}
}

// Interval is how long the worker waits before reading private chat again.
func (service *Service) Interval() time.Duration { return checkInterval }

// Answer reads the private chat of every peer with something of ours open,
// sends back the words they ask for, and asks again for what they refused. It
// reports whether anything is still open, so the worker stops looking once
// nothing is.
func (service *Service) Answer(ctx context.Context) (bool, error) {
	now := service.now()
	peers, err := service.store.PeersAwaitingTransfers(ctx, db.PeersAwaitingTransfersParams{
		Provider: slskdProviderName,
		Since:    stamp(now.Add(-failedWindow)),
	})
	if err != nil {
		return false, fmt.Errorf("list the peers with transfers waiting: %w", err)
	}
	if len(peers) == 0 {
		return false, nil
	}

	open := false
	waiting := make(map[string]bool, len(peers))
	for _, peer := range peers {
		waiting[peer.SourceUsername] = true
		if peer.Open {
			open = true
		}
	}

	chat, err := service.chat(ctx)
	if errors.Is(err, sources.ErrNotConfigured) {
		return false, nil
	}
	if err != nil {
		return open, err
	}

	conversations, err := chat.Conversations(ctx)
	if err != nil {
		return open, fmt.Errorf("read the peers with private messages: %w", err)
	}

	answered := make([]string, 0, len(conversations))
	for _, conversation := range conversations {
		if conversation.Unacknowledged == 0 || !waiting[conversation.Username] {
			continue
		}
		// The server's own announcements are not a peer's question, and its ban
		// notice says in its own words not to answer it.
		if conversation.Username == serverUsername {
			continue
		}
		sent, err := service.answerPeer(ctx, chat, conversation.Username, now)
		if sent {
			// A word that went out counts, whatever failed after it. Marking
			// the messages read is the step most likely to fail, because the
			// route it uses is the one nobody has confirmed against a running
			// slskd; treating that as "nothing was answered" would leave the
			// peer's files unasked for good, since the record of the word stops
			// it ever being sent again.
			answered = append(answered, conversation.Username)
		}
		if err != nil {
			// One peer that cannot be read or answered must not stop the rest.
			service.logger.Warn().Err(err).Str("username", conversation.Username).
				Msg("could not answer a peer's question")
			continue
		}
	}

	if len(answered) > 0 {
		service.retryAnswered(ctx, answered, now)
	}
	return open, nil
}

// answerPeer reads one peer's unread messages and answers what it can. It
// reports whether anything was sent, which is what earns the peer a retry.
func (service *Service) answerPeer(
	ctx context.Context, chat Chat, username string, now time.Time,
) (bool, error) {
	conversation, err := chat.Conversation(ctx, username)
	if err != nil {
		return false, fmt.Errorf("read the private chat with %s: %w", username, err)
	}

	sent, read := false, make([]int64, 0, len(conversation.Messages))
	for _, message := range conversation.Messages {
		if !message.Incoming || message.Acknowledged {
			continue
		}
		read = append(read, message.ID)
		answeredOne, err := service.answerMessage(ctx, chat, username, message.Text, now)
		if err != nil {
			return sent, err
		}
		sent = sent || answeredOne
	}

	// Marking them read is what stops the same message being considered again
	// on the next pass. It happens whether or not anything could be read from
	// them: an unreadable message is recorded and shown to a person instead.
	if err := chat.AcknowledgeMessages(ctx, username, read); err != nil {
		return sent, fmt.Errorf("mark %s's messages read: %w", username, err)
	}
	return sent, nil
}

// answerMessage decides what one message deserves: the word it asks for, a
// request for a new challenge, or nothing at all and a row for a person to
// read.
func (service *Service) answerMessage(
	ctx context.Context, chat Chat, username, text string, now time.Time,
) (bool, error) {
	word, ok := ChallengeWord(text)
	if !ok && Expired(text) {
		// The bot that sets a deadline says what to do when it passes. This is
		// the one command Schall sends, and it asks for a challenge it can then
		// answer with a word.
		word = RequestNew
	}
	if word == "" {
		if Informational(text) {
			// A peer telling us something is not a peer asking for something.
			// Showing its notes as questions put a reply box on the page after
			// every word that worked.
			return false, nil
		}
		return false, service.recordUnanswered(ctx, username, text)
	}

	answered, err := service.store.PeerChallengeAnswered(ctx, db.PeerChallengeAnsweredParams{
		Username:  username,
		Challenge: text,
		Since:     stamp(now.Add(-answerWindow)),
	})
	if err != nil {
		return false, fmt.Errorf("check what %s has already been sent: %w", username, err)
	}
	if answered {
		return false, nil
	}

	replies, err := service.store.PeerRepliesSince(ctx, stamp(now.Add(-answerWindow)))
	if err != nil {
		return false, fmt.Errorf("count today's messages to peers: %w", err)
	}
	if replies >= dailyReplyCap {
		service.logger.Warn().Str("username", username).Int64("replies", replies).
			Msg("reached the daily cap on messages to peers")
		return false, nil
	}

	if err := chat.SendPrivateMessage(ctx, username, word); err != nil {
		return false, fmt.Errorf("send %s the word it asked for: %w", username, err)
	}

	outcome := OutcomeSent
	if word == RequestNew {
		outcome = OutcomeRequestNew
	}
	if _, err := service.store.RecordPeerChallenge(ctx, db.RecordPeerChallengeParams{
		Username:  username,
		Challenge: text,
		Reply:     pgtype.Text{String: word, Valid: true},
		SentAt:    stamp(now),
		Outcome:   outcome,
	}); err != nil {
		// The message is gone whatever happens here. Saying so is what stops it
		// being sent again on the next pass.
		return true, fmt.Errorf("record the word sent to %s: %w", username, err)
	}
	service.logger.Info().Str("username", username).Str("word", word).
		Msg("answered a peer's human check")
	// A request for a new challenge is not an answer, and the files stay
	// refused until the word that follows it is sent.
	return outcome == OutcomeSent, nil
}

// recordUnanswered keeps the last message from this peer that nobody here could
// read, and only that one. The peers that challenge repeat themselves on every
// request, and one of them numbers its messages with a queue length that is
// different every time — kept per distinct text, that peer grew a new line on
// the Downloads page every few minutes.
func (service *Service) recordUnanswered(ctx context.Context, username, text string) error {
	seen, err := service.store.PeerChallengeSeen(ctx, db.PeerChallengeSeenParams{
		Username:  username,
		Challenge: text,
	})
	if err != nil {
		return fmt.Errorf("check whether %s has said this before: %w", username, err)
	}
	if seen {
		return nil
	}
	if err := service.store.ForgetPeerQuestions(ctx, username); err != nil {
		return fmt.Errorf("drop what %s said before: %w", username, err)
	}
	_, err = service.store.RecordPeerChallenge(ctx, db.RecordPeerChallengeParams{
		Username:  username,
		Challenge: text,
		Outcome:   OutcomeUnanswered,
	})
	if err != nil {
		return fmt.Errorf("record what %s asked: %w", username, err)
	}
	return nil
}

// retryAnswered asks the peers that were just answered for the files they
// refused. The pause is what the peer's plugin needs to act on the message
// before it judges another request.
func (service *Service) retryAnswered(ctx context.Context, usernames []string, now time.Time) {
	if service.retries == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(service.settle):
	}
	for _, username := range usernames {
		service.retryPeer(ctx, username, now)
	}
}

func (service *Service) retryPeer(ctx context.Context, username string, now time.Time) {
	if service.retries == nil {
		return
	}
	requests, err := service.store.FailedDownloadRequestsForPeer(ctx, db.FailedDownloadRequestsForPeerParams{
		Provider: slskdProviderName,
		Username: username,
		Since:    stamp(now.Add(-failedWindow)),
	})
	if err != nil {
		service.logger.Error().Err(err).Str("username", username).
			Msg("list the requests a peer refused")
		return
	}
	for _, requestID := range requests {
		if _, err := service.retries.Retry(ctx, requestID, nil); err != nil {
			// A request with nothing left to retry is the ordinary case once a
			// peer has been answered twice, and not worth an error.
			service.logger.Debug().Err(err).Str("request_id", requestID.String()).
				Str("username", username).Msg("could not ask a peer again")
			continue
		}
		service.logger.Info().Str("request_id", requestID.String()).Str("username", username).
			Msg("asked a peer again after answering its human check")
	}
}

// Reply sends what a person typed to the peer that asked, and asks that peer
// for the files it refused. Nothing is parsed here: a person read the question
// and decided what the answer is.
func (service *Service) Reply(ctx context.Context, username, message string) error {
	if message == "" {
		return errors.New("a reply is required")
	}
	if len([]rune(message)) > maxReplyLength {
		return fmt.Errorf("a reply of %d characters is longer than one chat line", len([]rune(message)))
	}
	if username == serverUsername {
		return errors.New("the Soulseek server is not a peer to answer")
	}

	questions, err := service.store.UnansweredPeerChallenges(ctx, []string{username})
	if err != nil {
		return fmt.Errorf("read what %s asked: %w", username, err)
	}
	if len(questions) == 0 {
		return ErrNoQuestion
	}

	chat, err := service.chat(ctx)
	if err != nil {
		return err
	}
	if err := chat.SendPrivateMessage(ctx, username, message); err != nil {
		return fmt.Errorf("send %s the reply: %w", username, err)
	}

	now := service.now()
	if _, err := service.store.RecordPeerChallenge(ctx, db.RecordPeerChallengeParams{
		Username:  username,
		Challenge: questions[0].Challenge,
		Reply:     pgtype.Text{String: message, Valid: true},
		SentAt:    stamp(now),
		Outcome:   OutcomeByPerson,
	}); err != nil {
		return fmt.Errorf("record the reply sent to %s: %w", username, err)
	}
	service.logger.Info().Str("username", username).Str("word", message).
		Msg("sent a peer the reply a person typed")

	service.retryPeer(ctx, username, now)
	return nil
}

// Questions reports the last thing each of these peers said that nobody here
// could read, for the Downloads page to show beside the request it is holding
// up.
func (service *Service) Questions(ctx context.Context, usernames []string) (map[string]db.UnansweredPeerChallengesRow, error) {
	questions := make(map[string]db.UnansweredPeerChallengesRow)
	if len(usernames) == 0 {
		return questions, nil
	}
	rows, err := service.store.UnansweredPeerChallenges(ctx, usernames)
	if err != nil {
		return nil, fmt.Errorf("read what these peers asked: %w", err)
	}
	for _, row := range rows {
		questions[row.Username] = row
	}
	return questions, nil
}

// chat builds the configured provider and reports whether it can hold a
// conversation at all.
func (service *Service) chat(ctx context.Context) (Chat, error) {
	if service.providers == nil {
		return nil, sources.ErrNotConfigured
	}
	row, err := service.store.SlskdSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, sources.ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	if !row.Enabled {
		return nil, sources.ErrNotConfigured
	}
	provider, err := service.providers.Build(sources.Settings{
		BaseURL:       row.BaseURL,
		APIKey:        row.APIKey,
		SearchTimeout: time.Duration(row.SearchTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	chat, ok := provider.(Chat)
	if !ok {
		return nil, sources.ErrNotConfigured
	}
	return chat, nil
}

func stamp(at time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: at, Valid: true}
}
