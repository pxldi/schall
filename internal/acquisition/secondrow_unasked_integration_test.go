package acquisition

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// unreachableCatalogue is MusicBrainz answering every lookup with a 503.
type unreachableCatalogue struct{ catalogueProvider }

func (unreachableCatalogue) Recording(context.Context, uuid.UUID) (musicbrainz.Recording, error) {
	return musicbrainz.Recording{}, errors.New("MusicBrainz returned HTTP 503")
}

// One 503 while asking whether the library holds a want's song under a second
// row failed the whole sweep on live (2026-10-14 21:05). The want now waits and
// asks again, the sweep succeeds, and nothing is fetched meanwhile.
func TestAMusicBrainzOutageDefersOnlyTheWantItWasAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, owned := uuid.New(), uuid.New()
	hunter := &fakeHunter{}
	service := hunting(pool, hunter, &fakeFetcher{}, true).
		WithResolver(identity.NewResolver(&unreachableCatalogue{}).WithAliases(db.New(pool)))
	ownedFile(ctx, t, pool, owned)
	target := createWant(ctx, t, service, wanted)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v, want the sweep to carry on", err)
	}

	deferred := targetRow(ctx, t, service, target.ID)
	if deferred.Status != "pending" || !deferred.NextAttemptAt.Valid ||
		!deferred.NextAttemptAt.Time.After(time.Now()) {
		t.Errorf("status = %s next = %v, want a pending want asked again later",
			deferred.Status, deferred.NextAttemptAt)
	}
	if deferred.Summary != unaskedSummary {
		t.Errorf("summary = %q, want %q", deferred.Summary, unaskedSummary)
	}
	if len(hunter.queries) != 0 {
		t.Errorf("searches = %d, want none while the library's answer is unknown", len(hunter.queries))
	}
}
