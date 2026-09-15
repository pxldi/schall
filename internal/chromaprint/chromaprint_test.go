package chromaprint

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// The two vectors below are real. Each is one piece of audio fingerprinted
// twice by fpcalc 1.6.1 — once in the compressed form Schall stores, once with
// -raw — so the frames are what the tool itself printed and not what this code
// believes it should have printed. They are held here rather than computed
// because fpcalc is not installed on every machine that runs the tests.

// Twelve seconds of audio, whose frames include distances wide enough to use
// the exception section.
const goldenCompressedA = "AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wdaD_y4XikD69yNJeCHzZqFX-E6QT1oxf24GmMM3rwOMWR7PBzpLeC3DmEI0TRPMGDI0eyP_ABny6u4_gvHGGO59ARUzmFXsdbotYS5M0J_Xh2RPqLM4J_MCuPPAr6EIrPCLp4lDy-4ccvnMdUpYaeSEg1sWAkAMMAIgQwAhhBVAoLhjBOIAOBAkw4I4ihBAEECDUOMQWUAM4EIBEWAhEhAQAMMUGMA8oRQAgzAhIDGUQA"

var goldenFramesA = []uint32{
	2860524384, 2864718192, 2856333652, 3137339740, 3103789432, 3091276648,
	4173540968, 4169342568, 3632466536, 3632270937, 3364884043, 3367243338,
	3371437642, 3362003914, 1214732746, 1214380538, 1215430138, 1215428058,
	1215553994, 1226023370, 1224909278, 1258467830, 1258480102, 1258414562,
	1262512098, 1254113122, 1250113890, 1250048354, 1250040177, 1266751825,
	1220548976, 1212185073, 1212185075, 1212250611, 1212250594, 1212250610,
	1212348866, 1212324290, 1212325314, 1212333507, 1237564880, 1237433840,
	1237433840, 1497603536, 1493408912, 1526963344, 4211326160, 4211325136,
	4194547937, 4194482529, 4194478435, 3997343010, 2923613490, 2924725618,
	2924499298, 2940232163, 2906661345, 2889945569, 3166777844, 3168211444,
	3184984532, 4258664780, 4258670413, 4258742109, 4239736411, 3702794841,
	3706993481, 3706986441, 3698614217, 3648282057, 1500859849, 1509382109,
	1509042143, 1507013502, 1507013158,
}

// Eight seconds of different audio, starting three seconds in.
const goldenCompressedB = "AQAAK0mi5EuyJEEW8gd1wsd5eIlOnOmP_njRB86XB9OFC0KZXvAqPKnwdoaTcoEjMsKz482F4zwuTbjooC-e40GoQ9SRcsePP8MH__hiHB81-HB-_Ec3AIKAEA4wAQAhQgADkKCMCaRAA0ZZpqQkghJgHDIOOWUUIwQ"

var goldenFramesB = []uint32{
	1048117487, 2130252278, 2130391518, 2130383358, 2147037694, 1576609502,
	3707453022, 3703250510, 3703246542, 3703258846, 3300400830, 3300406954,
	3300401834, 3300401832, 3321389496, 3321413784, 3347038360, 1161679000,
	1160036472, 1160761624, 1143922968, 338650392, 342844696, 351171864,
	350969624, 359233304, 489256712, 522798856, 522766088, 526960473,
	527026171, 528078298, 494458330, 493606298, 493548954, 426505658,
	434689466, 434688442, 465170874, 465301914, 196801018, 1268441594,
	1268442474,
}

func TestDecodeMatchesWhatFpcalcPrinted(t *testing.T) {
	for name, golden := range map[string]struct {
		compressed string
		frames     []uint32
	}{
		"twelve seconds": {goldenCompressedA, goldenFramesA},
		"eight seconds":  {goldenCompressedB, goldenFramesB},
	} {
		t.Run(name, func(t *testing.T) {
			frames, err := Decode(golden.compressed)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(frames) != len(golden.frames) {
				t.Fatalf("decoded %d frames, fpcalc printed %d", len(frames), len(golden.frames))
			}
			for index, want := range golden.frames {
				if frames[index] != want {
					t.Fatalf("frame %d is %d, fpcalc printed %d", index, frames[index], want)
				}
			}
		})
	}
}

func TestDecodeAcceptsPadding(t *testing.T) {
	padded, err := Decode(goldenCompressedB + "==")
	if err != nil {
		t.Fatalf("decode padded: %v", err)
	}
	if len(padded) != len(goldenFramesB) {
		t.Fatalf("padded decode gave %d frames, want %d", len(padded), len(goldenFramesB))
	}
}

func TestDecodeRefusesWhatIsNotAFingerprint(t *testing.T) {
	for name, value := range map[string]string{
		"empty":            "",
		"not base64":       "!!!!",
		"too short":        "AQA",
		"other algorithm":  "AwAAAQ",
		"stands for no au": "AQAAAA",
		"ends early":       goldenCompressedB[:12],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(value); !errors.Is(err, ErrNotAFingerprint) {
				t.Fatalf("got %v, want ErrNotAFingerprint", err)
			}
		})
	}
}

func TestBitErrorRateIsZeroAgainstItself(t *testing.T) {
	rate, err := BitErrorRate(goldenFramesA, goldenFramesA)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rate != 0 {
		t.Fatalf("a fingerprint differs from itself by %f", rate)
	}
}

func TestBitErrorRateFindsTheReferenceInsideALongerCandidate(t *testing.T) {
	// The candidate is other audio, then the reference, then other audio again,
	// which is the shape of the real operation: a whole file with the previewed
	// thirty seconds somewhere inside it.
	candidate := make([]uint32, 0, len(goldenFramesB)*2+len(goldenFramesA))
	candidate = append(candidate, goldenFramesB...)
	candidate = append(candidate, goldenFramesA...)
	candidate = append(candidate, goldenFramesB...)

	rate, err := BitErrorRate(goldenFramesA, candidate)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rate != 0 {
		t.Fatalf("the reference sits in the candidate exactly, yet measured %f", rate)
	}
	if Read(rate) != SameAudio {
		t.Fatalf("an exact position read as %v", Read(rate))
	}
}

func TestBitErrorRateSeparatesUnrelatedAudio(t *testing.T) {
	rate, err := BitErrorRate(goldenFramesB, goldenFramesA)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rate <= DiscardAbove {
		t.Fatalf("two unrelated pieces of audio measured %f, which is not above %f", rate, DiscardAbove)
	}
	if Read(rate) != OtherAudio {
		t.Fatalf("unrelated audio read as %v", Read(rate))
	}
}

// Re-encoding is what every real comparison survives: the copy was encoded by a
// stranger and the preview was encoded by a distributor. Flipping one bit in
// every frame stands in for that damage and must not move the answer.
func TestBitErrorRateToleratesDamage(t *testing.T) {
	damaged := make([]uint32, len(goldenFramesA))
	for index, frame := range goldenFramesA {
		damaged[index] = frame ^ (1 << (index % 32))
	}
	rate, err := BitErrorRate(goldenFramesA, damaged)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if want := 1.0 / 32.0; math.Abs(rate-want) > 1e-9 {
		t.Fatalf("one wrong bit per frame measured %f, want %f", rate, want)
	}
	if Read(rate) != SameAudio {
		t.Fatalf("damaged but matching audio read as %v", Read(rate))
	}
}

// A candidate too short to hold the reference is an unanswered question, never
// a disagreement. This is the case a caller must not read as a refusal.
func TestBitErrorRateRefusesAShortCandidate(t *testing.T) {
	_, err := BitErrorRate(goldenFramesA, goldenFramesA[:len(goldenFramesA)-1])
	if !errors.Is(err, ErrTooShort) {
		t.Fatalf("got %v, want ErrTooShort", err)
	}
	if _, err := BitErrorRate(nil, goldenFramesA); !errors.Is(err, ErrNotAFingerprint) {
		t.Fatalf("got %v, want ErrNotAFingerprint for an empty reference", err)
	}
}

func TestReadHoldsTheBandForAPerson(t *testing.T) {
	for _, rate := range []float64{AdmitBelow, 0.25, DiscardAbove} {
		if got := Read(rate); got != Undecided {
			t.Fatalf("%f read as %v, want Undecided", rate, got)
		}
	}
	// The worst true pair and the best false pair measured on 2026-08-16 must
	// both land on the side the thresholds were set to put them.
	if got := Read(0.0682); got != SameAudio {
		t.Fatalf("the worst measured true pair read as %v", got)
	}
	if got := Read(0.3521); got != OtherAudio {
		t.Fatalf("the best measured false pair read as %v", got)
	}
	// Two masters of one recording measured 0.140 against the same preview, and
	// both were correct. That is inside the band by design.
	if got := Read(0.140); got != SameAudio {
		t.Fatalf("a second master read as %v, want SameAudio", got)
	}
}

func TestDecodedFingerprintCanBeComparedWithoutRawInput(t *testing.T) {
	// The whole point of Decode: a fingerprint already on record is as good as
	// one computed again, so nothing has to be re-scanned.
	frames, err := Decode(strings.TrimSpace(goldenCompressedA))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rate, err := BitErrorRate(frames, goldenFramesA)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rate != 0 {
		t.Fatalf("a stored fingerprint measured %f against the raw one", rate)
	}
}

// A pinned tail overlaps the last regular window by more than half. It still
// contributes to the worst rate, but it is not another mismatch step.
func TestWindowErrorsDoesNotCountPinnedTailAsAnotherMismatch(t *testing.T) {
	left := make([]uint32, 61)
	right := make([]uint32, len(left))
	for index := 40; index < len(right); index++ {
		right[index] = ^uint32(0)
	}
	worst, run := windowErrors(left, right)
	if run != 1 {
		t.Fatalf("longest mismatch run = %d, want 1", run)
	}
	if worst < WindowMismatchAbove {
		t.Fatalf("worst window BER = %.6f, want a mismatching window", worst)
	}
}

// wholeTrack is a fingerprint long enough to be read from its start: the two
// golden vectors are a few seconds of audio each, and a comparison between two
// files is refused below ten seconds of it.
func wholeTrack() []uint32 {
	frames := make([]uint32, 0, len(goldenFramesA)*2+len(goldenFramesB))
	frames = append(frames, goldenFramesA...)
	frames = append(frames, goldenFramesB...)
	frames = append(frames, goldenFramesA...)
	return frames
}

func TestFromTheStartTwoCopiesOfOneFileAgreeExactly(t *testing.T) {
	rate, err := BitErrorRateFromStart(wholeTrack(), wholeTrack())
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rate != 0 {
		t.Fatalf("one file measured %f against itself", rate)
	}
}

// One rip keeps a moment of the silence before the music and another trims it,
// so the same music starts a second or two later in one of the two files. That
// is the ordinary difference between two copies and must not read as other
// audio.
func TestFromTheStartACopyThatStartsLateStillAgrees(t *testing.T) {
	late := make([]uint32, 0, len(goldenFramesB)+len(wholeTrack()))
	late = append(late, goldenFramesB[:16]...)
	late = append(late, wholeTrack()...)

	rate, err := BitErrorRateFromStart(wholeTrack(), late)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if Read(rate) != SameAudio {
		t.Fatalf("a copy starting two seconds late measured %f, read as %v", rate, Read(rate))
	}
}

// The case this comparison exists for. A long recording that plays the wanted
// track somewhere in the middle — a DJ mix, a radio show — contains the audio
// and is not that recording. Sliding the whole way finds it; reading from the
// start must not.
func TestFromTheStartAudioBuriedInALongerRecordingIsNotAMatch(t *testing.T) {
	buried := make([]uint32, 0, len(goldenFramesB)*10+len(wholeTrack()))
	for range 10 {
		buried = append(buried, goldenFramesB...)
	}
	buried = append(buried, wholeTrack()...)

	sliding, err := BitErrorRate(wholeTrack(), buried)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if sliding != 0 {
		t.Fatalf("the audio sits inside the longer recording exactly, yet measured %f", sliding)
	}

	rate, err := BitErrorRateFromStart(wholeTrack(), buried)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if Read(rate) == SameAudio {
		t.Fatalf("audio buried at 0:40 measured %f, which reads as the same recording", rate)
	}
}

func TestFromTheStartUnrelatedAudioDoesNotAgree(t *testing.T) {
	other := make([]uint32, 0, len(goldenFramesB)*3)
	for range 3 {
		other = append(other, goldenFramesB...)
	}
	rate, err := BitErrorRateFromStart(wholeTrack(), other)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if Read(rate) == SameAudio {
		t.Fatalf("unrelated audio measured %f, which reads as the same recording", rate)
	}
}

// Too little audio in common is an unanswered question. A caller must never
// read it as a disagreement.
func TestFromTheStartTooLittleAudioIsRefused(t *testing.T) {
	_, err := BitErrorRateFromStart(wholeTrack(), goldenFramesB)
	if !errors.Is(err, ErrTooShort) {
		t.Fatalf("got %v, want ErrTooShort", err)
	}
}

func TestFromTheStartAStoredFingerprintNeedsNoRescan(t *testing.T) {
	frames, err := Decode(strings.TrimSpace(goldenCompressedA))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	kept := make([]uint32, 0, len(frames)*2)
	kept = append(kept, frames...)
	kept = append(kept, frames...)

	rate, err := BitErrorRateFromStart(kept, kept)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rate != 0 {
		t.Fatalf("a fingerprint read back off the record measured %f against itself", rate)
	}
}
