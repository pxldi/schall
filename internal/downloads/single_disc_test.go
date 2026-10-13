package downloads

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/accuraterip"
	"github.com/pxldi/schall/internal/tagging"
)

// A lossless copy is looked up in AccurateRip. These tests are about what the
// importer does with the answer: that it asks only about a copy that can be a
// CD track, and that only a match reaches the grader. The rule is pinned in
// internal/identity, and the checksum in internal/accuraterip.

type fakeDiscChecker struct {
	answer accuraterip.Answer
	err    error
	asked  []uuid.UUID
}

func (*fakeDiscChecker) Available() bool { return true }

func (checker *fakeDiscChecker) Check(
	_ context.Context, recordingID uuid.UUID, _ string,
) (accuraterip.Answer, error) {
	checker.asked = append(checker.asked, recordingID)
	return checker.answer, checker.err
}

func cdImporter(
	t *testing.T, checker *fakeDiscChecker, sampleRate int, verifier *fakeVerifier,
) (*Importer, *fakeImportStore, uuid.UUID) {
	t.Helper()
	store, inbox, libraryPath, requestID := fetchedCandidate(t, 3)
	importer := candidateImporter(store, inbox, libraryPath, nil, verifier).WithDiscChecker(checker)
	importer.properties = func(string) (tagging.AudioProperties, error) {
		return tagging.AudioProperties{SampleRateHz: sampleRate, Channels: 2, DurationMS: 487_000}, nil
	}
	return importer, store, requestID
}

var cdMatch = accuraterip.Answer{Match: &accuraterip.Match{
	ReleaseID: uuid.MustParse("c7569949-0f67-4682-a0d8-75c4290c52dc"),
	Medium:    1, Track: 3, DiscID: "rdosqrxASN.ZqF.d.5pPhHtgVyA-", Confidence: 7,
}}

func TestACDTrackMatchReachesTheGraderNamingTheWantsRecording(t *testing.T) {
	checker := &fakeDiscChecker{answer: cdMatch}
	proven := provenByAudio()
	proven.Identity.Method = "cd-checksum"
	verifier := &fakeVerifier{verification: proven}
	importer, store, requestID := cdImporter(t, checker, 44100, verifier)

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(verifier.asked) != 1 || verifier.asked[0].Disc == nil ||
		verifier.asked[0].Disc.RecordingID != uuid.MustParse(wantedRecording) {
		t.Fatalf("evidence = %+v, want the match handed to the grader for the want's recording",
			verifier.asked)
	}
	disc := store.accepted.Evidence.Disc
	if disc == nil || !disc.Matched || disc.Track != 3 || disc.DiscID != cdMatch.Match.DiscID {
		t.Fatalf("disc = %+v, want the match written down", disc)
	}
}

func TestACopyAccurateRipFoundNothingForReachesTheGraderWithNoDisc(t *testing.T) {
	checker := &fakeDiscChecker{answer: accuraterip.Answer{Silence: "No rip matches."}}
	verifier := &fakeVerifier{verification: provenByAudio()}
	importer, store, requestID := cdImporter(t, checker, 44100, verifier)

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(verifier.asked) != 1 || verifier.asked[0].Disc != nil {
		t.Fatalf("evidence = %+v, want no disc handed to the grader", verifier.asked)
	}
	if disc := store.accepted.Evidence.Disc; disc == nil || disc.Silence != "No rip matches." {
		t.Fatalf("disc = %+v, want the silence written down", disc)
	}
}

func TestACopyThatIsNotAtACDsSampleRateIsNotLookedUp(t *testing.T) {
	checker := &fakeDiscChecker{answer: cdMatch}
	importer, store, requestID := cdImporter(t, checker, 48000, &fakeVerifier{verification: provenByAudio()})

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(checker.asked) != 0 {
		t.Fatalf("AccurateRip asked about %v, want a 48 kHz copy left alone", checker.asked)
	}
	if disc := store.accepted.Evidence.Disc; disc == nil || disc.Silence != notCDAudio {
		t.Errorf("disc = %+v, want it written down as looked at", disc)
	}
}

func TestALossyCopyIsNotLookedUp(t *testing.T) {
	checker := &fakeDiscChecker{answer: cdMatch}
	importer, store, requestID := cdImporter(t, checker, 44100, &fakeVerifier{verification: provenByAudio()})
	store.targetRow.File.Name, store.targetRow.File.Path = "03.mp3", `Music\Anetha\03.mp3`
	folder := filepath.Join(importer.inboxPath, "Anetha")
	if err := os.Rename(filepath.Join(folder, "03.flac"), filepath.Join(folder, "03.mp3")); err != nil {
		t.Fatal(err)
	}

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if len(checker.asked) != 0 {
		t.Fatalf("AccurateRip asked about %v, want an MP3 left alone", checker.asked)
	}
}

func TestAnAccurateRipThatCouldNotBeAskedIsWrittenDownAndAdmitsNothing(t *testing.T) {
	checker := &fakeDiscChecker{err: context.DeadlineExceeded}
	verifier := &fakeVerifier{verification: provenByAudio()}
	importer, store, requestID := cdImporter(t, checker, 44100, verifier)

	if err := importer.ImportOne(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if verifier.asked[0].Disc != nil {
		t.Fatalf("disc = %+v, want nothing handed to the grader", verifier.asked[0].Disc)
	}
	if disc := store.accepted.Evidence.Disc; disc == nil || disc.Unavailable == "" {
		t.Fatalf("disc = %+v, want the failure written down", disc)
	}
}
