package acquisition

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Handing an entry MusicBrainz has never heard of back to MusicBrainz.
//
// The chain that admits a fetched copy ends at a MusicBrainz recording ID:
// fingerprint, AcoustID, recording. An entry MusicBrainz holds no recording for
// therefore cannot be acquired at all, however many times it is asked about, and
// a second metadata provider would only rename the problem rather than solve it —
// AcoustID answers in MusicBrainz identifiers and nothing else. What does solve
// it is somebody adding the release, because MusicBrainz is a wiki.
//
// Schall already holds everything that form asks for, so it fills the form in.
// It never submits it. What is seeded here is a listing's metadata, unverified
// against the release itself, and the person who opens the editor is the one who
// checks it and presses the button.

// ReleaseEditorURL is where a seeded release editor opens.
//
// It is reached by POST because that is the only way it can be reached: the
// editor reads its seed out of the request body, and takes nothing from the
// query string but the release-group, artist and label shortcuts. A hand-built
// link would land on an empty form, so what Schall offers is a form of its own
// that somebody submits.
const ReleaseEditorURL = "https://musicbrainz.org/release/add"

// SeedField is one field of that form, named as the release editor names it.
type SeedField struct {
	Name  string
	Value string
}

// ReleaseSeed is the whole form: where it goes and what it carries.
type ReleaseSeed struct {
	URL    string
	Fields []SeedField
}

// SeedEntry is an entry as whoever listed it wrote it, which is all the evidence
// there is to fill a release editor with.
type SeedEntry struct {
	Artist     string
	Title      string
	Album      string
	DurationMS int
	ISRC       string
	// Source names where the metadata came from, in words an editor reading the
	// edit note will understand — "a Spotify playlist". An unattributed data
	// dump is rude, and an editor cannot judge metadata without knowing whose
	// it is.
	Source string
}

// MusicBrainzHasNothing reports the one state where adding the release is the
// action that changes anything.
//
// An entry waiting for its first attempt and an entry MusicBrainz has repeatedly
// had nothing for are both 'unresolved', and only the second is a dead end.
// Nothing on the target row separates them: the append-only attempt log records
// which it was, and it is documented as never being consulted to decide
// anything. What separates them here is the sentence the target wears, compared
// against the very constant that wrote it, so a copy edit to that sentence moves
// both halves at once. A target whose stored sentence predates such an edit
// simply is not offered the form until its next attempt rewrites it, which is
// the direction this should fail in.
func MusicBrainzHasNothing(status, summary string) bool {
	return status == "unresolved" && summary == unfoundSummary
}

// CreditNames is every name a credit might be naming: the credit whole, and then
// each name it separates when it was joined from several.
//
// Both readings are wanted because only the artists Schall already knows can say
// which is right, and that answer lives in the database rather than here.
func CreditNames(credit string) []string {
	credit = strings.TrimSpace(credit)
	if credit == "" {
		return nil
	}
	names := []string{credit}
	for _, name := range splitCredit(credit) {
		if name != credit {
			names = append(names, name)
		}
	}
	return names
}

// splitCredit is the credit read as several artists, whatever it turns out to be.
func splitCredit(credit string) []string {
	names := make([]string, 0, 2)
	for _, name := range strings.Split(credit, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// SeedRelease fills MusicBrainz's release editor in from one entry.
//
// artistIDs says which of the names in the credit MusicBrainz already holds, by
// exactly that name; anything absent from it is seeded as a name alone. Seeding
// a bare name leaves the editor searching, which is a person choosing, whereas
// seeding an identifier Schall inferred would be Schall choosing — the guess
// this codebase refuses to make on anyone's behalf.
//
// The second result is false when nothing can name the release, which is the one
// field MusicBrainz requires.
func SeedRelease(entry SeedEntry, artistIDs map[string]uuid.UUID) (ReleaseSeed, bool) {
	title := strings.TrimSpace(entry.Title)
	// A listing gives the release the track appeared on, and for the singles this
	// is mostly about, that is the track's own name. Where there is no album at
	// all the title is the only name there is, and a release editor cannot open
	// without one.
	release := strings.TrimSpace(entry.Album)
	if release == "" {
		release = title
	}
	if release == "" {
		return ReleaseSeed{}, false
	}

	seed := ReleaseSeed{URL: ReleaseEditorURL}
	seed.add("name", release)
	credits := creditedArtists(entry.Artist, artistIDs)
	for index, credit := range credits {
		field := "artist_credit.names." + strconv.Itoa(index) + "."
		if id, known := artistIDs[credit]; known {
			seed.add(field+"mbid", id.String())
		} else {
			// Without an identifier the editor searches for whatever name it is
			// given, and that search reads artist.name rather than the credited
			// name, so the name goes in as both.
			seed.add(field+"artist.name", credit)
		}
		seed.add(field+"name", credit)
		if index < len(credits)-1 {
			seed.add(field+"join_phrase", ", ")
		}
	}
	// One track and no track number. Schall knows this song is on the release and
	// nothing whatever about the rest of it, and a seeded position would be an
	// assertion about a tracklist nobody here has seen.
	if title != "" {
		seed.add("mediums.0.track.0.name", title)
	}
	// Milliseconds rather than MM:SS, which the editor also takes: the listing
	// gives milliseconds, and rounding them would be losing something for the
	// sake of the shorter form.
	if entry.DurationMS > 0 {
		seed.add("mediums.0.track.0.length", strconv.Itoa(entry.DurationMS))
	}
	seed.add("edit_note", editNote(entry))
	return seed, true
}

func (seed *ReleaseSeed) add(name, value string) {
	seed.Fields = append(seed.Fields, SeedField{Name: name, Value: value})
}

// creditedArtists reads a credit as the artists it credits.
//
// A playlist import joins the artists a listing names with ", ", so splitting on
// the comma unmakes exactly that join rather than parsing a format nobody
// promised — it is the same reading PrimaryCredit takes of the same string, one
// step further. The join is lossy where a single artist's name contains a comma,
// and the one piece of evidence available here about which it was is that Schall
// already knows an artist by the whole string. Where it does, the comma belongs
// to the name and the credit stays whole; where it does not, the person reviewing
// the form sees both names and can say so.
func creditedArtists(credit string, artistIDs map[string]uuid.UUID) []string {
	credit = strings.TrimSpace(credit)
	if credit == "" {
		return nil
	}
	if _, whole := artistIDs[credit]; whole {
		return []string{credit}
	}
	return splitCredit(credit)
}

// editNote says where the metadata came from and what nobody has checked, which
// is what an editor needs in order to judge an edit they did not make.
func editNote(entry SeedEntry) string {
	source := strings.TrimSpace(entry.Source)
	if source == "" {
		source = "a playlist"
	}
	note := "Seeded by Schall from " + source +
		", which MusicBrainz had no recording for. The release title, artist" +
		" credit, track title and track length here are that listing's own" +
		" metadata, and only this one track of the release is known to Schall," +
		" so the tracklist is likely incomplete."
	if isrc := strings.TrimSpace(entry.ISRC); isrc != "" {
		note += " The listing gives the ISRC of this track as " + isrc + "."
	}
	return note + " Please verify against the release before submitting."
}
