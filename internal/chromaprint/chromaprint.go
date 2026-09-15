// Package chromaprint compares two acoustic fingerprints directly, without
// asking anybody who the audio belongs to.
//
// A fingerprint here is the same thing AcoustID uses. The tool `fpcalc` reads
// the decoded audio of a file and emits about eight 32-bit numbers a second,
// each describing what the sound was like at that moment. Two recordings of the
// same performance produce nearly the same numbers even after the audio has
// been re-encoded, so the numbers can be compared.
//
// Everywhere else in Schall a fingerprint is sent to AcoustID, which answers
// with the MusicBrainz recordings it belongs to. That answer only exists for
// music somebody has already submitted. This package answers a narrower
// question that needs no service and no catalogue entry: are these two pieces
// of audio the same piece of audio. It is used where one side is a reference
// whose identity is already known — a distributor's thirty-second preview
// fetched by ISRC, or a library file AcoustID has already settled — and the
// other side is a copy whose identity is in question.
//
// See docs/decisions/0024-a-distributors-preview-is-an-audio-anchor.md. Nothing
// in this package decides anything on its own; it measures, and the caller
// applies the rule.
package chromaprint

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
)

// ErrNotAFingerprint reports that the text handed in is not a compressed
// Chromaprint fingerprint. It is a fact about the bytes and retrying will not
// change it.
var ErrNotAFingerprint = errors.New("the value is not a Chromaprint fingerprint")

// ErrTooShort reports that the candidate holds less audio than the reference,
// so there is no position at which the reference sits wholly inside it and
// nothing can be measured. It is never evidence that the two disagree.
var ErrTooShort = errors.New("the candidate is shorter than the reference")

// The one algorithm Chromaprint has ever emitted, and the one whose frame
// layout is decoded below. A different byte here means a format this code has
// not been told about, which is refused rather than guessed at.
const fingerprintAlgorithm = 1

// How the compressed form packs its frames. Each frame is a run of small
// numbers ended by a zero: every number is the distance to the next set bit in
// that frame. Numbers are stored three bits wide, and a three-bit field cannot
// hold more than 7, so 7 means "the rest of this number is in the exception
// section", which is a second run of five-bit numbers starting at the next
// whole byte.
const (
	normalBits     = 3
	exceptionBits  = 5
	normalMaxValue = 1<<normalBits - 1
)

// Decode turns the compressed fingerprint AcoustID and Schall store back into
// the frames it was built from.
//
// This matters because the two forms are not interchangeable. The compressed
// form is a bit stream whose length depends on the audio, so two compressed
// fingerprints cannot be compared against each other at all. The frames can.
// The round trip is exact: a fingerprint decoded here holds the same numbers
// `fpcalc -raw` printed for the same audio, so a fingerprint already on record
// is as good as one computed again, and nothing has to be re-scanned to use it.
func Decode(value string) ([]uint32, error) {
	// fpcalc emits the URL-safe alphabet, sometimes padded and sometimes not.
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(value), "="))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAFingerprint, err)
	}
	if len(raw) < 4 {
		return nil, fmt.Errorf("%w: %d bytes is too few for a header", ErrNotAFingerprint, len(raw))
	}
	if raw[0] != fingerprintAlgorithm {
		return nil, fmt.Errorf("%w: algorithm %d is not %d", ErrNotAFingerprint, raw[0], fingerprintAlgorithm)
	}
	frameCount := int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	if frameCount <= 0 {
		return nil, fmt.Errorf("%w: it stands for no audio", ErrNotAFingerprint)
	}

	reader := &bitReader{data: raw[4:]}

	// The first run holds one number per set bit plus a zero to end each frame,
	// so it is read until as many zeroes have been seen as there are frames.
	distances := make([]int, 0, frameCount*8)
	for complete := 0; complete < frameCount; {
		value, ok := reader.read(normalBits)
		if !ok {
			return nil, fmt.Errorf("%w: it ends part way through frame %d", ErrNotAFingerprint, complete+1)
		}
		if value == 0 {
			complete++
		}
		distances = append(distances, int(value))
	}

	// The exception section starts at the next whole byte, and holds the
	// remainder of every number that did not fit in three bits, in order.
	reader.align()
	for index, distance := range distances {
		if distance != normalMaxValue {
			continue
		}
		extra, ok := reader.read(exceptionBits)
		if !ok {
			return nil, fmt.Errorf("%w: a wide value has no remainder", ErrNotAFingerprint)
		}
		distances[index] += int(extra)
	}

	// Each frame is rebuilt by setting the bit each distance points at, and is
	// then stored as the difference from the frame before it, which is how the
	// compressed form keeps consecutive frames small.
	frames := make([]uint32, 0, frameCount)
	var frame uint32
	position := 0
	for _, distance := range distances {
		if distance == 0 {
			if len(frames) > 0 {
				frame ^= frames[len(frames)-1]
			}
			frames = append(frames, frame)
			frame, position = 0, 0
			continue
		}
		position += distance
		if position > 32 {
			return nil, fmt.Errorf("%w: a frame sets bit %d of 32", ErrNotAFingerprint, position)
		}
		frame |= 1 << (position - 1)
	}
	return frames, nil
}

// BitErrorRate reports how much of the reference is not in the candidate, as a
// share of the bits compared, at the best position the reference can sit in.
// Zero means the two agree on every bit; a half means they agree no more than
// two unrelated pieces of audio would.
//
// The reference must be the shorter side and the candidate the whole file.
// Order is not a convention here, it is the measurement: the reference is slid
// across the candidate and read only where it sits wholly inside, so the
// candidate has to be able to contain it. Two thirty-second previews of the
// same track compared against each other score as badly as strangers, because
// each distributor cuts its excerpt from a different part of the song and there
// is no true position to find.
//
// This also fixes what the candidate has to be fingerprinted from. `fpcalc`
// reads only the opening two minutes of a file unless it is told otherwise, and
// a preview is often cut from the middle of a song. A candidate fingerprinted
// at that default stops one minute in, the true position is not inside it, and
// a correct copy measures like a stranger. A candidate fingerprint must
// therefore cover the whole file — see CandidateLengthSeconds — and a caller
// that cannot promise that must not read a high rate as disagreement.

// Compare aligns a shorter reference anywhere in a candidate and measures the
// overlapping windows at the best global alignment. It is the excerpt comparison
// and intentionally differs from CompareFromStart.
func Compare(reference, candidate []uint32) (Comparison, error) {
	if len(reference) == 0 {
		return Comparison{}, fmt.Errorf("%w: the reference is empty", ErrNotAFingerprint)
	}
	if len(candidate) < len(reference) {
		return Comparison{}, fmt.Errorf("%w: %d frames against %d", ErrTooShort,
			len(candidate), len(reference))
	}
	best := Comparison{GlobalBER: math.Inf(1)}
	for offset := 0; offset+len(reference) <= len(candidate); offset++ {
		differing := 0
		for index, value := range reference {
			differing += bits.OnesCount32(value ^ candidate[offset+index])
		}
		global := float64(differing) / float64(len(reference)*32)
		if global >= best.GlobalBER {
			continue
		}
		best = Comparison{
			AlignedOffsetFrames: offset,
			ComparedSpanFrames:  len(reference),
			LeftCoverageFrames:  len(reference),
			RightCoverageFrames: len(reference),
			GlobalBER:           global,
		}
		best.WorstWindowBER, best.LongestMismatchRun = windowErrors(
			reference, candidate[offset:offset+len(reference)])
	}
	return best, nil
}

func BitErrorRate(reference, candidate []uint32) (float64, error) {
	if len(reference) == 0 {
		return 0, fmt.Errorf("%w: the reference is empty", ErrNotAFingerprint)
	}
	if len(candidate) < len(reference) {
		return 0, fmt.Errorf("%w: %d frames against %d", ErrTooShort, len(candidate), len(reference))
	}

	best := len(reference)*32 + 1
	for offset := 0; offset+len(reference) <= len(candidate); offset++ {
		differing := 0
		for index, value := range reference {
			differing += bits.OnesCount32(value ^ candidate[offset+index])
			if differing >= best {
				break
			}
		}
		if differing < best {
			best = differing
		}
	}
	return float64(best) / float64(len(reference)*32), nil
}

// startShiftFrames is how far the two sides of a from-the-start comparison may
// be out of step with each other and still be read as starting together.
//
// A frame is about an eighth of a second, so forty frames is about five seconds.
// It is there for the ordinary difference between two copies of one track: a rip
// that kept a moment of the silence before the music, and one that trimmed it.
const startShiftFrames = 40

// Comparison is the measurement from one aligned pair of fingerprints. The
// coverage fields count frames in each input, so a caller can tell a complete
// comparison from a prefix comparison even when their decoded durations agree.
// The rates are kept separately because a global average can hide a local edit.
type Comparison struct {
	AlignedOffsetFrames int     `json:"alignedOffsetFrames"`
	ComparedSpanFrames  int     `json:"comparedSpanFrames"`
	LeftCoverageFrames  int     `json:"leftCoverageFrames"`
	RightCoverageFrames int     `json:"rightCoverageFrames"`
	WorstWindowBER      float64 `json:"worstWindowBER"`
	LongestMismatchRun  int     `json:"longestMismatchRun"`
	GlobalBER           float64 `json:"globalBER"`
}

const (
	// fpcalc emits about eight frames a second. Five seconds is long enough to
	// tolerate codec damage while still isolating an edit in the supplied
	// changed-tail and two-second-mute signals. The 0.20 floor is the existing
	// measured admission floor; the current corpus's worst true pair was 0.140.
	comparisonWindowFrames = 40
	comparisonWindowStep   = 20
	WindowMismatchAbove    = AdmitBelow
)

// WindowsAgree reports whether every complete comparison window is below the
// local mismatch floor. It does not say that the pair has enough coverage to
// prove the candidate.
func (comparison Comparison) WindowsAgree() bool {
	return comparison.WorstWindowBER < WindowMismatchAbove
}

func (comparison Comparison) CoversCandidate(candidateFrames int) bool {
	return candidateFrames > 0 &&
		comparison.RightCoverageFrames >= candidateFrames-startShiftFrames
}

// MinimumComparableFrames is the least audio two fingerprints must share before
// a from-the-start comparison is worth making. Eight frames is about a second,
// so this is about ten seconds. Below it the two hold too little music in common
// for the number to mean anything, and a comparison nobody could read is refused
// rather than reported.
const MinimumComparableFrames = 80

// BitErrorRateFromStart compares two fingerprints that begin at the same point
// in the music: two whole files, rather than an excerpt against a file.
//
// It answers a different question from BitErrorRate, and that difference is why
// it exists. BitErrorRate slides the reference across the whole candidate and
// reports the best position anywhere inside it, which answers "does this file
// contain that audio" — true of an hour-long DJ mix that plays the track twenty
// minutes in. Two copies of one recording both start at the start, so this reads
// them from their beginnings and allows only a few seconds of drift between them
// (startShiftFrames). That answers "is this file that audio".
//
// The two are read over as much audio as both of them hold. A fingerprint kept
// for a library file covers the opening two minutes, because that is all
// `fpcalc` reads unless it is told otherwise, so a copy fingerprinted for its
// whole length is compared over that opening alone. That is sound between two
// files and it is not sound against an excerpt, which is cut from wherever in
// the song the distributor liked — BitErrorRate is what an excerpt is read with.
//
// ErrTooShort means the two hold too little audio in common to be compared. It
// is never evidence that they disagree.
func BitErrorRateFromStart(left, right []uint32) (float64, error) {
	comparison, err := CompareFromStart(left, right)
	if err != nil {
		return 0, err
	}
	return comparison.GlobalBER, nil
}

// CompareFromStart aligns two whole-file fingerprints within the existing
// bounded start-shift search and checks overlapping windows across the chosen
// span. The reference and candidate are still read from their starts; this is
// not the sliding comparison used for a distributor excerpt.
func CompareFromStart(left, right []uint32) (Comparison, error) {
	if len(left) == 0 || len(right) == 0 {
		return Comparison{}, fmt.Errorf("%w: an input is empty", ErrNotAFingerprint)
	}
	best := Comparison{GlobalBER: math.Inf(1)}
	for offset := -startShiftFrames; offset <= startShiftFrames; offset++ {
		leftStart, rightStart := 0, offset
		if offset < 0 {
			leftStart, rightStart = -offset, 0
		}
		if leftStart >= len(left) || rightStart >= len(right) {
			continue
		}
		span := min(len(left)-leftStart, len(right)-rightStart)
		if span < MinimumComparableFrames {
			continue
		}
		// A shift may only run into the longer side's extra frames. Letting the
		// compared span shrink instead would leave up to startShiftFrames
		// unverified at each end of two equal-length files, and the audit's
		// changed-opening case would pass on the rest.
		if span < min(len(left), len(right)) {
			continue
		}
		differing := 0
		for index := 0; index < span; index++ {
			differing += bits.OnesCount32(left[leftStart+index] ^ right[rightStart+index])
		}
		global := float64(differing) / float64(span*32)
		if global > best.GlobalBER ||
			(global == best.GlobalBER && absInt(offset) >= absInt(best.AlignedOffsetFrames)) {
			continue
		}
		best = Comparison{
			AlignedOffsetFrames: offset,
			ComparedSpanFrames:  span,
			LeftCoverageFrames:  span,
			RightCoverageFrames: span,
			GlobalBER:           global,
		}
		best.WorstWindowBER, best.LongestMismatchRun = windowErrors(
			left[leftStart:leftStart+span], right[rightStart:rightStart+span])
	}
	if math.IsInf(best.GlobalBER, 1) {
		return Comparison{}, fmt.Errorf("%w: %d frames of audio in common",
			ErrTooShort, min(len(left), len(right)))
	}
	return best, nil
}

func windowErrors(left, right []uint32) (float64, int) {
	worst := 0.0
	longest, run := 0, 0
	windows := 0
	lastStart := -1
	for start := 0; start+comparisonWindowFrames <= len(left); start += comparisonWindowStep {
		lastStart = start
		windows++
		rate := windowBER(left[start:start+comparisonWindowFrames], right[start:start+comparisonWindowFrames])
		if rate > worst {
			worst = rate
		}
		if rate >= WindowMismatchAbove {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	if lastStart != len(left)-comparisonWindowFrames {
		start := len(left) - comparisonWindowFrames
		if start >= 0 && start != lastStart {
			windows++
			rate := windowBER(left[start:start+comparisonWindowFrames], right[start:start+comparisonWindowFrames])
			if rate > worst {
				worst = rate
			}
			if rate >= WindowMismatchAbove && run == 0 {
				longest = 1
			}
		}
	}
	if windows == 0 {
		return windowBER(left, right), 0
	}
	return worst, longest
}

func windowBER(left, right []uint32) float64 {
	differing := 0
	for index := range left {
		differing += bits.OnesCount32(left[index] ^ right[index])
	}
	return float64(differing) / float64(len(left)*32)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// How long each side is read for, in seconds, when a fingerprint is computed
// for this comparison. They are passed to `fpcalc -length`.
//
// The reference is a distributor's preview, which is thirty seconds; sixty
// gives it room and costs nothing. The candidate is read long enough to hold a
// whole track, because a candidate that stops early cannot be read as
// disagreement — see BitErrorRate.
const (
	ReferenceLengthSeconds = 60
	CandidateLengthSeconds = 900
)

// The two thresholds the comparison is read at, measured on 2026-08-16 against
// the copies waiting in the review queue and again against library files whose
// identity AcoustID had already settled. Across both runs a true pair never
// scored worse than 0.0682 and a false pair never scored better than 0.3521.
//
// The band between them is wide on purpose. One want in that run held eleven
// copies of the same recording in two groups, at 0.042 and at 0.140, because
// two different masters of one recording were in circulation and both were
// correct. A threshold set close to the worst true pair would have refused one
// of them. Nothing is admitted or discarded inside the band; a person decides.
const (
	AdmitBelow   = 0.20
	DiscardAbove = 0.30
)

// Verdict is what a measured rate means. It is the three outcomes of
// docs/decisions/0024 and there is deliberately no fourth and no score: a
// number between the thresholds is a question for a person, not a weaker yes.
type Verdict int

const (
	// Undecided is the answer whenever nothing was proven, which includes a
	// rate inside the band, a missing reference and a comparison that could not
	// run. It is never read as agreement.
	Undecided Verdict = iota
	// SameAudio means the candidate holds the reference audio.
	SameAudio
	// OtherAudio means the candidate holds some other audio.
	OtherAudio
)

// Read turns a measured rate into one of the three outcomes.
func Read(rate float64) Verdict {
	switch {
	case rate < AdmitBelow:
		return SameAudio
	case rate > DiscardAbove:
		return OtherAudio
	default:
		return Undecided
	}
}

// bitReader hands out small values from a byte slice, lowest bit of each byte
// first, which is the order Chromaprint wrote them in.
type bitReader struct {
	data      []byte
	next      int
	buffer    uint32
	available uint
}

func (reader *bitReader) read(width uint) (uint32, bool) {
	for reader.available < width {
		if reader.next >= len(reader.data) {
			return 0, false
		}
		reader.buffer |= uint32(reader.data[reader.next]) << reader.available
		reader.next++
		reader.available += 8
	}
	value := reader.buffer & (1<<width - 1)
	reader.buffer >>= width
	reader.available -= width
	return value, true
}

// align moves to the start of the next whole byte, throwing away the bits left
// over in the one being read. Values are never wider than a byte, so what is
// thrown away is always the tail of the byte just used.
func (reader *bitReader) align() {
	reader.buffer, reader.available = 0, 0
}
