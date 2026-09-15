//go:build !ignore

package chromaprint

import (
	"embed"
	"encoding/json"
	"testing"
)

//go:embed testdata/*.json
var auditFingerprints embed.FS

func auditFingerprint(t *testing.T, name string) []uint32 {
	t.Helper()
	data, err := auditFingerprints.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Fingerprint []uint32 `json:"fingerprint"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Fingerprint) == 0 {
		t.Fatalf("%s has no frames", name)
	}
	return payload.Fingerprint
}

func auditComparisonSame(c Comparison, candidateFrames int) bool {
	return Read(c.GlobalBER) == SameAudio && c.WindowsAgree() && c.CoversCandidate(candidateFrames)
}

func TestFullLengthComparisonRejectsChangedTail(t *testing.T) {
	reference := auditFingerprint(t, "reference-full")
	tail := auditFingerprint(t, "different-tail-full")
	comparison, err := CompareFromStart(reference, tail)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("changed tail: global BER=%.6f worst window BER=%.6f mismatch run=%d frames=%d/%d",
		comparison.GlobalBER, comparison.WorstWindowBER, comparison.LongestMismatchRun,
		comparison.RightCoverageFrames, len(tail))
	if auditComparisonSame(comparison, len(tail)) {
		t.Fatal("changed tail was treated as the same audio")
	}
	if comparison.WorstWindowBER < WindowMismatchAbove {
		t.Fatalf("worst window BER = %.6f, want a mismatching tail window", comparison.WorstWindowBER)
	}
}

func TestFullLengthComparisonDoesNotProveTwoSecondMute(t *testing.T) {
	reference := auditFingerprint(t, "reference-full")
	muted := auditFingerprint(t, "two-second-mute-full")
	comparison, err := CompareFromStart(reference, muted)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("two-second mute: global BER=%.6f worst window BER=%.6f mismatch run=%d",
		comparison.GlobalBER, comparison.WorstWindowBER, comparison.LongestMismatchRun)
	if auditComparisonSame(comparison, len(muted)) {
		t.Fatal("two-second mute was treated as proven")
	}
}

func TestPrefixComparisonReportsUnverifiedRemainder(t *testing.T) {
	prefix := auditFingerprint(t, "reference-prefix")
	tail := auditFingerprint(t, "different-tail-full")
	comparison, err := CompareFromStart(prefix, tail)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.RightCoverageFrames >= len(tail) || comparison.CoversCandidate(len(tail)) {
		t.Fatalf("coverage = %d/%d, want the tail left unverified",
			comparison.RightCoverageFrames, len(tail))
	}
	if comparison.GlobalBER != 0 || !comparison.WindowsAgree() {
		t.Fatalf("prefix comparison = %+v, want the observed prefix to match", comparison)
	}
}

func TestFullLengthComparisonAgreesWithItself(t *testing.T) {
	reference := auditFingerprint(t, "reference-full")
	comparison, err := CompareFromStart(reference, reference)
	if err != nil {
		t.Fatal(err)
	}
	if !auditComparisonSame(comparison, len(reference)) {
		t.Fatalf("self comparison = %+v, want SameAudio", comparison)
	}

}

// A shift may not shorten the compared span. Two equal-length fingerprints whose
// first forty frames differ must not agree by sliding one along the other, and a
// candidate that merely starts late still aligns over the whole reference.
func TestAShiftCannotHideADifferentOpening(t *testing.T) {
	reference := make([]uint32, 120)
	for index := range reference {
		reference[index] = uint32(index * 7919)
	}
	changedOpening := make([]uint32, 120)
	for index := range changedOpening {
		if index < startShiftFrames {
			changedOpening[index] = 0xaaaaaaaa
		} else {
			changedOpening[index] = reference[index-startShiftFrames]
		}
	}
	comparison, err := CompareFromStart(reference, changedOpening)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.ComparedSpanFrames != len(reference) {
		t.Fatalf("compared %d of %d frames", comparison.ComparedSpanFrames, len(reference))
	}
	if comparison.WindowsAgree() || Read(comparison.GlobalBER) == SameAudio {
		t.Fatalf("a different opening agreed: global %.3f worst window %.3f",
			comparison.GlobalBER, comparison.WorstWindowBER)
	}

	lateStart := append(make([]uint32, 20), reference...)
	comparison, err = CompareFromStart(reference, lateStart)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.AlignedOffsetFrames != 20 || comparison.ComparedSpanFrames != len(reference) ||
		comparison.GlobalBER != 0 || !comparison.WindowsAgree() {
		t.Fatalf("a late start did not align over the whole reference: %+v", comparison)
	}
}
