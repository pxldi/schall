package tagmatch

import "testing"

// The join spellings are equivalent: a comma, an ampersand, "and", and the
// multiplication sign all say "together with", and none of them may change
// what a credit agrees with.
func TestCreditReadsJoinSpellingsAsOneJoin(t *testing.T) {
	cases := []struct {
		name     string
		observed string
		expected string
		want     Verdict
	}{
		{"comma-joined collab is the primary with guests",
			"Quality Control, Lil Yachty & Young Thug", "Quality Control", Agrees},
		{"primary against a comma-joined collab",
			"Gucci Mane", "Gucci Mane, Bruno Mars & Kodak Black", Agrees},
		{"multiplication sign is the x join",
			"Marshmello × Lil Peep", "Marshmello", Agrees},
		{"x join both spelled out",
			"Marshmello × Lil Peep", "Marshmello x Lil Peep", Agrees},
		{"comma and ampersand spell the same credit",
			"Crosby, Stills & Nash", "Crosby, Stills and Nash", Agrees},
		{"comma-joined guests after a feat credit",
			"88rising, Famous Dex, Keith Ape & Verbal", "88rising", Agrees},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Credit(tc.observed, tc.expected); got != tc.want {
				t.Fatalf("Credit(%q, %q) = %v, want %v", tc.observed, tc.expected, got, tc.want)
			}
		})
	}
}

// The refusals the join change must not loosen. A credit that continues with
// an ordinary word is a different artist — the cover-version trap — and a
// name that merely shares a prefix stays a disagreement.
func TestCreditStillRefusesWhatIsNotAJoin(t *testing.T) {
	cases := []struct {
		name     string
		observed string
		expected string
		want     Verdict
	}{
		{"a longer name is not the short one plus a guest",
			"Jaden Smith", "Jaden", Differs},
		{"shared prefix without a join word",
			"Paris Shadows", "Paris", Differs},
		{"different artists entirely", "Nirvana", "Pearl Jam", Differs},
		{"a credit that omits a join is a different spelling too far",
			"Crosby, Stills & Nash", "Crosby Stills and Nash", Differs},
		{"the single-letter artist X is a name, not a join", "X", "X", Agrees},
		{"X against another artist", "X", "Y", Differs},
		{"silence on either side proves nothing", "", "Quality Control", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Credit(tc.observed, tc.expected); got != tc.want {
				t.Fatalf("Credit(%q, %q) = %v, want %v", tc.observed, tc.expected, got, tc.want)
			}
		})
	}
}

// A compilation credit says nothing about any one track on it, so it is silence
// rather than a disagreement: the track really is by whoever it is by, and
// "Various Artists" is a statement about the record.
func TestCreditReadsACompilationAsSayingNothing(t *testing.T) {
	cases := []struct {
		name     string
		observed string
		expected string
	}{
		{"the file says various artists", "Various Artists", "Portishead"},
		{"the catalogue says various artists", "Portishead", "various artists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Credit(tc.observed, tc.expected); got != Unknown {
				t.Fatalf("Credit(%q, %q) = %v, want Unknown", tc.observed, tc.expected, got)
			}
		})
	}
}

// Both sides naming the compilation is the same name, which agrees before the
// compilation rule is ever reached.
func TestCreditAgreesWhenBothSidesAreTheCompilation(t *testing.T) {
	if got := Credit("Various Artists", "various artists"); got != Agrees {
		t.Fatalf("Credit() = %v, want Agrees", got)
	}
}

// An identifier is exact. Taggers write an ISRC in either case and with stray
// spacing, and neither of those makes it a different recording.
func TestIdentifierIgnoresCaseAndSurroundingSpace(t *testing.T) {
	if got := Identifier(" gbaye9700325 ", "GBAYE9700325"); got != Agrees {
		t.Fatalf("Identifier() = %v, want Agrees", got)
	}
}

// Silence is not agreement. An absent identifier proves nothing in either
// direction, so it can never be the evidence a match rests on.
func TestIdentifierReadsAnAbsentSideAsSilence(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"nothing on the left", "", "GBAYE9700325"},
		{"nothing on the right", "GBAYE9700325", ""},
		{"nothing on either side", "", ""},
		{"whitespace is nothing", "   ", "GBAYE9700325"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Identifier(tc.left, tc.right); got != Unknown {
				t.Fatalf("Identifier(%q, %q) = %v, want Unknown", tc.left, tc.right, got)
			}
		})
	}
}

// Two identifiers that are not the same name two different recordings, and that
// is a contradiction rather than an absence of evidence.
func TestIdentifierReportsTwoDifferentIdentifiersAsADisagreement(t *testing.T) {
	if got := Identifier("GBAYE9700325", "GBAYE9700326"); got != Differs {
		t.Fatalf("Identifier() = %v, want Differs", got)
	}
}

// Taggers disagree about punctuation and casing and about nothing else that
// matters here, so a title survives all of it.
func TestTitleIgnoresWhatTaggersDisagreeAbout(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"case", "KARMA POLICE", "karma police"},
		{"punctuation", "Who Are You? (Live)", "Who Are You  Live"},
		{"an ASCII apostrophe against a typographic one", "Don't Look Back", "Don’t Look Back"},
		{"an apostrophe against none at all", "Dont Look Back", "Don't Look Back"},
		{"collapsed spacing", "  Karma   Police  ", "Karma Police"},
		{"a title written in another script", "君の名は", "君の名は"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.left, tc.right); got != Agrees {
				t.Fatalf("Title(%q, %q) = %v, want Agrees", tc.left, tc.right, got)
			}
		})
	}
}

// A title that reduces to nothing is a tag that says nothing, and silence is
// never agreement — otherwise two files with unreadable titles would agree.
func TestTitleReadsASideThatSaysNothingAsSilence(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"an absent title", "", "Karma Police"},
		{"a title of only punctuation", "---", "Karma Police"},
		{"a title of only an apostrophe", "'", "Karma Police"},
		{"neither side says anything", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.left, tc.right); got != Unknown {
				t.Fatalf("Title(%q, %q) = %v, want Unknown", tc.left, tc.right, got)
			}
		})
	}
}

// Normalization must not reach so far that two different titles collapse into
// one: the letters and digits themselves still have to agree.
func TestTitleReportsTwoDifferentTitlesAsADisagreement(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"different words", "Karma Police", "Paranoid Android"},
		{"a remix qualifier is part of the title", "Lvstlove", "Lvstlove (Night Drive Mix)"},
		{"a digit is part of the title", "Cyberia lyr1", "Cyberia lyr3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.left, tc.right); got != Differs {
				t.Fatalf("Title(%q, %q) = %v, want Differs", tc.left, tc.right, got)
			}
		})
	}
}

// The case this rule was written for. Spotify lists Hadone's "How To Fake
// Success" with the version suffix its release prints on every track, while
// MusicBrainz recording 3ddb41a9-40bd-4f9a-bbed-68a4646d1a44 writes the title
// plainly. The recording carries no ISRC, so the title was the only thing left
// that could have told the two apart, and it was telling them apart wrongly:
// this is one 6:27 of audio under two naming conventions.
func TestTitleAgreesWithTheOriginalMixSuffixSpotifyPrints(t *testing.T) {
	if got := Title("How To Fake Success - Original mix", "How to Fake Success"); got != Agrees {
		t.Fatalf("Title() = %v, want Agrees", got)
	}
}

// "Original Mix" says a recording is not a remix; it does not say which
// recording it is. Whichever side writes it, and however it is punctuated, the
// two sides are naming the same audio.
func TestTitleReadsAnOriginalMixLabelAsNoPartOfTheTitle(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"the entry carries the label", "Detection - Original Mix", "Detection"},
		{"the catalogue carries the label", "Detection", "Detection - Original Mix"},
		{"the label in parentheses", "Detection (Original Mix)", "Detection"},
		{"the label with no punctuation at all", "Detection Original Mix", "Detection"},
		{"version is the same convention as mix", "Detection - Original Version", "Detection"},
		{"both sides label it, spelled differently", "Detection - Original Mix", "Detection (Original Version)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.left, tc.right); got != Agrees {
				t.Fatalf("Title(%q, %q) = %v, want Agrees", tc.left, tc.right, got)
			}
		})
	}
}

// Every other trailing qualifier names different audio, and dropping the
// original-mix label must not have made this package readier to believe any of
// them. A remix is somebody else's record of the same composition, and calling
// it the original is the false match this codebase exists to refuse.
func TestTitleStillRefusesEveryQualifierThatIsNotTheOriginal(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"a remix is a different recording", "Detection - Regal Remix", "Detection"},
		{"so is a differently named mix", "June - Raw Version Mix", "June"},
		{"so is a remixer's own mix", "Rigid - Kobosil 44 Rush Mix", "Rigid"},
		{"a radio edit is a different recording", "Detection - Radio Edit", "Detection"},
		{"a live take is a different recording", "Detection - Live", "Detection"},
		{"an extended mix is not the original one", "Detection - Extended Mix", "Detection"},
		{"two labelled recordings, only one of them the original",
			"Detection - Original Mix", "Detection - Radio Edit"},
		{"the remixer is named where the original label would be",
			"Detection - Regal Remix", "Detection - Original Mix"},
		{"a title merely ending in those letters is not the label",
			"Aboriginal Mix", "A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.left, tc.right); got != Differs {
				t.Fatalf("Title(%q, %q) = %v, want Differs", tc.left, tc.right, got)
			}
		})
	}
}

// A recording really called "Original Mix" has to keep the only title it has.
// Stripping it to nothing would turn a title that says something into silence,
// and silence agrees with every other silent title there is.
func TestTitleKeepsATitleThatIsOnlyTheOriginalMixLabel(t *testing.T) {
	if got := Title("Original Mix", "Original Mix"); got != Agrees {
		t.Fatalf("Title() = %v, want Agrees", got)
	}
	if got := Title("Original Mix", "How to Fake Success"); got != Differs {
		t.Fatalf("Title() = %v, want Differs", got)
	}
	if got := Title("Original Version", ""); got != Unknown {
		t.Fatalf("Title() = %v, want Unknown", got)
	}
}

// The qualifier word is what makes "original" readable as a label rather than
// as part of a name. Without it there is nothing to tell the convention from a
// title that simply ends that way, and this package does not guess which one it
// is looking at.
func TestTitleReadsATrailingOriginalWithoutAQualifierAsPartOfTheTitle(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
	}{
		{"the bare word after a dash", "Detection - Original", "Detection"},
		{"the bare word as the last word of a name", "The Original", "The"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.left, tc.right); got != Differs {
				t.Fatalf("Title(%q, %q) = %v, want Differs", tc.left, tc.right, got)
			}
		})
	}
}

// SameText is the normalization the verdicts are built on, and it has to stay
// the same or two callers would disagree about what agreement is. It is that
// normalization and no more: Title goes one step further than it does, dropping
// an original-mix label that SameText keeps, because SameText is asked about
// values that are not titles at all.
func TestSameTextIsTheNormalizationAndNothingBeyondIt(t *testing.T) {
	cases := []struct {
		name        string
		left, right string
		want        bool
	}{
		{"case and punctuation", "OK Computer", "ok, computer!", true},
		{"apostrophe styles", "Don’t", "Dont", true},
		{"different words", "Dummy", "Portishead", false},
		{"nothing against nothing", "", "  ", true},
		{"nothing against something", "", "Dummy", false},
		{"the label Title drops is text like any other", "Detection", "Detection - Original Mix", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameText(tc.left, tc.right); got != tc.want {
				t.Fatalf("SameText(%q, %q) = %v, want %v", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

// PrimaryCredit is parsing, not matching: it cuts at the first comma and
// nothing else, and what it returns still has to agree under Credit before it
// counts for anything.
func TestPrimaryCredit(t *testing.T) {
	cases := []struct {
		credit string
		want   string
	}{
		{"Quality Control, Lil Yachty, Young Thug", "Quality Control"},
		{"Marshmello × Lil Peep", "Marshmello × Lil Peep"},
		{"BONES", "BONES"},
		{"Tyler, The Creator", "Tyler"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := PrimaryCredit(tc.credit); got != tc.want {
			t.Fatalf("PrimaryCredit(%q) = %q, want %q", tc.credit, got, tc.want)
		}
	}
}
