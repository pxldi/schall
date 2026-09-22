package downloads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tagging"
)

// A want a person keyed to an address is searched on the excerpt fetched from
// it, and a copy is admitted only when its audio reproduces that excerpt and it
// meets the want's floor (ADR 0038 §3, §7).

var keyedAddress = db.ImportSource{
	Source: "soundcloud", ExternalID: "soundcloud:293",
	ExternalURL: "https://soundcloud.com/abo/illegal-edit",
}

// keyedCandidate is one MP3 fetched for a keyed want whose floor is 320 kbit/s.
// The file reports kbps as its bit rate.
func keyedCandidate(t *testing.T, fingerprint string, kbps int) (*fakeImportStore, *Importer, *fakeVerifier) {
	t.Helper()
	store, importer, verifier, _ := recordingless(t, "source", keyedAddress.ExternalID, fingerprint)
	key := keyedAddress
	store.targetRow.SourceKey = &key
	store.targetRow.MinimumBitrate = db.KeyedMinimumBitrate
	store.targetRow.File.Path = `Music\Anetha\03.mp3`
	store.targetRow.File.Name = "03.mp3"
	if err := os.WriteFile(filepath.Join(importer.inboxPath, "Anetha", "03.mp3"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	importer.properties = func(string) (tagging.AudioProperties, error) {
		return tagging.AudioProperties{BitRateKbps: kbps, SampleRateHz: 44100}, nil
	}
	return store, importer, verifier
}

// Every tag agrees with the entry and the audio is something else. The copy is
// refused: the want is the track at the address, and other audio is not it.
func TestACopyForAKeyedWantWhoseAudioIsNotTheExcerptIsRefused(t *testing.T) {
	store, importer, verifier := keyedCandidate(t, otherAudioFingerprint, 320)

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want other audio kept out whatever the tags say", store.accepted)
	}
	if len(verifier.asked) != 0 {
		t.Fatalf("asked the recording verifier %d times about a keyed want", len(verifier.asked))
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("settled = %+v, want the copy refused on the excerpt", store.settled)
	}
}

// Same audio at the floor admits, and the copy carries the want's own key.
func TestACopyThatReproducesAKeyedWantsExcerptIsAdmittedAsTheAddressedTrack(t *testing.T) {
	store, importer, verifier := keyedCandidate(t, anchorFingerprint, 320)

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if len(verifier.asked) != 0 {
		t.Fatal("asked the recording verifier about a keyed want")
	}
	if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
		t.Fatalf("accepted = %+v, settled = %+v, want the copy admitted", store.accepted, store.settled)
	}
	if got := store.accepted.Evidence.Source; got == nil || *got != keyedAddress {
		t.Fatalf("source = %+v, want the address the want is keyed to", got)
	}
}

// Without the excerpt nothing admits: a keyed want whose copy could not be
// measured is held, however well its tags agree.
func TestACopyForAKeyedWantThatCouldNotBeMeasuredIsNotAdmitted(t *testing.T) {
	store, importer, _ := keyedCandidate(t, anchorFingerprint, 320)
	importer.WithFingerprinter(&fakeFingerprinter{err: errors.New("fpcalc crashed"), available: true})

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want an unmeasured copy kept out", store.accepted)
	}
}

// A copy the audio proves and that falls below the floor is held, with both
// numbers in its reason (ADR 0038 §7).
func TestAProvenCopyBelowAKeyedWantsFloorIsHeld(t *testing.T) {
	store, importer, _ := keyedCandidate(t, anchorFingerprint, 128)

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want a copy below the floor held", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("settled = %+v, want the copy held", store.settled)
	}
	if !strings.HasPrefix(store.settled.Summary, belowWantFloorSummary) ||
		!strings.Contains(store.settled.Summary, "128 kbit/s") ||
		!strings.Contains(store.settled.Summary, "320 kbit/s") {
		t.Fatalf("summary = %q, want both bit rates named", store.settled.Summary)
	}
}

// A file whose bit rate cannot be read has not shown it meets the floor. A
// lossless one meets any floor without being read.
func TestAKeyedWantsFloorHoldsAnUnreadableBitRateAndPassesLossless(t *testing.T) {
	store, importer, _ := keyedCandidate(t, anchorFingerprint, 0)
	importer.properties = func(string) (tagging.AudioProperties, error) {
		return tagging.AudioProperties{}, errors.New("taglib could not open it")
	}
	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}
	if store.accepted != nil || store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("accepted = %+v, settled = %+v, want an unreadable bit rate held",
			store.accepted, store.settled)
	}

	lossless, importer, _ := keyedCandidate(t, anchorFingerprint, 0)
	lossless.targetRow.File.Path = `Music\Anetha\03.flac`
	lossless.targetRow.File.Name = "03.flac"
	importer.properties = func(string) (tagging.AudioProperties, error) {
		t.Fatal("read the bit rate of a lossless file")
		return tagging.AudioProperties{}, nil
	}
	if err := importer.ImportOne(context.Background(), lossless.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}
	if lossless.accepted == nil {
		t.Fatalf("settled = %+v, want a lossless copy admitted", lossless.settled)
	}
}

// A person accepting a held copy is the exception the floor allows, and the
// copy carries the want's key.
func TestACopyBelowTheFloorAcceptedByHandCarriesTheKeyedAddress(t *testing.T) {
	store, importer, _ := keyedCandidate(t, anchorFingerprint, 128)
	store.acceptedByHand = true

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted == nil {
		t.Fatalf("settled = %+v, want the person's copy imported", store.settled)
	}
	evidence := store.accepted.Evidence
	if evidence.Method != "manual" || evidence.Source == nil || *evidence.Source != keyedAddress {
		t.Fatalf("evidence = %+v, want a manual decision keyed by the address", evidence)
	}
}
