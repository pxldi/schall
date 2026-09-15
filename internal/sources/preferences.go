package sources

import (
	"fmt"
	"strings"
)

// Preferences are what the user wants Schall to prefer among the copies peers
// are sharing, and what it must never fetch.
//
// Everything here decides what gets *tried*. None of it decides what is
// admitted: a fetched copy is still identified by its audio, a copy nobody
// could identify is still held rather than imported, and no preference has ever
// admitted a file. A preference also never deletes anything the library has.
//
// The zero value is the project's own taste, which is what every installation
// has until somebody says otherwise: lossless above lossy, lossy by bitrate,
// nothing refused outright.
type Preferences struct {
	// Preferred are formats to put above all others, best first, as bare
	// lower-case extensions. A format not named here keeps its usual place
	// below the ones that are.
	Preferred []string
	// Unacceptable are formats never to fetch. This is a floor rather than a
	// preference: a candidate in one of these formats leaves the results with
	// its reason recorded, instead of being ranked low and then picked because
	// it was the only thing on offer.
	Unacceptable []string
	// MinimumBitRate is the lowest bitrate in kbps a lossy copy may have when
	// its format has no floor of its own. Zero is no floor. It says nothing
	// about a lossless copy, which has no bitrate to compare.
	MinimumBitRate int
	// FormatMinimumBitRate is the lowest bitrate in kbps for one named lossy
	// format, keyed by the bare lower-case extension: {"mp3": 320}. A format
	// named here is held to its own number and to nothing else, so "MP3 at
	// least 320" means that whatever MinimumBitRate says. A lossy format not
	// named here falls back to MinimumBitRate.
	FormatMinimumBitRate map[string]int
}

// Stated reports whether anybody has said anything. An installation that has
// not is ranked exactly as it was before this existed.
func (preferences Preferences) Stated() bool {
	return len(preferences.Preferred) > 0 || len(preferences.Unacceptable) > 0 ||
		preferences.MinimumBitRate > 0 || len(preferences.FormatMinimumBitRate) > 0
}

// floorFor is the lowest bitrate this format may have, and whether there is one
// at all. The format's own number wins over the general one rather than being
// taken with it: somebody who wrote "MP3 at least 320" asked for 320, not for
// whichever of two numbers happens to be larger.
func (preferences Preferences) floorFor(format string) (int, bool) {
	format = normalFormat(format)
	if format == "" || losslessExtensions[format] {
		return 0, false
	}
	if floor, named := preferences.FormatMinimumBitRate[format]; named {
		return floor, floor > 0
	}
	return preferences.MinimumBitRate, preferences.MinimumBitRate > 0
}

// Refused is one candidate the preferences took out of the results, and why.
//
// It is kept rather than dropped because the difference matters to whoever
// asked. A want told "nothing on offer" when six peers were sharing copies in a
// format the user refuses has been told something untrue, and the person who
// wrote the preference is the only one who can act on the real answer.
type Refused struct {
	Username  string
	Directory string
	Format    string
	Reason    string
	// Kind is which rule turned it away: RefusedFormat or RefusedBitRate. The
	// reason is the sentence a person reads; this is what the code counts, so
	// "six copies below your minimum bit rate" never has to be worked out by
	// reading English back.
	Kind string
}

// The two rules that take a copy out of the results.
const (
	// RefusedFormat is a format the user said never to fetch.
	RefusedFormat = "format"
	// RefusedBitRate is a lossy copy under the floor set for its format, or one
	// whose bitrate the peer never stated while a floor is set.
	RefusedBitRate = "bit_rate"
)

// refuse reports why these preferences will not fetch this candidate, and which
// rule said so. An empty reason means they will fetch it.
func (preferences Preferences) refuse(candidate Candidate) (string, string) {
	format := normalFormat(candidate.Format)
	if format == "" {
		// A folder whose format nobody could work out is not refused. The
		// preference names formats, and this candidate has not told us one;
		// refusing it would be reading silence as an answer.
		return "", ""
	}
	for _, unacceptable := range preferences.Unacceptable {
		if format == normalFormat(unacceptable) {
			return fmt.Sprintf("%s is a format you do not accept",
				strings.ToUpper(format)), RefusedFormat
		}
	}
	return preferences.refuseBitRate(format, candidate.AverageBitRate)
}

// refuseBitRate applies the floor for one format to one stated bitrate. It is
// the whole of the bitrate rule and the only place it lives, so a folder and a
// single file inside one are held to the same number.
//
// A bitrate the peer never stated does not meet a floor. It is the one part of
// this that reads as a guess and is not: a floor says what quality of copy to
// fetch, and a copy of no stated quality is exactly what somebody who wrote a
// floor was refusing. The alternative — letting silence through — hands them the
// low-bitrate copy the setting exists to keep out. Nothing is being decided
// about what the music *is* here; the audio still decides that, after the file
// arrives, and it never reads a word of this.
func (preferences Preferences) refuseBitRate(format string, bitRate int) (string, string) {
	floor, set := preferences.floorFor(format)
	if !set {
		return "", ""
	}
	label := strings.ToUpper(normalFormat(format))
	if bitRate <= 0 {
		return fmt.Sprintf("%s at a bitrate the peer did not state, under your %d kbps floor",
			label, floor), RefusedBitRate
	}
	if bitRate < floor {
		return fmt.Sprintf("%s at %d kbps is below the %d kbps you asked for",
			label, bitRate, floor), RefusedBitRate
	}
	return "", ""
}

// RefuseFile applies the bitrate floor to one file a peer is sharing, rather
// than to the folder holding it.
//
// The folder's bitrate is an average, and a folder averaging 320 kbps can hold a
// 128 kbps track. A want asks for one file, so the file it asks for is held to
// the floor itself. Formats the user refuses outright are not asked again here:
// the folder was already taken out of the results for that.
func (preferences Preferences) RefuseFile(format string, bitRate int) (string, string) {
	return preferences.refuseBitRate(format, bitRate)
}

// BitRateFloor returns the floor applied to a lossy format. It lets acquisition
// name the setting that caused a want to park without parsing refusal text.
func (preferences Preferences) BitRateFloor(format string) (int, bool) {
	return preferences.floorFor(format)
}

// BelowFloor reports whether a file already in the library, in this format at
// this stated bitrate, falls under the same floor a candidate on offer would
// be refused for. It is RefuseFile with the reason discarded and the kind
// reduced to a yes-or-no, so the upgrade sweep and the ordinary search ask
// exactly the same question rather than two that could drift apart.
//
// Unlike RefuseFile, a bitrate of zero never reads as below the floor here.
// RefuseFile treats an unstated candidate bitrate as a refusal because a copy
// of unknown quality is exactly what a stated floor exists to keep out; a
// library file's bitrate being zero means nobody has read it, which is
// silence about this file and never agreement that it is worth replacing.
// Callers are expected to leave such files out of consideration before this
// is asked at all.
func (preferences Preferences) BelowFloor(format string, bitRate int) bool {
	if bitRate <= 0 {
		return false
	}
	_, kind := preferences.RefuseFile(format, bitRate)
	return kind == RefusedBitRate
}

// rankOf reports where a format sits in the stated order, and whether it is in
// it at all.
func (preferences Preferences) rankOf(format string) (int, bool) {
	format = normalFormat(format)
	for position, preferred := range preferences.Preferred {
		if format == normalFormat(preferred) {
			return position, true
		}
	}
	return 0, false
}

// scoreFormatFor is scoreFormat with the user's order laid over it.
//
// A preferred format sits in a band no unpreferred format can reach, in the
// order given, so "prefer MP3" really does put an MP3 folder above a FLAC one.
// Below that band the usual scoring still decides, because the user has said
// nothing about those formats and the project's own order is the best answer
// left. Every candidate still records why it scored what it did.
func scoreFormatFor(candidate Candidate, preferences Preferences) (float64, string) {
	score, reason := scoreFormat(candidate)
	if len(preferences.Preferred) == 0 {
		return score, reason
	}
	label := strings.ToUpper(normalFormat(candidate.Format))
	if position, preferred := preferences.rankOf(candidate.Format); preferred {
		// Evenly spaced across the top tenth, so the order the user gave is the
		// order the results come back in.
		step := 0.0
		if len(preferences.Preferred) > 1 {
			step = float64(position) / float64(len(preferences.Preferred)-1) * 0.1
		}
		return 1 - step, fmt.Sprintf("%s, %s", reason, placing(position))
	}
	if label == "" {
		return score * unpreferredCeiling, reason
	}
	return score * unpreferredCeiling,
		fmt.Sprintf("%s, below the formats you prefer", reason)
}

// unpreferredCeiling keeps every unpreferred format under the band the stated
// ones occupy. The best unpreferred score is a lossless 1.0, so anything below
// 0.9 does it; the value is what keeps the usual order readable underneath.
const unpreferredCeiling = 0.85

func placing(position int) string {
	switch position {
	case 0:
		return "the format you prefer"
	case 1:
		return "your second choice of format"
	case 2:
		return "your third choice of format"
	default:
		return "one of the formats you prefer"
	}
}

// Lossless reports whether this format keeps every bit of the recording, and so
// has no bitrate for a floor to be set against. It is the one list, read here by
// the settings screen so it cannot drift from the one the ranking reads.
func Lossless(format string) bool {
	return losslessExtensions[normalFormat(format)]
}

func normalFormat(format string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
}

// Prefer lays the user's stored preferences over a ranking that has already
// been made, and reports what they refused.
//
// It runs where the search comes back rather than inside the provider, so one
// rule decides what may be fetched for every search there is: the release run,
// the want's ladder, and anything asked for by hand. Corroboration is left
// exactly as the provider found it — what the music is was never a matter of
// taste, and only the order and the floor are the user's to set.
//
// A refusal is removal, never a low score. A floor that only ranked low would
// be picked the moment it was the only copy on offer, which is precisely the
// case it exists for. What it removes is reported rather than dropped, because
// "nothing on offer" and "nothing on offer that you accept" are different
// answers and only the second one has somebody who can act on it.
//
// An installation that has said nothing gets back exactly what it handed in.
func Prefer(candidates []Candidate, preferences Preferences) ([]Candidate, []Refused) {
	if !preferences.Stated() {
		return candidates, nil
	}
	kept := make([]Candidate, 0, len(candidates))
	var refused []Refused
	for _, candidate := range candidates {
		if reason, kind := preferences.refuse(candidate); reason != "" {
			refused = append(refused, Refused{
				Username: candidate.Username, Directory: candidate.Directory,
				Format: normalFormat(candidate.Format), Reason: reason, Kind: kind,
			})
			continue
		}
		was, _ := scoreFormat(candidate)
		now, reason := scoreFormatFor(candidate, preferences)
		candidate.Score = round(candidate.Score + (now-was)*formatWeight)
		candidate.Reasons = withFormatReason(candidate.Reasons, reason)
		kept = append(kept, candidate)
	}
	order(kept)
	return kept, refused
}

// withFormatReason replaces the sentence about the format with the one the
// preferences produced, and leaves every other reason alone. The format reason
// is the first one score records, which is what makes this a replacement rather
// than a second opinion appended below the first.
func withFormatReason(reasons []string, reason string) []string {
	if len(reasons) == 0 {
		return []string{reason}
	}
	replaced := make([]string, len(reasons))
	copy(replaced, reasons)
	replaced[0] = reason
	return replaced
}
