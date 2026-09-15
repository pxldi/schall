package coverart

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/wikipedia"
	"github.com/rs/zerolog"
)

func biographiesWaiting(count int) []db.ArtistsMissingBiographyRow {
	rows := make([]db.ArtistsMissingBiographyRow, 0, count)
	for range count {
		rows = append(rows, db.ArtistsMissingBiographyRow{
			ID:            uuid.New(),
			MusicbrainzID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		})
	}
	return rows
}

// biographySweeperFor builds a pass that describes artists and pictures nobody.
func biographySweeperFor(
	t *testing.T, store *fakeCoverStore, encyclopaedia Encyclopaedia,
) *Sweeper {
	t.Helper()
	sweeper := NewSweeper(store, Sources{}, zerolog.Nop()).
		WithBiographies(NewBiographies(fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
		}}, wikidataServing(t, `{"entities": {"Q1": {
			"sitelinks": {"enwiki": {"title": "Somebody"}}}}}`), encyclopaedia))
	sweeper.pause = 0
	return sweeper
}

// The pass describes the artists nobody has asked an encyclopaedia about, and
// keeps the article beside the words because the licence requires the link.
func TestASweepDescribesTheArtistsNobodyHasAskedAbout(t *testing.T) {
	store := &fakeCoverStore{biographiesWaiting: biographiesWaiting(2)}
	sweeper := biographySweeperFor(t, store, &fakeEncyclopaedia{
		summary: wikipedia.Summary{
			Text: "A band.", URL: "https://en.wikipedia.org/wiki/Somebody",
		},
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.savedBiographies) != 2 {
		t.Fatalf("saved %d, want every waiting artist described", len(store.savedBiographies))
	}
	for _, saved := range store.savedBiographies {
		if saved.Biography != "A band." ||
			saved.SourceUrl != "https://en.wikipedia.org/wiki/Somebody" {
			t.Fatalf("saved = %+v, want the words and the article they came from", saved)
		}
	}
}

// An artist no encyclopaedia has heard of is recorded as asked, which is what
// stops the pass finding the same artist every time.
func TestAnArtistWithNoArticleIsRecordedAsAsked(t *testing.T) {
	store := &fakeCoverStore{biographiesWaiting: biographiesWaiting(1)}
	sweeper := biographySweeperFor(t, store, &fakeEncyclopaedia{err: wikipedia.ErrNoArticle})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.savedBiographies) != 1 || store.savedBiographies[0].Biography != "" {
		t.Fatalf("saved = %+v, want the absence recorded with no words", store.savedBiographies)
	}
}

// An encyclopaedia that is down is an outage rather than an absence, so nothing
// is written and the artists are asked about again later.
func TestAnEncyclopaediaThatFailedLeavesTheArtistsUnasked(t *testing.T) {
	store := &fakeCoverStore{biographiesWaiting: biographiesWaiting(5)}
	sweeper := biographySweeperFor(t, store, &fakeEncyclopaedia{
		err: errors.New("Wikipedia returned 503 Service Unavailable"),
	})

	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() error = nil, want the failure reported")
	}
	if len(store.savedBiographies) != 0 {
		t.Fatalf("saved = %+v, want an outage not recorded as an absence",
			store.savedBiographies)
	}
}

// A pass with no biographies configured describes nobody and fails nothing.
func TestASweepWithoutAnEncyclopaediaDescribesNobody(t *testing.T) {
	store := &fakeCoverStore{biographiesWaiting: biographiesWaiting(3)}
	sweeper := NewSweeper(store, Sources{}, zerolog.Nop())
	sweeper.pause = 0

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.savedBiographies) != 0 {
		t.Fatalf("saved = %+v, want nothing described", store.savedBiographies)
	}
}
