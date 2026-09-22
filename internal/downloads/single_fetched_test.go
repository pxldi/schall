package downloads

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

// A keyed want's track fetched from its own address is judged exactly as a
// peer's copy is: against the excerpt, then the floor (ADR 0038 §6, §7). It is
// read from the fetch folder, because the download inbox is slskd's.

// fetchedFromTheAddress is keyedCandidate for the track Schall fetched from the
// want's address. Its file is in the fetch folder and nowhere else.
func fetchedFromTheAddress(t *testing.T, fingerprint string, kbps int) (*fakeImportStore, *Importer) {
	t.Helper()
	store, importer, _ := keyedCandidate(t, fingerprint, kbps)
	fetchRoot := t.TempDir()
	folder := store.targetRow.AcquisitionTargetID.String()
	store.targetRow.Provider = keyedAddress.Source
	store.targetRow.SourceUsername = keyedAddress.ExternalID
	store.targetRow.SourceDirectory = folder
	store.targetRow.File.Path = folder + "/293.mp3"
	store.targetRow.File.Name = "293.mp3"
	if err := os.MkdirAll(filepath.Join(fetchRoot, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fetchRoot, folder, "293.mp3"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	importer.WithFetchFolder(fetchRoot)
	return store, importer
}

// SoundCloud serves 128 kbit/s. The audio is the track, and the copy is held
// with both numbers so a person can accept it.
func TestATrackFetchedFromTheAddressBelowTheFloorIsHeld(t *testing.T) {
	store, importer := fetchedFromTheAddress(t, anchorFingerprint, 128)

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want the fetched track held below the floor", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("settled = %+v, want the fetched track held", store.settled)
	}
	if !strings.Contains(store.settled.Summary, "128 kbit/s") ||
		!strings.Contains(store.settled.Summary, "320 kbit/s") {
		t.Fatalf("summary = %q, want both bit rates named", store.settled.Summary)
	}
}

// Coming from the address proves nothing. Audio that does not reproduce the
// excerpt is refused.
func TestATrackFetchedFromTheAddressWhoseAudioIsNotTheExcerptIsRefused(t *testing.T) {
	store, importer := fetchedFromTheAddress(t, otherAudioFingerprint, 320)

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted != nil {
		t.Fatalf("accepted = %+v, want other audio refused wherever it came from", store.accepted)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("settled = %+v, want the fetched track refused on the excerpt", store.settled)
	}
}

// Lowering a keyed want's floor judges its held copies again (ADR 0038 §7). The
// held track fetched from the address is found in the fetch folder and admitted.
func TestAHeldTrackFetchedFromTheAddressIsAdmittedWhenTheFloorIsLowered(t *testing.T) {
	store, importer := fetchedFromTheAddress(t, anchorFingerprint, 128)
	store.targetRow.MinimumBitrate = 128
	store.judgeAgain = []db.JudgeAgainRow{{
		CopyID: uuid.New(), AcquisitionTargetID: store.targetRow.AcquisitionTargetID,
		RequestID: store.targetRow.RequestID, Provider: store.targetRow.Provider,
		SourceDirectory: store.targetRow.SourceDirectory, FileName: store.targetRow.File.Name,
		Verdict: db.AcquiredFileHeld,
	}}

	judged, err := importer.judgeAgain(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if judged.Missing != 0 || judged.Judged != 1 {
		t.Fatalf("judged = %+v, want the fetched track found and judged", judged)
	}
	if store.accepted == nil {
		t.Fatalf("settled = %+v, want the track admitted at the lower floor", store.settled)
	}
}

// A fetched track that reproduces the excerpt and meets the floor is admitted
// with the want's key, as a peer's copy would be.
func TestATrackFetchedFromTheAddressThatMeetsTheFloorIsAdmitted(t *testing.T) {
	store, importer := fetchedFromTheAddress(t, anchorFingerprint, 320)

	if err := importer.ImportOne(context.Background(), store.targetRow.RequestID); err != nil {
		t.Fatal(err)
	}

	if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
		t.Fatalf("accepted = %+v, settled = %+v, want the fetched track admitted",
			store.accepted, store.settled)
	}
	if got := store.accepted.Evidence.Source; got == nil || *got != keyedAddress {
		t.Fatalf("source = %+v, want the address the want is keyed to", got)
	}
}
