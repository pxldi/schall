package tagging

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"go.senan.xyz/taglib"
)

// The fixtures under testdata are a second of silence encoded by ffmpeg and
// tagged the way a peer's copy is: a title in the wrong case, an album, an
// artist, a track number, and nothing that identifies the recording.
func fixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read the fixture %q: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("place the fixture %q: %v", name, err)
	}
	return path
}

func read(t *testing.T, path string) map[string][]string {
	t.Helper()
	tags, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatalf("read the tags on %q: %v", path, err)
	}
	return tags
}

func proved() Tags {
	return Tags{
		Title:          "Walk to the Closed Bodega",
		Album:          "Halfway House Near Me",
		Artist:         "AgonyOST",
		AlbumArtist:    "AgonyOST",
		TrackNumber:    4,
		DiscNumber:     1,
		Date:           "2023-11-03",
		RecordingID:    uuid.MustParse("44444444-4444-4444-4444-444444444444"),
		ReleaseID:      uuid.MustParse("66666666-6666-6666-6666-666666666666"),
		ReleaseGroupID: uuid.MustParse("55555555-5555-5555-5555-555555555555"),
	}
}

// formats is every format the library holds. Each keeps tags in frames of its
// own — a Vorbis comment in FLAC, an ID3 frame in MP3 and WAV, an atom in M4A —
// and they do not agree about what a list may contain, so anything written as a
// list is read back in all four rather than assumed from one.
var formats = []string{"silence.flac", "silence.mp3", "silence.m4a", "silence.wav"}

// fullyCredited is a file by two artists MusicBrainz has an entity for.
func fullyCredited() Tags {
	tags := proved()
	tags.Artists = []Credit{
		{Name: "Porter Robinson", MusicBrainzArtistID: uuid.MustParse("11111111-1111-1111-1111-111111111111")},
		{Name: "Ninajirachi", MusicBrainzArtistID: uuid.MustParse("22222222-2222-2222-2222-222222222222")},
	}
	tags.AlbumArtists = tags.Artists
	return tags
}

// partlyCredited puts the artist nobody identified in the middle of the credit,
// which is where a dropped empty value does the most damage: the guest would
// take the headliner's identifier and the headliner would take nothing.
func partlyCredited() Tags {
	tags := proved()
	tags.Artists = []Credit{
		{Name: "Porter Robinson", MusicBrainzArtistID: uuid.MustParse("11111111-1111-1111-1111-111111111111")},
		{Name: "Unidentified Guest"},
		{Name: "Ninajirachi", MusicBrainzArtistID: uuid.MustParse("22222222-2222-2222-2222-222222222222")},
	}
	tags.AlbumArtists = tags.Artists
	return tags
}

// namelessCredit is a credit MusicBrainz printed one artist in with no name of
// any kind. Every identifier is there, so the identifier list is written; the
// name in the middle is the one thing missing, and it is the list itself.
func namelessCredit() Tags {
	tags := proved()
	tags.Artist = "Porter Robinson feat. ? & Ninajirachi"
	tags.AlbumArtist = tags.Artist
	tags.Artists = []Credit{
		{Name: "Porter Robinson", MusicBrainzArtistID: uuid.MustParse("11111111-1111-1111-1111-111111111111")},
		{Name: "", MusicBrainzArtistID: uuid.MustParse("33333333-3333-3333-3333-333333333333")},
		{Name: "Ninajirachi", MusicBrainzArtistID: uuid.MustParse("22222222-2222-2222-2222-222222222222")},
	}
	tags.AlbumArtists = tags.Artists
	return tags
}

// The measurement the whole rule rests on, taken through taglib with nothing of
// Schall's in between. An empty value in a list does not survive a Vorbis
// comment or an ID3 frame, wherever in the list it sits; an M4A atom keeps it.
// That one format keeping it is not a reprieve — it is why the rule is not
// written per format: the same credit comes back whole on one file and short on
// the next, and the short one pairs an identifier with the wrong artist.
func TestAnEmptyValueInAListDoesNotSurviveTheWrite(t *testing.T) {
	keeps := map[string]bool{"silence.m4a": true}
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			for _, written := range [][]string{
				{"", "Second", "Third"},
				{"First", "", "Third"},
				{"First", "Second", ""},
			} {
				path := fixture(t, name)
				if err := taglib.WriteTags(path, map[string][]string{keyArtists: written}, 0); err != nil {
					t.Fatalf("write %q: %v", written, err)
				}

				got := read(t, path)[keyArtists]
				if keeps[name] {
					if !slices.Equal(got, written) {
						t.Errorf("%q reads back as %q, and this format was measured to keep it", written, got)
					}
					continue
				}
				if len(got) != 2 {
					t.Errorf("%q reads back as %q, so the empty value survived", written, got)
				}
			}
		})
	}
}

// A file whose credit has a nameless artist in it is tagged. Before the rule,
// the names went to the write with a gap in them: on three formats the gap was
// dropped, verify caught the copy saying something other than what it was told,
// and the file was never tagged at all.
func TestAFileCreditedToSomebodyWithNoNameIsStillTagged(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, namelessCredit()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			got := read(t, path)[keyArtist]
			if len(got) != 1 || got[0] != "Porter Robinson feat. ? & Ninajirachi" {
				t.Fatalf("%s reads %q", keyArtist, got)
			}
		})
	}
}

// And it carries no lists. A name is what a reader pairs an identifier against;
// where one credit has none, neither list is on the file and the joined string
// is the whole account of who made the music.
func TestAFileCreditedToSomebodyWithNoNameCarriesNeitherList(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, namelessCredit()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			tags := read(t, path)
			for _, key := range []string{keyArtists, keyArtistIDs, keyAlbumArtists, keyAlbumArtistIDs} {
				if got := tags[key]; len(got) != 0 {
					t.Errorf("%s reads %q", key, got)
				}
			}
		})
	}
}

// Every format the library holds keeps what is written into it, each in its own
// frames: a Vorbis comment in FLAC, an ID3 frame in MP3 and WAV, an atom in
// M4A. A format that could not hold one of these would be a file traded for a
// worse one, so all four are read back here rather than assumed.
func TestAProvenFileGetsTheIdentifiersWritten(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, proved()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			tags := read(t, path)
			for key, want := range map[string]string{
				keyTitle:          "Walk to the Closed Bodega",
				keyAlbum:          "Halfway House Near Me",
				keyArtist:         "AgonyOST",
				keyAlbumArtist:    "AgonyOST",
				keyTrackNumber:    "4",
				keyDiscNumber:     "1",
				keyReleaseID:      "66666666-6666-6666-6666-666666666666",
				keyReleaseGroupID: "55555555-5555-5555-5555-555555555555",
			} {
				if got := tags[key]; len(got) != 1 || got[0] != want {
					t.Errorf("%s reads %q, wanted %q", key, got, want)
				}
			}
		})
	}
}

// A credit whose artists are all identified is written whole, and comes back
// whole: as many identifiers as names, in the same order, so a music server
// pairing them by position links each artist to itself.
func TestACreditEveryArtistIsIdentifiedInSurvivesTheWrite(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, fullyCredited()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			tags := read(t, path)
			for _, pair := range [][2]string{{keyArtists, keyArtistIDs}, {keyAlbumArtists, keyAlbumArtistIDs}} {
				names, identifiers := tags[pair[0]], tags[pair[1]]
				if len(names) != 2 || names[0] != "Porter Robinson" || names[1] != "Ninajirachi" {
					t.Errorf("%s reads %q", pair[0], names)
				}
				if len(identifiers) != 2 ||
					identifiers[0] != "11111111-1111-1111-1111-111111111111" ||
					identifiers[1] != "22222222-2222-2222-2222-222222222222" {
					t.Errorf("%s reads %q", pair[1], identifiers)
				}
			}
		})
	}
}

// The one that matters. An empty value does not survive a Vorbis comment or an
// ID3 frame, so a list holding a place for the artist nobody identified comes
// back short and hands his neighbour's identifier to him. No identifier is
// written at all rather than one written against the wrong name.
func TestACreditWithAnUnidentifiedArtistIsWrittenWithNoIdentifiersAtAll(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, partlyCredited()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			tags := read(t, path)
			if got := tags[keyArtistIDs]; len(got) != 0 {
				t.Errorf("%s reads %q, and one of those names is not that artist's", keyArtistIDs, got)
			}
			if got := tags[keyAlbumArtistIDs]; len(got) != 0 {
				t.Errorf("%s reads %q, and one of those names is not that artist's", keyAlbumArtistIDs, got)
			}
		})
	}
}

// Only the identifiers are withheld. Every credited artist is still named, in
// the order MusicBrainz printed them, because rejoining that list has to
// reproduce the credit the release was published with.
func TestAnArtistNobodyIdentifiedIsStillNamedOnTheFile(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, partlyCredited()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			got := read(t, path)[keyArtists]
			if len(got) != 3 || got[0] != "Porter Robinson" ||
				got[1] != "Unidentified Guest" || got[2] != "Ninajirachi" {
				t.Fatalf("%s reads %q", keyArtists, got)
			}
		})
	}
}

// A file nobody identified keeps whatever it came with. Silence about what a
// file is, is not permission to invent an album name for it.
func TestAFileNobodyIdentifiedIsLeftExactlyAsItIs(t *testing.T) {
	path := fixture(t, "silence.flac")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}

	if err := Write(path, Tags{}); err != ErrNothingProved {
		t.Fatalf("writing nothing returned %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the file again: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a file nobody identified was written to")
	}
}

// The point of the whole exercise: what Schall writes must never come back to
// it as though a stranger had written it.
func TestATaggedFileStillTellsMatchingWhatItArrivedCarrying(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, proved()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			arrived, tagged := AsArrived(path)
			if !tagged {
				t.Fatal("a file Schall tagged does not say so")
			}
			if arrived.Title != "peer title" || arrived.Album != "Peer Album" ||
				arrived.Artist != "Peer Artist" {
				t.Fatalf("the account of the file's arrival reads %+v", arrived)
			}
		})
	}
}

func TestTheTrackNumberAFileArrivedWithIsPartOfItsAccount(t *testing.T) {
	path := fixture(t, "silence.flac")

	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	arrived, _ := AsArrived(path)
	if arrived.TrackNumber != 7 {
		t.Fatalf("the track number it arrived with reads %d", arrived.TrackNumber)
	}
}

func TestAFileSchallNeverTaggedHasNoAccountOfItsArrival(t *testing.T) {
	path := fixture(t, "silence.flac")

	if arrived, tagged := AsArrived(path); tagged {
		t.Fatalf("a peer's file claims Schall tagged it: %+v", arrived)
	}
}

// Tagging the same file twice must keep the first account. The second write's
// "before" is Schall's own writing, and recording that would hand Schall's
// claims to matching as the stranger's.
func TestTaggingAFileTwiceKeepsWhatItArrivedCarrying(t *testing.T) {
	path := fixture(t, "silence.flac")

	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	second := proved()
	second.Title = "Walk To The Closed Bodega"
	if err := Write(path, second); err != nil {
		t.Fatalf("write the tags again: %v", err)
	}

	arrived, tagged := AsArrived(path)
	if !tagged {
		t.Fatal("a file Schall tagged twice does not say so")
	}
	if arrived.Title != "peer title" {
		t.Fatalf("the second write recorded %q as what the file arrived carrying", arrived.Title)
	}
}

// The same, for the two readings that prove an identity on their own. A retag
// that recorded Schall's own account as the arrival would destroy the only copy
// of the stranger's, and these two are what a rewritten account would prove.
func TestTaggingAFileTwiceKeepsTheIdentifiersItArrivedCarrying(t *testing.T) {
	path := fixture(t, "silence.flac")
	present := read(t, path)
	present[keyRecordingID] = []string{"11111111-1111-1111-1111-111111111111"}
	present[keyISRC] = []string{"GBAYE0601498"}
	if err := taglib.WriteTags(path, present, 0); err != nil {
		t.Fatalf("give the fixture a peer's identifiers: %v", err)
	}

	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags again: %v", err)
	}

	arrived, _ := AsArrived(path)
	if arrived.ISRC != "GBAYE0601498" {
		t.Errorf("the ISRC it arrived with reads %q", arrived.ISRC)
	}
	if arrived.MusicBrainzRecordingID == nil ||
		arrived.MusicBrainzRecordingID.String() != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("the recording it arrived with reads %v", arrived.MusicBrainzRecordingID)
	}
}

// A pass over a library that has already been tagged reads each file rather
// than rewriting it: a rewrite costs a copy of the whole file and changes
// everything that watches its size and its time.
func TestAFileAlreadySayingItNeedsNoWriting(t *testing.T) {
	path := fixture(t, "silence.flac")

	if AlreadySays(path, proved()) {
		t.Fatal("a peer's file already says what Schall proved")
	}
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	if !AlreadySays(path, proved()) {
		t.Fatal("a file Schall has just written says something else")
	}
}

// The same for a credited file. What was written has to read back as what was
// written, or the library pass rewrites the file on every run it ever makes.
func TestACreditedFileAlreadySayingItNeedsNoWriting(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, fullyCredited()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			if !AlreadySays(path, fullyCredited()) {
				t.Fatal("a credited file Schall has just written says something else")
			}
		})
	}
}

// And for the credit that has a gap in it, which is the case that never settled
// before: a list holding an empty value read back one value shorter every time,
// so the file was rewritten on every pass forever.
func TestAFileCreditedToAnUnidentifiedArtistAlreadySayingItNeedsNoWriting(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, partlyCredited()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			if !AlreadySays(path, partlyCredited()) {
				t.Fatal("a file with an unidentified artist on it reads as needing writing again")
			}
		})
	}
}

// And for the credit nobody named, which the write refuses to list at all: what
// it does write has to read back as what it wrote, or the library pass rewrites
// the file on every run it ever makes.
func TestAFileCreditedToSomebodyWithNoNameAlreadySayingItNeedsNoWriting(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)

			if err := Write(path, namelessCredit()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}

			if !AlreadySays(path, namelessCredit()) {
				t.Fatal("a file with a nameless artist on it reads as needing writing again")
			}
		})
	}
}

func TestAFileWhoseCatalogueMovedOnNeedsWritingAgain(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	renamed := proved()
	renamed.Album = "Halfway House Near You"
	if AlreadySays(path, renamed) {
		t.Fatal("a file still holding the old album reads as up to date")
	}
}

func TestAFileTooLittleWasProvedAboutNeverReadsAsUpToDate(t *testing.T) {
	path := fixture(t, "silence.flac")

	if AlreadySays(path, Tags{}) {
		t.Fatal("a file nobody identified reads as up to date")
	}
}

// An album Schall cannot name is nameless rather than named by the peer. One
// field left holding a stranger's value while its neighbours hold Schall's is
// what files one album under two names.
func TestAnAlbumSchallCannotNameIsRemovedFromTheFile(t *testing.T) {
	path := fixture(t, "silence.flac")
	if got := read(t, path)[keyAlbum]; len(got) != 1 {
		t.Fatalf("the fixture must arrive with an album, and reads %q", got)
	}

	if err := Write(path, Tags{Title: "Tears", Artist: "Himera"}); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	if got := read(t, path)[keyAlbum]; len(got) != 0 {
		t.Fatalf("an album nobody proved was left on the file: %q", got)
	}
}

// A track number a stranger wrote orders an album; it does not split one.
// Dropping a position Schall does not happen to know would leave a release in
// no order at all.
func TestATrackNumberSchallDoesNotKnowIsLeftWhereThePeerPutIt(t *testing.T) {
	path := fixture(t, "silence.flac")

	if err := Write(path, Tags{Title: "Tears", Artist: "Himera"}); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	if got := read(t, path)[keyTrackNumber]; len(got) != 1 || got[0] != "7" {
		t.Fatalf("the track number the file arrived with now reads %q", got)
	}
}

// The one that matters most. A recording identifier proves an identity on its
// own, with no audio consulted, so one Schall wrote into a file it had already
// decided about would prove Schall's own decision back to the next scan — and
// the mark that says Schall wrote it survives only until the next tool to
// rewrite the file drops the fields it does not recognise.
func TestTheRecordingSchallProvedIsNeverWrittenIntoTheFile(t *testing.T) {
	path := fixture(t, "silence.flac")

	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	if got := read(t, path)[keyRecordingID]; len(got) != 0 {
		t.Fatalf("the recording Schall proved was written into the file as %q", got)
	}
}

// What a stranger said about which recording this is stays said. It is his
// claim, it is evidence, and Schall neither adds to it nor takes it away.
func TestARecordingIdentifierThePeerWroteIsLeftWhereItIs(t *testing.T) {
	path := fixture(t, "silence.flac")
	const peerRecording = "77777777-7777-7777-7777-777777777777"
	if err := taglib.WriteTags(path, map[string][]string{keyRecordingID: {peerRecording}}, 0); err != nil {
		t.Fatalf("write the peer's recording identifier: %v", err)
	}

	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	got := read(t, path)[keyRecordingID]
	if len(got) != 1 || got[0] != peerRecording {
		t.Fatalf("the peer's recording identifier now reads %q", got)
	}
}

// An ISRC identifies a recording outright, so Schall writes none and takes none
// away either.
func TestAnISRCThePeerWroteIsLeftWhereItIs(t *testing.T) {
	path := fixture(t, "silence.flac")
	const peerISRC = "GBAYE0601498"
	if err := taglib.WriteTags(path, map[string][]string{keyISRC: {peerISRC}}, 0); err != nil {
		t.Fatalf("write the peer's ISRC: %v", err)
	}

	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	got := read(t, path)[keyISRC]
	if len(got) != 1 || got[0] != peerISRC {
		t.Fatalf("the peer's ISRC now reads %q", got)
	}
}

func TestAWriteToSomethingThatIsNotAudioChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.flac")
	if err := os.WriteFile(path, []byte("this is not audio"), 0o644); err != nil {
		t.Fatalf("write the file: %v", err)
	}

	if err := Write(path, proved()); err == nil {
		t.Fatal("writing tags into something that is not audio was reported as done")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the file: %v", err)
	}
	if string(content) != "this is not audio" {
		t.Fatalf("the file now reads %q", content)
	}
}

// A write that cannot finish leaves the original where it was. Nothing is
// written into the user's only copy until a whole tagged copy exists beside it.
func TestAWriteThatFailsLeavesTheOriginalIntact(t *testing.T) {
	path := fixture(t, "silence.flac")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	// The staging name is taken by a directory that cannot be removed, so the
	// copy never happens — the same position a write is in when the process
	// dies before the rename.
	staging := filepath.Join(filepath.Dir(path), stagingPrefix+filepath.Base(path)+".part")
	if err := os.MkdirAll(filepath.Join(staging, "occupied"), 0o755); err != nil {
		t.Fatalf("occupy the staging name: %v", err)
	}

	if err := Write(path, proved()); err == nil {
		t.Fatal("a write that could not be staged was reported as done")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the file again: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a failed write changed the original")
	}
	if arrived, tagged := AsArrived(path); tagged {
		t.Fatalf("a failed write marked the file as tagged: %+v", arrived)
	}
}

// A file stops being a catalogue track's — somebody withdrew the decision, or
// the track went to another file — and what Schall wrote into it is a claim
// nothing stands behind. The file arrived saying something, and that is what it
// says again: every tag it came with, and nothing Schall put there.
func TestAFileThatIsNoLongerATracksSaysExactlyWhatItArrivedSaying(t *testing.T) {
	// The two shapes a peer's copy arrives in. The plain one carries one artist
	// and a bare track number. The compilation carries the two artists that
	// disagree — the track's and the album's — and its positions written as one
	// of so many, which is what a copy off a two-disc set says.
	arrangements := map[string]map[string][]string{
		"as the peer sent it": nil,
		"a copy off a compilation": {
			keyArtist:      {"Aphex Twin"},
			keyAlbumArtist: {"Various Artists"},
			keyTrackNumber: {"4/12"},
			keyDiscNumber:  {"1/2"},
		},
	}
	for _, name := range formats {
		for arrangement, extra := range arrangements {
			t.Run(name+", "+arrangement, func(t *testing.T) {
				revertRestoresTheArrival(t, name, extra)
			})
		}
	}
}

func revertRestoresTheArrival(t *testing.T, name string, extra map[string][]string) {
	t.Helper()
	path := fixture(t, name)
	if len(extra) > 0 {
		if err := taglib.WriteTags(path, extra, 0); err != nil {
			t.Fatalf("arrange the fixture: %v", err)
		}
	}
	arrived := read(t, path)

	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	restored, err := Revert(path)
	if err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}
	if !restored {
		t.Fatal("a file Schall tagged had nothing to give back")
	}

	after := read(t, path)
	if len(after) != len(arrived) {
		t.Fatalf("the file reads %v, and it arrived reading %v", after, arrived)
	}
	for key, want := range arrived {
		if !slices.Equal(after[key], want) {
			t.Errorf("%s reads %q, and the file arrived saying %q", key, after[key], want)
		}
	}
}

// A file tagged before the tags themselves were kept has a record of the
// readings alone: one artist, and a position as a number. That is what it gets
// back. Schall does not invent the tag a reading came from — writing "4/12"
// because the album has twelve tracks would be Schall writing the peer's
// account for him.
func TestAFileTaggedBeforeItsOwnTagsWereKeptGetsBackWhatItsRecordHolds(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := taglib.WriteTags(path, map[string][]string{
		keyTitle: {"Schall Title"}, keyAlbum: {"Schall Album"},
		keyArtist: {"Schall Artist"}, keyAlbumArtist: {"Schall Artist"},
		keyTrackNumber: {"4"}, keyDiscNumber: {"1"},
		keyTaggedAt: {"2026-08-05T00:00:00Z"},
		keyTagsAsArrived: {`{"artist":"Peer Artist","album":"Peer Album",` +
			`"title":"peer title","trackNumber":7}`},
	}, 0); err != nil {
		t.Fatalf("write a record of the shape the older writes made: %v", err)
	}

	if _, err := Revert(path); err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}

	after := read(t, path)
	for key, want := range map[string][]string{
		keyTitle: {"peer title"}, keyAlbum: {"Peer Album"},
		keyArtist: {"Peer Artist"}, keyTrackNumber: {"7"},
		keyAlbumArtist: nil, keyDiscNumber: nil, keyTaggedAt: nil,
	} {
		if !slices.Equal(after[key], want) {
			t.Errorf("%s reads %q, and the record holds %q", key, after[key], want)
		}
	}
}

// And it is a stranger's file again. The marks say that the tags on the file
// are Schall's and that the account beside them is the peer's; once the peer's
// account is the tags, there is nothing left for either to point at.
func TestARevertedFileNoLongerSaysSchallTaggedIt(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	if _, err := Revert(path); err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}

	if arrived, tagged := AsArrived(path); tagged {
		t.Fatalf("a file given its own tags back still claims Schall tagged it: %+v", arrived)
	}
}

// The account of the arrival is written once and kept, so a file tagged twice
// is given back what the peer sent rather than what Schall wrote the first
// time.
func TestAFileTaggedTwiceIsGivenBackWhatThePeerSent(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	second := proved()
	second.Title = "Walk To The Closed Bodega"
	if err := Write(path, second); err != nil {
		t.Fatalf("write the tags again: %v", err)
	}

	if _, err := Revert(path); err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}

	if got := read(t, path)[keyTitle]; len(got) != 1 || got[0] != "peer title" {
		t.Fatalf("the title reads %q, and the peer sent it as \"peer title\"", got)
	}
}

// A file Schall never wrote into is nobody's business here. Its tags are the
// stranger's own and there is nothing to give back.
func TestAFileSchallNeverTaggedIsLeftExactlyAsItIsByARevert(t *testing.T) {
	path := fixture(t, "silence.flac")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}

	restored, err := Revert(path)
	if err != nil {
		t.Fatalf("revert a file Schall never tagged: %v", err)
	}
	if restored {
		t.Fatal("a peer's file was reported as having Schall's tags taken back")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the file again: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a file Schall never tagged was written to")
	}
}

// A file marked as Schall's whose account of its own arrival cannot be read has
// lost what it said. What Schall wrote is still a claim nothing stands behind,
// so it is removed rather than left standing over a file nobody can restore.
func TestAFileWhoseAccountOfItsArrivalIsLostKeepsNothingSchallWrote(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	present := read(t, path)
	present[keyTagsAsArrived] = []string{"this is not a record"}
	if err := taglib.WriteTags(path, present, 0); err != nil {
		t.Fatalf("damage the account of the file's arrival: %v", err)
	}

	restored, err := Revert(path)
	if err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}
	if !restored {
		t.Fatal("a file marked as Schall's was left holding what Schall wrote")
	}

	after := read(t, path)
	for _, key := range []string{
		keyTitle, keyAlbum, keyArtist, keyAlbumArtist, keyDate, keyReleaseDate,
		keyReleaseID, keyReleaseGroupID, keyTaggedAt, keyTagsAsArrived,
	} {
		if got := after[key]; len(got) != 0 {
			t.Errorf("%s reads %q, and nothing says the file is that", key, got)
		}
	}
}

// The two readings that prove an identity on their own are the peer's, written
// by him and never by Schall. A revert has no more business with them than a
// write has.
func TestARevertLeavesThePeersIdentifiersWhereTheyAre(t *testing.T) {
	path := fixture(t, "silence.flac")
	const peerRecording = "77777777-7777-7777-7777-777777777777"
	const peerISRC = "GBAYE0601498"
	if err := taglib.WriteTags(path, map[string][]string{
		keyRecordingID: {peerRecording}, keyISRC: {peerISRC},
	}, 0); err != nil {
		t.Fatalf("write the peer's identifiers: %v", err)
	}
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}

	if _, err := Revert(path); err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}

	after := read(t, path)
	if got := after[keyRecordingID]; len(got) != 1 || got[0] != peerRecording {
		t.Errorf("the peer's recording identifier now reads %q", got)
	}
	if got := after[keyISRC]; len(got) != 1 || got[0] != peerISRC {
		t.Errorf("the peer's ISRC now reads %q", got)
	}
}

// A revert that cannot finish leaves the file where it was, for the same reason
// a write does: nothing touches the user's only copy until a whole rewritten
// copy exists beside it.
func TestARevertThatFailsLeavesTheFileIntact(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the tagged file: %v", err)
	}
	staging := filepath.Join(filepath.Dir(path), stagingPrefix+filepath.Base(path)+".part")
	if err := os.MkdirAll(filepath.Join(staging, "occupied"), 0o755); err != nil {
		t.Fatalf("occupy the staging name: %v", err)
	}

	if restored, err := Revert(path); err == nil || restored {
		t.Fatalf("a revert that could not be staged answered (%v, %v)", restored, err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the file again: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a failed revert changed the file")
	}
}
