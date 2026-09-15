package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// Without a hub the stream must say so rather than holding a connection open
// that will never carry anything.
func TestEventStreamReportsItselfUnavailableWithoutAHub(t *testing.T) {
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A notice published while the stream is open reaches the client as an SSE
// event naming the topic that went stale.
func TestEventStreamWritesAPublishedNotice(t *testing.T) {
	hub := events.NewHub()
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}

	// The opening comment proves the headers were flushed rather than buffered
	// until the response ended, which is the difference between a push and a
	// very slow reply.
	buffer := make([]byte, 128)
	read, err := response.Body.Read(buffer)
	if err != nil {
		t.Fatalf("reading the opening comment: %v", err)
	}
	if !strings.Contains(string(buffer[:read]), ": connected") {
		t.Fatalf("opening bytes = %q", buffer[:read])
	}

	// Subscribing happens before the opening comment is written, so by now the
	// hub has this connection and a notice cannot be missed.
	hub.Publish(events.TopicDownloads)

	read, err = response.Body.Read(buffer)
	if err != nil {
		t.Fatalf("reading the notice: %v", err)
	}
	if got := string(buffer[:read]); !strings.Contains(got, "event: downloads") {
		t.Fatalf("notice = %q, want the topic that went stale", got)
	}
}

// A scan sits queued until the worker claims it, which can be minutes while the
// worker is busy with something else. The interface that asked for it is not the
// only one showing the library, so queueing is announced rather than left for
// the worker to announce when it gets there.
func TestQueueingALibraryScanAnnouncesIt(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/library/scan", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicLibrary {
			t.Fatalf("notice = %q, want the library topic", topic)
		}
	default:
		t.Fatal("queueing a scan published nothing")
	}
}

// The artists list shows a refresh as queued, and the same argument applies:
// the worker announces only once it has claimed the job.
func TestQueueingAnArtistRefreshAnnouncesIt(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+uuid.NewString()+"/refresh", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicCatalogue {
			t.Fatalf("notice = %q, want the catalogue topic", topic)
		}
	default:
		t.Fatal("queueing an artist refresh published nothing")
	}
}

// Asking about one file again queues a resolution job like a scan does, and the
// library view shows a file as queued the whole time the worker takes to get to
// it.
func TestQueueingAFileResolutionAnnouncesIt(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+uuid.NewString()+"/resolve", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicLibrary {
			t.Fatalf("notice = %q, want the library topic", topic)
		}
	default:
		t.Fatal("queueing a file resolution published nothing")
	}
}

// Withdrawing a decision puts the file back to the providers, so the answer a
// library view elsewhere is showing has gone stale twice over: the decision is
// gone and a resolution is queued.
func TestWithdrawingAnIdentityAnnouncesIt(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithIdentityResolver(&fakeIdentityResolver{}), WithEventHub(hub))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/library/files/"+uuid.NewString()+"/identity", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicLibrary {
			t.Fatalf("notice = %q, want the library topic", topic)
		}
	default:
		t.Fatal("withdrawing an identity published nothing")
	}
}

// Choosing an edition queues the refresh that fetches its tracks, and the
// release is shown in the artist's list as well as on its own page.
func TestSelectingAnAlbumEditionAnnouncesIt(t *testing.T) {
	releaseGroupID, editionID := uuid.New(), uuid.New()
	store := &fakeEditionStore{fakeStore: &fakeStore{album: db.GetAlbumRow{
		MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: releaseGroupID, Valid: true},
	}}}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{},
		editions:           []musicbrainz.ReleaseEdition{{ID: editionID, Title: "Dummy"}},
	}
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop(), WithEventHub(hub))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+editionID.String()+`"}`)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicCatalogue {
			t.Fatalf("notice = %q, want the catalogue topic", topic)
		}
	default:
		t.Fatal("choosing an edition published nothing")
	}
}

// The API discovers edition selection by asserting the store and the searcher
// support it, so these two exist to make the endpoint reachable at all.
type fakeEditionStore struct {
	*fakeStore
	chosen uuid.UUID
	// selectErr answers the write alone, so a lookup that worked and a choice
	// that could not be recorded are different tests.
	selectErr error
}

func (store *fakeEditionStore) SelectAlbumEdition(_ context.Context, _ uuid.UUID, releaseID uuid.UUID) (db.LibraryScanJobRow, error) {
	if store.selectErr != nil {
		return db.LibraryScanJobRow{}, store.selectErr
	}
	store.chosen = releaseID
	return store.libraryScan, store.err
}

type fakeEditionSearcher struct {
	*fakeArtistSearcher
	editions []musicbrainz.ReleaseEdition
}

func (searcher *fakeEditionSearcher) ListReleaseEditions(context.Context, uuid.UUID) ([]musicbrainz.ReleaseEdition, error) {
	return searcher.editions, searcher.err
}

// brokenStream is a connection that fails partway through. Writes succeed until
// writeLimit is reached and fail after that, and flushing fails once flushLimit
// flushes have gone through — which is what a client hanging up mid-stream looks
// like from inside the handler.
type brokenStream struct {
	*httptest.ResponseRecorder
	writeLimit int
	writes     int
	flushLimit int
	flushes    int
}

func (stream *brokenStream) Write(payload []byte) (int, error) {
	stream.writes++
	if stream.writes > stream.writeLimit {
		return 0, errors.New("broken pipe")
	}
	return stream.ResponseRecorder.Write(payload)
}

func (stream *brokenStream) FlushError() error {
	stream.flushes++
	if stream.flushes > stream.flushLimit {
		return errors.New("broken pipe")
	}
	stream.ResponseRecorder.Flush()
	return nil
}

// streamUntilItEnds opens the event stream on a connection of the test's
// choosing and waits for the handler to give up on it, so what is asserted is
// that the stream ended rather than that it wrote something.
func streamUntilItEnds(t *testing.T, handler http.Handler, stream http.ResponseWriter, hub *events.Hub) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream was still open on a connection nothing can be written to")
	}
	if hub.Subscribers() != 0 {
		t.Fatalf("subscribers = %d after the stream ended", hub.Subscribers())
	}
}

// A connection that cannot take the opening comment is one nothing will ever be
// read from, and holding a subscription open for it costs every publisher.
func TestAStreamThatCannotBeWrittenToEndsAtOnce(t *testing.T) {
	hub := events.NewHub()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	streamUntilItEnds(t, handler, &brokenStream{
		ResponseRecorder: httptest.NewRecorder(), flushLimit: 1,
	}, hub)
}

// Without flushing, every notice would arrive at once at the end of the
// response — that is, when none of them mattered any more — so the stream is
// given up rather than offered as a very slow reply.
func TestAStreamThatCannotFlushIsGivenUp(t *testing.T) {
	hub := events.NewHub()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	streamUntilItEnds(t, handler, &brokenStream{
		ResponseRecorder: httptest.NewRecorder(), writeLimit: 1,
	}, hub)
}

// A notice that cannot be written is the client having gone away between the
// opening comment and the first change.
func TestAStreamEndsWhenANoticeCannotBeWritten(t *testing.T) {
	hub := events.NewHub()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))
	stream := &brokenStream{ResponseRecorder: httptest.NewRecorder(), writeLimit: 1, flushLimit: 1}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	}()
	waitForSubscriber(t, hub)
	hub.Publish(events.TopicDownloads)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream stayed open after a notice it could not write")
	}
	if hub.Subscribers() != 0 {
		t.Fatalf("subscribers = %d after the stream ended", hub.Subscribers())
	}
}

// A stream that says nothing for long enough is closed by proxies and by
// browsers, and a download that takes twenty minutes says nothing for most of
// them. So the stream writes its own keep-alive rather than relying on being
// left alone.
func TestAStreamWithNothingToSayKeepsItselfWarm(t *testing.T) {
	previous := heartbeatInterval
	heartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = previous })

	hub := events.NewHub()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	buffer := make([]byte, 128)
	for {
		read, err := response.Body.Read(buffer)
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if strings.Contains(string(buffer[:read]), ": keep-alive") {
			return
		}
	}
}

// The keep-alive is the one thing a stream with nothing to say still writes, so
// a connection that has gone quietly away is discovered by it rather than at
// the next change — which may be hours off.
func TestAStreamEndsWhenItsKeepAliveCannotBeWritten(t *testing.T) {
	previous := heartbeatInterval
	heartbeatInterval = time.Millisecond
	t.Cleanup(func() { heartbeatInterval = previous })

	hub := events.NewHub()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	streamUntilItEnds(t, handler, &brokenStream{
		ResponseRecorder: httptest.NewRecorder(), writeLimit: 1, flushLimit: 1,
	}, hub)
}

// A notice written but not flushed would sit in a buffer until the response
// ended, which for a stream is never. A flush that fails is the connection
// going, and the stream ends with it.
func TestAStreamEndsWhenANoticeCannotBeFlushed(t *testing.T) {
	hub := events.NewHub()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))
	stream := &brokenStream{ResponseRecorder: httptest.NewRecorder(), writeLimit: 2, flushLimit: 1}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	}()
	waitForSubscriber(t, hub)
	hub.Publish(events.TopicDownloads)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream stayed open after a notice it could not flush")
	}
	if hub.Subscribers() != 0 {
		t.Fatalf("subscribers = %d after the stream ended", hub.Subscribers())
	}
}

// waitForSubscriber waits until the stream under test has registered with the
// hub, so a notice published afterwards cannot be missed.
func waitForSubscriber(t *testing.T, hub *events.Hub) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stream never subscribed")
		}
		time.Sleep(time.Millisecond)
	}
}

// A client that goes away must not leave its subscription behind, or the hub
// would fan out to a growing list of connections nobody is reading.
func TestEventStreamUnsubscribesWhenTheClientGoesAway(t *testing.T) {
	hub := events.NewHub()
	handler := NewAPI(downloadStore(), fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub))

	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	if _, err := response.Body.Read(buffer); err != nil {
		cancel()
		t.Fatal(err)
	}
	if hub.Subscribers() != 1 {
		cancel()
		t.Fatalf("subscribers = %d while a stream is open", hub.Subscribers())
	}

	cancel()
	response.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers = %d after the client went away", hub.Subscribers())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The four decisions a person makes about one file by hand: accepting what a
// file is, mapping it onto a track, withdrawing that mapping, and asking for it
// to be evaluated again. Each one changes what every open interface is showing
// — the file's own row, the counts above it, the dashboard in a third tab — and
// none of them used to say so, so a second screen kept the answered file among
// the unanswered until somebody reloaded it.
func TestDecidingAboutOneFileAnnouncesIt(t *testing.T) {
	fileID := uuid.NewString()
	for _, decision := range []struct {
		name    string
		request *http.Request
		want    int
	}{
		{
			name:    "accepting what the file is",
			request: httptest.NewRequest(http.MethodPost, "/api/v1/library/files/"+fileID+"/identity", strings.NewReader(`{"localOnly":true}`)),
			want:    http.StatusOK,
		},
		{
			name:    "mapping it onto a track by hand",
			request: httptest.NewRequest(http.MethodPost, "/api/v1/library/files/"+fileID+"/match", strings.NewReader(`{"trackId":"`+uuid.NewString()+`"}`)),
			want:    http.StatusNoContent,
		},
		{
			name:    "withdrawing that mapping",
			request: httptest.NewRequest(http.MethodDelete, "/api/v1/library/files/"+fileID+"/match", nil),
			want:    http.StatusNoContent,
		},
		{
			name:    "asking for a fresh evaluation",
			request: httptest.NewRequest(http.MethodPost, "/api/v1/library/files/"+fileID+"/reconcile", nil),
			want:    http.StatusNoContent,
		},
	} {
		t.Run(decision.name, func(t *testing.T) {
			hub := events.NewHub()
			notices, unsubscribe := hub.Subscribe()
			defer unsubscribe()
			handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
				WithEventHub(hub), WithMatchResolver(&fakeMatchResolver{}),
				WithIdentityResolver(&fakeIdentityResolver{}))

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, decision.request)
			if response.Code != decision.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, decision.want, response.Body)
			}

			select {
			case topic := <-notices:
				if topic != events.TopicLibrary {
					t.Fatalf("notice = %q, want the library topic", topic)
				}
			default:
				t.Fatal("the decision published nothing")
			}
		})
	}
}

// A person working down a list of unresolved files presses these as fast as
// they can click. Every notice costs each other open interface a refetch, so
// the notices are rationed: the first one leaves at once, and the rest of the
// burst becomes a single one at the end of the gap. What is asserted here is
// the bound, not the timing — waiting out the gap would put two seconds into
// the suite to prove something the rationing already has its own tests for.
func TestABurstOfDecisionsIsNotABurstOfNotices(t *testing.T) {
	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithEventHub(hub), WithMatchResolver(&fakeMatchResolver{}))

	for range 5 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/files/"+uuid.NewString()+"/reconcile", nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d; body = %s", response.Code, response.Body)
		}
	}

	sent := 0
	for {
		select {
		case <-notices:
			sent++
			continue
		default:
		}
		break
	}
	if sent != 1 {
		t.Fatalf("five decisions sent %d notices, want the one the gap allows", sent)
	}
}
