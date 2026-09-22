package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/sources"
)

// A keyed want is never resolved, and it is searched on its excerpt the way
// 0037 searches a want on its anchor (ADR 0038 §1, §4).
func TestASweepSearchesAKeyedWantAndNeverResolvesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// The provider would resolve anything it is asked about to this recording.
	provider := &stubProvider{results: []musicbrainz.Recording{
		recording(uuid.New(), "Glory Box", "Portishead", 301000, "GBAAA9400123"),
	}}
	hunter := &fakeHunter{candidates: offering(sources.File{
		Path: `Music\Portishead\Dummy\05 Glory Box.flac`, Name: "05 Glory Box.flac",
		Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 301,
	})}
	fetcher := &fakeFetcher{}
	service := withProvider(hunting(pool, hunter, fetcher, true), provider).
		WithTrackSources(&fakeTrackSources{track: addressedTrack}, fakePrints{value: excerptFingerprint})
	target := createEntry(ctx, t, service)

	keyed, err := service.KeyToSource(ctx, target.ID, addressedTrack.URL, addressedTrack.ExternalID)
	if err != nil {
		t.Fatalf("KeyToSource() error = %v", err)
	}
	if keyed.Status != "unresolved" || keyed.NextAttemptAt.Valid || !keyed.NextSearchAt.Valid {
		t.Fatalf("want = %s next %v search %v, want a search due and no resolution",
			keyed.Status, keyed.NextAttemptAt, keyed.NextSearchAt)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(provider.searches) != 0 {
		t.Fatalf("MusicBrainz was asked %d times about a keyed want", len(provider.searches))
	}
	if len(hunter.texts) != 1 || hunter.texts[0][0] != "Portishead Glory Box" {
		t.Fatalf("searched %v, want the entry's own words", hunter.texts)
	}
	if len(fetcher.started) != 1 {
		t.Fatalf("started %d transfers, want the copy fetched", len(fetcher.started))
	}
	after, err := service.Target(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "unresolved" || after.MusicBrainzRecordingID.Valid || after.NextAttemptAt.Valid {
		t.Errorf("want = %s recording %v next %v, want it still keyed and unresolved",
			after.Status, after.MusicBrainzRecordingID, after.NextAttemptAt)
	}
	due, err := service.NextDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !due.IsZero() && due.Before(after.NextSearchAt.Time) {
		t.Errorf("NextDue() = %v, want nothing due before the search at %v", due, after.NextSearchAt.Time)
	}
}
