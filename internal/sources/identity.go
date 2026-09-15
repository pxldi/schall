package sources

import (
	"fmt"
	"sort"
	"strings"
)

// durationTolerance is how far a peer's file may run from the catalogue's
// length and still be the same recording. Masters differ by a second or two of
// lead-in or fade, and peers round; twenty seconds apart is a different edit.
const durationTolerance = 10

// Evidence levels for one catalogue track against one offered file.
const (
	evidenceNone = iota
	evidencePartial
	evidenceConfirmed
)

// Match is the evidence that a candidate holds the release that was searched
// for. It counts catalogue tracks, not files, because the question it answers
// is "is all of this release here?" rather than "how much is on offer?".
//
// A track is Confirmed when a file both carries its name and runs to its
// length. Either one alone is Partial: names collide across remixes and live
// sets, and durations collide across short tracks, but the two together are
// hard to hit by accident. A file that carries the name but runs to a
// different length is Absent, not Partial: the peer disclosed the length, and
// it says this is another edit or another song with the same words in it.
type Match struct {
	Expected  int
	Confirmed int
	Partial   int
	Absent    int
}

// Complete reports whether every catalogue track is confirmed by both name and
// length. It is the only condition under which Schall may act without asking.
func (match Match) Complete() bool {
	return match.Expected > 0 && match.Confirmed == match.Expected
}

// Known reports whether the candidate was corroborated at all. A query carrying
// no catalogue tracks produces a zero Match, which must not be read as absence
// of evidence against the candidate — there was simply nothing to check.
func (match Match) Known() bool { return match.Expected > 0 }

// Summary describes the match in the words the interface shows.
func (match Match) Summary() string {
	switch {
	case !match.Known():
		return "Not checked against the catalogue"
	case match.Complete() && match.Expected == 1:
		return "Track confirmed by name and length"
	case match.Complete():
		return fmt.Sprintf("All %d tracks confirmed by name and length", match.Expected)
	case match.Confirmed == 0 && match.Partial == 0:
		return fmt.Sprintf("Nothing here matches the %s", pluralTracks(match.Expected))
	}

	// Only the counts that are not zero are named. "2 of 3 confirmed, 0 partial,
	// 1 missing" reads as though there were something partial to look at.
	parts := []string{fmt.Sprintf("%d of %d confirmed", match.Confirmed, match.Expected)}
	if match.Partial > 0 {
		parts = append(parts, fmt.Sprintf("%d partial", match.Partial))
	}
	if match.Absent > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", match.Absent))
	}
	return strings.Join(parts, ", ")
}

// Corroborate checks a candidate's files against the catalogue tracks the query
// carries.
//
// Each file may stand for at most one track. Without that, a folder whose files
// all happen to contain the artist's name would appear to hold every track of
// the release several times over, and a discography dump would corroborate
// perfectly against any album in it.
func Corroborate(candidate Candidate, query Query) Match {
	if len(query.Tracks) == 0 {
		return Match{}
	}
	match := Match{Expected: len(query.Tracks)}

	names := make([]string, len(candidate.Files))
	for index, file := range candidate.Files {
		names[index] = normalizeForComparison(file.Name)
	}
	titles := make([]string, len(query.Tracks))
	for index, track := range query.Tracks {
		titles[index] = normalizeForComparison(track.Title)
	}

	claimed := make([]bool, len(candidate.Files))
	settled := make([]bool, len(query.Tracks))

	// Confirmed evidence is claimed before partial evidence, so a file that
	// confirms one track is never spent standing in for another it merely
	// shares a length with.
	for _, wanted := range []int{evidenceConfirmed, evidencePartial} {
		for trackIndex, track := range query.Tracks {
			if settled[trackIndex] {
				continue
			}
			for fileIndex, file := range candidate.Files {
				if claimed[fileIndex] {
					continue
				}
				if evidenceFor(track, titles[trackIndex], file, names[fileIndex]) != wanted {
					continue
				}
				claimed[fileIndex], settled[trackIndex] = true, true
				if wanted == evidenceConfirmed {
					match.Confirmed++
				} else {
					match.Partial++
				}
				break
			}
		}
	}

	match.Absent = match.Expected - match.Confirmed - match.Partial
	return match
}

// evidenceFor grades one catalogue track against one offered file.
//
// A disclosed length outside the tolerance is a contradiction, and it voids the
// name. On 2026-09-11 a want for a 160-second track was offered twenty files
// called "Nikki war nie weg", every one disclosed at 243 seconds, and the name
// alone had them fetched one after another, each refused by its audio. An
// undisclosed length is silence and leaves the name standing.
func evidenceFor(track QueryTrack, title string, file File, name string) int {
	byTitle := title != "" && strings.Contains(name, title)
	lengthKnown := track.DurationSeconds > 0 && file.DurationSeconds > 0
	byDuration := lengthKnown &&
		absDifference(track.DurationSeconds, file.DurationSeconds) <= durationTolerance

	switch {
	case lengthKnown && !byDuration:
		return evidenceNone
	case byTitle && byDuration:
		return evidenceConfirmed
	case byTitle || byDuration:
		return evidencePartial
	default:
		return evidenceNone
	}
}

// lengthDiffers reports whether the peer disclosed a length and it is outside
// the tolerance. It exists so the interface can say which of the two things the
// peer disclosed ruled the file out of the automatic pick.
func lengthDiffers(track QueryTrack, file File) bool {
	return track.DurationSeconds > 0 && file.DurationSeconds > 0 &&
		absDifference(track.DurationSeconds, file.DurationSeconds) > durationTolerance
}

// normalizeForComparison prepares catalogue and remote text for matching.
//
// It reuses the search normalizer, which already joins words back together
// across apostrophes, so a peer who wrote "Cherus Theme" still matches a
// catalogue that spells it "Cheru's Theme". Case is folded here rather than
// there because the search phrase is shown back to the user and this is not.
func normalizeForComparison(value string) string {
	return strings.ToLower(NormalizeQueryText(value))
}

func absDifference(left, right int) int {
	if left > right {
		return left - right
	}
	return right - left
}

// Offer is one remote file a peer is sharing, together with how well the little
// the peer disclosed about it fits the recording somebody is looking for.
type Offer struct {
	Provider  string
	Username  string
	Directory string
	File      File
	// Confirmed is set when the filename carries the wanted title and the
	// disclosed length runs to it. Partial is either one alone.
	Confirmed bool
	Partial   bool
	// LengthDiffers is set when the peer disclosed a length and it is outside
	// the tolerance. Such a file is never Confirmed or Partial, whatever its
	// name says.
	LengthDiffers bool
	// Named is set when the filename carries the wanted title, whatever the
	// length said. It is the one thing a peer discloses that is about this track
	// rather than about the folder, which is what lets a folder found for one
	// want be read for another. It is not evidence of anything: the audio still
	// decides, and a file named for a track is routinely not that track.
	Named bool
	// Score is the candidate's copy quality, carried through as the tie-break.
	Score float64
}

// OffersFor orders every file on offer by how well it fits one wanted recording,
// best first.
//
// This is reading order and nothing else. It decides which bytes are worth
// fetching first and never what a file is: a peer's filename is not evidence of
// anything, and every copy is checked against the recording after it arrives, by
// rules that do not read a word of this. A file nothing here recommends is still
// offered — last — because the only thing that can rule a copy out is the audio,
// and nobody has heard it yet.
//
// Files that cannot be asked for are dropped: a peer serves the exact name it
// advertised, so a file with no path is one nothing could ever fetch.
func OffersFor(candidates []Candidate, wanted QueryTrack) []Offer {
	title := normalizeForComparison(wanted.Title)
	offers := make([]Offer, 0, len(candidates))
	for _, candidate := range candidates {
		for _, file := range candidate.Files {
			if strings.TrimSpace(file.Path) == "" {
				continue
			}
			name := normalizeForComparison(file.Name)
			evidence := evidenceFor(wanted, title, file, name)
			offers = append(offers, Offer{
				Provider: candidate.Provider, Username: candidate.Username,
				Directory: candidate.Directory, File: file,
				Confirmed:     evidence == evidenceConfirmed,
				Partial:       evidence == evidencePartial,
				LengthDiffers: lengthDiffers(wanted, file),
				Named:         title != "" && strings.Contains(name, title),
				Score:         candidate.Score,
			})
		}
	}
	sort.SliceStable(offers, func(left, right int) bool {
		first, second := offers[left], offers[right]
		if first.rank() != second.rank() {
			return first.rank() > second.rank()
		}
		if first.Score != second.Score {
			return first.Score > second.Score
		}
		return first.File.Path < second.File.Path
	})
	return offers
}

func (offer Offer) rank() int {
	switch {
	case offer.Confirmed:
		return evidenceConfirmed
	case offer.Partial:
		return evidencePartial
	}
	return evidenceNone
}

// Summary says what the peer disclosed about this file, in the words the
// interface shows. It is never a claim that the file is the recording.
func (offer Offer) Summary() string {
	switch {
	case offer.Confirmed:
		return "Named and timed like the wanted recording"
	case offer.Partial:
		return "Either the name or the length fits, not both"
	case offer.LengthDiffers && offer.Named:
		return "Named like the wanted recording, but the peer says it runs to another length"
	case offer.LengthDiffers:
		return "The peer says it runs to another length"
	}
	return "Nothing the peer disclosed fits; it has not been heard either"
}
