package slskd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxMessageLength bounds one private message. Soulseek itself takes more, but
// nothing Schall sends is longer than a challenge word, and a long message is a
// bug on the way to a chat flood the server bans this account for.
const maxMessageLength = 512

// ServerUsername is the Soulseek server's own name in the conversation list.
// Its messages are announcements, and its ban notice says in its own words not
// to answer it.
const ServerUsername = "server"

// Conversation is one peer's private chat. The list endpoint answers without
// messages, so Messages is empty there and filled in by Conversation.
type Conversation struct {
	Username       string
	Unacknowledged int
	Messages       []Message
}

// Message is one line of private chat. ID is slskd's own, and is what an
// acknowledgement names; slskd numbers only the messages it received, so a
// message Schall sent carries zero.
type Message struct {
	ID        int64
	Timestamp time.Time
	Incoming  bool
	Text      string
	// Acknowledged is slskd's record of whether this message has been marked
	// read. It is how a peer repeating the same challenge is told apart from
	// the copy of it that was already answered.
	Acknowledged bool
}

// Conversations lists the peers with private chat. Messages are not included:
// slskd answers the list without them, and reading one peer's messages is a
// second call.
func (client *Client) Conversations(ctx context.Context) ([]Conversation, error) {
	var payload []conversationResponse
	if err := client.get(ctx, "/conversations", nil, &payload); err != nil {
		return nil, client.describe(ctx, err)
	}
	conversations := make([]Conversation, 0, len(payload))
	for _, row := range payload {
		conversations = append(conversations, row.conversation())
	}
	return conversations, nil
}

// Conversation reads one peer's messages. A peer slskd has forgotten is
// reported as an empty conversation rather than as a failure, the way Transfers
// treats a forgotten peer.
func (client *Client) Conversation(ctx context.Context, username string) (Conversation, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return Conversation{}, errors.New("a peer username is required")
	}

	var payload conversationResponse
	err := client.get(ctx, "/conversations/"+url.PathEscape(username), nil, &payload)
	var httpError *HTTPError
	if errors.As(err, &httpError) && httpError.StatusCode == http.StatusNotFound {
		return Conversation{Username: username}, nil
	}
	if err != nil {
		return Conversation{}, client.describe(ctx, err)
	}
	conversation := payload.conversation()
	if conversation.Username == "" {
		conversation.Username = username
	}
	return conversation, nil
}

// SendPrivateMessage sends one line of private chat. slskd takes the message as
// a bare JSON string rather than as an object.
//
// The Soulseek server bans this account for flooding private chat, so what may
// be sent and how often is decided by the caller, not here.
func (client *Client) SendPrivateMessage(ctx context.Context, username, message string) error {
	username, message = strings.TrimSpace(username), strings.TrimSpace(message)
	if username == "" {
		return errors.New("a peer username is required")
	}
	if message == "" {
		return errors.New("a message is required")
	}
	if len(message) > maxMessageLength {
		return fmt.Errorf("a message of %d characters is longer than one chat line", len(message))
	}
	if strings.EqualFold(username, ServerUsername) {
		return errors.New("the Soulseek server is not a peer to answer")
	}
	if err := client.post(ctx, "/conversations/"+url.PathEscape(username), message, nil); err != nil {
		return client.describe(ctx, err)
	}
	return nil
}

// AcknowledgeMessages marks a peer's messages read, so the next pass can tell a
// challenge that has been dealt with from one that has just arrived.
//
// slskd 0.26 documents PUT /conversations/{username} as acknowledging the whole
// conversation. That was assumed rather than read from a running instance, so a
// build that does not have it — 404 or 405 — is handled by acknowledging each
// message at PUT /conversations/{username}/{id} instead.
func (client *Client) AcknowledgeMessages(ctx context.Context, username string, ids []int64) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("a peer username is required")
	}
	if len(ids) == 0 {
		return nil
	}

	err := client.acknowledge(ctx, "/conversations/"+url.PathEscape(username))
	if !routeMissing(err) {
		if err != nil {
			return client.describe(ctx, err)
		}
		return nil
	}

	for _, id := range ids {
		endpoint := "/conversations/" + url.PathEscape(username) + "/" + strconv.FormatInt(id, 10)
		if err := client.acknowledge(ctx, endpoint); err != nil {
			return client.describe(ctx, err)
		}
	}
	return nil
}

func (client *Client) acknowledge(ctx context.Context, endpoint string) error {
	request, err := client.newRequest(ctx, http.MethodPut, endpoint, nil, nil)
	if err != nil {
		return err
	}
	return client.do(request, nil)
}

// routeMissing reports a build of slskd that does not have this path at all, as
// opposed to one that refused what was asked of it.
func routeMissing(err error) bool {
	var httpError *HTTPError
	if !errors.As(err, &httpError) {
		return false
	}
	return httpError.StatusCode == http.StatusNotFound ||
		httpError.StatusCode == http.StatusMethodNotAllowed
}

func (response conversationResponse) conversation() Conversation {
	conversation := Conversation{
		Username:       response.Username,
		Unacknowledged: response.UnAcknowledgedMessageCount,
		Messages:       make([]Message, 0, len(response.Messages)),
	}
	for _, message := range response.Messages {
		conversation.Messages = append(conversation.Messages, Message{
			ID:        message.ID,
			Timestamp: message.Timestamp,
			// Anything that is not slskd's word for an incoming message is
			// treated as one Schall sent, so a direction nobody here recognises
			// can never be answered.
			Incoming:     strings.EqualFold(message.Direction, "In"),
			Text:         message.Message,
			Acknowledged: message.IsAcknowledged,
		})
	}
	return conversation
}
