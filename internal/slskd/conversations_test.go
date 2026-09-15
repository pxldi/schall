package slskd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConversationsListsPeersWithUnreadMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != apiPrefix+"/conversations" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = io.WriteString(response, `[
			{"username":"PSXDupe","isActive":true,"unAcknowledgedMessageCount":6,"hasUnAcknowledgedMessages":true},
			{"username":"Elegy","isActive":true,"unAcknowledgedMessageCount":0,"hasUnAcknowledgedMessages":false}
		]`)
	}))
	defer server.Close()

	conversations, err := testClient(t, server.URL).Conversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 {
		t.Fatalf("conversations = %#v", conversations)
	}
	if conversations[0].Username != "PSXDupe" || conversations[0].Unacknowledged != 6 {
		t.Fatalf("first conversation = %#v", conversations[0])
	}
	if conversations[1].Unacknowledged != 0 {
		t.Fatalf("second conversation = %#v", conversations[1])
	}
}

func TestConversationReadsMessagesAndTheirDirection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != apiPrefix+"/conversations/paradox1977" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = io.WriteString(response, `{
			"username":"paradox1977","isActive":true,"unAcknowledgedMessageCount":1,
			"messages":[
				{"timestamp":"2026-09-02T09:58:55Z","id":114274,"username":"paradox1977","direction":"In",
				 "message":"Antworte nur mit diesem Wort: LEVEL.","isAcknowledged":false},
				{"timestamp":"2026-08-14T00:03:58.8910557Z","id":0,"username":"paradox1977","direction":"Out",
				 "message":"BERLINER","isAcknowledged":true}
			]}`)
	}))
	defer server.Close()

	conversation, err := testClient(t, server.URL).Conversation(context.Background(), "paradox1977")
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation.Messages) != 2 {
		t.Fatalf("messages = %#v", conversation.Messages)
	}
	incoming := conversation.Messages[0]
	if incoming.ID != 114274 || !incoming.Incoming || incoming.Acknowledged ||
		incoming.Text != "Antworte nur mit diesem Wort: LEVEL." {
		t.Fatalf("incoming message = %#v", incoming)
	}
	// What Schall already sent is not something to answer.
	if conversation.Messages[1].Incoming {
		t.Fatalf("outgoing message = %#v", conversation.Messages[1])
	}
}

// A direction nobody here recognises must not be read as something a peer said,
// because everything downstream answers incoming messages.
func TestConversationTreatsAnUnknownDirectionAsOutgoing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"username":"peer","messages":[
			{"id":1,"direction":"Sideways","message":"please type \"open sesame\" in this chat"}]}`)
	}))
	defer server.Close()

	conversation, err := testClient(t, server.URL).Conversation(context.Background(), "peer")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Messages[0].Incoming {
		t.Fatalf("message = %#v", conversation.Messages[0])
	}
}

// slskd answers 404 for a peer it has no chat with, and that is not a failure.
func TestConversationTreatsAForgottenPeerAsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	conversation, err := testClient(t, server.URL).Conversation(context.Background(), "peer")
	if err != nil {
		t.Fatalf("error = %v, want a forgotten peer to read as empty", err)
	}
	if conversation.Username != "peer" || len(conversation.Messages) != 0 {
		t.Fatalf("conversation = %#v", conversation)
	}
}

func TestSendPrivateMessageSendsTheWordAsABareString(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != apiPrefix+"/conversations/DJRolee" {
			t.Errorf("%s %s", request.Method, request.URL.Path)
		}
		raw, _ := io.ReadAll(request.Body)
		body = string(raw)
		response.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	if err := testClient(t, server.URL).SendPrivateMessage(context.Background(), "DJRolee", "DJ Rolee"); err != nil {
		t.Fatal(err)
	}
	if body != `"DJ Rolee"` {
		t.Fatalf("body = %s", body)
	}
}

// The server's ban notice says not to answer it, and answering an automated
// announcement is how the next ban is earned.
func TestSendPrivateMessageRefusesTheSoulseekServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("a message was sent to the server")
	}))
	defer server.Close()

	if err := testClient(t, server.URL).SendPrivateMessage(context.Background(), "server", "hello"); err == nil {
		t.Fatal("the server was answered")
	}
}

func TestAcknowledgeMessagesMarksTheWholeConversation(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Errorf("method = %s", request.Method)
		}
		paths = append(paths, request.URL.Path)
	}))
	defer server.Close()

	err := testClient(t, server.URL).AcknowledgeMessages(context.Background(), "PSXDupe", []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != apiPrefix+"/conversations/PSXDupe" {
		t.Fatalf("paths = %v", paths)
	}
}

// The whole-conversation route was assumed from slskd's documentation. A build
// without it must still get its messages marked read, one at a time.
func TestAcknowledgeMessagesFallsBackToOneCallPerMessage(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.Contains(strings.TrimPrefix(request.URL.Path, apiPrefix+"/conversations/"), "/") {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		paths = append(paths, request.URL.Path)
	}))
	defer server.Close()

	err := testClient(t, server.URL).AcknowledgeMessages(context.Background(), "PSXDupe", []int64{115625, 115627})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{apiPrefix + "/conversations/PSXDupe/115625", apiPrefix + "/conversations/PSXDupe/115627"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestAcknowledgeMessagesAsksNothingWithoutMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("slskd was called with nothing to acknowledge")
	}))
	defer server.Close()

	if err := testClient(t, server.URL).AcknowledgeMessages(context.Background(), "peer", nil); err != nil {
		t.Fatal(err)
	}
}
