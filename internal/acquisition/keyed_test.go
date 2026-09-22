package acquisition

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tracksource"
	"github.com/rs/zerolog"
)

// A person keys a want to the address of a track (ADR 0038). These tests are
// about what the service does before and after the store writes the key.

// excerptFingerprint is a real fpcalc fingerprint of thirty seconds of audio,
// the one internal/downloads measures copies against.
const excerptFingerprint = "AQAA3dGkaEzEJPjyo1r0oEmaD2fQ5jd-aE_xoeGJPidyNai4o3lMnIbVIN7xaQukKieaqCwYdxfO4C" +
	"EPJle6wPqhiRGaHz10Im1U4_nxkaguo0kqT8jxxNB99Dg9NEceHQ-_ItSPps7xD91HFWLooMEXxuhZ" +
	"wRf0HE5WGqEuPDzOBz_ewN9E_MPxoxKD5GyO8FlONNdxwpt2ozf6BM3xI34gKraS4P-" +
	"QK8ZHw1OmhPjm4Avyo4mjEl10BeoSGz_8CLeCfRb-4EoYaKlyVBGq_-" +
	"hDEblT4cmJP2h_hPmx5_izgj9EpgfzCz6chsGzHT6PWh2LJwvMKNCVHM-VFU2ONjt-HNp7uGglnFdQ" +
	"kT4aQfp4kBxKVIvO4Az0w5uNL7hjiFeCB_G44KaC_ricPPCP8BdOnwJrpcero4lzDYrOHCGZXTgeXI" +
	"6D5uXwHb-CZFGaBKGEB3t0CX8Exj6ah8dNqThyHWoHX4d0PpAchUdzHk8PHxfRo6aCJk4CuaxwOit8" +
	"_Dhha8G_whfxEq-po4WoNYezKA_uU7gIqjr6SEazzuihC09aCc6FH3mT48sO3_h5ZFUTPDGcI78z4Y" +
	"Wr4GbQwx3xHD9aJsctJdAio8eX41oa1MYPv2gDYgBiVgAQiDoIEIWYQAoIRgiBChBpGICAIOIYE8QA" +
	"5YgBgBBBjBFEEIOUEoBBIZAQAhgMlAAIAECIM8hJp5gFBFFBiCACGeMYEsAKAwRhTBiIgARCIQCAqE" +
	"oh4QgyQCFCGDIEIIAIQYQAAIRARBhFFRFGAcGAUAgBBBDgAgCkAFRKKAGYUsw4wAFwyAhFhXCAGmcM" +
	"cQoJwCQRAhCkHDGAA6UAFgA"

var addressedTrack = tracksource.Track{
	Source: "soundcloud", ID: "293", ExternalID: "soundcloud:293",
	URL:   "https://soundcloud.com/abo/illegal-edit",
	Title: "PinkPantheress - Illegal (Abo Edit)", Artist: "Abo", Uploader: "Abo",
	AccountID: "soundcloud:https://soundcloud.com/abo", DurationMS: 187000,
}

type fakeTrackSources struct {
	track    tracksource.Track
	lookups  int
	excerpts int
}

func (sources *fakeTrackSources) Lookup(context.Context, string) (tracksource.Track, error) {
	sources.lookups++
	return sources.track, nil
}

func (sources *fakeTrackSources) Excerpt(context.Context, tracksource.Track) ([]byte, error) {
	sources.excerpts++
	return []byte("mp3"), nil
}

type fakePrints struct{ value string }

func (prints fakePrints) Available() bool { return true }

func (prints fakePrints) Compute(context.Context, string, int) (string, int, error) {
	return prints.value, 30, nil
}

type keyingStore struct {
	Store
	target db.AcquisitionTargetRow
	keyed  *db.KeySourceParams
}

func (store *keyingStore) AcquisitionTarget(context.Context, uuid.UUID) (db.AcquisitionTargetRow, error) {
	return store.target, nil
}

func (store *keyingStore) KeyAcquisitionTargetToSource(
	_ context.Context, params db.KeySourceParams,
) (db.AcquisitionTargetRow, error) {
	store.keyed = &params
	return store.target, nil
}

func keying(target db.AcquisitionTargetRow, fingerprint string) (*Service, *keyingStore, *fakeTrackSources) {
	store := &keyingStore{target: target}
	sources := &fakeTrackSources{track: addressedTrack}
	service := NewService(store, zerolog.Nop()).WithTrackSources(sources, fakePrints{value: fingerprint})
	return service, store, sources
}

func TestKeyingWritesTheAddressWithItsExcerpt(t *testing.T) {
	service, store, _ := keying(db.AcquisitionTargetRow{ID: uuid.New(), Status: "unresolved"},
		excerptFingerprint)

	if _, err := service.KeyToSource(context.Background(), store.target.ID,
		"https://soundcloud.com/abo/illegal-edit", "soundcloud:293"); err != nil {
		t.Fatalf("KeyToSource() error = %v", err)
	}
	keyed := store.keyed
	if keyed == nil {
		t.Fatal("nothing was keyed")
	}
	if keyed.Source != "soundcloud" || keyed.ExternalID != "soundcloud:293" ||
		keyed.ExternalURL != "https://soundcloud.com/abo/illegal-edit" ||
		keyed.Fingerprint != excerptFingerprint || keyed.Seconds != 30 {
		t.Errorf("keyed = %+v, want the address and its excerpt", keyed)
	}
	if keyed.Lookup.Title != addressedTrack.Title || keyed.Lookup.AccountID != addressedTrack.AccountID {
		t.Errorf("lookup = %+v, want what the address said", keyed.Lookup)
	}
}

// A want that cannot take a key is refused before any audio is fetched.
func TestKeyingAWantWithARecordingAsksNothing(t *testing.T) {
	service, store, sources := keying(db.AcquisitionTargetRow{
		ID: uuid.New(), Status: "pending",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}, excerptFingerprint)

	_, err := service.KeyToSource(context.Background(), store.target.ID, "https://soundcloud.com/a/b", "")
	if !errors.Is(err, db.ErrNotKeyable) {
		t.Fatalf("KeyToSource() error = %v, want %v", err, db.ErrNotKeyable)
	}
	if sources.lookups != 0 || store.keyed != nil {
		t.Fatalf("looked up %d times and keyed %+v, want nothing", sources.lookups, store.keyed)
	}
}

func TestKeyingRefusesAnAddressThatNowNamesAnotherTrack(t *testing.T) {
	service, store, sources := keying(db.AcquisitionTargetRow{ID: uuid.New(), Status: "unresolved"},
		excerptFingerprint)

	_, err := service.KeyToSource(context.Background(), store.target.ID,
		"https://soundcloud.com/abo/illegal-edit", "soundcloud:294")
	if !errors.Is(err, ErrAddressChanged) {
		t.Fatalf("KeyToSource() error = %v, want %v", err, ErrAddressChanged)
	}
	if sources.excerpts != 0 || store.keyed != nil {
		t.Fatalf("fetched %d excerpts and keyed %+v, want nothing", sources.excerpts, store.keyed)
	}
}

// An excerpt too short to compare against is no anchor, and a keyed want
// without one could prove nothing.
func TestKeyingRefusesAnExcerptThatCannotBeFingerprinted(t *testing.T) {
	service, store, _ := keying(db.AcquisitionTargetRow{ID: uuid.New(), Status: "unresolved"}, "AQAAAA")

	_, err := service.KeyToSource(context.Background(), store.target.ID,
		"https://soundcloud.com/abo/illegal-edit", "")
	if !errors.Is(err, ErrExcerptUnreadable) {
		t.Fatalf("KeyToSource() error = %v, want %v", err, ErrExcerptUnreadable)
	}
	if store.keyed != nil {
		t.Fatalf("keyed %+v, want nothing written", store.keyed)
	}
}

type countingNamer struct{ named []uuid.UUID }

func (namer *countingNamer) NameKeyedFile(_ context.Context, targetID uuid.UUID) error {
	namer.named = append(namer.named, targetID)
	return nil
}

// The file a keyed want settles on is named from the address; any other want's
// file is left to the tagging that proves it.
func TestSettlingAKeyedWantNamesItsFile(t *testing.T) {
	store := &strandedStore{outcome: db.AcquiredFileOutcome{
		Settled: true, LibraryFileID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}
	namer := &countingNamer{}
	service := NewService(store, zerolog.Nop()).WithSourceNamer(namer)

	keyed := db.AcquisitionTargetRow{ID: uuid.New(), Status: "unresolved", Source: "soundcloud"}
	if _, err := service.completeAcquired(context.Background(), keyed); err != nil {
		t.Fatal(err)
	}
	anchored := db.AcquisitionTargetRow{ID: uuid.New(), Status: "unresolved", AnchorSource: "deezer"}
	if _, err := service.completeAcquired(context.Background(), anchored); err != nil {
		t.Fatal(err)
	}
	if len(namer.named) != 1 || namer.named[0] != keyed.ID {
		t.Fatalf("named %v, want only the keyed want's file", namer.named)
	}
}
