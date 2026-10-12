package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pxldi/schall/internal/musicbrainz"
)

type countingEditions struct {
	calls int
	err   error
}

func (source *countingEditions) ListReleaseEditions(context.Context, uuid.UUID) ([]musicbrainz.ReleaseEdition, error) {
	source.calls++
	if source.err != nil {
		return nil, source.err
	}
	return []musicbrainz.ReleaseEdition{{ID: uuid.New(), Title: "Dummy"}}, nil
}

func TestEditionCacheAnswersARepeatVisitWithoutAskingMusicBrainz(t *testing.T) {
	source := &countingEditions{}
	cache := newEditionCache(source, time.Hour)
	group := uuid.New()

	for range 3 {
		if _, err := cache.ListReleaseEditions(context.Background(), group); err != nil {
			t.Fatal(err)
		}
	}

	if source.calls != 1 {
		t.Fatalf("MusicBrainz was asked %d times, want 1", source.calls)
	}
}

func TestEditionCacheAsksAgainOnceTheListIsOld(t *testing.T) {
	source := &countingEditions{}
	cache := newEditionCache(source, time.Hour)
	clock := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return clock }
	group := uuid.New()

	_, _ = cache.ListReleaseEditions(context.Background(), group)
	clock = clock.Add(time.Hour)
	_, _ = cache.ListReleaseEditions(context.Background(), group)

	if source.calls != 2 {
		t.Fatalf("MusicBrainz was asked %d times, want 2", source.calls)
	}
}

func TestEditionCacheDoesNotKeepAFailure(t *testing.T) {
	source := &countingEditions{err: errors.New("unavailable")}
	cache := newEditionCache(source, time.Hour)
	group := uuid.New()

	_, _ = cache.ListReleaseEditions(context.Background(), group)
	source.err = nil
	items, err := cache.ListReleaseEditions(context.Background(), group)

	if err != nil || len(items) != 1 {
		t.Fatalf("second visit got %v, %v; want the list", items, err)
	}
}
