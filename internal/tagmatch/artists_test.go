package tagmatch

import "testing"

// artist is one credited artist for the tables below: the name the recording
// prints first, then the artist's own name and any aliases.
func artist(names ...string) CreditedArtist { return CreditedArtist{Names: names} }

// The credits the acquisition loop refused on the artist tag alone, 308 of them
// over the project's life. Every pair here is one artist under two names, or a
// file naming part of a longer credit, and neither is two artists disagreeing.
func TestArtistsReadsOneArtistUnderSeveralNames(t *testing.T) {
	travis := artist("Travi$ Scott", "Travis Scott")
	carti := artist("$ir Cartier", "Playboi Carti")
	cases := []struct {
		name     string
		observed string
		credited []CreditedArtist
		want     Verdict
	}{
		{"the credited name is an alias of the artist the file names",
			"Travis Scott", []CreditedArtist{travis}, Agrees},
		{"the file names the lead artist of a feat credit",
			"Travis Scott", []CreditedArtist{travis, artist("Bon Iver")}, Unknown},
		{"a credited alias nobody else uses",
			"Playboi Carti", []CreditedArtist{carti}, Agrees},
		{"the file names the guest rather than the lead",
			"Travis Scott", []CreditedArtist{artist("DJ Mustard"), travis}, Unknown},
		{"the guest of a credit with a punctuated lead",
			"Playboi Carti", []CreditedArtist{artist("D.R.A.M."), carti}, Unknown},
		{"the last of four credited artists",
			"M.I.A.", []CreditedArtist{
				travis, artist("Future"), artist("Young Thug"), artist("M.I.A."),
			}, Unknown},
		{"the producer named last on a five-artist credit",
			"Rvssian", []CreditedArtist{
				artist("Farruko"), artist("Nicki Minaj"), travis,
				artist("Bad Bunny"), artist("Rvssian"),
			}, Unknown},
		{"the same five artists in another order",
			"Hudson Mohawke, Pusha T, French Montana, Future, Travis Scott",
			[]CreditedArtist{
				artist("Hudson Mohawke"), artist("Pusha T"), artist("Future"),
				travis, artist("French Montana"),
			}, Agrees},
		{"feat. and an ampersand are the same join",
			"Kavinsky feat. Lovefoxxx",
			[]CreditedArtist{artist("Kavinsky"), artist("Lovefoxxx")}, Agrees},
		{"the file names the lead of a two-artist credit",
			"Travis Scott", []CreditedArtist{artist("Asake"), travis}, Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Artists(tc.observed, tc.credited); got != tc.want {
				t.Fatalf("Artists(%q, %v) = %v, want %v",
					tc.observed, tc.credited, got, tc.want)
			}
		})
	}
}

// What the set comparison still refuses. A name nobody credits is somebody
// else, whatever else the file and the recording agree about, and that is the
// cover-version trap this comparison exists to keep shut.
func TestArtistsRefusesAnUncreditedName(t *testing.T) {
	travis := artist("Travi$ Scott", "Travis Scott")
	cases := []struct {
		name     string
		observed string
		credited []CreditedArtist
		want     Verdict
	}{
		{"a collective is not the artists it collects",
			"JACKBOYS", []CreditedArtist{travis, artist("Don Toliver")}, Differs},
		{"an artist outside the credit",
			"Drake", []CreditedArtist{travis}, Differs},
		{"one credited name and one stranger",
			"Travis Scott & Drake", []CreditedArtist{travis, artist("Don Toliver")},
			Differs},
		{"a longer name is not the short one",
			"Jaden Smith", []CreditedArtist{artist("Jaden")}, Differs},
		{"an empty tag says nothing",
			"", []CreditedArtist{travis}, Unknown},
		{"a credit with no names says nothing",
			"Travis Scott", []CreditedArtist{artist("")}, Unknown},
		{"no credit at all says nothing", "Travis Scott", nil, Unknown},
		{"a compilation credit says nothing about one track",
			"Various Artists", []CreditedArtist{travis}, Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Artists(tc.observed, tc.credited); got != tc.want {
				t.Fatalf("Artists(%q, %v) = %v, want %v",
					tc.observed, tc.credited, got, tc.want)
			}
		})
	}
}

// A name that carries a join of its own is the one artist it is. Splitting it
// would leave pieces nobody credits, and the pieces would read as strangers.
func TestArtistsKeepsANameThatHoldsAJoin(t *testing.T) {
	cases := []struct {
		name     string
		observed string
		credited []CreditedArtist
		want     Verdict
	}{
		{"a comma inside a name",
			"Tyler, The Creator", []CreditedArtist{artist("Tyler, The Creator")}, Agrees},
		{"a comma and an ampersand inside a name",
			"Earth, Wind & Fire", []CreditedArtist{artist("Earth, Wind & Fire")}, Agrees},
		{"the same name with the ampersand written out",
			"Earth, Wind and Fire", []CreditedArtist{artist("Earth, Wind & Fire")}, Agrees},
		{"a slash inside a name",
			"AC/DC", []CreditedArtist{artist("AC/DC")}, Agrees},
		{"a comma name beside a second artist",
			"Tyler, The Creator & Kali Uchis",
			[]CreditedArtist{artist("Tyler, The Creator"), artist("Kali Uchis")}, Agrees},
		{"a trailing x is part of the name",
			"Malcolm X", []CreditedArtist{artist("Malcolm X")}, Agrees},
		{"an artist called X",
			"X", []CreditedArtist{artist("X")}, Agrees},
		{"the pieces are read as artists where the recording credits them",
			"Earth, Wind & Fire",
			[]CreditedArtist{artist("Earth"), artist("Wind"), artist("Fire")}, Agrees},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Artists(tc.observed, tc.credited); got != tc.want {
				t.Fatalf("Artists(%q, %v) = %v, want %v",
					tc.observed, tc.credited, got, tc.want)
			}
		})
	}
}

// identified is one credited artist the provider says who is: an identifier and
// the names the credit prints for them.
func identified(id string, names ...string) CreditedArtist {
	return CreditedArtist{ID: id, Names: names}
}

// Two credits from the same catalogue are compared by who they credit, which the
// catalogue states outright. A name is what the catalogue printed at the time,
// and MusicBrainz renamed Kanye West to Ye in 2021 without touching the rows
// entered before it.
func TestSameArtistIDsComparesWhoIsCredited(t *testing.T) {
	const kanye = "164f0d73-1234-4e2c-8743-d77bf2191051"
	cases := []struct {
		name        string
		left, right []CreditedArtist
		want        Verdict
	}{
		{"one artist under the name before and after a rename",
			[]CreditedArtist{identified(kanye, "Kanye West")},
			[]CreditedArtist{identified(kanye, "Ye")}, Agrees},
		{"the same artists in another order",
			[]CreditedArtist{identified("a", "Kids See Ghosts"), identified(kanye, "Ye")},
			[]CreditedArtist{identified(kanye, "Ye"), identified("a", "Kids See Ghosts")},
			Agrees},
		{"an identifier written in the other case",
			[]CreditedArtist{identified("164F0D73-1234-4E2C-8743-D77BF2191051", "Ye")},
			[]CreditedArtist{identified(kanye, "Ye")}, Agrees},
		{"one artist credited twice is one artist",
			[]CreditedArtist{identified(kanye, "Ye"), identified(kanye, "Kanye West")},
			[]CreditedArtist{identified(kanye, "Ye")}, Agrees},
		{"two artists the catalogue spells alike",
			[]CreditedArtist{identified("a", "Nova")},
			[]CreditedArtist{identified("b", "Nova")}, Differs},
		{"a guest on one side only",
			[]CreditedArtist{identified("a", "88GLAM")},
			[]CreditedArtist{identified("a", "88GLAM"), identified("b", "PnB Rock")},
			Differs},
		{"an artist the catalogue holds no entity for",
			[]CreditedArtist{artist("Ye")},
			[]CreditedArtist{identified(kanye, "Ye")}, Unknown},
		{"a compilation credit says nothing about any one track",
			[]CreditedArtist{identified("a", "Various Artists")},
			[]CreditedArtist{identified("a", "Various Artists")}, Unknown},
		{"a side with no artists at all",
			nil, []CreditedArtist{identified(kanye, "Ye")}, Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameArtistIDs(tc.left, tc.right); got != tc.want {
				t.Fatalf("SameArtistIDs(%v, %v) = %v, want %v",
					tc.left, tc.right, got, tc.want)
			}
		})
	}
}
