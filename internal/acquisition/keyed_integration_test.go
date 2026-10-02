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

// Two entries keyed to one address are one want (ADR 0040 §1). Before
// acquisition_targets_source_key_idx both were searched and both could import
// the same track.
func TestASecondWantKeyedToOneAddressIsSupersededByTheFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newTestService(pool).
		WithTrackSources(&fakeTrackSources{track: addressedTrack}, fakePrints{value: excerptFingerprint})
	first := createEntry(ctx, t, service)
	duration := int32(187000)
	second, _, err := service.Create(ctx, Entry{
		Origin: "playlist", Artist: "Abo", Title: "Illegal (Abo Edit)", DurationMS: &duration,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := service.KeyToSource(ctx, first.ID, addressedTrack.URL, addressedTrack.ExternalID); err != nil {
		t.Fatalf("KeyToSource(first) error = %v", err)
	}
	merged, err := service.KeyToSource(ctx, second.ID, addressedTrack.URL, addressedTrack.ExternalID)
	if err != nil {
		t.Fatalf("KeyToSource(second) error = %v", err)
	}

	if merged.Status != "superseded" || !merged.SupersededByID.Valid ||
		merged.SupersededByID.UUID != first.ID || merged.ExternalID != addressedTrack.ExternalID {
		t.Fatalf("second want = %s by %v key %q, want it superseded by the first and keyed",
			merged.Status, merged.SupersededByID, merged.ExternalID)
	}
	if merged.NextSearchAt.Valid || merged.Summary != keyedSupersededSummary {
		t.Errorf("second want search %v summary %q, want no search and the merge named",
			merged.NextSearchAt, merged.Summary)
	}
	survivor, err := service.Target(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if survivor.Status != "unresolved" || !survivor.NextSearchAt.Valid {
		t.Errorf("first want = %s search %v, want it still searched", survivor.Status, survivor.NextSearchAt)
	}

	// The index holds whatever writes the key.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET status = 'unresolved', superseded_by_id = NULL
		WHERE id = $1
	`, second.ID); err == nil {
		t.Fatal("two live wants share one address, want acquisition_targets_source_key_idx to refuse it")
	}
}
