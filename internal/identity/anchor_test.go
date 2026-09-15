package identity

import (
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/chromaprint"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// An anchor is a piece of audio whose identity is already known: the short
// sample a distributor publishes for a recording, fetched by that recording's
// own registration code. A file is compared against it directly, and the
// comparison is one number — how much of the sample the file fails to reproduce.
//
// These tests pin what that number is allowed to do. It refuses a file, it
// admits a file, and it does both only for the recording the sample was
// published for.

// A local mismatch cannot be hidden by a low global average.
func TestASampleWithAMismatchingWindowDoesNotAdmit(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.01,
			Comparison: &chromaprint.Comparison{
				ComparedSpanFrames: 240, LeftCoverageFrames: 240, RightCoverageFrames: 240,
				WorstWindowBER: 0.24, GlobalBER: 0.01,
			},
			CandidateCoverageSeconds: 308, CandidateDurationMS: 308000,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, known)
	if pair.anchor != tagmatch.Unknown {
		t.Fatalf("anchor = %v, want the mismatching window to remain unknown", pair.anchor)
	}
}

// A sample is audio of one particular recording, and it answers about that
// recording alone. Set beside a different one it says nothing at all — neither
// agreement nor refusal — because a measurement that spoke for whatever
// recording it was put next to would prove a whole list of candidates at once.
func TestASampleAnswersOnlyForItsOwnRecording(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Title: "Karma Police (slowed)", DurationMS: 308000,
		Anchor: &AnchorComparison{RecordingID: uuid.MustParse(secondID), Rate: 0.01},
	}
	other := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, other)
	if pair.anchor != tagmatch.Unknown {
		t.Fatalf("anchor = %v, want silence about a recording the sample is not of", pair.anchor)
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want another recording's sample to prove nothing", level)
	}
}

// The file agrees with the recording on everything a file can agree on, and its
// audio is the wrong audio. It is refused, because a contradiction voids a pair
// however much else agrees.
func TestASampleRefusesAFileEverythingElseAgreesWith(t *testing.T) {
	file := Evidence{
		RecordingID: uuid.MustParse(firstID), ISRC: "GBAYE0601498",
		Artist: "Radiohead", Album: "OK Computer", Title: "Karma Police",
		DurationMS: 308000,
		Anchor:     &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.42},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	known.ISRCs = []string{"GBAYE0601498"}

	pair := compare(file, known)
	if !pair.contradicted() {
		t.Fatal("the pair is not contradicted, want the sample to void it")
	}
	if outcome := decide(file, []musicbrainz.Recording{known}); outcome.Identity != nil {
		t.Errorf("identity = %+v, want nothing proven", outcome.Identity)
	}
}

// The audio reproducing the sample is proof on its own, and it is the whole
// point of the sample: this file's tags name nothing, MusicBrainz was never
// asked to hold this music, and before samples existed there was no way to
// admit it at all.
func TestASampleAdmitsAFileNothingElseCouldPlace(t *testing.T) {
	// Tags a stranger wrote and nothing else: no identifier, and a title that
	// agrees with nothing.
	file := Evidence{
		Artist: "Radiohead", Title: "Karma Police (slowed)", DurationMS: 308000,
		Anchor: &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.01},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, known)
	if pair.anchor != tagmatch.Agrees {
		t.Fatalf("anchor = %v, want it read as agreement", pair.anchor)
	}
	if level := gradeOf(pair); level != bySample {
		t.Fatalf("grade = %v, want the sample to prove it", level)
	}
	outcome := decide(file, []musicbrainz.Recording{known})
	if outcome.Identity == nil || outcome.Identity.Method != "published-sample" {
		t.Fatalf("identity = %+v, want it recorded as proven by the sample", outcome.Identity)
	}
}

// A sample the audio reproduces is still not enough while something else says
// this is different music. Rule 3 of docs/decisions/0024: evidence is never
// summed, and a contradiction voids a pair whatever agrees with it.
func TestASampleThatAgreesIsStillVoidedByAContradiction(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		// The file's own identifier names other music.
		RecordingID: uuid.MustParse(secondID),
		Anchor:      &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.01},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, known)
	if pair.anchor != tagmatch.Agrees {
		t.Fatalf("anchor = %v, want the sample to agree", pair.anchor)
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want a contradicted pair to prove nothing", level)
	}
}

// A copy AcoustID has already named keeps the name it always had. Both
// witnesses agreeing is the ordinary case for music MusicBrainz does hold, and
// what identified it must not change wording because a second check now runs.
func TestAcoustIDKeepsItsOwnNameWhenTheSampleAgreesToo(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Acoustic: heard(cluster(0.97, firstID)),
		Anchor:   &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.01},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	if level := gradeOf(compare(file, known)); level != byFingerprint {
		t.Errorf("grade = %v, want AcoustID's own name kept", level)
	}
}

// Between the two thresholds nothing is concluded. It is not a weaker yes and it
// is not a refusal — it is the copy a person is asked about.
func TestASampleInTheDeadBandConcludesNothing(t *testing.T) {
	file := Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308000,
		Anchor: &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.25},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, known)
	if pair.anchor != tagmatch.Unknown {
		t.Fatalf("anchor = %v, want nothing concluded", pair.anchor)
	}
	if pair.contradicted() {
		t.Error("the pair is contradicted, want the dead band to refuse nothing")
	}
	// And the file is still proven by its identifier, exactly as it was before
	// there was a sample to measure. A check that concluded nothing must not take
	// anything away either.
	if level := gradeOf(pair); level != byRecordingID {
		t.Errorf("grade = %v, want the recording ID to still prove it", level)
	}
}

// No comparison at all. Most music has no sample published for it, and a file
// nothing measured is judged exactly as it was before samples existed.
func TestNoSampleChangesNothing(t *testing.T) {
	file := Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308000,
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, known)
	if pair.anchor != tagmatch.Unknown {
		t.Fatalf("anchor = %v, want silence", pair.anchor)
	}
	if level := gradeOf(pair); level != byRecordingID {
		t.Errorf("grade = %v, want the recording ID to prove it", level)
	}
}

// The two witnesses that speak about audio are kept apart. AcoustID naming this
// recording correctly does not stop the sample refusing the file, and the
// refusal is reported as the sample's rather than as AcoustID's — reading one as
// the other would write down a thing AcoustID never said.
func TestTheSampleAndAcoustIDAnswerSeparately(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308000,
		Acoustic: heard(cluster(0.97, firstID)),
		Anchor:   &AnchorComparison{RecordingID: uuid.MustParse(firstID), Rate: 0.44},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, known)
	if pair.acoustic != tagmatch.Agrees {
		t.Fatalf("acoustic = %v, want AcoustID's own answer left alone", pair.acoustic)
	}
	if pair.anchor != tagmatch.Differs {
		t.Fatalf("anchor = %v, want the sample to disagree", pair.anchor)
	}
	if !pair.contradicted() {
		t.Error("the pair is not contradicted, want the sample to void it anyway")
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Errorf("grade = %v, want a contradicted pair to prove nothing", level)
	}
}

// What the comparison is called when it reaches a caller. The two flags are
// separate so that a copy the sample refused is never recorded as one AcoustID
// refused, and both being false means nothing was concluded.
func TestSampleVerdictsTravelSeparately(t *testing.T) {
	for _, want := range []struct {
		name          string
		rate          float64
		saysOtherwise bool
		agrees        bool
	}{
		{"the sample is reproduced", 0.02, false, true},
		{"nothing is concluded", 0.25, false, false},
		{"the sample is not reproduced", 0.44, true, false},
	} {
		t.Run(want.name, func(t *testing.T) {
			pair := compare(Evidence{Anchor: &AnchorComparison{
				RecordingID: uuid.MustParse(firstID), Rate: want.rate,
			}}, recording(firstID, "Karma Police", "Radiohead", 308973))
			if got := pair.anchor == tagmatch.Differs; got != want.saysOtherwise {
				t.Errorf("says otherwise = %v, want %v", got, want.saysOtherwise)
			}
			if got := pair.anchor == tagmatch.Agrees; got != want.agrees {
				t.Errorf("agrees = %v, want %v", got, want.agrees)
			}
		})
	}
}

// A sample found by searching for the want's name is a different witness from
// one fetched by a registration code, and it is different in exactly one
// direction. A plain name-found upload leaves a copy a question, a Topic upload
// admits a copy on the same threshold, and neither may refuse
// one: the upload can be a sibling version of the song, and a wrong reference
// must not throw the right copy away for good (docs/decisions/0029 §3).
func TestAnUploadFoundByNameAdmitsButNeverRefuses(t *testing.T) {
	for _, want := range []struct {
		name    string
		source  string
		rate    float64
		verdict tagmatch.Verdict
	}{
		{"a keyed sample the copy reproduces", "deezer", 0.02, tagmatch.Agrees},
		{"a keyed sample the copy does not reproduce", "deezer", 0.44, tagmatch.Differs},
		{"an upload the copy reproduces", AnchorByName, 0.02, tagmatch.Unknown},
		{"a Topic upload the copy reproduces", AnchorByNameTopic, 0.02, tagmatch.Agrees},
		{"an upload the copy does not reproduce", AnchorByName, 0.44, tagmatch.Unknown},
		{"a Topic upload the copy does not reproduce", AnchorByNameTopic, 0.44, tagmatch.Unknown},
		{"an upload in the dead band", AnchorByName, 0.25, tagmatch.Unknown},
	} {
		t.Run(want.name, func(t *testing.T) {
			pair := compare(Evidence{Anchor: &AnchorComparison{
				RecordingID: uuid.MustParse(firstID), Rate: want.rate, Source: want.source,
			}}, recording(firstID, "Karma Police", "Radiohead", 308973))
			if pair.anchor != want.verdict {
				t.Errorf("anchor = %v, want %v", pair.anchor, want.verdict)
			}
			if got := ReadAnchor(want.source, want.rate); got != want.verdict {
				t.Errorf("ReadAnchor = %v, want %v", got, want.verdict)
			}
		})
	}
}

func TestAPlainNameFoundUploadDoesNotAdmitAnAgreeingCopy(t *testing.T) {
	file := Evidence{Anchor: &AnchorComparison{
		RecordingID: uuid.MustParse(firstID), Rate: 0.02, Source: AnchorByName,
	}}
	known := recording(firstID, "Karma Police", "Radiohead", 308973)

	pair := compare(file, known)
	if pair.anchor != tagmatch.Unknown {
		t.Fatalf("anchor = %v, want a plain name-found upload to leave a question", pair.anchor)
	}
	if level := gradeOf(pair); level.conclusive() {
		t.Fatalf("grade = %v, want no conclusive grade", level)
	}
	// The person still reads that the audio matched the upload: that is the
	// fact they are asked to weigh, and hiding it would leave a bare number.
	agreements, _ := describeVerdicts(pair)
	if len(agreements) != 1 || !strings.Contains(agreements[0], "upload found by name") {
		t.Fatalf("agreements = %v, want the upload match named for the person", agreements)
	}
	if !pair.anchorReproduced {
		t.Fatal("anchorReproduced = false, want the match kept for the row")
	}
	file.Anchor.Rate = 0.44
	if again := compare(file, known); again.anchorReproduced || again.contradicted() {
		t.Fatalf("other audio: reproduced=%v contradicted=%v, want neither", again.anchorReproduced, again.contradicted())
	}
}

func TestATopicUploadAdmitsAnAgreeingCopy(t *testing.T) {
	file := Evidence{Anchor: &AnchorComparison{
		RecordingID: uuid.MustParse(firstID), Rate: 0.02, Source: AnchorByNameTopic,
	}}
	known := recording(firstID, "Karma Police", "Radiohead", 308973)

	pair := compare(file, known)
	if level := gradeOf(pair); level != bySample {
		t.Fatalf("grade = %v, want the Topic upload to prove it", level)
	}
	if outcome := decide(file, []musicbrainz.Recording{known}); outcome.Identity == nil ||
		outcome.Identity.Method != "published-sample" {
		t.Fatalf("identity = %+v, want it recorded as proven by the Topic upload", outcome.Identity)
	}
	file.Anchor.Rate = 0.44
	if compare(file, known).contradicted() {
		t.Fatal("the Topic upload is refusing a copy, want it left for a person")
	}
}

// The other half of the same rule: a copy an upload does not reproduce must not
// be voided by it, even where nothing else in the pair objects. That is what
// keeps the copy a question instead of a discard.
func TestAnUploadThatIsOtherAudioDoesNotVoidThePair(t *testing.T) {
	file := Evidence{
		RecordingID: uuid.MustParse(firstID),
		Artist:      "Radiohead", Album: "OK Computer", Title: "Karma Police",
		DurationMS: 308000,
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.44, Source: AnchorByName,
		},
	}
	known := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")

	pair := compare(file, known)
	if pair.contradicted() {
		t.Fatal("the pair is voided, want an upload found by name to refuse nothing")
	}

	// The same measurement against a keyed sample does void it, which is the
	// difference this test exists to hold.
	file.Anchor.Source = "deezer"
	if !compare(file, known).contradicted() {
		t.Fatal("a distributor's own sample stopped refusing a copy")
	}
}
