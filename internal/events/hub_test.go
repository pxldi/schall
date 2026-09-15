package events

import (
	"testing"
	"time"
)

func TestEverySubscriberIsToldWhatChanged(t *testing.T) {
	hub := NewHub()
	first, stopFirst := hub.Subscribe()
	defer stopFirst()
	second, stopSecond := hub.Subscribe()
	defer stopSecond()

	hub.Publish(TopicDownloads)

	for name, channel := range map[string]<-chan Topic{"first": first, "second": second} {
		select {
		case topic := <-channel:
			if topic != TopicDownloads {
				t.Fatalf("%s received %q", name, topic)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s was never told", name)
		}
	}
}

// A publisher is a worker doing real work. A browser that stopped reading must
// not be able to hold it up, so a full subscriber loses notices instead.
func TestPublishingDoesNotBlockOnASubscriberThatStoppedReading(t *testing.T) {
	hub := NewHub()
	_, stop := hub.Subscribe()
	defer stop()

	done := make(chan struct{})
	go func() {
		for range subscriberBuffer * 4 {
			hub.Publish(TopicDownloads)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publishing blocked on a subscriber nobody is reading")
	}
}

// Unsubscribing closes the channel, so a handler ranging over it returns rather
// than waiting for a notice that will never come.
func TestUnsubscribingClosesTheChannel(t *testing.T) {
	hub := NewHub()
	channel, stop := hub.Subscribe()

	stop()

	select {
	case _, open := <-channel:
		if open {
			t.Fatal("the channel is still open after unsubscribing")
		}
	case <-time.After(time.Second):
		t.Fatal("the channel was never closed")
	}
	if hub.Subscribers() != 0 {
		t.Fatalf("subscribers = %d after unsubscribing", hub.Subscribers())
	}
}

// Stopping twice is what a deferred stop beside an explicit one looks like, and
// it must not close an already-closed channel.
func TestUnsubscribingTwiceIsHarmless(t *testing.T) {
	hub := NewHub()
	_, stop := hub.Subscribe()

	stop()
	stop()
}

// A publisher must not have to know whether anything was configured.
func TestPublishingToNoHubIsHarmless(t *testing.T) {
	var hub *Hub
	hub.Publish(TopicDownloads)
	if hub.Subscribers() != 0 {
		t.Fatal("a nil hub reported subscribers")
	}
}

// The gap that matters most is the one between work starting and the interface
// showing anything at all, so the first notice waits for nothing.
func TestARationedNoticeGoesOutImmediately(t *testing.T) {
	hub := NewHub()
	channel, stop := hub.Subscribe()
	defer stop()

	NewRationed(hub, TopicLibrary, time.Minute).Publish()

	select {
	case topic := <-channel:
		if topic != TopicLibrary {
			t.Fatalf("topic = %q", topic)
		}
	case <-time.After(time.Second):
		t.Fatal("the first notice was held back")
	}
}

// A burst is what a scan walking a folder looks like. Every notice costs each
// open interface a refetch, so the burst must cost one.
func TestARationedNoticeHoldsBackABurst(t *testing.T) {
	hub := NewHub()
	channel, stop := hub.Subscribe()
	defer stop()

	rationed := NewRationed(hub, TopicLibrary, time.Minute)
	for range 100 {
		rationed.Publish()
	}

	<-channel
	select {
	case topic := <-channel:
		t.Fatalf("a burst inside one gap published %q twice", topic)
	default:
	}
}

// Rationing delays a notice; it never drops the last one. A view told nothing
// about the end of a burst would sit on what the notice it was allowed to send
// had said, which is worse than the timer this replaces.
func TestARationedNoticeStillSendsTheLastChange(t *testing.T) {
	hub := NewHub()
	channel, stop := hub.Subscribe()
	defer stop()

	rationed := NewRationed(hub, TopicLibrary, 20*time.Millisecond)
	for range 100 {
		rationed.Publish()
	}

	<-channel
	select {
	case topic := <-channel:
		if topic != TopicLibrary {
			t.Fatalf("topic = %q", topic)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the last change of a burst was never announced")
	}
}

// A publisher must not have to know whether anything was configured.
func TestARationedNoticeWithoutAHubIsHarmless(t *testing.T) {
	NewRationed(nil, TopicLibrary, time.Minute).Publish()

	var rationed *Rationed
	rationed.Publish()
}
