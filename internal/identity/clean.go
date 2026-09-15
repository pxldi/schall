package identity

import (
	"slices"
	"strings"
	"unicode"

	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// cleanEditionWords are the words MusicBrainz uses to say a row is the censored
// edition of a track. They are matched as whole words, case-insensitive, and
// nothing else is read as one.
//
// "edit" is deliberately absent. A radio edit runs a different length and is a
// different recording, and reading it as a censored edition would let the wrong
// audio answer for a want (docs/decisions/0032).
var cleanEditionWords = []string{"clean", "censored", "edited"}

// editionQualifiers may stand beside a marker inside a title group without
// changing what the group says. "(Clean Version)" is a marker; "(Clean Bandit
// Remix)" is not, because "bandit" is neither a marker nor a qualifier.
var editionQualifiers = []string{"version", "edit"}

// The brackets a title carries a qualifier group in.
var editionBrackets = map[rune]rune{'(': ')', '[': ']', '{': '}'}

// cleanEdition reports MusicBrainz marking this row as the clean edition of a
// track.
//
// It reads what MusicBrainz wrote and nothing else: the editors' disambiguation
// comment on the recording, or every release the row appears on saying so in
// its own comment or title. Every release, because a row that appears on one
// clean compilation and on the ordinary album is the ordinary recording.
func cleanEdition(recording musicbrainz.Recording) bool {
	if marksClean(recording.Disambiguation) || titleMarksClean(recording.Title) {
		return true
	}
	if len(recording.Releases) == 0 {
		return false
	}
	for _, release := range recording.Releases {
		if !marksClean(release.Disambiguation) && !titleMarksClean(release.Title) {
			return false
		}
	}
	return true
}

// explicitEditionOf reports other holding the explicit edition of the track the
// clean row wanted names.
//
// The two rows are one registration under docs/decisions/0031's first three
// tests and fail only its fourth: both carry registration codes and share none,
// which is how a label registers a censored edition beside its master. Exactly
// one of them is marked clean, and this answers only where that one is the row
// asked about. Where neither or both are marked, or where both directions would
// hold, nothing here answers and the pair stays a person's question.
func explicitEditionOf(wanted, other musicbrainz.Recording, copyDurationMS int) bool {
	return registrationCodesDisagree(wanted, other) &&
		cleanEdition(wanted) && !cleanEdition(other) &&
		sameArtists(wanted, other) && sameEditionTitle(wanted, other) &&
		sameLength(wanted, other, copyDurationMS)
}

// sameEditionTitle is 0031's title test with the clean marker dropped as well,
// so "Timeless - Clean" and "Timeless" are the one title they are.
func sameEditionTitle(wanted, other musicbrainz.Recording) bool {
	return tagmatch.Title(
		withoutEditionLabels(wanted.Title),
		withoutEditionLabels(other.Title)) == tagmatch.Agrees
}

func withoutEditionLabels(title string) string {
	stripped, _ := withoutCleanMarker(title)
	return tagmatch.WithoutBracketedLabels(stripped)
}

// marksClean reports free text saying, in MusicBrainz's own words, that this is
// the clean edition. It is read off a disambiguation comment, which is written
// to describe the row and holds nothing else.
func marksClean(text string) bool {
	for _, word := range editionWords(text) {
		if slices.Contains(cleanEditionWords, word) {
			return true
		}
	}
	return false
}

// titleMarksClean reports a title marking its edition: "(Clean)", "[Clean]",
// "Song - Clean", "Song (Clean Version)", "Song (Edited)".
//
// The marker has to sit in a group of its own. A title is a name, and "Clean"
// by Taylor Swift is a song rather than an edition of one.
func titleMarksClean(title string) bool {
	_, marked := withoutCleanMarker(title)
	return marked
}

// withoutCleanMarker drops the qualifier groups that mark a title as the clean
// edition, and reports whether it dropped one.
func withoutCleanMarker(title string) (string, bool) {
	kept, marked := withoutBracketedCleanGroups(title)
	segments := strings.Split(kept, " - ")
	for len(segments) > 1 && groupMarksClean(segments[len(segments)-1]) {
		segments = segments[:len(segments)-1]
		marked = true
	}
	return strings.TrimSpace(strings.Join(segments, " - ")), marked
}

func withoutBracketedCleanGroups(title string) (string, bool) {
	runes := []rune(title)
	var kept strings.Builder
	marked := false
	for index := 0; index < len(runes); index++ {
		closer, opens := editionBrackets[runes[index]]
		if !opens {
			kept.WriteRune(runes[index])
			continue
		}
		end := -1
		for scan := index + 1; scan < len(runes); scan++ {
			if runes[scan] == closer {
				end = scan
				break
			}
		}
		if end < 0 {
			kept.WriteRune(runes[index])
			continue
		}
		if groupMarksClean(string(runes[index+1 : end])) {
			marked = true
		} else {
			kept.WriteString(string(runes[index : end+1]))
		}
		index = end
	}
	return strings.Join(strings.Fields(kept.String()), " "), marked
}

// groupMarksClean reports one qualifier group saying clean: it holds a marker
// word, and every other word in it is a qualifier that leaves the marker
// meaning what it means. "Radio Edit" holds no marker, so it is not one.
func groupMarksClean(group string) bool {
	found := false
	for _, word := range editionWords(group) {
		switch {
		case slices.Contains(cleanEditionWords, word):
			found = true
		case slices.Contains(editionQualifiers, word):
		default:
			return false
		}
	}
	return found
}

// editionWords is the text as the lower-case words it holds, so a marker is
// matched whole and "cleanup" is not "clean".
func editionWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(letter rune) bool {
		return !unicode.IsLetter(letter) && !unicode.IsDigit(letter)
	})
}
