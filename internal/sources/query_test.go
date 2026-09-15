package sources

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeQueryTextRemovesPunctuationSoulseekCannotMatch(t *testing.T) {
	// Every input below is a real catalogue title that returned no candidates
	// at all against a live Soulseek network until it was normalized.
	for name, testCase := range map[string]struct{ input, want string }{
		"typographic apostrophe": {
			input: "Sewerslvt Don’t Be Afraid of Dying",
			want:  "Sewerslvt Dont Be Afraid of Dying",
		},
		"ascii apostrophe": {
			input: "Sewerslvt Don't Be Afraid of Dying",
			want:  "Sewerslvt Dont Be Afraid of Dying",
		},
		"comma and apostrophe": {
			input: "Sewerslvt we had good times together, don’t forget that",
			want:  "Sewerslvt we had good times together dont forget that",
		},
		"double slash": {
			input: "Sewerslvt Sewer//Slvt",
			want:  "Sewerslvt Sewer Slvt",
		},
		"parentheses and plus": {
			input: "Sewerslvt Cyberia lyr1+2=3 (Nfract remix)",
			want:  "Sewerslvt Cyberia lyr1 2 3 Nfract remix",
		},
		"en dash": {
			input: "Selected Sewer Works (2017–19)",
			want:  "Selected Sewer Works 2017 19",
		},
		"symbol art": {
			input: "if you’re out there i miss you ｡ﾟ･ (>﹏<) ･ﾟ｡ - EP",
			want:  "if youre out there i miss you EP",
		},
		"case is preserved": {
			input: "Mr. Kill Myself",
			want:  "Mr Kill Myself",
		},
		"non-latin script survives": {
			input: "宇多田ヒカル First Love",
			want:  "宇多田ヒカル First Love",
		},
		"symbols only": {input: "&/(", want: ""},
	} {
		if got := NormalizeQueryText(testCase.input); got != testCase.want {
			t.Errorf("%s: NormalizeQueryText(%q) = %q, want %q", name, testCase.input, got, testCase.want)
		}
	}
}

func TestNormalizeQueryTextIsIdempotent(t *testing.T) {
	once := NormalizeQueryText("Sewerslvt Don’t Be Afraid of Dying")
	if twice := NormalizeQueryText(once); twice != once {
		t.Fatalf("normalizing twice = %q, want %q", twice, once)
	}
}

func TestCatalogueQueryTextsRetriesWithoutTrailingReleaseKind(t *testing.T) {
	for _, test := range []struct {
		title string
		want  []string
	}{
		{"Suffering From Melancholia - EP", []string{
			"Sewerslvt Suffering From Melancholia EP",
			"Sewerslvt Suffering From Melancholia",
		}},
		{"New Love (Single)", []string{
			"Sewerslvt New Love Single",
			"Sewerslvt New Love",
		}},
		{"EP", []string{"Sewerslvt EP"}},
		{"The Singles", []string{"Sewerslvt The Singles"}},
		{"Don't Be Afraid of Dying", []string{"Sewerslvt Dont Be Afraid of Dying"}},
	} {
		t.Run(test.title, func(t *testing.T) {
			if got := CatalogueQueryTexts("Sewerslvt", test.title); !slices.Equal(got, test.want) {
				t.Fatalf("CatalogueQueryTexts() = %#v, want %#v", got, test.want)
			}
		})
	}
}

// Soulseek blacklists words, and every phrase above begins with the artist, so a
// band whose name is one of them was unfindable however many rungs were tried.
func TestATrackCanBeLookedForByTitleAloneWhenTheArtistIsUnsearchable(t *testing.T) {
	texts := TrackQueryTexts("Gorillaz", "Feel Good Inc", "Demon Days")

	if texts[0] != "Gorillaz Feel Good Inc" {
		t.Fatalf("texts = %v, want the artist and title asked for first", texts)
	}
	if texts[len(texts)-1] != "Feel Good Inc" {
		t.Fatalf("texts = %v, want the title alone as the last thing tried", texts)
	}
}

// A title of one common word matches everything and identifies nothing, and the
// fifty candidates a search keeps would not hold the wanted copy.
func TestAOneWordTitleIsNotLookedForOnItsOwn(t *testing.T) {
	texts := TrackQueryTexts("Jamie xx", "Gosh", "In Colour")

	for _, text := range texts {
		if text == "Gosh" {
			t.Fatalf("texts = %v, want a single word not searched for alone", texts)
		}
	}
}

// A want with no artist to speak of already searches by title. Asking twice
// would spend a second twenty-second window learning the same thing.
func TestATitleIsNotAskedForTwice(t *testing.T) {
	texts := TrackQueryTexts("", "Feel Good Inc", "Feel Good Inc")

	if len(texts) != 1 {
		t.Fatalf("texts = %v, want the one phrase there is", texts)
	}
}

// The qualifier is how MusicBrainz tells one recording from another, and it is
// exactly what peers leave out of a filename. Every token has to match, so the
// full title asked for a phrase nobody was sharing and the want reported that
// no peer had it.
func TestATrackIsLookedForWithoutTheQualifierTheCatalogueHangsOffItsTitle(t *testing.T) {
	for _, test := range []struct {
		name  string
		title string
		want  string
	}{
		{"parenthesised mix", "Lvstlove (Night Drive At 177mph Mix)", "Cynthoni Lvstlove"},
		{"bracketed label", "Begin Again [Cynthoni Bootleg]", "Cynthoni Begin Again"},
		{"trailing dash", "Lifeless Misery - 20th November 2025", "Cynthoni Lifeless Misery"},
	} {
		t.Run(test.name, func(t *testing.T) {
			texts := TrackQueryTexts("Cynthoni", test.title, test.title)
			if !slices.Contains(texts, test.want) {
				t.Fatalf("texts = %v, want %q asked for", texts, test.want)
			}
		})
	}
}

// Dropping the qualifier can leave nothing to search for, and a phrase of no
// words finds every file on the network.
func TestATitleThatIsOnlyAQualifierKeepsItsWords(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "(Untitled)", "(Untitled)")

	for _, text := range texts {
		if text == "Cynthoni" {
			t.Fatalf("texts = %v, want no phrase that is the artist alone", texts)
		}
	}
}

// The ladder costs a twenty-second reply window a rung, so a phrase the exact
// title already asked for is not asked again.
func TestATitleWithNoQualifierGainsNoExtraRung(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "Femcels Forever", "Femcels Forever")

	if !slices.Equal(texts, []string{"Cynthoni Femcels Forever", "Femcels Forever"}) {
		t.Fatalf("texts = %v, want the two phrases there are", texts)
	}
}

// Peers share folders named for the release, not for the track inside them, so a
// want asking only by track name never reaches the folder holding its music.
func TestATrackIsAlsoLookedForByTheReleaseItSitsOn(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "Disappearing", "LIFELESS MISERY")

	if !slices.Contains(texts, "Cynthoni LIFELESS MISERY") {
		t.Fatalf("texts = %v, want the release asked for too", texts)
	}
}

// The release carries the same qualifiers a title does, and the same tokens have
// to match for a peer to answer.
func TestTheReleaseIsAskedForWithoutItsQualifier(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "Femcels",
		"Draining Love Story (In the Eyes of Cynthoni)")

	if !slices.Contains(texts, "Cynthoni Draining Love Story") {
		t.Fatalf("texts = %v, want the release asked for without its qualifier", texts)
	}
}

// A single names its release after its track. Asking twice would spend a reply
// window learning what the first phrase already said, and the one-word title is
// never asked for on its own.
func TestASingleIsNotAskedForTwiceUnderItsOwnName(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "Lvstlove", "Lvstlove")

	if !slices.Equal(texts, []string{"Cynthoni Lvstlove"}) {
		t.Fatalf("texts = %v, want the one phrase there is", texts)
	}
}

// Soulseek matches a phrase token by token, so a phrase carrying no tokens asks
// every peer for everything they have. A rung whose words all normalize away is
// not added to the ladder.
func TestARungWhoseWordsAllNormalizeAwayIsNotAdded(t *testing.T) {
	texts := TrackQueryTexts("", "｡ﾟ･ (>﹏<) ･ﾟ｡", "Draining Love Story")

	if !slices.Equal(texts, []string{"Draining Love Story"}) {
		t.Fatalf("texts = %#v, want the release as the only rung worth asking", texts)
	}
}

// A want with no release recorded still searches by everything else it has.
func TestAWantWithNoReleaseStillAsksForItsTrack(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "Femcels Forever", "")

	if !slices.Equal(texts, []string{"Cynthoni Femcels Forever", "Femcels Forever"}) {
		t.Fatalf("texts = %v, want no empty phrase asked for", texts)
	}
}

// Soulseek requires every token of the phrase to be present, and German peers
// write ä, ö, ü and ß as themselves, as ae/oe/ue/ss, and as the bare letter
// alike. No one phrase can carry all of those, so the word carrying the
// character is not asked for and every spelling of it is reached at once. Both
// titles below went out with their diacritics intact and found nothing.
func TestATrackIsLookedForWithoutTheWordsPeersTransliterate(t *testing.T) {
	for _, test := range []struct {
		name, title, release, want string
	}{
		{"eszett", "draußen essen spät schlafen 💤", "Tage vor 2000", "Pashanim essen schlafen"},
		{"umlaut", "Es wird ein heißer Sommer", "Tage vor 2000", "Pashanim Es wird ein Sommer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			texts := TrackQueryTexts("Pashanim", test.title, test.release)
			if texts[0] != test.want {
				t.Fatalf("texts = %#v, want %q asked for first", texts, test.want)
			}
		})
	}
}

// A release run asks in the same words a want does, so a release nobody spells
// the same way is asked for the same way.
func TestAReleaseIsLookedForWithoutTheWordsPeersTransliterate(t *testing.T) {
	got := CatalogueQueryTexts("Pashanim", "Es wird ein heißer Sommer")

	if !slices.Equal(got, []string{"Pashanim Es wird ein Sommer"}) {
		t.Fatalf("CatalogueQueryTexts() = %#v, want the one phrase every peer could answer", got)
	}
}

// A year names no music. Asking for it would reach every folder of theirs
// carrying one, and because the ladder stops at the first rung anybody answers,
// those folders would be the last word on a want whose exact spelling was never
// tried.
func TestATitleLeftWithOnlyAYearIsAskedForAsItIsSpelled(t *testing.T) {
	got := CatalogueQueryTexts("Pashanim", "Träume (2016)")

	if !slices.Equal(got, []string{"Pashanim Träume 2016"}) {
		t.Fatalf("CatalogueQueryTexts() = %#v, want the title asked for as it is spelled", got)
	}
}

// The label the catalogue hangs off a release is not what any peer's folder is
// named for. A title left with only that label would ask for every EP the artist
// ever put out, so the word that names the music stays, whatever peers do to its
// spelling.
func TestATitleLeftWithOnlyItsReleaseKindKeepsTheWordNamingIt(t *testing.T) {
	got := CatalogueQueryTexts("Pashanim", "Träume EP")

	if !slices.Equal(got, []string{"Pashanim Träume EP", "Pashanim Träume"}) {
		t.Fatalf("CatalogueQueryTexts() = %#v, want the release asked for by name", got)
	}
}

// A release named in symbol art has no words to ask for, and asking anyway
// leaves the artist standing alone: every file that peer is sharing by them,
// which is an answer to a question nobody asked.
func TestAReleaseWhoseNameIsAllSymbolsAddsNoRung(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "Femcels Forever", "｡ﾟ･ (>﹏<) ･ﾟ｡")

	if slices.Contains(texts, "Cynthoni") {
		t.Fatalf("texts = %v, want no phrase that is the artist alone", texts)
	}
}

// The last rung is what a want for a blacklisted artist has instead of nothing,
// so it is never lost to the reduction: a title left with one word is not worth
// asking alone, but the title as it is spelled still is.
func TestATitleReducedToOneWordIsStillAskedForAloneAsItIsSpelled(t *testing.T) {
	texts := TrackQueryTexts("Pashanim", "Spät Sommer", "Tage vor 2000")

	if texts[len(texts)-1] != "Spät Sommer" {
		t.Fatalf("texts = %v, want the title alone as the last thing tried", texts)
	}
}

// The label is a qualifier where the catalogue put it and nowhere else. Read off
// a shortened title, "EP" would look like the qualifier of a title it sits in
// the middle of, and one phrase would become two.
func TestALabelInTheMiddleOfATitleIsNotReadAsItsQualifier(t *testing.T) {
	got := CatalogueQueryTexts("Pashanim", "Airwaves EP Träume")

	if !slices.Equal(got, []string{"Pashanim Airwaves EP"}) {
		t.Fatalf("CatalogueQueryTexts() = %#v, want the one phrase there is", got)
	}
}

// Asking for less is not asking twice. The phrase gets shorter where the
// spellings disagree, which costs no reply window, because a ladder that grew a
// rung for every transliteration would outrun the budget one want's search has.
func TestAskingAroundADiacriticCostsTheLadderNoRung(t *testing.T) {
	texts := TrackQueryTexts("Pashanim", "draußen essen spät schlafen 💤", "Tage vor 2000")

	if !slices.Equal(texts, []string{
		"Pashanim essen schlafen", "Pashanim Tage vor 2000", "essen schlafen",
	}) {
		t.Fatalf("texts = %#v, want the three phrases there are", texts)
	}
}

// Dropping every word would leave a phrase naming none of the music, which is
// every file that peer is sharing rather than a question about this track.
func TestATitleWhoseEveryWordCarriesADiacriticIsAskedForAsItIsSpelled(t *testing.T) {
	texts := TrackQueryTexts("Pashanim", "Näher", "Tage vor 2000")

	if !slices.Contains(texts, "Pashanim Näher") {
		t.Fatalf("texts = %v, want the title asked for as it is spelled", texts)
	}
}

// The artist is what makes a phrase a question about this music at all. Every
// rung begins with it, and a ladder that dropped it would ask the network for a
// title alone from the top.
func TestTheArtistKeepsTheLettersPeersTransliterate(t *testing.T) {
	texts := TrackQueryTexts("Björk", "Hyperballad", "Post")

	if texts[0] != "Björk Hyperballad" {
		t.Fatalf("texts = %v, want the artist asked for as it is spelled", texts)
	}
}

// A title outside the Latin script has no ASCII form peers agree on, so there is
// nothing to ask around and dropping it would leave nothing worth asking.
func TestATitleInAnotherScriptIsAskedForAsItIsWritten(t *testing.T) {
	texts := TrackQueryTexts("宇多田ヒカル", "誰にも言わない", "初恋")

	if texts[0] != "宇多田ヒカル 誰にも言わない" {
		t.Fatalf("texts = %v, want the title asked for as it is written", texts)
	}
}

// The catalogue holds this title exactly as its artist wrote it: MusicBrainz
// spells both the recording and the release "erstersommerohnedich". The one long
// word is the music's name rather than a title that lost its spaces on the way
// here, so peers who tagged their copy from that release carry the same token —
// and splitting it would be inventing word boundaries nothing in the catalogue
// has.
func TestATitleItsArtistWroteAsOneWordIsAskedForAsOneWord(t *testing.T) {
	texts := TrackQueryTexts("Pashanim", "erstersommerohnedich", "erstersommerohnedich")

	if !slices.Equal(texts, []string{"Pashanim erstersommerohnedich"}) {
		t.Fatalf("texts = %#v, want the one phrase there is", texts)
	}
}

// A title written entirely in symbols leaves the exact rung as the artist and
// nothing else, which is every file that peer is sharing by them, put to the
// network as though it were a question about one release. The release side has
// refused that phrase since it was added; this is the title side of the same
// refusal.
func TestATitleThatSaysNothingLeavesTheArtistUnasked(t *testing.T) {
	texts := CatalogueQueryTexts("Cynthoni", "｡ﾟ･ (>﹏<) ･ﾟ｡")

	if len(texts) != 0 {
		t.Fatalf("texts = %#v, want nothing worth asking a peer", texts)
	}
}

// It is the first rung of a want's ladder that the artist alone reaches through,
// so the phrase was not merely asked but asked before everything else.
func TestAWantWithNothingButItsArtistToAskForHasNoLadder(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "｡ﾟ･ (>﹏<) ･ﾟ｡", "")

	if len(texts) != 0 {
		t.Fatalf("texts = %#v, want nothing worth asking a peer", texts)
	}
}

// Losing the title costs the rungs the title made, and no others.
func TestAWantWhoseTitleSaysNothingStillAsksForItsRelease(t *testing.T) {
	texts := TrackQueryTexts("Cynthoni", "｡ﾟ･ (>﹏<) ･ﾟ｡", "Draining Love Story")

	if !slices.Equal(texts, []string{"Cynthoni Draining Love Story"}) {
		t.Fatalf("texts = %#v, want the release still asked for", texts)
	}
}

// The title on its own is the last rung, and it keeps the release-kind label the
// rung above it exists to shed. A reader of the finished phrase cannot tell that
// "EP" from a word of the name, so the ladder says which it was.
func TestTheLadderSaysWhichRungStillCarriesAReleaseKind(t *testing.T) {
	rungs := TrackQueryRungs("Cynthoni", "Femcels Forever - EP", "Femcels Forever")

	for _, rung := range rungs {
		want := rung.Text == "Cynthoni Femcels Forever EP" || rung.Text == "Femcels Forever EP"
		if rung.CarriesReleaseKind != want {
			t.Fatalf("rungs = %#v, want only the phrases ending in the label to say so", rungs)
		}
	}
}

// "EP" in the middle of a title is a word of the name, and askableText can leave
// it at the end of the phrase by dropping what followed it. The label is read
// from the title as the catalogue spells it, so the phrase ending in it carries
// no label at all.
func TestAWordThatOnlyLooksLikeALabelIsNotOne(t *testing.T) {
	rungs := CatalogueQueryRungs("Pashanim", "Airwaves EP Träume")

	if len(rungs) != 1 || rungs[0].Text != "Pashanim Airwaves EP" || rungs[0].CarriesReleaseKind {
		t.Fatalf("rungs = %#v, want the one phrase there is, labelling nothing", rungs)
	}
}

// Two derivations can reach the same phrase and disagree about the label: the
// title "Single EP" sheds its label into "Anetha Single", which the release
// "Single" arrives at while still carrying one. The rung that got there first is
// the one the ladder asked in, and reading it the other way would have a repeat
// pass over a shed phrase for one that still carries what it shed.
func TestAPhraseTheLadderAlreadyCarriesKeepsItsFirstReading(t *testing.T) {
	rungs := TrackQueryRungs("Anetha", "Single EP", "Single")

	shed := slices.IndexFunc(rungs, func(rung Rung) bool { return rung.Text == "Anetha Single" })
	if shed < 0 || rungs[shed].CarriesReleaseKind {
		t.Fatalf("rungs = %#v, want the shed phrase reading as the rung that shed it", rungs)
	}
}

// Unicode spells an accented letter two ways, and the two render identically.
// A phrase typed on macOS, or carried in from a streaming service, may arrive
// in the decomposed spelling; a phrase built from the catalogue does not. Both
// have to become the same phrase, because a peer sharing the file named it one
// way and Soulseek requires every token to match.
func TestNormalizeQueryTextJoinsTheTwoSpellingsOfAnAccent(t *testing.T) {
	// "Träume": one code point for the accented letter, against "a" followed
	// by the combining diaeresis.
	composed := NormalizeQueryText("Airwaves EP Träume")
	decomposed := NormalizeQueryText("Airwaves EP Träume")
	if composed != decomposed {
		t.Fatalf("decomposed = %q, composed = %q; the two spellings must agree", decomposed, composed)
	}
	if composed != "Airwaves EP Träume" {
		t.Fatalf("normalized = %q, want the word kept whole", composed)
	}
}

// Composing joins two spellings of the same characters. It must never fold one
// character into another, which would make two different words one phrase and
// send a peer looking for the wrong thing.
func TestNormalizeQueryTextKeepsDifferentWordsDifferent(t *testing.T) {
	if accented, plain := NormalizeQueryText("Träume"), NormalizeQueryText("Traume"); accented == plain {
		t.Fatalf("%q and %q became the same phrase", "Träume", "Traume")
	}
}

// MusicBrainz stores some titles with a space after every character, and
// Soulseek requires every token of a phrase to match, so the title as
// spelled reaches nobody: "A D H D", "G R E Y G O D S" and
// "6 6 6 F O R E V E R" all came back Completed, Errored, with zero
// candidates, repeatedly, on the live instance.
func TestCollapseSingleCharacterRunsJoinsARunOfThreeOrMore(t *testing.T) {
	for name, testCase := range map[string]struct{ input, want string }{
		"a spelled-out word collapses": {
			input: "A D H D",
			want:  "ADHD",
		},
		"digits collapse the same as letters": {
			input: "6 6 6 F O R E V E R",
			want:  "666FOREVER",
		},
		"only the run collapses, not the word after it": {
			input: "A D H D live",
			want:  "ADHD live",
		},
		"two separate runs each collapse on their own": {
			input: "A D H D and G R E Y G O D S",
			want:  "ADHD and GREYGODS",
		},
		"a run of two is not a pattern": {
			input: "U K",
			want:  "U K",
		},
		// "Jay" is three letters, so the run it starts is the "Z" alone: one
		// token, well under the threshold, and the phrase keeps its two words.
		"Jay Z carries only one single-character token": {
			input: "Jay Z",
			want:  "Jay Z",
		},
		"three single-character tokens meet the threshold": {
			input: "M I A",
			want:  "MIA",
		},
		"a string with no run is returned unchanged": {
			input: "Sewerslvt Suffering From Melancholia",
			want:  "Sewerslvt Suffering From Melancholia",
		},
	} {
		if got := collapseSingleCharacterRuns(testCase.input); got != testCase.want {
			t.Errorf("%s: collapseSingleCharacterRuns(%q) = %q, want %q", name, testCase.input, got, testCase.want)
		}
	}
}

// The collapse runs on the title before it joins the artist. Doing it after
// would let a one-letter artist's own token get swept into a run that started
// in the title, turning "A" and "D H D" into "ADHD" and losing which part was
// the artist.
func TestCollapseSingleCharacterRunsDoesNotReachAcrossTheArtistJoin(t *testing.T) {
	texts := CatalogueQueryTexts("A", "D H D")

	if !slices.Equal(texts, []string{"A DHD"}) {
		t.Fatalf("CatalogueQueryTexts() = %#v, want the artist kept apart from the collapsed title", texts)
	}
}

// A want for "A D H D" now asks for the word the track is named, at every
// rung that carries the title.
func TestATrackTitleSpelledOutOneCharacterAtATimeIsAskedForAsOneWord(t *testing.T) {
	texts := TrackQueryTexts("Sewerslvt", "A D H D", "Miscellany")

	if !slices.Equal(texts, []string{"Sewerslvt ADHD", "Sewerslvt Miscellany", "ADHD"}) {
		t.Fatalf("texts = %#v, want the collapsed title asked for at every rung it reaches", texts)
	}
}

// The title and the album on a release can differ only in casing, and
// Soulseek's own matching does not: "Taking a Walk" and "Taking A Walk" are
// one question, and asking it twice spends a second search creation, a second
// reply window and a second tick against the ban for nothing.
func TestARungIsNotDuplicatedByCaseAlone(t *testing.T) {
	texts := TrackQueryTexts("Trippie Redd", "Taking a Walk", "Taking A Walk")

	seen := map[string]bool{}
	for _, text := range texts {
		lower := strings.ToLower(text)
		if seen[lower] {
			t.Fatalf("texts = %#v, want %q asked for once", texts, text)
		}
		seen[lower] = true
	}
}

// A trailing release-kind label carries no dash or bracket of its own, so it
// has to be read off the last word the same way labelsARelease reads it.
func TestPlainTitleDropsATrailingReleaseKindWithNoDash(t *testing.T) {
	if got := plainTitle("Femcels Forever EP"); got != "Femcels Forever" {
		t.Fatalf("plainTitle(%q) = %q, want the label shed", "Femcels Forever EP", got)
	}
}

// A single-word label is not dropped: shedding it would leave nothing to ask
// for, and "EP" alone is still worth trying as it is spelled.
func TestPlainTitleKeepsALabelThatIsTheWholeTitle(t *testing.T) {
	if got := plainTitle("EP"); got != "EP" {
		t.Fatalf("plainTitle(%q) = %q, want the word kept", "EP", got)
	}
}

// The last-resort rung is the ladder's broadest, reached only once everything
// above it has already found nothing. Built from the raw title it kept every
// featured artist as a literal required token — "ROOT OF EVIL (feat. Destroy
// Lonely & Lil Uzi Vert)" asked for nine tokens, narrower than the rung above
// it that had already failed. Built from plainTitle it asks for the title
// alone, a subset of every rung above it rather than a superset.
func TestTheLastResortRungIsBuiltFromThePlainTitle(t *testing.T) {
	rungs := TrackQueryRungs("Playboi Carti", "ROOT OF EVIL (feat. Destroy Lonely & Lil Uzi Vert)", "")

	last := rungs[len(rungs)-1].Text
	if last != "ROOT OF EVIL" {
		t.Fatalf("last rung = %q, want the plain title asked for alone", last)
	}
	if strings.Contains(strings.ToLower(last), "feat") {
		t.Fatalf("last rung = %q, want no featured-artist tokens in the broadest rung", last)
	}
}
