package acquisition

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// field reads one seeded value back, so a test can say what the form carries
// rather than how the form is built.
func field(seed ReleaseSeed, name string) (string, bool) {
	for _, seeded := range seed.Fields {
		if seeded.Name == name {
			return seeded.Value, true
		}
	}
	return "", false
}

func seedOf(t *testing.T, entry SeedEntry, artistIDs map[string]uuid.UUID) ReleaseSeed {
	t.Helper()
	seed, ok := SeedRelease(entry, artistIDs)
	if !ok {
		t.Fatalf("SeedRelease(%+v) refused to seed anything", entry)
	}
	return seed
}

// The three things a release editor cannot be filled in without, and the three
// things a listing always has.
func TestTheSeedCarriesTheTrackTitle(t *testing.T) {
	seed := seedOf(t, SeedEntry{Artist: "umbraid", Title: "France 98", Album: "France 98"}, nil)

	if got, _ := field(seed, "mediums.0.track.0.name"); got != "France 98" {
		t.Errorf("seeded track title = %q, want %q", got, "France 98")
	}
}

func TestTheSeedCarriesTheArtistAsCredited(t *testing.T) {
	seed := seedOf(t, SeedEntry{Artist: "umbraid", Title: "France 98"}, nil)

	if got, _ := field(seed, "artist_credit.names.0.name"); got != "umbraid" {
		t.Errorf("seeded artist credit = %q, want %q", got, "umbraid")
	}
}

// Milliseconds, because that is what the listing gave and the editor takes them.
func TestTheSeedCarriesTheTrackLengthInMilliseconds(t *testing.T) {
	seed := seedOf(t, SeedEntry{Title: "In Trance", DurationMS: 372_500}, nil)

	if got, _ := field(seed, "mediums.0.track.0.length"); got != "372500" {
		t.Errorf("seeded length = %q, want %q", got, "372500")
	}
}

// A length nobody knows is silence, exactly as an absent tag is, and a seeded
// zero would be a claim about the music.
func TestAnEntryWithoutALengthSeedsNone(t *testing.T) {
	seed := seedOf(t, SeedEntry{Title: "In Trance"}, nil)

	if got, ok := field(seed, "mediums.0.track.0.length"); ok {
		t.Errorf("seeded length = %q, want no length field at all", got)
	}
}

// The release editor is opened for a release, and the album is what the listing
// says the release is.
func TestTheSeedNamesTheReleaseAfterTheAlbum(t *testing.T) {
	seed := seedOf(t, SeedEntry{Title: "Ataraxia", Album: "Ataraxia EP"}, nil)

	if got, _ := field(seed, "name"); got != "Ataraxia EP" {
		t.Errorf("seeded release name = %q, want %q", got, "Ataraxia EP")
	}
}

func TestAnEntryWithoutAnAlbumNamesTheReleaseAfterTheTrack(t *testing.T) {
	seed := seedOf(t, SeedEntry{Title: "June"}, nil)

	if got, _ := field(seed, "name"); got != "June" {
		t.Errorf("seeded release name = %q, want %q", got, "June")
	}
}

// MusicBrainz requires a release name, so an entry that cannot supply one is
// offered nothing rather than a form that will not open.
func TestAnEntryWithNothingToNameTheReleaseIsNotSeeded(t *testing.T) {
	if _, ok := SeedRelease(SeedEntry{Artist: "umbraid", ISRC: "GBKQU1700123"}, nil); ok {
		t.Error("an entry with neither a title nor an album was seeded anyway")
	}
}

// An artist Schall already holds under exactly this name is the one thing here
// that is not a guess, and it is the difference between the editor knowing who
// this is and the editor searching.
func TestAKnownArtistIsSeededByItsMusicBrainzID(t *testing.T) {
	id := uuid.MustParse("6a4e5f9c-91c3-4d2f-9d3a-1c8a2d5f0e11")
	seed := seedOf(t, SeedEntry{Artist: "umbraid", Title: "France 98"}, map[string]uuid.UUID{
		"umbraid": id,
	})

	if got, _ := field(seed, "artist_credit.names.0.mbid"); got != id.String() {
		t.Errorf("seeded artist mbid = %q, want %q", got, id)
	}
}

func TestAnArtistNobodyKnowsIsSeededByNameForTheEditorToSearch(t *testing.T) {
	seed := seedOf(t, SeedEntry{Artist: "Ket Robinson", Title: "June"}, nil)

	if got, ok := field(seed, "artist_credit.names.0.mbid"); ok {
		t.Errorf("seeded artist mbid = %q, want no identifier at all", got)
	}
	if got, _ := field(seed, "artist_credit.names.0.artist.name"); got != "Ket Robinson" {
		t.Errorf("seeded artist search name = %q, want %q", got, "Ket Robinson")
	}
}

// The import joined the artists the listing named with ", ", so the seed unmakes
// that join: an artist credit is a list of artists, and one field holding both
// names would ask the editor to invent an artist called "A, B".
func TestAJoinedCreditIsSeededAsOneNamePerArtist(t *testing.T) {
	seed := seedOf(t, SeedEntry{Artist: "Dominik Saltevski, Ket Robinson", Title: "June"}, nil)

	first, _ := field(seed, "artist_credit.names.0.name")
	second, _ := field(seed, "artist_credit.names.1.name")
	if first != "Dominik Saltevski" || second != "Ket Robinson" {
		t.Errorf("seeded credits = %q and %q, want %q and %q",
			first, second, "Dominik Saltevski", "Ket Robinson")
	}
}

func TestTheArtistsOfAJoinedCreditAreSeededWithTheJoinBetweenThem(t *testing.T) {
	seed := seedOf(t, SeedEntry{Artist: "Dominik Saltevski, Ket Robinson", Title: "June"}, nil)

	if got, _ := field(seed, "artist_credit.names.0.join_phrase"); got != ", " {
		t.Errorf("seeded join phrase = %q, want %q", got, ", ")
	}
	if got, ok := field(seed, "artist_credit.names.1.join_phrase"); ok {
		t.Errorf("seeded a join phrase %q after the last artist", got)
	}
}

// The join is lossy where an artist's own name holds a comma, and Schall knowing
// an artist by the whole string is the only evidence available that it does.
func TestACommaInsideAKnownArtistNameIsNotReadAsAJoin(t *testing.T) {
	id := uuid.MustParse("f4b6c4e5-2a37-4f83-9a06-7b2d1a5c9e42")
	seed := seedOf(t, SeedEntry{Artist: "Tyler, The Creator", Title: "Yonkers"}, map[string]uuid.UUID{
		"Tyler, The Creator": id,
	})

	if got, _ := field(seed, "artist_credit.names.0.name"); got != "Tyler, The Creator" {
		t.Errorf("seeded credit = %q, want %q", got, "Tyler, The Creator")
	}
	if got, ok := field(seed, "artist_credit.names.1.name"); ok {
		t.Errorf("seeded a second artist %q from a name that only holds a comma", got)
	}
}

// The names an entry has to be asked about are both readings of the credit, since
// only the database can say which of them is the artist.
func TestACreditIsAskedAboutWholeAndInParts(t *testing.T) {
	got := CreditNames(" Dominik Saltevski, Ket Robinson ")

	want := []string{"Dominik Saltevski, Ket Robinson", "Dominik Saltevski", "Ket Robinson"}
	if len(got) != len(want) {
		t.Fatalf("CreditNames() = %q, want %q", got, want)
	}
	for index, name := range want {
		if got[index] != name {
			t.Errorf("CreditNames()[%d] = %q, want %q", index, got[index], name)
		}
	}
}

func TestACreditNamingOneArtistIsAskedAboutOnce(t *testing.T) {
	if got := CreditNames("umbraid"); len(got) != 1 || got[0] != "umbraid" {
		t.Errorf("CreditNames() = %q, want [umbraid]", got)
	}
}

// Nothing here escapes anything. The values are carried verbatim and the form
// that submits them does the encoding, so a title full of the characters a
// hand-built query string would have mangled arrives as it was written.
func TestATitleFullOfAwkwardCharactersIsSeededVerbatim(t *testing.T) {
	title := `50% Off & "Free" / <untitled> #3 + ?`
	album := "Ataraxia & Co. / 100%"
	seed := seedOf(t, SeedEntry{Artist: "A & B", Title: title, Album: album}, nil)

	if got, _ := field(seed, "mediums.0.track.0.name"); got != title {
		t.Errorf("seeded track title = %q, want %q", got, title)
	}
	if got, _ := field(seed, "name"); got != album {
		t.Errorf("seeded release name = %q, want %q", got, album)
	}
	if got, _ := field(seed, "artist_credit.names.0.name"); got != "A & B" {
		t.Errorf("seeded artist credit = %q, want %q", got, "A & B")
	}
}

// An unattributed data dump is rude, and an editor cannot judge metadata without
// being told whose it is and what nobody checked.
func TestTheEditNoteSaysWhereTheMetadataCameFrom(t *testing.T) {
	seed := seedOf(t, SeedEntry{Title: "June", Source: "a Spotify playlist"}, nil)

	note, _ := field(seed, "edit_note")
	if !strings.Contains(note, "a Spotify playlist") {
		t.Errorf("edit note = %q, want it to name a Spotify playlist", note)
	}
	if !strings.Contains(note, "verify") {
		t.Errorf("edit note = %q, want it to ask for the release to be verified", note)
	}
}

// The strongest identifier a listing carries cannot be seeded into any field the
// release editor has, so it is told to the person instead.
func TestTheEditNoteCarriesTheISRCWhenTheListingGaveOne(t *testing.T) {
	seed := seedOf(t, SeedEntry{Title: "June", ISRC: "GBKQU1700123"}, nil)

	if note, _ := field(seed, "edit_note"); !strings.Contains(note, "GBKQU1700123") {
		t.Errorf("edit note = %q, want it to carry the ISRC", note)
	}
}

// The one state this offer is true for. An entry waiting for its first attempt
// has lost nothing yet; adding a release for it would be answering a question
// nobody asked.
func TestAnEntryWaitingToBeResolvedIsNotOneMusicBrainzLacks(t *testing.T) {
	if MusicBrainzHasNothing("unresolved", UnresolvedSummary) {
		t.Error("an entry that has not been asked about yet was offered the release editor")
	}
}

func TestAnEntryNothingWasFoundForIsOneMusicBrainzLacks(t *testing.T) {
	if !MusicBrainzHasNothing("unresolved", unfoundSummary) {
		t.Error("an entry MusicBrainz had no recording for was not offered the release editor")
	}
}

// Being unable to ask says nothing about whether MusicBrainz knows the recording,
// so it is not a dead end and gets no offer.
func TestAnEntryMusicBrainzCouldNotBeAskedAboutIsNotOneItLacks(t *testing.T) {
	if MusicBrainzHasNothing("unresolved", unaskedSummary) {
		t.Error("an entry Schall could not ask about was offered the release editor")
	}
}

func TestAnEntryAlreadyResolvedIsNotOneMusicBrainzLacks(t *testing.T) {
	if MusicBrainzHasNothing("pending", unfoundSummary) {
		t.Error("a resolved entry was offered the release editor")
	}
}
