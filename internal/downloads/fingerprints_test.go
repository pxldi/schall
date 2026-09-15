package downloads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/rs/zerolog"
)

// The backfill pass measures the audio of the copies a person is being asked
// about, so the review queue can show the copies that are one piece of audio as
// one row. It decides nothing, and these tests are about what it writes and what
// it leaves alone.

// heldCopy is one copy waiting to be measured, with its file where validation
// would look for it.
func heldCopy(t *testing.T, inbox, folder, name string) db.UnfingerprintedCopyRow {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(inbox, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, folder, name), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return db.UnfingerprintedCopyRow{
		CopyID: uuid.New(), AcquisitionTargetID: uuid.New(),
		SourceDirectory: `Music\` + folder, FileName: name,
	}
}

func backfillImporter(store *fakeImportStore, inbox string, prints Fingerprinter) *Importer {
	importer := NewImporter(store, inbox, t0LibraryPath, zerolog.Nop())
	if prints != nil {
		importer = importer.WithFingerprinter(prints)
	}
	return importer
}

// The library path plays no part in a pass that only reads the inbox, so it is
// named once rather than made per test.
const t0LibraryPath = "/tmp/schall-not-used"

// A copy whose file is still there is measured, and the measurement is written
// against that copy and nothing else.
func TestAHeldCopyIsFingerprinted(t *testing.T) {
	inbox := t.TempDir()
	waiting := heldCopy(t, inbox, "Anetha", "03.flac")
	store := &fakeImportStore{unfingerprinted: []db.UnfingerprintedCopyRow{waiting}}
	prints := &fakeFingerprinter{value: anchorFingerprint, available: true}

	result, err := backfillImporter(store, inbox, prints).fingerprintHeldCopies(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if result.Measured != 1 || result.Missing != 0 {
		t.Fatalf("pass = %+v, want the one copy measured", result)
	}
	if store.fingerprinted[waiting.CopyID] != anchorFingerprint {
		t.Errorf("fingerprint = %q, want what fpcalc computed for the copy",
			store.fingerprinted[waiting.CopyID])
	}
	if store.fingerprintSeconds != 30 {
		t.Errorf("seconds = %d, want how much audio was read", store.fingerprintSeconds)
	}
	if store.settled != nil || store.accepted != nil {
		t.Error("the pass settled a copy, and it may only measure one")
	}
}

// A copy whose bytes have gone. Nothing can measure it, and nothing is written:
// the copy keeps saying exactly what it says now, and the question a person was
// going to answer is left standing.
func TestACopyWhoseFileIsGoneIsLeftAlone(t *testing.T) {
	inbox := t.TempDir()
	store := &fakeImportStore{unfingerprinted: []db.UnfingerprintedCopyRow{{
		CopyID: uuid.New(), SourceDirectory: `Music\Anetha`, FileName: "03.flac",
	}}}

	result, err := backfillImporter(
		store, inbox, &fakeFingerprinter{value: anchorFingerprint, available: true},
	).fingerprintHeldCopies(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if result.Missing != 1 || result.Measured != 0 {
		t.Fatalf("pass = %+v, want the copy counted as gone and nothing written", result)
	}
	if len(store.fingerprinted) != 0 {
		t.Errorf("fingerprints = %v, want none", store.fingerprinted)
	}
}

// Audio nothing could read. It is a fact about this copy and never a reason to
// stop the pass, so the copies after it are still measured.
func TestAnUnreadableCopyDoesNotStopThePass(t *testing.T) {
	inbox := t.TempDir()
	first := heldCopy(t, inbox, "Anetha", "03.flac")
	second := heldCopy(t, inbox, "Anetha", "04.flac")
	store := &fakeImportStore{unfingerprinted: []db.UnfingerprintedCopyRow{first, second}}
	prints := &refusingOnceFingerprinter{value: anchorFingerprint}

	result, err := backfillImporter(store, inbox, prints).fingerprintHeldCopies(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if result.Unreadable != 1 || result.Measured != 1 {
		t.Fatalf("pass = %+v, want one refusal and one measurement", result)
	}
	if _, written := store.fingerprinted[first.CopyID]; written {
		t.Error("a copy nothing could read was given a fingerprint")
	}
}

// Nothing waiting. An installation with every copy measured is not missing
// anything, whether or not it could fingerprint audio at all, so the pass ends
// quietly rather than reporting that fpcalc is absent.
func TestAPassWithNothingWaitingNeedsNoFpcalc(t *testing.T) {
	result, err := backfillImporter(&fakeImportStore{}, t.TempDir(), nil).
		fingerprintHeldCopies(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want a quiet pass", err)
	}
	if result.Offered != 0 || result.MoreWaiting {
		t.Errorf("pass = %+v, want nothing offered and nothing waiting", result)
	}
}

// Copies waiting and no way to measure them. The pass says so once, for the
// pass, rather than counting every copy as a failure of its own.
func TestAPassWithNoFpcalcSaysSoOnce(t *testing.T) {
	inbox := t.TempDir()
	store := &fakeImportStore{unfingerprinted: []db.UnfingerprintedCopyRow{
		heldCopy(t, inbox, "Anetha", "03.flac"),
	}}

	_, err := backfillImporter(store, inbox, &fakeFingerprinter{available: false}).
		fingerprintHeldCopies(context.Background())

	if err == nil {
		t.Fatal("err = nil, want the pass to report that nothing can fingerprint audio")
	}
	if len(store.fingerprinted) != 0 {
		t.Errorf("fingerprints = %v, want none", store.fingerprinted)
	}
}

// The live path. A copy fetched for a want is fingerprinted while it is judged,
// even when the want carries no published sample to compare it against — which
// is most wants — so it never has to be read a second time by the backfill.
func TestAJudgedCopyKeepsItsFingerprint(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.settled == nil || store.settled.Evidence == nil {
		t.Fatal("nothing was settled")
	}
	evidence := store.settled.Evidence
	if evidence.AudioFingerprint != anchorFingerprint || evidence.AudioFingerprintSeconds != 30 {
		t.Errorf("fingerprint = %q/%d, want the copy's own audio kept with the verdict",
			evidence.AudioFingerprint, evidence.AudioFingerprintSeconds)
	}
	if evidence.Anchor != nil {
		t.Errorf("anchor = %+v, want none: the want has no published sample", evidence.Anchor)
	}
}

// refusingOnceFingerprinter refuses the first file it is handed and computes for
// every one after it, which is one copy of damaged audio in a batch.
type refusingOnceFingerprinter struct {
	value  string
	asked  int
	length int
}

func (prints *refusingOnceFingerprinter) Available() bool { return true }

func (prints *refusingOnceFingerprinter) Compute(
	_ context.Context, _ string, lengthSeconds int,
) (string, int, error) {
	prints.asked++
	prints.length = lengthSeconds
	if prints.asked == 1 {
		return "", 0, errors.New("no audio streams")
	}
	return prints.value, 30, nil
}
