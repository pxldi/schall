package sources

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// A Rung is one phrase of a query ladder together with what the ladder made it
// out of.
//
// The text is the whole of what a peer is asked, and for walking the ladder it is
// everything there is to know. What it cannot say is why it was asked, and by the
// time a phrase is rendered that is gone: "EP" is the label the catalogue hangs
// off "Femcels Forever - EP" and an ordinary word in the middle of "Airwaves EP
// Träume", and both reach a reader as the same two letters at the end of a
// phrase, the second because askableText dropped the word after them. Only the
// ladder, which read the title, can tell those apart, so what it knew is carried
// alongside the phrase rather than left to be guessed back out of it.
//
// None of this is evidence and none of it may become any. It decides which
// question a peer is asked and nothing else: a candidate is still corroborated by
// what the peer disclosed, and the copy that arrives is still proven by its audio.
type Rung struct {
	// Text is the phrase to put to the provider.
	Text string
	// CarriesReleaseKind marks a rung whose catalogue text ended in the
	// release-kind label MusicBrainz appends and peer folders leave off.
	// CatalogueQueryRungs spends a whole rung shedding that token, so where two
	// rungs are otherwise the same question, the one still carrying it is the
	// worse of the two.
	CarriesReleaseKind bool
}

// QueryTexts reads a ladder as its phrases alone, for the callers that ask every
// rung in turn and so have nothing to choose between them.
func QueryTexts(rungs []Rung) []string {
	texts := make([]string, 0, len(rungs))
	for _, rung := range rungs {
		texts = append(texts, rung.Text)
	}
	return texts
}

// CatalogueQueryRungs returns the conservative sequence of phrases used for a
// catalogue release. The exact artist and title always go first. MusicBrainz
// sometimes appends a release-kind label to the title while peer folders omit
// it; because Soulseek requires every token to match, that one extra token can
// turn a precise query into no results.
//
// A title that normalizes to nothing adds no rung at all. What is left is the
// artist standing on their own, which is every file that peer is sharing by them
// and an answer to a question nobody asked; the release side of TrackQueryRungs
// has refused that phrase since it was added, and this is the same refusal read
// from the title. A ladder can therefore come back empty, and every caller says
// so rather than searching for it.
func CatalogueQueryRungs(artist, title string) []Rung {
	var rungs []Rung
	// Read from the title as the catalogue spells it, never from what is left
	// after askableText: deciding on a shortened title would call "EP" the
	// qualifier of "Deluxe EP Träume", which it never was, and split one phrase
	// into two. The rung above carries the same reading, so a searcher choosing
	// between rungs is choosing on the title rather than on the phrase.
	labelled := labelsARelease(title)
	if named := collapseSingleCharacterRuns(askableText(title)); named != "" {
		rungs = appendRung(rungs, Rung{
			Text:               NormalizeQueryText(artist + " " + named),
			CarriesReleaseKind: labelled,
		})
	}

	words := strings.Fields(NormalizeQueryText(title))
	if len(words) < 2 || !labelled {
		return rungs
	}
	return appendRung(rungs, Rung{Text: NormalizeQueryText(artist + " " +
		collapseSingleCharacterRuns(askableText(strings.Join(words[:len(words)-1], " "))))})
}

// CatalogueQueryTexts is CatalogueQueryRungs read as phrases alone.
func CatalogueQueryTexts(artist, title string) []string {
	return QueryTexts(CatalogueQueryRungs(artist, title))
}

// TrackQueryRungs returns the phrases to look for one track by, which is the
// catalogue ladder with the title on its own at the end.
//
// Soulseek blacklists words. An artist whose name is one of them — "Gorillaz" is
// the reported case — returns nothing for every phrase that contains it, and
// every rung above begins with the artist, so a want for such a track reported
// "no peer is sharing a copy yet" forever and never said why. The title alone
// carries no blacklisted token and finds the same files, in the same folders.
//
// It is last because it is the worst phrase: without the artist it matches every
// peer who has a file of that name, and Soulseek's reply window is the same
// twenty seconds either way. It is reached only when everything above found
// nothing, which is the ladder working as it always has.
//
// A one-word title is left out. "Golden" on its own is thousands of unrelated
// files and the fifty a search keeps would not be the wanted one — the cost is
// reach, never a wrong copy, because the search phrase is not evidence: a
// candidate is still corroborated by what the peer disclosed, and the file that
// arrives is still proven by its audio.
func TrackQueryRungs(artist, title, release string) []Rung {
	rungs := CatalogueQueryRungs(artist, title)
	// The qualifier is dropped, never the whole title: a title that leaves nothing
	// askable behind would leave the artist alone here too.
	if named := collapseSingleCharacterRuns(askableText(plainTitle(title))); named != "" {
		rungs = appendRung(rungs, Rung{
			Text:               NormalizeQueryText(artist + " " + named),
			CarriesReleaseKind: labelsARelease(plainTitle(title)),
		})
	}
	// Peers share folders, not loose tracks. Somebody holding one track of an EP
	// holds the folder it came in, named for the release and not for the track,
	// and asking for the track by name never reaches it: nineteen wants reported
	// that nobody was sharing their music while a search for the release found it
	// at once. The release is asked for after every phrase naming the track,
	// because it is the broader question and the narrow one deserves the first
	// reply window; what comes back is a folder, and OffersFor still reads the
	// file inside it that fits, which the audio still has to prove.
	// A want with no release recorded asks for nothing extra. Without this the
	// phrase collapses to the artist's name on its own, which is every file that
	// peer is sharing by them and an answer to a question nobody asked.
	if named := collapseSingleCharacterRuns(askableText(plainTitle(release))); named != "" {
		rungs = appendRung(rungs, Rung{
			Text:               NormalizeQueryText(artist + " " + named),
			CarriesReleaseKind: labelsARelease(plainTitle(release)),
		})
	}
	// A title the reduction leaves one word of is not worth asking alone either,
	// but the rung is: "Spät Sommer" reduced to "Sommer" is thousands of files,
	// while the title as it is spelled is the same last resort it has always
	// been, and losing the rung entirely would cost a blacklisted artist's want
	// the only phrase it had.
	//
	// It is built from plainTitle, not the raw title: this is the broadest rung
	// in the ladder, reached only when every phrase above it found nothing, and
	// askableText never strips a parenthetical -- that is plainTitle's job, run
	// earlier in the ladder. "ROOT OF EVIL (feat. Destroy Lonely & Lil Uzi
	// Vert)" without plainTitle asks nine literal tokens, narrower than the rung
	// above it that already failed; no ordinary peer folder spells out "feat."
	// and the featured artists' names in that order.
	plain := plainTitle(title)
	alone := askableText(plain)
	if len(strings.Fields(alone)) < 2 {
		alone = NormalizeQueryText(plain)
	}
	if len(strings.Fields(alone)) < 2 {
		return rungs
	}
	return appendRung(rungs, Rung{
		Text:               collapseSingleCharacterRuns(alone),
		CarriesReleaseKind: labelsARelease(plain),
	})
}

// TrackQueryTexts is TrackQueryRungs read as phrases alone.
func TrackQueryTexts(artist, title, release string) []string {
	return QueryTexts(TrackQueryRungs(artist, title, release))
}

// collapseSingleCharacterRuns joins a run of three or more consecutive
// single-character tokens into one word.
//
// MusicBrainz spells some titles with a space after every character: "A D H
// D", "G R E Y G O D S", "6 6 6 F O R E V E R". Soulseek requires every token
// of a phrase to match, and no peer names a file that way, so the phrase as
// spelled reached nobody: "A D H D" came back Completed, Errored three times
// running on the live instance.
//
// The threshold is three tokens. "Jay Z" carries one single-character token
// ("Z"), so it stays untouched; a run of two is still ordinary text, and
// collapsing at that length risks folding a real short phrase into a word
// nobody searches for.
//
// It is called on the title or release text alone, before that text joins
// the artist, so a one-letter artist never collapses into a run that started
// in the title.
func collapseSingleCharacterRuns(value string) string {
	tokens := strings.Fields(value)
	result := make([]string, 0, len(tokens))
	var run []string
	flush := func() {
		if len(run) >= 3 {
			result = append(result, strings.Join(run, ""))
		} else {
			result = append(result, run...)
		}
		run = nil
	}
	for _, token := range tokens {
		if utf8.RuneCountInString(token) == 1 {
			run = append(run, token)
			continue
		}
		flush()
		result = append(result, token)
	}
	flush()
	return strings.Join(result, " ")
}

// plainTitle drops a qualifier the catalogue hangs off the end of a title: the
// parenthesised or bracketed remix, version and bootleg labels, and the trailing
// " - something" MusicBrainz uses for mix names and broadcast dates.
//
// Soulseek requires every token of the phrase to match, so "Lvstlove (Night
// Drive At 177mph Mix)" asks a peer for seven tokens in that order and finds
// nothing, while the folder sharing it is named for the track. The qualifier is
// how MusicBrainz distinguishes one recording from another, and dropping it
// widens what comes back rather than deciding anything: the copy that arrives is
// still proven by its audio against the recording that was wanted, so a search
// that reaches the wrong mix ends in a refusal, not a wrong file.
//
// The title has to survive as something worth asking for. Everything up to the
// first qualifier is kept, and if that leaves nothing -- a title that is only a
// parenthesis -- the original stands.
//
// A release-kind label carries no dash or bracket of its own: "Femcels Forever
// EP" is the catalogue's whole spelling, with the label as the last word rather
// than a qualifier in parentheses. It is read off the last word the same way
// labelsARelease reads it, and dropped for the same reason the other qualifiers
// are: no peer folder is named with it.
func plainTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return ""
	}
	cut := len(title)
	for index, symbol := range title {
		if symbol == '(' || symbol == '[' {
			cut = min(cut, index)
			break
		}
	}
	if dash := strings.Index(title, " - "); dash >= 0 {
		cut = min(cut, dash)
	}
	plain := strings.TrimSpace(title[:cut])
	if plain == "" {
		return title
	}
	if words := strings.Fields(plain); len(words) > 1 && isReleaseKind(words[len(words)-1]) {
		plain = strings.Join(words[:len(words)-1], " ")
	}
	return plain
}

// askableText reduces catalogue text to the words a peer can be relied on to
// have written the same way.
//
// Soulseek requires every token of the phrase to appear in the peer's path, so
// one character peers spell more than one way costs the whole search. "Pashanim
// draußen essen spät schlafen" reaches nobody who typed "draussen", and nobody
// who typed "spat". German writes ß as "ss", ä as "ae" and ä as "a", all three
// in ordinary use, and Soulseek's all-token matching means no single phrase can
// carry more than one of them: picking one would be guessing which spelling this
// peer used, and asking each in its own rung would cost a reply window a want's
// search budget does not have.
//
// The token carrying the character is therefore not asked for at all. Dropping a
// token can only widen what a phrase reaches — every peer the longer phrase
// matched still matches the shorter one, because it is asked for strictly less —
// so this trades no spelling away for another and costs no rung: the phrase gets
// shorter, never the ladder longer. What it costs is precision, and precision is
// not what a search phrase is for here: a candidate is still corroborated
// against the full title as the catalogue spells it, and the copy that arrives
// is still proven by its audio.
//
// Only Latin letters outside ASCII are read this way. A title written in another
// script has no ASCII form peers agree on either, and dropping it would leave a
// phrase naming none of the music.
//
// The text has to survive as something that still names the music, or it is
// asked for exactly as it is spelled instead. "Näher" has nothing else to say;
// "Träume EP" and "Träume (2016)" are left with a label and a year, neither of
// which any peer names a folder for. For the same reason the artist is never
// read this way: without it a phrase stops being a question about this music and
// becomes every file on the network by that name.
//
// Nothing is dropped from a title whose words all survive, so a title without a
// diacritic comes through exactly as it always has.
func askableText(value string) string {
	normalized := NormalizeQueryText(value)
	tokens := strings.Fields(normalized)
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !transliterated(token) {
			kept = append(kept, token)
		}
	}
	if !namesTheMusic(kept) {
		return normalized
	}
	return strings.Join(kept, " ")
}

// namesTheMusic reports whether these words say what the music is.
//
// A release kind does not: "EP" and "Single" are how the catalogue labels a
// release, and a phrase left with only those asks for every EP that artist ever
// put out. Nor does a bare number: "Träume (2016)" reduced to "2016" asks for
// every folder of theirs carrying a year, which is most of them.
//
// Reaching the wrong folders is not merely noise. The ladder stops at the first
// rung anybody answers, so a phrase that finds unrelated files is the last one
// asked, and the exact spelling waiting below it is never tried at all. Where
// what survives says nothing, the exact spelling is the better question.
func namesTheMusic(tokens []string) bool {
	for _, token := range tokens {
		if !isReleaseKind(token) && !isNumber(token) {
			return true
		}
	}
	return false
}

func isNumber(token string) bool {
	for _, symbol := range token {
		if !unicode.IsDigit(symbol) {
			return false
		}
	}
	return token != ""
}

// transliterated reports whether a token carries a letter peers write more than
// one way: the German ä, ö, ü and ß, and every other accented Latin letter with
// a bare ASCII form somebody will have typed instead.
func transliterated(token string) bool {
	for _, symbol := range token {
		if symbol > unicode.MaxASCII && unicode.Is(unicode.Latin, symbol) {
			return true
		}
	}
	return false
}

// appendRung adds a rung the ladder does not already carry. An empty phrase, or
// one already asked for higher up, is not asked again.
//
// The rung that got there first keeps its account of itself, and the later one is
// dropped whole rather than merged. Two derivations reaching the same phrase can
// disagree about the label — "Single EP" sheds its label into "Anetha Single",
// which the release "Single" arrives at while still carrying one — and the
// earlier rung is the one the ladder asked in, so its reading is the one that
// stands. Taking the more pessimistic of the two would pass over a shed rung in
// favour of a phrase that still carries what it shed, which is the choice this
// exists to avoid.
//
// The comparison is case-insensitive. Soulseek's own matching is, so a title
// and an album spelled the same but for casing -- "Taking a Walk" against
// "Taking A Walk" -- are one question asked twice: the same search creation,
// the same reply window, and the same tick against the ban.
func appendRung(rungs []Rung, rung Rung) []Rung {
	if rung.Text == "" {
		return rungs
	}
	for _, existing := range rungs {
		if strings.EqualFold(existing.Text, rung.Text) {
			return rungs
		}
	}
	return append(rungs, rung)
}

// labelsARelease reports whether catalogue text ends in the release-kind label
// MusicBrainz appends to a title, which is the token peer folders leave off.
//
// It reads the text as the catalogue spells it, for the same reason the fallback
// rung is built from that spelling: whether "EP" labels the release is decided by
// where it stands in the title, and the finished phrase is the wrong place to
// look. "Airwaves EP Träume" reaches a peer as "Airwaves EP" — every word after
// the label dropped by askableText — and asking the phrase would call the middle
// of a name a label.
func labelsARelease(value string) bool {
	words := strings.Fields(NormalizeQueryText(value))
	return len(words) > 0 && isReleaseKind(words[len(words)-1])
}

func isReleaseKind(value string) bool {
	switch strings.ToLower(value) {
	case "ep", "single":
		return true
	default:
		return false
	}
}

// NormalizeQueryText prepares catalogue text for a source search.
//
// Soulseek matches a search phrase token by token against remote paths, and a
// token carrying punctuation matches nothing. Searching a real network for
// "Sewerslvt we had good times together, don't forget that" returned no
// candidates at all, while the same words without the comma and apostrophe
// returned a complete lossless folder. The apostrophe alone is enough to lose
// every result, in its ASCII and typographic form equally, so a catalogue title
// can never be sent as MusicBrainz spells it.
//
// Apostrophes are removed without leaving a separator, because peers name
// folders "dont_be_afraid_of_dying" rather than "don t". Every other
// non-alphanumeric character becomes a space. Letters and digits of any script
// are kept, so a title that is not written in Latin script stays searchable.
// Case is preserved, because Soulseek ignores it and the phrase is shown back
// to the user. The function is idempotent, so normalizing already-normalized
// text is safe.
//
// The text is put into NFC first. Unicode gives an accented letter two
// spellings that render identically: composed, "ü" is one code point and a
// letter; decomposed, it is "u" followed by a combining mark, and the mark is
// not a letter — so the loop below would turn it into a space and "Träume"
// would go to Soulseek as "Tra ume", which matches almost nothing. MusicBrainz
// serves composed text, but a Spotify-imported entry or anything typed by hand
// need not, and macOS produces the decomposed spelling routinely. Composing
// only ever joins two spellings of the same characters; it never turns one
// character into another, so no word becomes a different word.
func NormalizeQueryText(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, symbol := range norm.NFC.String(value) {
		switch {
		case unicode.IsLetter(symbol) || unicode.IsDigit(symbol):
			builder.WriteRune(symbol)
		case isApostrophe(symbol):
			// Dropped without a separator, joining the word back together.
		default:
			builder.WriteRune(' ')
		}
	}
	return strings.Join(substantialTokens(builder.String()), " ")
}

// substantialTokens drops tokens that carry no real word, such as the lone
// katakana sound marks left behind by the symbol art in a title like
// "if you’re out there i miss you ｡ﾟ･ (>﹏<) ･ﾟ｡". Soulseek requires every
// token to match, so one decorative leftover loses the whole search.
func substantialTokens(value string) []string {
	tokens := make([]string, 0, 8)
	for _, token := range strings.Fields(value) {
		for _, symbol := range token {
			if unicode.IsDigit(symbol) || (unicode.IsLetter(symbol) && !unicode.Is(unicode.Lm, symbol)) {
				tokens = append(tokens, token)
				break
			}
		}
	}
	return tokens
}

func isApostrophe(symbol rune) bool {
	switch symbol {
	case '\'', '‘', '’', 'ʼ', '`', '´':
		return true
	}
	return false
}
