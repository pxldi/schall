package identity

import (
	"testing"

	"github.com/pxldi/schall/internal/chromaprint"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// A witness is a file the library already holds whose identity was settled by a
// proof. A copy a stranger sent is compared against its fingerprint, and the
// comparison is one number: how much of the owned file's opening the copy fails
// to reproduce.
//
// These tests pin what that number is allowed to do. It admits a copy, it
// answers only about the recording the owned file was proven to be, and — unlike
// every other witness here — it never refuses anything.

// The copy holds the same music as a file already proven to be this recording.
// Nothing else places it: the tags name no identifier and the title is a
// stranger's invention. The owned file is what admits it.
func TestAnOwnedFileAdmitsACopyNothingElseCouldPlace(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "karma police (192)", DurationMS: 308000,
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.03,
			FileDurationMS: 308000, CandidateDurationMS: 308400,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness != tagmatch.Agrees {
		t.Fatalf("witness = %v, want it read as agreement", pair.witness)
	}
	if level := gradeOf(pair); level != byLibraryWitness {
		t.Fatalf("grade = %v, want the owned file to prove it", level)
	}
	outcome := decide(copied, []musicbrainz.Recording{known})
	if outcome.Identity == nil || outcome.Identity.Method != "library-witness" {
		t.Fatalf("identity = %+v, want it recorded as proven by an owned file", outcome.Identity)
	}
}

// A legacy prefix can still be measured, but it cannot prove the unseen tail.
func TestAPartialWitnessDoesNotAdmitTheUnverifiedRemainder(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "karma police (192)", DurationMS: 308000,
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.01,
			FileDurationMS: 308000, CandidateDurationMS: 308400,
			Comparison: &chromaprint.Comparison{
				ComparedSpanFrames: 100, LeftCoverageFrames: 100, RightCoverageFrames: 100,
				WorstWindowBER: 0, GlobalBER: 0,
			},
			ReferenceCoverageSeconds: 120, CandidateCoverageSeconds: 308,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness != tagmatch.Unknown {
		t.Fatalf("witness = %v, want an unverified tail to remain unknown", pair.witness)
	}
}

// An owned file is audio of one particular recording and answers about that one
// alone. Set beside a different recording it says nothing, because a
// measurement that spoke for whatever recording it was put next to would prove
// every candidate on the page at once.
func TestAnOwnedFileAnswersOnlyForTheRecordingItWasProvenToBe(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Witness: &WitnessComparison{RecordingID: uuid.MustParse(secondID), Rate: 0.01},
	}
	other := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, other)
	if pair.witness != tagmatch.Unknown {
		t.Fatalf("witness = %v, want silence about another recording", pair.witness)
	}
}

// The one thing this witness may never do. A copy far from an owned file is a
// second master of the same recording as often as it is other music — a
// remaster, another pressing — and only a witness told what the recording is by
// somebody who published it may call a copy other music.
func TestACopyUnlikeTheOwnedFileIsNotRefused(t *testing.T) {
	copied := Evidence{
		RecordingID: uuid.MustParse(firstID),
		Artist:      "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Witness: &WitnessComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.48},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness == tagmatch.Differs {
		t.Fatal("witness = Differs, want a distant owned file to refuse nothing")
	}
	if pair.contradicted() {
		t.Fatal("the pair is contradicted, want the copy's own identifier to still admit it")
	}
	if level := gradeOf(pair); level != byRecordingID {
		t.Fatalf("grade = %v, want the copy judged as it was before the library was asked", level)
	}
}

// A distance inside the band between the thresholds concludes nothing. It is a
// question for a person and never a weaker yes.
func TestADistanceInsideTheBandProvesNothing(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "karma police (192)", DurationMS: 308000,
		Witness: &WitnessComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.25},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness != tagmatch.Unknown {
		t.Fatalf("witness = %v, want the band to conclude nothing", pair.witness)
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want nothing proven inside the band", level)
	}
}

// The audio naming another recording voids the pair whatever the library says.
// AcoustID was told what this audio is by the database that holds it; an owned
// file agreeing is Schall recognising the same music, and the two cannot both be
// right. The refusal stands.
func TestAnOwnedFileDoesNotOverrideAudioThatNamesAnotherRecording(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Acoustic: heard(cluster(0.97, secondID)),
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.02,
			FileDurationMS: 308000, CandidateDurationMS: 308400,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness != tagmatch.Agrees {
		t.Fatalf("witness = %v, want the owned file to agree", pair.witness)
	}
	if !pair.contradicted() {
		t.Fatal("the pair is not contradicted, want the audio's own name to void it")
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want a contradicted pair to prove nothing", level)
	}
}

// The other witness that may refuse. The distributor published the sample for
// this very recording, so a copy that fails to reproduce it is other music
// however well it matches something in the library.
func TestAnOwnedFileDoesNotOverrideARefusingSample(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Anchor: &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.42},
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.02,
			FileDurationMS: 308000, CandidateDurationMS: 308400,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if !pair.contradicted() {
		t.Fatal("the pair is not contradicted, want the published sample to void it")
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want a contradicted pair to prove nothing", level)
	}
}

// A copy the older witnesses already placed keeps the name it always had. The
// library agreeing as well must not change what identified it.
func TestTheOlderWitnessesKeepTheirNameWhenTheLibraryAgreesToo(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Acoustic: heard(cluster(0.97, firstID)),
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.02,
			FileDurationMS: 308000, CandidateDurationMS: 308400,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	if level := gradeOf(compare(copied, known)); level != byFingerprint {
		t.Fatalf("grade = %v, want AcoustID to keep its own name", level)
	}

	sampled := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Anchor: &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.02},
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.02,
			FileDurationMS: 308000, CandidateDurationMS: 308400,
		},
	}
	if level := gradeOf(compare(sampled, known)); level != bySample {
		t.Fatalf("grade = %v, want the published sample to keep its own name", level)
	}
}

// A rate this close over a shared prefix is exactly what an hour-long mix that
// opens with the wanted recording would measure, if only the rate were asked.
// It is refused because the length nothing decoded is not a length that agrees
// with anything — a copy whose decode never finished, or hit fingerprintCopy's
// cap, carries no candidate length at all, and this witness must not admit on
// the rate alone.
func TestAnOwnedFileDoesNotAdmitACopyWithNoMeasuredLength(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "karma police (192)", DurationMS: 308000,
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.03,
			FileDurationMS: 308000, CandidateDurationMS: 0,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness != tagmatch.Unknown {
		t.Fatalf("witness = %v, want silence about a copy with no measured length", pair.witness)
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want nothing proven without a measured length", level)
	}
}

// The settled file's own length is not known either — a file scanned before
// duration was recorded, or one whose tag was blank. The same rule applies: a
// prefix that agrees proves nothing about a length nobody can compare.
func TestAnOwnedFileDoesNotAdmitWhenItsOwnLengthIsUnknown(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "karma police (192)", DurationMS: 308000,
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.03,
			FileDurationMS: 0, CandidateDurationMS: 308400,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness != tagmatch.Unknown {
		t.Fatalf("witness = %v, want silence when the owned file's own length is unknown", pair.witness)
	}
}

// A copy thirty seconds longer than the owned file, whose opening still matches
// closely — a track appended to a longer file, or a mix that starts with it.
// The prefix agreeing is not enough: the lengths disagree past the tolerance a
// duration tag would be held to, so the witness must not admit it.
func TestAnOwnedFileDoesNotAdmitACopyThirtySecondsLongerThanIt(t *testing.T) {
	copied := Evidence{
		Artist: "Radiohead", Title: "karma police (192)", DurationMS: 308000,
		Witness: &WitnessComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.03,
			FileDurationMS: 308000, CandidateDurationMS: 338000,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(copied, known)
	if pair.witness != tagmatch.Unknown {
		t.Fatalf("witness = %v, want a thirty-second disagreement in length to admit nothing", pair.witness)
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want nothing proven when the lengths disagree", level)
	}
}
