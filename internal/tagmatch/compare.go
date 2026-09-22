// Package tags compares what one piece of music says about itself with what
// another does.
//
// It exists because Schall decides identity in two places — the files a
// download produced, and the files a person put in the library themselves —
// and those two must not drift apart in what they are willing to call
// agreement. A rule loosened for one of them is loosened for both, on purpose.
package tagmatch

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Verdict is what one kind of evidence says about two pieces of music.
type Verdict int

const (
	// Unknown means at least one side is silent. Silence is not agreement: an
	// absent tag proves nothing in either direction.
	Unknown Verdict = iota
	Agrees
	Differs
)

// Identifier compares two identifiers, such as a MusicBrainz recording ID or
// an ISRC. Identifiers are exact: either they are the same or they name
// different recordings.
func Identifier(left, right string) Verdict {
	left, right = strings.ToLower(strings.TrimSpace(left)), strings.ToLower(strings.TrimSpace(right))
	if left == "" || right == "" {
		return Unknown
	}
	if left == right {
		return Agrees
	}
	return Differs
}

// Title compares two titles, ignoring the punctuation and casing that taggers
// disagree about, and ignoring a trailing "original mix".
func Title(left, right string) Verdict {
	left, right = withoutOriginalQualifier(Text(left)), withoutOriginalQualifier(Text(right))
	if left == "" || right == "" {
		return Unknown
	}
	if left == right {
		return Agrees
	}
	return Differs
}

// originalQualifiers are the trailing phrases that name no recording of their
// own. "Original Mix" is how a release that also carries remixes labels the
// track those remixes were made from, so a catalogue that has only ever held
// the one recording simply has no reason to write it, and a streaming service
// that lists the whole release writes it on every track. The phrase is
// therefore a statement about what the recording is not — not a remix — rather
// than about which recording it is, and dropping it before comparison narrows
// nothing: "Detection" and "Detection - Original Mix" are the same audio under
// two naming conventions.
//
// The qualifier word has to be there. A title ending in the bare word
// "original" is a different claim: nothing in it distinguishes the convention
// from a recording whose name simply ends that way, and this package does not
// guess which one it is looking at. "Mix" and "Version" are the two spellings
// the convention actually uses, and they are what makes the preceding word
// readable as a label rather than as part of the name.
//
// Every other qualifier stays, because every other qualifier names different
// audio. "Detection - Regal Remix" is somebody else's record of the same
// composition, and calling it "Detection" is precisely the false match this
// package exists to refuse.
var originalQualifiers = []string{"original mix", "original version"}

// withoutOriginalQualifier drops a trailing original-mix label from an
// already-normalized title. It cuts the qualifier only together with the space
// in front of it, which is what keeps this a rule about whole trailing words:
// "Aboriginal Mix" ends in those letters without ending in that phrase, and a
// recording actually called "Original Mix" has no space in front of the phrase
// to cut, so it keeps the only title it has instead of falling to silence.
func withoutOriginalQualifier(title string) string {
	for _, qualifier := range originalQualifiers {
		if remainder, found := strings.CutSuffix(title, " "+qualifier); found {
			return remainder
		}
	}
	return title
}

// creditWords are the words that introduce a guest on a credit. A credit that
// continues with any other word is a different artist.
var creditWords = map[string]bool{
	"feat": true, "featuring": true, "ft": true, "with": true,
	"and": true, "x": true, "vs": true, "versus": true, "presents": true,
}

// Credit compares an observed artist credit with an expected one. A track
// credited to the expected artist together with a guest is still that artist's
// track, and a compilation credit says nothing about any one track, so neither
// counts as a disagreement. Anything else does: a matching title and duration
// under a different artist is how a cover version gets taken for the original.
func Credit(observed, expected string) Verdict {
	left, right := creditText(observed), creditText(expected)
	switch {
	case left == "" || right == "":
		return Unknown
	case left == right:
		return Agrees
	case left == "various artists" || right == "various artists":
		return Unknown
	case creditedWith(left, right) || creditedWith(right, left):
		return Agrees
	}
	return Differs
}

// creditText normalizes a credit for comparison. An ampersand is the word it
// stands for: identity normalization would drop it, and "Artist & Guest" would
// then be indistinguishable from an unrelated name that merely starts the same.
// A comma and the multiplication sign are the same joins spelled differently —
// streaming credits write "Artist, Guest" and MusicBrainz prints "Artist ×
// Guest" — so they become the join words they stand for rather than vanishing
// into a space and leaving the guest to read as an unrelated continuation.
func creditText(value string) string {
	joins := strings.NewReplacer("&", " and ", ",", " and ", "×", " x ")
	return Text(joins.Replace(value))
}

// PrimaryCredit is the artist a comma-joined credit names first. It is how
// entry evidence offers a single credit to compare and search by when its
// source lists several artists without saying how they relate. Cutting at a
// comma is deliberate parsing, never matching: a primary credit extracted from
// a band name that happens to contain a comma still has to agree under Credit
// before it counts for anything.
func PrimaryCredit(credit string) string {
	primary, _, _ := strings.Cut(credit, ",")
	return strings.TrimSpace(primary)
}

// CreditedArtist is one artist a recording credits, together with every name
// that artist answers to: the name the release prints, the artist's own name,
// and the aliases the provider records for them. "Travi$ Scott" and "Travis
// Scott" are one artist because the provider says so. Nothing here reads one
// name as the other by resemblance.
//
// The names are compared, never ranked. An entry with no usable name matches
// nothing rather than matching everything.
type CreditedArtist struct {
	Names []string
	// ID is the provider's own identifier for this artist. It is empty where the
	// credit names somebody the provider holds no entity for, which is silence
	// about who the artist is rather than a claim that they are nobody.
	ID string
}

// SameArtistIDs reports whether two credits name the same set of artists, by the
// identifiers the provider gave them rather than by their names.
//
// It exists because a name is not the artist. MusicBrainz renamed Kanye West to
// Ye in 2021 and left the old rows printing the old name, so two rows of one
// recording can print two credits that point at one artist. Comparing the
// identifiers is reading what the provider says about who is credited, and it is
// no more a resemblance than comparing the names is.
//
// Unknown is the answer wherever the identifiers cannot settle it: either side
// empty, either side holding an artist with no identifier, or a compilation
// credit, which says nothing about any one track. The caller then compares the
// names, which is the comparison this replaces nothing of.
func SameArtistIDs(left, right []CreditedArtist) Verdict {
	leftIDs, known := artistIDs(left)
	if !known {
		return Unknown
	}
	rightIDs, known := artistIDs(right)
	if !known {
		return Unknown
	}
	if len(leftIDs) != len(rightIDs) {
		return Differs
	}
	for id := range leftIDs {
		if _, credited := rightIDs[id]; !credited {
			return Differs
		}
	}
	return Agrees
}

// artistIDs is the set of artists a credit names, and whether the credit says
// who all of them are. One artist credited twice is one artist, which is what a
// set is for.
func artistIDs(credited []CreditedArtist) (map[string]struct{}, bool) {
	if len(credited) == 0 {
		return nil, false
	}
	ids := make(map[string]struct{}, len(credited))
	for _, artist := range credited {
		id := strings.ToLower(strings.TrimSpace(artist.ID))
		if id == "" {
			return nil, false
		}
		for _, name := range artist.Names {
			if creditName(name) == "various artists" {
				return nil, false
			}
		}
		ids[id] = struct{}{}
	}
	return ids, true
}

// maxCreditParts caps how many artists one tag is read as. A credit longer than
// this is a tag nobody wrote by hand, and reading it costs a run for every span
// of it. Past the cap the comparison says nothing, which is what it says about
// any credit it cannot read.
const maxCreditParts = 24

// Artists compares an observed artist tag with the artists a recording credits,
// as the set of artists it is rather than as a string (ADR 0030).
//
// Two credits are the same credit when they name the same artists, whatever
// order they print them in and whichever of each artist's names they use. So a
// file tagged "Travis Scott" and a recording credited to "Travi$ Scott feat. Bon
// Iver" are not two artists disagreeing; they are one artist under two names,
// with the file silent about the guest.
//
// The three outcomes are three different facts:
//
//   - Agrees: every name the tag holds is a credited artist, and every credited
//     artist is named. The two credits are the same credit.
//   - Unknown: every name the tag holds is a credited artist, and some credited
//     artist is not named. The file names part of the credit, which is what a
//     tagger writing only the lead artist does, and part of a credit contradicts
//     nothing.
//   - Differs: some name the tag holds is nobody this recording credits. That is
//     the file naming somebody else, which is how a cover version is told from
//     the original it shares a title and a length with.
//
// The tag is read as a whole before it is read as a list, so a name that carries
// a join of its own — "Tyler, The Creator", "Earth, Wind & Fire", "AC/DC" — is
// the one artist it is. Every span of the split is tried, not only the whole and
// the pieces, which is what lets "Tyler, The Creator & Kali Uchis" read as two
// artists of which the first holds a comma. A span matches only by being exactly
// a name the artist answers to, so a split that lands in the wrong place fails to
// match and never matches the wrong artist.
func Artists(observed string, credited []CreditedArtist) Verdict {
	names, artists := creditedNames(credited)
	whole := creditName(observed)
	if artists == 0 || whole == "" {
		return Unknown
	}
	// A compilation credit says nothing about any one track, on either side.
	if _, compilation := names["various artists"]; compilation || whole == "various artists" {
		return Unknown
	}
	parts := creditParts(observed)
	if len(parts) == 0 || len(parts) > maxCreditParts {
		return Unknown
	}
	matched, read := readCredit(parts, names)
	switch {
	case !read:
		return Differs
	case len(matched) < artists:
		return Unknown
	}
	return Agrees
}

// creditPart is one name a tag holds, together with the text that joined it to
// the next one. The join is kept verbatim so that a span of several parts can be
// put back together exactly as it was written: the pieces of "Earth, Wind & Fire"
// only read as that artist while the commas and the ampersand are still between
// them.
type creditPart struct {
	name string
	join string
}

// creditJoins are the words and symbols a tag puts between two artists. They are
// the ones taggers and streaming services actually write; a credit that
// continues with any other word is one name.
var creditJoins = regexp.MustCompile(
	`(?i)\s*(?:,|&|/|\+|×|\bx\b|\bfeat\.?\b|\bft\.?\b|\bfeaturing\b|\bwith\b|\band\b|` +
		`\bvs\.?\b|\bversus\b|\bpres\.?\b|\bpresents\b)\s*`)

// creditParts splits a tag where it joins two artists.
//
// A join with nothing on one side of it is not a join: it is part of the name,
// which is what keeps "Malcolm X" one artist and not an artist called Malcolm
// followed by nothing. Splitting is parsing and never matching — every piece
// still has to be exactly somebody this recording credits before it counts for
// anything.
func creditParts(value string) []creditPart {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var parts []creditPart
	cursor := 0
	for _, span := range creditJoins.FindAllStringIndex(value, -1) {
		if strings.TrimSpace(value[cursor:span[0]]) == "" ||
			strings.TrimSpace(value[span[1]:]) == "" {
			continue
		}
		parts = append(parts, creditPart{
			name: value[cursor:span[0]], join: value[span[0]:span[1]],
		})
		cursor = span[1]
	}
	return append(parts, creditPart{name: value[cursor:]})
}

// spanText puts the parts from first up to last back together as they were
// written.
func spanText(parts []creditPart, first, last int) string {
	var builder strings.Builder
	for index := first; index < last; index++ {
		builder.WriteString(parts[index].name)
		if index < last-1 {
			builder.WriteString(parts[index].join)
		}
	}
	return builder.String()
}

// readCredit reads the parts as a sequence of credited artists, and reports
// which artists that named. It fails when no reading covers the whole tag, which
// is the tag holding somebody this recording does not credit.
//
// Longer spans are preferred, so an artist whose name holds a join is read as
// that artist rather than as its pieces. Where several readings cover the tag
// they name the same artists in every case that occurs: a span matches by being
// exactly a credited name, so two readings differ only where one credited artist
// is spelled out of the pieces of others.
func readCredit(parts []creditPart, names map[string]int) (map[int]bool, bool) {
	// covered[index] reports that the parts from index on can be read as whole
	// credited artists. It is filled from the end, so a span is only taken when
	// what follows it can be read too.
	covered := make([]bool, len(parts)+1)
	covered[len(parts)] = true
	for first := len(parts) - 1; first >= 0; first-- {
		for last := len(parts); last > first; last-- {
			if !covered[last] {
				continue
			}
			if _, found := names[creditName(spanText(parts, first, last))]; found {
				covered[first] = true
				break
			}
		}
	}
	if !covered[0] {
		return nil, false
	}
	matched := make(map[int]bool, len(parts))
	for first := 0; first < len(parts); {
		for last := len(parts); last > first; last-- {
			if !covered[last] {
				continue
			}
			artist, found := names[creditName(spanText(parts, first, last))]
			if !found {
				continue
			}
			matched[artist] = true
			first = last
			break
		}
	}
	return matched, true
}

// creditedNames indexes every name each credited artist answers to, and counts
// the artists a tag can name. An artist whose names are all empty is left out of
// both: it would otherwise be a credited artist nothing can ever name, and no
// tag would agree with the credit again.
func creditedNames(credited []CreditedArtist) (map[string]int, int) {
	names, artists := make(map[string]int), 0
	for index, artist := range credited {
		named := false
		for _, name := range artist.Names {
			normalized := creditName(name)
			if normalized == "" {
				continue
			}
			named = true
			if _, taken := names[normalized]; taken {
				continue
			}
			names[normalized] = index
		}
		if named {
			artists++
		}
	}
	return names, artists
}

// creditName normalizes one artist's name for the set comparison. It spells
// every join a name can contain the one way, so that "Earth, Wind & Fire",
// "Earth, Wind and Fire" and "Earth Wind + Fire" are the one artist they are —
// and so that "A & B" stays two artists rather than collapsing onto a band
// called "A B".
func creditName(value string) string {
	joins := strings.NewReplacer(
		"&", " and ", ",", " and ", "+", " and ", "/", " and ", "×", " x ")
	return Text(joins.Replace(value))
}

// creditedWith reports whether one credit is the other followed by a guest.
func creditedWith(long, short string) bool {
	rest, found := strings.CutPrefix(long, short+" ")
	if !found {
		return false
	}
	word, _, _ := strings.Cut(rest, " ")
	return creditWords[word]
}

// SameText reports whether two values are the same once normalized.
func SameText(left, right string) bool {
	return Text(left) == Text(right)
}

// Text reduces a value to what taggers cannot disagree about: its letters and
// digits, lowercased, with equivalent apostrophe styles and the characters that
// print nothing removed.
func Text(value string) string {
	var builder strings.Builder
	for _, symbol := range norm.NFD.String(strings.ToLower(strings.TrimSpace(value))) {
		switch {
		case unicode.IsLetter(symbol) || unicode.IsDigit(symbol):
			builder.WriteRune(symbol)
		case unicode.Is(unicode.Mn, symbol):
			// Decomposed marks are the spelling difference in names such as
			// "Cancún" and "Cancun".
		case symbol == '\'' || symbol == '‘' || symbol == '’' || symbol == 'ʼ':
			// Equivalent apostrophe styles disappear within the word.
		case invisible(symbol):
			// A character nobody can see is not part of the name.
		default:
			builder.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// invisible reports a character that prints nothing: the directional marks,
// joiners and byte-order marks a text editor leaves behind, and the control
// characters a broken tag carries.
//
// They are dropped instead of being read as separators, because a separator is
// what they are not. MusicBrainz holds the artist "$‪uicideboy$" with a
// left-to-right embedding inside the name, and turning that character into a
// space splits one name into two words, neither of which is anybody.
//
// Whitespace keeps its job. A tab or a newline between two words is a space,
// whatever category it belongs to.
func invisible(symbol rune) bool {
	if unicode.IsSpace(symbol) {
		return false
	}
	return unicode.Is(unicode.Cf, symbol) || unicode.Is(unicode.Cc, symbol)
}
