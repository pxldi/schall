package tagging

import (
	"testing"

	"github.com/google/uuid"
)

func TestTwoArtistsAreWrittenAsTwoValuesRatherThanOneString(t *testing.T) {
	porter := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	nina := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	written := Tags{
		Artist: "Porter Robinson & Ninajirachi",
		Artists: []Credit{
			{Name: "Porter Robinson", MusicBrainzArtistID: porter},
			{Name: "Ninajirachi", MusicBrainzArtistID: nina},
		},
	}.values()

	names := written[keyArtists]
	if len(names) != 2 || names[0] != "Porter Robinson" || names[1] != "Ninajirachi" {
		t.Fatalf("the two artists were written as %q", names)
	}
	identifiers := written[keyArtistIDs]
	if len(identifiers) != 2 || identifiers[0] != porter.String() || identifiers[1] != nina.String() {
		t.Fatalf("the artist identifiers were written as %q", identifiers)
	}
}

// A credit Schall holds only as one joined string is one value. Cutting it at
// the comma would make a band whose name holds one into two artists, and a tag
// nobody checks afterwards would carry that forever.
func TestAJoinedCreditWithNoArrayBehindItIsWrittenAsOneValue(t *testing.T) {
	written := Tags{Artist: "Earth, Wind & Fire"}.values()

	if got := written[keyArtist]; len(got) != 1 || got[0] != "Earth, Wind & Fire" {
		t.Fatalf("the joined credit was written as %q", got)
	}
	if got := written[keyArtists]; len(got) != 0 {
		t.Fatalf("a credit nobody split was written as a list: %q", got)
	}
	if got := written[keyArtistIDs]; len(got) != 0 {
		t.Fatalf("artist identifiers were invented: %q", got)
	}
}

// The names and the identifiers are read off against each other by position,
// and a list with a gap in it cannot say where the gap is: an empty value does
// not survive the write, so a partial list hands one artist's identifier to
// another's name. One unidentified artist withholds the whole list.
func TestAnArtistWithNoIdentifierWithholdsTheWholeIdentifierList(t *testing.T) {
	known := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	written := Tags{Artists: []Credit{
		{Name: "Unidentified Support"},
		{Name: "The Headliner", MusicBrainzArtistID: known},
	}}.values()

	if identifiers := written[keyArtistIDs]; len(identifiers) != 0 {
		t.Fatalf("a credit with an unidentified artist wrote identifiers %q", identifiers)
	}
}

// Only the identifiers are ever withheld. Rejoining the names has to reproduce
// what MusicBrainz printed, so no credited artist is dropped from that list for
// being unknown.
func TestAnArtistWithNoIdentifierIsStillNamedInItsPlace(t *testing.T) {
	written := Tags{Artists: []Credit{
		{Name: "Unidentified Support"},
		{Name: "The Headliner", MusicBrainzArtistID: uuid.MustParse("33333333-3333-3333-3333-333333333333")},
	}}.values()

	names := written[keyArtists]
	if len(names) != 2 || names[0] != "Unidentified Support" || names[1] != "The Headliner" {
		t.Fatalf("the artists were written as %q", names)
	}
}

// The album's artists are the same bargain as the track's; setCredits serves
// both, and an album credited to one known artist and one unknown links neither.
func TestAnUnidentifiedAlbumArtistWithholdsTheAlbumIdentifierList(t *testing.T) {
	written := Tags{AlbumArtists: []Credit{
		{Name: "The Headliner", MusicBrainzArtistID: uuid.MustParse("33333333-3333-3333-3333-333333333333")},
		{Name: "Unidentified Support"},
	}}.values()

	if identifiers := written[keyAlbumArtistIDs]; len(identifiers) != 0 {
		t.Fatalf("an album credit with an unidentified artist wrote identifiers %q", identifiers)
	}
	names := written[keyAlbumArtists]
	if len(names) != 2 || names[0] != "The Headliner" || names[1] != "Unidentified Support" {
		t.Fatalf("the album artists were written as %q", names)
	}
}

// A credit nobody named cannot hold its place any more than a missing
// identifier can, and here there is nothing to withhold instead: the names are
// the list. So neither list is written and ARTIST carries the credit alone.
func TestACreditWithNoNameWithholdsBothLists(t *testing.T) {
	written := Tags{
		Artist: "Porter Robinson & Ninajirachi",
		Artists: []Credit{
			{Name: "Porter Robinson", MusicBrainzArtistID: uuid.MustParse("11111111-1111-1111-1111-111111111111")},
			{Name: "", MusicBrainzArtistID: uuid.MustParse("22222222-2222-2222-2222-222222222222")},
		},
	}.values()

	if names := written[keyArtists]; len(names) != 0 {
		t.Errorf("a credit with a nameless artist wrote names %q", names)
	}
	if identifiers := written[keyArtistIDs]; len(identifiers) != 0 {
		t.Errorf("a credit with a nameless artist wrote identifiers %q", identifiers)
	}
}

// The credit is still written: the joined string is what every file carried
// before the list existed, and it names everybody the record names.
func TestACreditWithNoNameIsStillWrittenAsTheJoinedString(t *testing.T) {
	written := Tags{
		Artist:  "Porter Robinson & Ninajirachi",
		Artists: []Credit{{Name: "Porter Robinson"}, {Name: ""}},
	}.values()

	if got := written[keyArtist]; len(got) != 1 || got[0] != "Porter Robinson & Ninajirachi" {
		t.Fatalf("the joined credit was written as %q", got)
	}
}

// The album's artists are the same bargain as the track's.
func TestAnAlbumCreditWithNoNameWithholdsBothLists(t *testing.T) {
	written := Tags{
		AlbumArtist:  "Porter Robinson & Ninajirachi",
		AlbumArtists: []Credit{{Name: ""}, {Name: "Ninajirachi"}},
	}.values()

	if names := written[keyAlbumArtists]; len(names) != 0 {
		t.Errorf("an album credit with a nameless artist wrote names %q", names)
	}
	if identifiers := written[keyAlbumArtistIDs]; len(identifiers) != 0 {
		t.Errorf("an album credit with a nameless artist wrote identifiers %q", identifiers)
	}
}

// A name made of spaces names nobody. Every tag read here is trimmed before it
// is believed, so that credit reaches a reader as the blank it is.
func TestACreditNamedOnlyWithSpacesIsACreditWithNoName(t *testing.T) {
	written := Tags{
		Artist:  "Porter Robinson & Ninajirachi",
		Artists: []Credit{{Name: "Porter Robinson"}, {Name: "   "}},
	}.values()

	if names := written[keyArtists]; len(names) != 0 {
		t.Fatalf("a credit named with spaces wrote names %q", names)
	}
}

// A file whose only artist is one nobody named has nobody behind it. Writing
// would take the peer's artist away and put nothing in its place, and half an
// identity is not worth what the file already says.
func TestATitleWhoseOnlyArtistHasNoNameIsNotEnoughToWrite(t *testing.T) {
	if (Tags{Title: "Tears", Artists: []Credit{{Name: ""}}}).proved() {
		t.Fatal("a title whose only credit names nobody claims to describe a file")
	}
	if (Tags{Title: "Tears", AlbumArtists: []Credit{{Name: "Himera"}, {Name: ""}}}).proved() {
		t.Fatal("a title whose only album credit has a nameless artist claims to describe a file")
	}
}

func TestAnOwnedTagSchallCannotFillIsRemovedRatherThanLeftBehind(t *testing.T) {
	written := Tags{Title: "Walk to the Closed Bodega"}.values()

	present, owned := written[keyAlbum]
	if !owned {
		t.Fatalf("%s is not among the tags Schall writes", keyAlbum)
	}
	if len(present) != 0 {
		t.Fatalf("an album nobody proved was written as %q", present)
	}
}

func TestTheReleaseDateIsWrittenWhereBothAnAlbumAndATrackAreReadFrom(t *testing.T) {
	written := Tags{Title: "Tears", Date: "2019-04-05"}.values()

	if got := written[keyDate]; len(got) != 1 || got[0] != "2019-04-05" {
		t.Fatalf("the recording date was written as %q", got)
	}
	if got := written[keyReleaseDate]; len(got) != 1 || got[0] != "2019-04-05" {
		t.Fatalf("the release date was written as %q", got)
	}
}

// Half an identity is not enough to trade for what the file already says: the
// tags Schall owns are cleared when it cannot fill them, so a write with no
// artist behind it would leave the file saying less than it did.
func TestATitleAndSomebodyItIsByAreTheLeastThatIsWritten(t *testing.T) {
	if (Tags{}).proved() {
		t.Fatal("an empty identity claims to describe a file")
	}
	if (Tags{RecordingID: uuid.New(), Title: "Tears"}).proved() {
		t.Fatal("a title with nobody behind it claims to describe a file")
	}
	if (Tags{Artist: "Himera"}).proved() {
		t.Fatal("an artist with no title claims to describe a file")
	}
	if !(Tags{Title: "Tears", Artist: "Himera"}).proved() {
		t.Fatal("a title and an artist do not describe a file")
	}
	if !(Tags{Title: "Tears", Artists: []Credit{{Name: "Himera"}}}).proved() {
		t.Fatal("a title and a credited artist do not describe a file")
	}
}
