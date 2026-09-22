package identity

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
)

// A want with no recording is judged on its anchor alone (ADR 0037 §3). These
// tests pin what that judgement may conclude.

// entryForAnchor is what somebody asked for, as a playlist wrote it.
var entryForAnchor = Evidence{
	Subject: SubjectEntry, Artist: "LONOWN, Riserayss", Title: "worry - ultra slowed",
	ISRC: "QZES82415069", DurationMS: 255000,
}

// copyMeasuredAt is a copy whose tags say what the entry says, measured at
// rate against an anchor from source.
func copyMeasuredAt(source string, rate float64) Evidence {
	return Evidence{
		Artist: "LONOWN", Title: "worry (ultra slowed)", ISRC: "QZES82415069",
		DurationMS: 255400,
		Anchor: &AnchorComparison{
			Rate: rate, Source: source,
			Comparison: &chromaprint.Comparison{
				ComparedSpanFrames: 240, LeftCoverageFrames: 240, RightCoverageFrames: 240,
				WorstWindowBER: rate, GlobalBER: rate,
			},
			CandidateCoverageSeconds: 255, CandidateDurationMS: 255400,
		},
	}
}

func TestSameAudioAsAnAdmittingAnchorAdmits(t *testing.T) {
	for _, source := range []string{AnchorSourceDeezer, AnchorByNameTopic} {
		verdict := VerifyAgainstAnchor(copyMeasuredAt(source, 0.04), entryForAnchor)
		if !verdict.Admitted || !verdict.AnchorAgrees {
			t.Errorf("%s: verdict = %+v, want the copy admitted", source, verdict)
		}
	}
}

// Tags that agree admit nothing. The Deezer preview refuses other audio,
// whatever the tags say.
func TestTagsThatAgreeDoNotAdmitOtherAudio(t *testing.T) {
	verdict := VerifyAgainstAnchor(copyMeasuredAt(AnchorSourceDeezer, 0.45), entryForAnchor)
	if verdict.Admitted || !verdict.Refused {
		t.Fatalf("verdict = %+v, want other audio refused", verdict)
	}

	topic := VerifyAgainstAnchor(copyMeasuredAt(AnchorByNameTopic, 0.45), entryForAnchor)
	if topic.Admitted || topic.Refused {
		t.Fatalf("verdict = %+v, want a name-found upload neither to admit nor to refuse", topic)
	}

	between := VerifyAgainstAnchor(copyMeasuredAt(AnchorSourceDeezer, 0.25), entryForAnchor)
	if between.Admitted || between.Refused {
		t.Fatalf("verdict = %+v, want the band between the thresholds held", between)
	}
}

// A plain name-found upload never admits (ADR 0034), and no anchor at all is
// silence.
func TestAnAnchorThatCannotAdmitAdmitsNothing(t *testing.T) {
	if verdict := VerifyAgainstAnchor(copyMeasuredAt(AnchorByName, 0.01), entryForAnchor); verdict.Admitted {
		t.Errorf("verdict = %+v, want a name-found upload to admit nothing", verdict)
	}
	unmeasured := copyMeasuredAt(AnchorSourceDeezer, 0.01)
	unmeasured.Anchor = nil
	if verdict := VerifyAgainstAnchor(unmeasured, entryForAnchor); verdict.Admitted || verdict.Refused {
		t.Errorf("verdict = %+v, want no measurement to decide nothing", verdict)
	}
}

// A contradiction holds a copy whatever the audio says.
func TestAContradictionHoldsACopyTheAnchorAgreesWith(t *testing.T) {
	for name, change := range map[string]func(*Evidence){
		"artist":   func(file *Evidence) { file.Artist = "Somebody Else" },
		"title":    func(file *Evidence) { file.Title = "worry" },
		"isrc":     func(file *Evidence) { file.ISRC = "GBAAA9400123" },
		"duration": func(file *Evidence) { file.DurationMS = 201000 },
	} {
		file := copyMeasuredAt(AnchorSourceDeezer, 0.04)
		change(&file)
		verdict := VerifyAgainstAnchor(file, entryForAnchor)
		if verdict.Admitted || !verdict.Contradicted || verdict.Refused {
			t.Errorf("%s: verdict = %+v, want the copy held on the contradiction", name, verdict)
		}
	}
}

// An absent tag is silence, never a contradiction.
func TestAbsentTagsDoNotHoldACopyTheAnchorAgreesWith(t *testing.T) {
	file := copyMeasuredAt(AnchorSourceDeezer, 0.04)
	file.Artist, file.Title, file.ISRC, file.DurationMS = "", "", "", 0
	if verdict := VerifyAgainstAnchor(file, entryForAnchor); !verdict.Admitted {
		t.Errorf("verdict = %+v, want silence to hold nothing back", verdict)
	}
}

// A MusicBrainz recording ID on the copy has nothing to agree with. The copy is
// held, because MusicBrainz knowing the file means resolution has not caught up.
func TestARecordingIDOnTheCopyHoldsIt(t *testing.T) {
	file := copyMeasuredAt(AnchorSourceDeezer, 0.04)
	file.RecordingID = uuid.New()
	verdict := VerifyAgainstAnchor(file, entryForAnchor)
	if verdict.Admitted || !verdict.NamesARecording {
		t.Errorf("verdict = %+v, want the copy held", verdict)
	}
}

// The excerpt from an address a person keyed the want to admits the same audio
// and refuses other audio, as a Deezer preview does (ADR 0038 §3). Tags that
// agree admit nothing.
func TestAKeyedExcerptAdmitsOnlyTheAudioItReproduces(t *testing.T) {
	same := VerifyAgainstAnchor(copyMeasuredAt(AnchorSourceKeyed, 0.04), entryForAnchor)
	if !same.Admitted || !same.AnchorAgrees {
		t.Fatalf("verdict = %+v, want the same audio admitted", same)
	}

	other := VerifyAgainstAnchor(copyMeasuredAt(AnchorSourceKeyed, 0.45), entryForAnchor)
	if other.Admitted || !other.Refused {
		t.Fatalf("verdict = %+v, want other audio refused although every tag agrees", other)
	}

	between := VerifyAgainstAnchor(copyMeasuredAt(AnchorSourceKeyed, 0.25), entryForAnchor)
	if between.Admitted || between.Refused {
		t.Fatalf("verdict = %+v, want the band between the thresholds held", between)
	}

	unmeasured := copyMeasuredAt(AnchorSourceKeyed, 0.04)
	unmeasured.Anchor = nil
	if verdict := VerifyAgainstAnchor(unmeasured, entryForAnchor); verdict.Admitted {
		t.Fatalf("verdict = %+v, want agreeing tags alone to admit nothing", verdict)
	}

	contradicted := copyMeasuredAt(AnchorSourceKeyed, 0.04)
	contradicted.Title = "worry"
	if verdict := VerifyAgainstAnchor(contradicted, entryForAnchor); verdict.Admitted || !verdict.Contradicted {
		t.Fatalf("verdict = %+v, want a contradiction to hold the copy", verdict)
	}
}
