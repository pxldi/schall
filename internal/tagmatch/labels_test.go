package tagmatch

import "testing"

// A bracketed group that describes the copy, or names who produced it, says
// nothing about which recording this is. Everything else stays, because
// everything else names different audio.
func TestBracketedLabelsComeOffAndVersionWordsStay(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"Watchfire (Official Video)", "Watchfire"},
		{"Watchfire [OFFICIAL AUDIO] (HD)", "Watchfire"},
		{"Watchfire [Prod. Halden Rowe]", "Watchfire"},
		{"Watchfire (Produced by Halden Rowe)", "Watchfire"},
		{"Watchfire [Halden Rowe Prod.]", "Watchfire [Halden Rowe Prod.]"},
		{"Watchfire (sped up)", "Watchfire (sped up)"},
		{"Watchfire (Explicit)", "Watchfire"},
		{"Watchfire (Clean)", "Watchfire"},
		{"Watchfire [Instrumental]", "Watchfire [Instrumental]"},
		{"Watchfire (Official Video Slowed)", "Watchfire (Official Video Slowed)"},
		{"Watchfire ()", "Watchfire ()"},
		{"Watchfire (Official Video", "Watchfire (Official Video"},
	}

	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			if got := WithoutBracketedLabels(test.raw); got != test.want {
				t.Errorf("WithoutBracketedLabels(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

// A character that prints nothing is not a separator. MusicBrainz credits one
// row of "I Will Celebrate for Stepping on Broken Glass" to "$uicideboy$" with a
// left-to-right embedding inside the name, and read as a space that character
// makes the name two words that are nobody.
func TestTitleFoldsDiacritics(t *testing.T) {
	if got := Title("Cancún", "Cancun"); got != Agrees {
		t.Fatalf("Title() = %v, want %v", got, Agrees)
	}
}

func TestInvisibleCharactersAreNotSeparators(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"left-to-right embedding", "Sou‪ly", "souly"},
		{"zero-width joiner", "Sou‍ly", "souly"},
		{"byte-order mark", "\ufeffSouly", "souly"},
		{"a control character", "Sou\x01ly", "souly"},
		{"a newline still separates", "Souly\nHazel", "souly hazel"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Text(test.raw); got != test.want {
				t.Errorf("Text(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

// The credit comparison reads the same normalization, so a name carrying an
// invisible character is the artist it is.
func TestACreditWithAnInvisibleCharacterIsTheSameArtist(t *testing.T) {
	credited := []CreditedArtist{{Names: []string{"$uicideboy$"}}}
	if verdict := Artists("$‪uicideboy$", credited); verdict != Agrees {
		t.Errorf("Artists() = %v, want %v", verdict, Agrees)
	}
}
