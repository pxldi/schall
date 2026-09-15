package db

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Which of two copies of one recording is the lesser one.
//
// This decides nothing about music. That two files hold the same recording was
// already proven elsewhere, by a recording identifier or by a decision somebody
// made, and this is only ever asked about files that came out of that proof. So
// nothing here can produce a false match; what it can produce is a wrong
// recommendation to delete, which is the failure it is written against.
//
// The shape is the same as everywhere else: it compares stored facts, and where
// the stored facts do not separate two files it refuses and says so. There is
// no scoring, no threshold and no preference for the copy that happens to be
// first. A copy is redundant only when another copy is at least as good in every
// respect the library can measure and better in at least one — and when it is
// also no worse a fit for the catalogue, because the point of keeping music is
// to be able to find it again.

// durationSlack is how far two lengths may differ and still be the same piece of
// music, the same five seconds matching allows. Beyond it these are not two
// copies of one thing but two different files that happen to answer to one
// recording — an edit, a fade, a track with the next one running into it — and
// neither is the lesser version of the other.
const durationSlack = 5000

// A container tells you what a stream cannot be, not what it is. FLAC is
// lossless; MP3 is not; .m4a is either AAC or ALAC and nothing stored says
// which, so it is compared only against another .m4a.
var (
	losslessContainers = map[string]bool{
		".flac": true, ".wav": true, ".aiff": true, ".aif": true, ".dsf": true,
	}
	lossyContainers = map[string]bool{
		".mp3": true, ".ogg": true, ".oga": true, ".opus": true,
	}
)

type family int

const (
	familyUnknown family = iota
	familyLossless
	familyLossy
)

func familyOf(path string) family {
	extension := strings.ToLower(filepath.Ext(path))
	switch {
	case losslessContainers[extension]:
		return familyLossless
	case lossyContainers[extension]:
		return familyLossy
	default:
		return familyUnknown
	}
}

type comparison int

const (
	// incomparable is the answer that matters: nothing stored puts these two in
	// an order, so neither may be offered up for deletion.
	incomparable comparison = iota
	equivalent
	superior
	inferior
)

// judge names the copy of one recording worth keeping, marks the rest, and says
// in one sentence why.
//
// A keeper has to beat every other copy, not merely the next one along. Three
// copies where the best two are indistinguishable have no keeper, because
// deleting one of a tie is a choice nobody made.
func judge(copies []HeldTwiceCopy) (string, bool) {
	if len(copies) < 2 {
		return "", false
	}
	for _, copied := range copies {
		if copied.byHand {
			return "One of these copies was matched by hand. Schall does not " +
				"weigh up a decision somebody already made.", false
		}
	}

	keeper := -1
	for candidate := range copies {
		beatsAll := true
		for other := range copies {
			if candidate == other {
				continue
			}
			if !supersedes(copies[candidate], copies[other]) {
				beatsAll = false
				break
			}
		}
		if beatsAll {
			keeper = candidate
			break
		}
	}
	if keeper < 0 {
		// Explained against the first pair. For the ordinary group of two that
		// is the whole story; for a larger one it is one true reason out of
		// several rather than a summary of all of them.
		return cannotSeparate(copies[0], copies[1]), false
	}

	copies[keeper].Keeper = true
	worst := 0
	for index := range copies {
		if index == keeper {
			continue
		}
		copies[index].Redundant = true
		worst = index
	}
	return whyKeep(copies[keeper], copies[worst]), true
}

// supersedes reports whether one copy can stand in for another entirely.
//
// Quality decides it, and where quality is equal the catalogue does: of two
// files that are the same audio, the one matched to a track is the one the
// library can find again. A better copy that is a worse fit supersedes nothing,
// because trading a matched file for an unmatched one is losing something.
func supersedes(keeper, loser HeldTwiceCopy) bool {
	switch compareAudio(keeper, loser) {
	case superior:
		return fit(keeper) >= fit(loser)
	case equivalent:
		return fit(keeper) > fit(loser)
	default:
		return false
	}
}

// fit is how well the library holds this copy: matched to a catalogue track, or
// only ever identified as the recording.
func fit(copied HeldTwiceCopy) int {
	if copied.mapped {
		return 2
	}
	return 1
}

// compareAudio puts two streams in an order, or declines to.
func compareAudio(first, second HeldTwiceCopy) comparison {
	if !listened(first) || !listened(second) {
		return incomparable
	}
	// Same recording, different lengths: one of these is not a copy of the
	// other in any sense that would make deleting it safe.
	if first.measured == 0 || second.measured == 0 {
		return incomparable
	}
	if abs(first.measured-second.measured) > durationSlack {
		return incomparable
	}

	firstFamily, secondFamily := familyOf(first.Path), familyOf(second.Path)
	if sameContainer(first.Path, second.Path) {
		// One container is one codec, whichever codec it turns out to be, so
		// the numbers are being compared against numbers of the same kind.
		//
		// For two lossless files this can prefer the one that was compressed
		// less: same audio, more bytes. That is the safe side of the mistake —
		// what survives is still every sample the recording had — and it is the
		// same comparison that correctly keeps 24-bit over 16-bit, which is
		// worth much more than the disc space it sometimes costs.
		return dominance(
			[]int{value(first.BitRateKbps), value(first.SampleRateHz), value(first.Channels)},
			[]int{value(second.BitRateKbps), value(second.SampleRateHz), value(second.Channels)},
		)
	}
	if firstFamily == familyUnknown || secondFamily == familyUnknown {
		return incomparable
	}
	switch {
	case firstFamily == familyLossless && secondFamily == familyLossy:
		return superior
	case firstFamily == familyLossy && secondFamily == familyLossless:
		return inferior
	case firstFamily == familyLossy:
		// Two lossy files in different containers are two different codecs, and
		// 256 kbps of one is not 256 kbps of the other.
		return incomparable
	}
	// Both lossless in different containers: the compression is not the audio,
	// so the bitrate says nothing here and only the stream's shape is compared.
	return dominance(
		[]int{value(first.SampleRateHz), value(first.Channels)},
		[]int{value(second.SampleRateHz), value(second.Channels)},
	)
}

// dominance answers by every term at once. One value higher and another lower is
// not a better copy, it is a different one.
func dominance(first, second []int) comparison {
	atLeast, atMost := true, true
	for index := range first {
		if first[index] < second[index] {
			atLeast = false
		}
		if first[index] > second[index] {
			atMost = false
		}
	}
	switch {
	case atLeast && atMost:
		return equivalent
	case atLeast:
		return superior
	case atMost:
		return inferior
	default:
		return incomparable
	}
}

// listened reports whether anything has read this file's audio. A sample rate is
// what every readable stream has; without it the row is silent about quality,
// and silence is never agreement here either.
func listened(copied HeldTwiceCopy) bool {
	return copied.SampleRateHz != nil && *copied.SampleRateHz > 0
}

func sameContainer(first, second string) bool {
	return strings.EqualFold(filepath.Ext(first), filepath.Ext(second))
}

func value(number *int32) int {
	if number == nil {
		return 0
	}
	return int(*number)
}

func abs(number int) int {
	if number < 0 {
		return -number
	}
	return number
}

// whyKeep says what makes the keeper the better copy, naming the thing that
// actually decided it rather than listing everything that agreed.
func whyKeep(keeper, loser HeldTwiceCopy) string {
	keeperFamily, loserFamily := familyOf(keeper.Path), familyOf(loser.Path)
	switch {
	case keeperFamily == familyLossless && loserFamily == familyLossy:
		return fmt.Sprintf("Keep the %s: it is lossless and the %s is not.",
			label(keeper), label(loser))
	// Only where the containers match, because that is the only comparison a
	// bitrate took part in.
	case sameContainer(keeper.Path, loser.Path) &&
		value(keeper.BitRateKbps) > value(loser.BitRateKbps):
		return fmt.Sprintf("Keep the %d kbps copy: the other is %d kbps.",
			value(keeper.BitRateKbps), value(loser.BitRateKbps))
	case value(keeper.SampleRateHz) > value(loser.SampleRateHz):
		return fmt.Sprintf("Keep the %s copy: the other is %s.",
			kilohertz(value(keeper.SampleRateHz)), kilohertz(value(loser.SampleRateHz)))
	case value(keeper.Channels) > value(loser.Channels):
		return fmt.Sprintf("Keep the %d-channel copy: the other has %d.",
			value(keeper.Channels), value(loser.Channels))
	default:
		return "Both copies are the same audio. Keep the one matched to a " +
			"catalogue track: the other is only identified as this recording."
	}
}

// cannotSeparate says why Schall will not choose, in terms of the thing that
// stopped it. Every branch here is a refusal, and each one names what would
// have to be true instead.
func cannotSeparate(first, second HeldTwiceCopy) string {
	switch {
	case !listened(first) || !listened(second):
		return "Nothing has read the audio of one of these copies yet. " +
			"Rescan the library and this answers itself."
	case first.measured == 0 || second.measured == 0:
		return "One of these copies has no measurable length, so there is " +
			"nothing to compare it by."
	case abs(first.measured-second.measured) > durationSlack:
		return "These copies are not the same length. One of them is not " +
			"simply a lesser version of the other."
	case !sameContainer(first.Path, second.Path) &&
		(familyOf(first.Path) == familyUnknown || familyOf(second.Path) == familyUnknown):
		return fmt.Sprintf("A %s holds either lossless or lossy audio and "+
			"nothing stored says which, so it cannot be weighed against a %s.",
			label(unknownOf(first, second)), label(knownOf(first, second)))
	case !sameContainer(first.Path, second.Path) && familyOf(first.Path) == familyLossy:
		return fmt.Sprintf("One is %s and the other %s. A bitrate from one "+
			"codec says nothing about a bitrate from another.",
			label(first), label(second))
	case compareAudio(first, second) == incomparable:
		return "One copy is better in one respect and worse in another, so " +
			"neither replaces the other."
	case compareAudio(first, second) == equivalent:
		return "These copies are the same audio and the library holds them " +
			"the same way. Either one will do; Schall will not pick for you."
	default:
		// The audio does put these in an order. What stopped it was the other
		// half of the question.
		return "The better copy is the one nothing has matched to a catalogue " +
			"track. Schall will not trade a matched file for an unmatched one."
	}
}

func unknownOf(first, second HeldTwiceCopy) HeldTwiceCopy {
	if familyOf(first.Path) == familyUnknown {
		return first
	}
	return second
}

func knownOf(first, second HeldTwiceCopy) HeldTwiceCopy {
	if familyOf(first.Path) == familyUnknown {
		return second
	}
	return first
}

func label(copied HeldTwiceCopy) string {
	if format := fileFormat(copied.Path); format != "" {
		return format
	}
	return "file"
}

func kilohertz(hertz int) string {
	return strings.TrimSuffix(
		strings.TrimRight(fmt.Sprintf("%.1f", float64(hertz)/1000), "0"), ".") + " kHz"
}

// ChooseKeeperWhenAnyWillDo names the copy to hold on to when the person has
// said they do not mind which.
//
// It is a second question, asked only when somebody asks it. judge answers "is
// one of these provably the better copy"; this answers "the person has looked
// at a group of copies that are the same audio and told Schall to keep any
// one". The first is a fact and the second is a decision, which is why they are
// separate functions and why nothing calls this on its own.
//
// It still refuses the groups judge refuses for a reason. A person saying "they
// are all fine" is saying it about copies of one piece of audio; it is not
// permission to delete a file whose length differs by more than the matching
// slack, one nothing has ever decoded, one in a codec that cannot be compared,
// or one somebody matched by hand. Those are not copies to choose between —
// they are the cases where Schall does not know that it is looking at the same
// audio twice, and no instruction about copies applies to them.
//
// Where it does choose, it chooses the same copy every time: the best fit for
// the catalogue first, then the larger file, then the earlier path. A press
// repeated is a press with the same answer.
func ChooseKeeperWhenAnyWillDo(copies []HeldTwiceCopy) (uuid.UUID, bool) {
	if len(copies) < 2 {
		return uuid.Nil, false
	}
	// judge has the last word where it has one: a group with a provably better
	// copy keeps that copy, not whichever this would have picked.
	weighed := append([]HeldTwiceCopy(nil), copies...)
	if _, decided := judge(weighed); decided {
		for _, copied := range weighed {
			if copied.Keeper {
				return copied.ID, true
			}
		}
		return uuid.Nil, false
	}

	for _, copied := range copies {
		if copied.byHand {
			return uuid.Nil, false
		}
	}
	// Every copy has to be the same audio as every other. A group where one
	// pair is a tie and another pair cannot be compared is not a group of
	// interchangeable copies.
	for first := range copies {
		for second := range copies {
			if first == second {
				continue
			}
			if compareAudio(copies[first], copies[second]) != equivalent {
				return uuid.Nil, false
			}
		}
	}

	best := -1
	for index := range copies {
		if best < 0 || better(copies[index], copies[best]) {
			best = index
		}
	}
	return copies[best].ID, true
}

// better breaks a tie the same way every time, so that pressing twice keeps the
// same file. None of it is a quality judgement — the copies are already known
// to be the same audio — it is only a rule for picking one.
func better(candidate, incumbent HeldTwiceCopy) bool {
	if fit(candidate) != fit(incumbent) {
		return fit(candidate) > fit(incumbent)
	}
	if candidate.SizeBytes != incumbent.SizeBytes {
		return candidate.SizeBytes > incumbent.SizeBytes
	}
	return candidate.Path < incumbent.Path
}
