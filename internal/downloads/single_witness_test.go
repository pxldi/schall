package downloads

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/acoustid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/rs/zerolog"
)

// The library can speak for a copy. A want names one recording; the library may
// already hold a file proven to be that recording, and a copy holding the same
// music as that file is that recording too.
//
// These tests are about what the importer does with the comparison — that it
// runs before anything is asked of a service, that it never refuses, and that it
// writes down which file answered. The rule itself is pinned in
// internal/identity, and the measurement in internal/chromaprint.

// ownedCopy is a want the library already holds a settled file for. The owned
// file's fingerprint is whatever the test names it.
func ownedCopy(
	t *testing.T, fingerprint, proof string,
) (*fakeImportStore, string, string, uuid.UUID, uuid.UUID) {
	t.Helper()
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	ancestor := uuid.MustParse("aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa")
	store.witnesses = map[uuid.UUID][]db.LibraryWitnessRow{
		store.targetRow.MusicBrainzRecordingID: {{
			FileID:      ancestor,
			Path:        "/music/Anetha/Candy from Strangers.flac",
			Fingerprint: fingerprint,
			Proof:       proof,
			// The fake fingerprinter measures every copy at 30s
			// (single_anchor_test.go), so the owned file agrees with it here —
			// this fixture is for tests about the comparison running at all,
			// not about the length check, which single_witness_duration_test.go
			// pins on its own.
			DurationMS:      30000,
			CoverageSeconds: 30,
		}},
	}
	return store, inbox, libraryPath, requestID, ancestor
}

// The copy holds the same music as a file the library has already proven. That
// is what admits it, and the file that answered is written down with the proof
// that settled it — an identity borrowed this way has to be able to fall when
// the decision behind it is taken back.
func TestACopyTheLibraryRecognisesIsProvenByIt(t *testing.T) {
	store, inbox, libraryPath, requestID, ancestor := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "manual")
	proven := provenByAudio()
	proven.Identity.Method = "library-witness"
	proven.WitnessAgrees = true
	verifier := &fakeVerifier{verification: proven}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(verifier.asked) != 1 || verifier.asked[0].Witness == nil {
		t.Fatalf("evidence = %+v, want the measurement handed to the grader", verifier.asked)
	}
	if rate := verifier.asked[0].Witness.Rate; rate != 0 {
		t.Errorf("rate = %v, want a fingerprint measured against itself to be 0", rate)
	}
	if store.accepted == nil || store.accepted.Verdict != db.AcquiredFileAccepted {
		t.Fatalf("verdict = %+v, want the copy imported", store.accepted)
	}
	witness := store.accepted.Evidence.Witness
	if witness == nil || witness.Agrees == nil || !*witness.Agrees {
		t.Fatalf("witness = %+v, want the agreement written down", witness)
	}
	if witness.FileID != ancestor.String() || witness.Proof != "manual" {
		t.Errorf("witness = %+v, want the file that answered and what proved it", witness)
	}
	if witness.Path != "/music/Anetha/Candy from Strangers.flac" {
		t.Errorf("path = %q, want the owned file named", witness.Path)
	}
}

func TestALibraryWitnessReadsTheKeptFingerprint(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, anchorFingerprint, "manual")
	store.witnesses[store.targetRow.MusicBrainzRecordingID][0].Fingerprint = keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	)
	var logs bytes.Buffer
	proven := provenByAudio()
	proven.Identity.Method = "library-witness"
	proven.WitnessAgrees = true
	verifier := &fakeVerifier{verification: proven}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier)
	importer.logger = zerolog.New(&logs)
	importer = importer.WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(verifier.asked) != 1 || verifier.asked[0].Witness == nil {
		t.Fatalf("evidence = %+v, want a measured library witness", verifier.asked)
	}
	if verifier.asked[0].Witness.Rate != 0 {
		t.Errorf("witness = %+v, want the kept fingerprint compared", verifier.asked[0].Witness)
	}
	if strings.Contains(logs.String(), "a library file's kept fingerprint is not a fingerprint") {
		t.Errorf("logs = %q, want no unreadable-fingerprint error", logs.String())
	}
}

// The point of asking the library first. A copy the library has answered for
// needs no question put to AcoustID, so the request is not made at all.
func TestACopyTheLibraryRecognisesCostsNoRequest(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "isrc")
	identifier := &fakeIdentifier{configured: true}
	proven := provenByAudio()
	proven.Identity.Method = "library-witness"
	proven.WitnessAgrees = true
	importer := candidateImporter(store, inbox, libraryPath, identifier,
		&fakeVerifier{verification: proven}).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(identifier.asked) != 0 {
		t.Errorf("AcoustID was asked about %v, want the library's answer to be enough",
			identifier.asked)
	}
	if store.accepted == nil {
		t.Fatal("the copy was not imported")
	}
	if store.accepted.Evidence.Acoustic != nil {
		t.Errorf("acoustic = %+v, want nothing written down about a check that never ran",
			store.accepted.Evidence.Acoustic)
	}
}

// The one thing this witness may never do. A copy unlike every file the library
// holds is a second master of the recording as often as it is other music, so
// the library saying nothing leaves the copy exactly where it was — and AcoustID
// is asked, because the question is still open.
func TestACopyTheLibraryDoesNotRecogniseIsNotRefused(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "fingerprint")
	identifier := &fakeIdentifier{configured: true}
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, identifier, verifier).
		WithFingerprinter(&fakeFingerprinter{value: otherAudioFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(identifier.asked) != 1 {
		t.Errorf("AcoustID was asked about %v, want the open question put to it", identifier.asked)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("verdict = %+v, want the copy held rather than discarded", store.settled)
	}
	witness := store.settled.Evidence.Witness
	if witness == nil || !witness.Measured {
		t.Fatalf("witness = %+v, want the comparison recorded", witness)
	}
	if witness.Agrees != nil {
		t.Errorf("agrees = %v, want the library to conclude nothing", *witness.Agrees)
	}
}

// The measurement reaches the grader naming the recording the owned file was
// proven to be. Without that name one number would answer for whatever recording
// it was set beside.
func TestTheLibrarysAnswerReachesTheGraderNamingItsRecording(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "recording-id")
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	witness := verifier.asked[0].Witness
	if witness == nil || witness.RecordingID != store.targetRow.MusicBrainzRecordingID {
		t.Fatalf("witness = %+v, want it naming the want's own recording %s",
			witness, store.targetRow.MusicBrainzRecordingID)
	}
}

// A want the library holds nothing settled for. That is most wants, and nothing
// about judging a copy for one changes.
func TestAWantTheLibraryHoldsNothingForIsJudgedAsBefore(t *testing.T) {
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath,
		&fakeIdentifier{configured: true}, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if verifier.asked[0].Witness != nil {
		t.Errorf("witness = %+v, want nothing measured", verifier.asked[0].Witness)
	}
	if store.settled.Evidence.Witness != nil {
		t.Errorf("witness evidence = %+v, want none recorded", store.settled.Evidence.Witness)
	}
}

// A comparison that could not run. The library held a settled file and something
// stopped the copy being read, which is a check that did not happen and never a
// verdict about the music. Why it did not happen is written down.
func TestAComparisonWithTheLibraryThatCouldNotRunRefusesNothing(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "manual")
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{available: false})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if verifier.asked[0].Witness != nil {
		t.Errorf("witness = %+v, want the grader told nothing was measured",
			verifier.asked[0].Witness)
	}
	witness := store.settled.Evidence.Witness
	if witness == nil || witness.Measured || witness.Unavailable == "" {
		t.Fatalf("witness = %+v, want a reason no comparison was made", witness)
	}
	if witness.Considered != 1 {
		t.Errorf("considered = %d, want the file the library offered counted", witness.Considered)
	}
}

// A stored fingerprint that is not a fingerprint is a fault at home. It says
// nothing about the copy, and the copy is judged as though the library had held
// nothing.
func TestALibraryFingerprintThatCannotBeReadRefusesNothing(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, "not a fingerprint at all", "manual")
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if verifier.asked[0].Witness != nil {
		t.Errorf("witness = %+v, want nothing measured", verifier.asked[0].Witness)
	}
	if store.settled == nil || store.settled.Verdict != db.AcquiredFileHeld {
		t.Fatalf("verdict = %+v, want the copy held", store.settled)
	}
}

// The library recognising a copy never outranks a witness that can refuse. The
// distributor published its sample for this very recording, so a copy that fails
// to reproduce it is other music whatever the library says.
func TestTheLibraryDoesNotOverrideASampleThatRefuses(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "manual")
	store.targetRow.AnchorFingerprint = otherAudioFingerprint
	store.targetRow.AnchorSource = "deezer"
	verifier := &fakeVerifier{verification: identity.Verification{
		AnchorSaysOtherwise: true, Contradicted: true,
		Summary: "the audio is not the published sample",
		Agrees:  []string{}, Differs: []string{"audio (not the published sample of this recording)"},
	}}
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if store.settled == nil || store.settled.Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("verdict = %+v, want the copy discarded on its audio", store.settled)
	}
	if store.accepted != nil {
		t.Fatal("the copy was imported, want the sample's refusal to stand")
	}
}

// A copy that hit fingerprintCopy's length limit while decoding has no measured
// length at all — the concrete case this guards is an hour-long mix whose
// opening happens to be the wanted recording, where the prefix the two
// fingerprints share would otherwise measure as close as a true copy. The
// comparison is not admitted, and AcoustID is still asked: the question the
// library could not answer is still open.
func TestALibraryWitnessDoesNotAdmitACopyPastTheFingerprintCap(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "manual")
	identifier := &fakeIdentifier{configured: true}
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, identifier, verifier).
		WithFingerprinter(&fakeFingerprinter{
			value: anchorFingerprint, available: true, seconds: chromaprint.CandidateLengthSeconds,
		})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	witness := verifier.asked[0].Witness
	if witness == nil || witness.CandidateDurationMS != 0 {
		t.Fatalf("witness = %+v, want the grader told the copy's length is unmeasured", witness)
	}
	if len(identifier.asked) != 1 {
		t.Errorf("AcoustID was asked about %v, want the still-open question put to it", identifier.asked)
	}
	settled := store.settled.Evidence.Witness
	if settled == nil || settled.Agrees != nil {
		t.Fatalf("witness = %+v, want the comparison recorded without an agreement", settled)
	}
}

// A copy thirty seconds longer than the settled file, whose opening still
// matches it closely. The rate over the shared prefix is not enough on its own:
// the two lengths disagree past the tolerance a duration tag would be held to,
// so the witness must not admit it, and AcoustID is still asked.
func TestALibraryWitnessDoesNotAdmitACopyThirtySecondsLongerThanTheSettledFile(t *testing.T) {
	store, inbox, libraryPath, requestID, _ := ownedCopy(t, keptFingerprint(
		acoustid.Fingerprint{DurationSeconds: 30, Value: anchorFingerprint},
	), "manual")
	identifier := &fakeIdentifier{configured: true}
	verifier := &fakeVerifier{verification: identity.Verification{
		Summary: "nothing could decide", Agrees: []string{}, Differs: []string{},
	}}
	importer := candidateImporter(store, inbox, libraryPath, identifier, verifier).
		WithFingerprinter(&fakeFingerprinter{value: anchorFingerprint, available: true, seconds: 60})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	witness := verifier.asked[0].Witness
	if witness == nil {
		t.Fatal("witness = nil, want the measured lengths still handed to the grader")
	}
	if witness.CandidateDurationMS != 60000 || witness.FileDurationMS != 30000 {
		t.Errorf("lengths = %d/%d, want the copy's and the settled file's own measured lengths",
			witness.CandidateDurationMS, witness.FileDurationMS)
	}
	if len(identifier.asked) != 1 {
		t.Errorf("AcoustID was asked about %v, want the still-open question put to it", identifier.asked)
	}
	settled := store.settled.Evidence.Witness
	if settled == nil || settled.Agrees != nil {
		t.Fatalf("witness = %+v, want the disagreeing lengths to admit nothing", settled)
	}
}
