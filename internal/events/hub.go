// Package events broadcasts one-line notices that something changed, so a view
// can refetch the moment it happens rather than asking every few seconds.
//
// A notice says only that a topic is stale, never what it now holds. That is
// deliberate: the interface already knows how to fetch each view, and a second
// path that carries state would be a second thing that can disagree with the
// database. The stream makes the interface prompt; it never makes it
// authoritative.
package events

import (
	"sync"
	"time"
)

// Topic names what changed. One should be added only alongside something that
// publishes it: a topic nobody publishes is a view that quietly never updates.
// A notice is an invalidation, and a finer one would only mean more ways to be
// wrong about which view it applies to.
type Topic string

// TopicDownloads covers a request, its transfers, and its import.
const TopicDownloads Topic = "downloads"

// TopicSourceSearches covers a bulk source search: one release of a run having
// been searched for. A run is a queue of provider searches that take as long as
// the provider's timeout allows, so a review screen that only asked on a timer
// would spend most of a run showing work that had already finished.
const TopicSourceSearches Topic = "source-searches"

// TopicLibrary covers the local music folders: a scan being queued, running its
// way through them, and finishing, and a file learning what it is afterwards. A
// scan of a large library is where a view asking on a timer is most obviously
// behind, because the numbers it shows change for minutes on end.
const TopicLibrary Topic = "library"

// TopicCatalogue covers what Schall knows about artists and their releases: a
// metadata refresh, and a release group being brought in behind a proven file
// identity. It is one topic rather than one per artist because a notice is an
// invalidation, and the views it concerns are lists as often as they are pages.
const TopicCatalogue Topic = "catalogue"

// TopicAcquisitions covers the wants: a target being asked for, stopped,
// wanted again, and the sweeper settling one because the library turned out to
// hold it. A want changes on a schedule nobody is watching — hours after the
// person who asked for it closed the page — so the view that shows them has no
// interval short enough to be useful and no reason to poll between sweeps.
const TopicAcquisitions Topic = "acquisitions"

// TopicPlaylists covers the want lists: one being followed, an import running
// its way through the remote rows, and the entries learning what became of
// them. An import is minutes of rate-limited work after a single click, which
// is exactly the shape polling serves worst.
const TopicPlaylists Topic = "playlists"

// subscriberBuffer is how many notices a subscriber may fall behind by before
// they start being dropped. Every publisher is either a job settling or a scan
// throttled to one notice every couple of seconds, so a subscriber has to be
// wedged for a long time to lose one, and a lost notice costs a slower refresh
// rather than a wrong view: the client keeps an unhurried refetch of its own
// for exactly that case.
const subscriberBuffer = 16

// Hub fans notices out to whoever is listening. The zero value is not usable;
// use NewHub.
type Hub struct {
	mutex       sync.RWMutex
	subscribers map[chan Topic]struct{}
}

func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan Topic]struct{})}
}

// Subscribe returns a channel of notices and the function that stops it. The
// channel is closed by that function and by nothing else, so a caller ranging
// over it always terminates on its own cancellation rather than on the hub's
// timing.
func (hub *Hub) Subscribe() (<-chan Topic, func()) {
	channel := make(chan Topic, subscriberBuffer)

	hub.mutex.Lock()
	hub.subscribers[channel] = struct{}{}
	hub.mutex.Unlock()

	var once sync.Once
	return channel, func() {
		once.Do(func() {
			hub.mutex.Lock()
			delete(hub.subscribers, channel)
			hub.mutex.Unlock()
			close(channel)
		})
	}
}

// Publish tells every subscriber that a topic changed. It never blocks: a
// publisher is a worker doing real work, and it must not be held up by a
// browser that stopped reading.
func (hub *Hub) Publish(topic Topic) {
	if hub == nil {
		return
	}
	hub.mutex.RLock()
	defer hub.mutex.RUnlock()

	for channel := range hub.subscribers {
		select {
		case channel <- topic:
		default:
			// Dropped rather than queued. See subscriberBuffer.
		}
	}
}

// Rationed publishes one topic no more often than a gap allows, and never
// swallows the last change. Some publishers produce changes far faster than a
// view can be redrawn — a scan walking a folder, a run of file resolutions
// answering from what the library already knew — and every notice costs each
// open interface a refetch. Without the trailing notice this would be a worse
// bargain than a timer: the burst would be quiet and the view would then sit on
// whatever the last notice it was allowed to send had said.
//
// The zero value is not usable; use NewRationed. A Rationed built on no hub, or
// none at all, is a no-op, so a caller never has to check.
type Rationed struct {
	hub   *Hub
	topic Topic
	gap   time.Duration

	mutex   sync.Mutex
	last    time.Time
	pending *time.Timer
}

func NewRationed(hub *Hub, topic Topic, gap time.Duration) *Rationed {
	return &Rationed{hub: hub, topic: topic, gap: gap}
}

// Publish sends the notice now if the gap has passed, and otherwise arranges
// for one at the end of it. It never blocks and never sends twice for one gap:
// a burst is one notice at its start and one at its end.
func (rationed *Rationed) Publish() {
	if rationed == nil || rationed.hub == nil {
		return
	}
	rationed.mutex.Lock()
	defer rationed.mutex.Unlock()

	now := time.Now()
	if rationed.last.IsZero() || now.Sub(rationed.last) >= rationed.gap {
		rationed.last = now
		rationed.hub.Publish(rationed.topic)
		return
	}
	if rationed.pending != nil {
		return
	}
	rationed.pending = time.AfterFunc(rationed.gap-now.Sub(rationed.last), func() {
		rationed.mutex.Lock()
		rationed.pending = nil
		rationed.last = time.Now()
		rationed.mutex.Unlock()
		rationed.hub.Publish(rationed.topic)
	})
}

// Subscribers reports how many listeners the hub has, which is what lets a
// publisher skip work nobody would see.
func (hub *Hub) Subscribers() int {
	if hub == nil {
		return 0
	}
	hub.mutex.RLock()
	defer hub.mutex.RUnlock()
	return len(hub.subscribers)
}
