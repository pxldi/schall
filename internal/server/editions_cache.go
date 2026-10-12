package server

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/pxldi/schall/internal/musicbrainz"
)

// editionListTTL is how long a release group's editions are kept. MusicBrainz
// adds an edition to a release group rarely, and the release page asked for the
// list on every visit, waiting 2 to 7 s behind the shared one-request-a-second
// pacer for an answer that had not changed.
const editionListTTL = 6 * time.Hour

// editionCache keeps the editions the release page lists. Choosing an edition
// still asks MusicBrainz directly, so a choice is never checked against an old
// list.
type editionCache struct {
	source EditionSearcher
	ttl    time.Duration
	now    func() time.Time

	group   singleflight.Group
	mutex   sync.Mutex
	entries map[uuid.UUID]editionCacheEntry
}

type editionCacheEntry struct {
	items []musicbrainz.ReleaseEdition
	at    time.Time
}

func newEditionCache(source EditionSearcher, ttl time.Duration) *editionCache {
	return &editionCache{
		source:  source,
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[uuid.UUID]editionCacheEntry),
	}
}

// ListReleaseEditions answers from the cache while the entry is fresh. Two
// pages asking at once share one request. A failure is not kept, so the next
// visit asks again.
func (cache *editionCache) ListReleaseEditions(ctx context.Context, releaseGroupID uuid.UUID) ([]musicbrainz.ReleaseEdition, error) {
	cache.mutex.Lock()
	entry, ok := cache.entries[releaseGroupID]
	cache.mutex.Unlock()
	if ok && cache.now().Sub(entry.at) < cache.ttl {
		return entry.items, nil
	}
	result, err, _ := cache.group.Do(releaseGroupID.String(), func() (any, error) {
		items, err := cache.source.ListReleaseEditions(ctx, releaseGroupID)
		if err != nil {
			return nil, err
		}
		cache.mutex.Lock()
		cache.entries[releaseGroupID] = editionCacheEntry{items: items, at: cache.now()}
		cache.mutex.Unlock()
		return items, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]musicbrainz.ReleaseEdition), nil
}
