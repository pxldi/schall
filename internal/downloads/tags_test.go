package downloads

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/tagging"
)

func TestATrackIsTaggedWithTheReleaseTheCatalogueSaysItIsOn(t *testing.T) {
	recording := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	release := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	tags := releaseTags(db.DownloadImportRow{
		ArtistName:     "AgonyOST",
		AlbumTitle:     "Halfway House Near Me",
		ReleaseID:      uuid.NullUUID{UUID: release, Valid: true},
		ReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		ReleaseDate:    "2023-11",
	}, db.ImportTrack{
		Title: "Walk to the Closed Bodega", DiscNumber: 1, TrackNumber: 4,
		MusicBrainzRecordingID: &recording,
	})

	if tags.Title != "Walk to the Closed Bodega" || tags.Album != "Halfway House Near Me" {
		t.Fatalf("the file was tagged %+v", tags)
	}
	if tags.RecordingID != recording || tags.ReleaseID != release || tags.ReleaseGroupID != group {
		t.Fatalf("the identifiers written were %+v", tags)
	}
	if tags.TrackNumber != 4 || tags.DiscNumber != 1 || tags.Date != "2023-11" {
		t.Fatalf("where the track sits on the release was written as %+v", tags)
	}
}

// A track credited to two artists is written as two, in MusicBrainz's order and
// MusicBrainz's spelling. Written as one string it becomes a third artist in the
// music server, named after the pair, and neither real artist is reachable from
// it.
func TestATrackIsWrittenWithTheArtistsItsCreditNames(t *testing.T) {
	porter := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	nina := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	tags := releaseTags(db.DownloadImportRow{
		ArtistName: "Porter Robinson",
		AlbumTitle: "SMILE! :D",
		AlbumCredits: []db.ArtistCredit{
			{Name: "Porter Robinson", ArtistID: porter},
		},
	}, db.ImportTrack{
		Title: "Everything to Me", TrackNumber: 1,
		Credits: []db.ArtistCredit{
			{Name: "Porter Robinson", ArtistID: porter},
			{Name: "Ninajirachi", ArtistID: nina},
		},
	})

	if len(tags.Artists) != 2 {
		t.Fatalf("the track was written as credited to %#v", tags.Artists)
	}
	if tags.Artists[0] != (tagging.Credit{Name: "Porter Robinson", MusicBrainzArtistID: porter}) {
		t.Fatalf("the first artist written reads %#v", tags.Artists[0])
	}
	if tags.Artists[1] != (tagging.Credit{Name: "Ninajirachi", MusicBrainzArtistID: nina}) {
		t.Fatalf("the second artist written reads %#v", tags.Artists[1])
	}
	// The joined string is what a server shows as the credit on one line, and it
	// is the album's here as it has always been.
	if tags.Artist != "Porter Robinson" || tags.AlbumArtist != "Porter Robinson" {
		t.Fatalf("the credit as one string reads %+v", tags)
	}
}

// The album's credit and the track's are two different claims, and neither is
// written into the other's tag.
func TestTheAlbumsCreditIsWrittenAsTheAlbumsRatherThanTheTracks(t *testing.T) {
	tags := releaseTags(db.DownloadImportRow{
		ArtistName:   "Porter Robinson",
		AlbumTitle:   "SMILE! :D",
		AlbumCredits: []db.ArtistCredit{{Name: "Porter Robinson"}},
	}, db.ImportTrack{
		Title: "Everything to Me", TrackNumber: 1,
		Credits: []db.ArtistCredit{{Name: "Porter Robinson"}, {Name: "Ninajirachi"}},
	})

	if len(tags.AlbumArtists) != 1 || tags.AlbumArtists[0].Name != "Porter Robinson" {
		t.Fatalf("the album was written as credited to %#v", tags.AlbumArtists)
	}
}

// A track the catalogue holds no credit for is written with no list. One made by
// cutting the joined string at its commas would turn a band with a comma in its
// name into two artists forever.
func TestATrackTheCatalogueHoldsNoCreditForIsWrittenWithNoList(t *testing.T) {
	tags := releaseTags(db.DownloadImportRow{
		ArtistName: "Godspeed You! Black Emperor",
		AlbumTitle: "Lift Your Skinny Fists Like Antennas to Heaven",
	}, db.ImportTrack{Title: "Storm", TrackNumber: 1})

	if len(tags.Artists) != 0 || len(tags.AlbumArtists) != 0 {
		t.Fatalf("a credit was invented: %#v %#v", tags.Artists, tags.AlbumArtists)
	}
	if tags.Artist != "Godspeed You! Black Emperor" {
		t.Fatalf("the credit as one string reads %q", tags.Artist)
	}
}

// A credit naming somebody MusicBrainz holds no artist for keeps its place with
// no identifier. The gap is carried to the tagger untouched, which is what
// leaves the tagger free to decide what it does to the identifiers it writes.
func TestACreditWithNoArtistBehindItIsWrittenWithoutOne(t *testing.T) {
	known := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	tags := releaseTags(db.DownloadImportRow{
		ArtistName: "Some Nobody", AlbumTitle: "A Tape",
	}, db.ImportTrack{
		Title: "Untitled", TrackNumber: 1,
		Credits: []db.ArtistCredit{
			{Name: "Some Nobody"},
			{Name: "Porter Robinson", ArtistID: known},
		},
	})

	if len(tags.Artists) != 2 {
		t.Fatalf("the track was written as credited to %#v", tags.Artists)
	}
	if tags.Artists[0].Name != "Some Nobody" || tags.Artists[0].MusicBrainzArtistID != uuid.Nil {
		t.Fatalf("an identifier was invented for a name that has none: %#v", tags.Artists[0])
	}
	if tags.Artists[1].MusicBrainzArtistID != known {
		t.Fatalf("the identifier landed against %#v", tags.Artists[1])
	}
}

// A release whose edition has not been chosen has no release identifier and no
// date, and neither is invented from the group it belongs to.
func TestAReleaseWithNoEditionChosenIsTaggedWithoutOne(t *testing.T) {
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	tags := releaseTags(db.DownloadImportRow{
		ArtistName:     "AgonyOST",
		AlbumTitle:     "Halfway House Near Me",
		ReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
	}, db.ImportTrack{Title: "Walk to the Closed Bodega", TrackNumber: 4})

	if tags.ReleaseID != uuid.Nil {
		t.Fatalf("a release identifier was invented: %s", tags.ReleaseID)
	}
	if tags.Date != "" {
		t.Fatalf("a release date was invented: %q", tags.Date)
	}
}

// A file nothing in the catalogue answers for is left as it arrived. There is
// nothing to write into it, and a folder that would look tidier is not a
// reason to invent one.
func TestAFileWithNoCatalogueTrackBehindItIsLeftAsItArrived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mystery.flac")
	if err := os.WriteFile(path, []byte("not audio, and nobody identified it"), 0o644); err != nil {
		t.Fatalf("write the file: %v", err)
	}
	importer := NewImporter(nil, t.TempDir(), t.TempDir(), zerolog.Nop())

	importer.tagRelease(
		context.Background(),
		db.DownloadImportRow{ArtistName: "AgonyOST", AlbumTitle: "Halfway House Near Me"},
		map[string]db.ImportedDownloadFile{"peer/mystery.flac": {LocalPath: path}},
	)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the file: %v", err)
	}
	if string(content) != "not audio, and nobody identified it" {
		t.Fatalf("the file now reads %q", content)
	}
}

// Everything that confirms a managed copy is the file that was validated reads
// its size and its bytes against what the peer promised. Tagging rewrites both,
// so it is the last thing an import does: the provenance is recorded and the
// provider's copy released while the copy on disk is still byte for byte the one
// that arrived. Moving it earlier would leave a release that imported perfectly
// refusing to let go of the source it was made from.
func TestTheProviderCopyIsReleasedBeforeTaggingRewritesTheManagedOne(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(filepath.Join("..", "tagging", "testdata", "silence.flac"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(folder, "01.flac")
	if err := os.WriteFile(source, audio, 0o644); err != nil {
		t.Fatal(err)
	}

	requestID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	store := &fakeImportStore{retention: db.SourceRetentionDelete, row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum,
		ArtistName: "AgonyOST", AlbumTitle: "Halfway House Near Me",
		SourceDirectory: `Remote\Album`,
		Files: []db.DownloadRequestFile{
			{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: int64(len(audio))},
		},
		Tracks: []db.ImportTrack{{
			ID:    uuid.MustParse("44444444-4444-4444-4444-444444444444"),
			Title: "Walk to the Closed Bodega", DiscNumber: 1, TrackNumber: 1, DurationMS: 1_000,
		}},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "AgonyOST", Album: "Halfway House Near Me",
			Title: "Walk to the Closed Bodega", DiscNumber: 1, TrackNumber: 1, DurationMS: 1_000,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	// The copy has to have actually been rewritten, or the order it happened in
	// says nothing.
	managed := store.localPaths[`Music\Album\01.flac`].LocalPath
	info, err := os.Stat(managed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == int64(len(audio)) {
		t.Fatal("the managed copy was never tagged, so this test proves nothing")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("the provider copy was kept: %v", err)
	}
	if !store.removed {
		t.Fatalf("retention reported: %q", store.retentionDetail)
	}
}

// A want is named in the user's own words and a proof is MusicBrainz speaking.
// Where both have something to say, the proof wins.
func TestWhatWasProvenBeatsWhatThePlaylistCalledIt(t *testing.T) {
	recording := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:            "porter robinson, ninajirachi",
		EntryTitle:             "Everything to me",
		MusicBrainzRecordingID: recording,
	}, &identity.Identity{
		RecordingID: recording, ReleaseGroupID: group,
		ArtistName: "Porter Robinson", TrackTitle: "Everything to Me",
		ReleaseTitle: "SMILE! :D",
	})

	if tags.Title != "Everything to Me" || tags.Artist != "Porter Robinson" {
		t.Fatalf("the file was tagged %+v", tags)
	}
	if tags.Album != "SMILE! :D" || tags.ReleaseGroupID != group {
		t.Fatalf("the release it was proven against was written as %+v", tags)
	}
}

// A want is a track. Its title is not an album name, however much tidier an
// album called after the song would look in a music server.
func TestNoAlbumIsInventedForACopyAcceptedByHand(t *testing.T) {
	tags := copyTags(db.TargetImportRow{
		EntryArtist:            "Himera",
		EntryTitle:             "Tears",
		MusicBrainzRecordingID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
	}, nil)

	if tags.Album != "" {
		t.Fatalf("an album was invented: %q", tags.Album)
	}
	if tags.Title != "Tears" || tags.Artist != "Himera" {
		t.Fatalf("the copy was tagged %+v", tags)
	}
}

// A want whose release nobody has named has no date either. Nothing here knows
// when the track came out, and the year a music server would sort it by is not
// something to make up.
func TestNoDateIsInventedForAWantWhoseReleaseIsUnknown(t *testing.T) {
	tags := copyTags(db.TargetImportRow{
		EntryArtist:            "Himera",
		EntryTitle:             "Tears",
		MusicBrainzRecordingID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
	}, nil)

	if tags.Date != "" {
		t.Fatalf("a release date was invented: %q", tags.Date)
	}
	if tags.ReleaseID != uuid.Nil {
		t.Fatalf("a release identifier was invented: %s", tags.ReleaseID)
	}
}

// A track fetched one at a time is filed under the album the catalogue holds
// for the release group it was resolved through — the same album, by the same
// name and the same identifier, as the files that arrived as a whole release.
func TestACopyIsTaggedWithTheReleaseTheWantWasResolvedThrough(t *testing.T) {
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	release := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "Anetha",
		EntryTitle:                "Candy from Strangers",
		EntryAlbum:                "candy from strangers - single",
		MusicBrainzRecordingID:    uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		AlbumTitle:                "Candy from Strangers",
		ReleaseID:                 uuid.NullUUID{UUID: release, Valid: true},
		ReleaseDate:               "2021-06-11",
	}, nil)

	if tags.Album != "Candy from Strangers" || tags.Date != "2021-06-11" {
		t.Fatalf("the release was written as %+v", tags)
	}
	if tags.ReleaseID != release || tags.ReleaseGroupID != group {
		t.Fatalf("the identifiers written were %+v", tags)
	}
}

// A catalogued album whose edition has not been chosen yet has a name and no
// date, and neither the date nor the identifier is invented from the group it
// belongs to — the same answer a whole release of it would give.
func TestAWantWhoseAlbumHasNoEditionChosenIsTaggedWithoutOne(t *testing.T) {
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "Anetha",
		EntryTitle:                "Candy from Strangers",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		AlbumTitle:                "Candy from Strangers",
	}, nil)

	if tags.Album != "Candy from Strangers" {
		t.Fatalf("the album the catalogue holds was written as %q", tags.Album)
	}
	if tags.ReleaseID != uuid.Nil || tags.Date != "" {
		t.Fatalf("an edition was invented: %+v", tags)
	}
}

// The catalogue answers for the release group the want was resolved through. A
// proof that names another one is describing a different release, and the date
// and identifier of the album the want was looked for under belong to that
// other release no more than a stranger's would.
func TestTheCataloguesReleaseIsDroppedWhenTheProofNamesAnotherGroup(t *testing.T) {
	wanted := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	proven := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "Anetha",
		EntryTitle:                "Candy from Strangers",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: wanted, Valid: true},
		AlbumTitle:                "Candy from Strangers",
		ReleaseID: uuid.NullUUID{
			UUID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Valid: true,
		},
		ReleaseDate: "2021-06-11",
	}, &identity.Identity{
		ReleaseGroupID: proven, ReleaseTitle: "Mother Ep",
	})

	if tags.Album != "Mother Ep" || tags.ReleaseGroupID != proven {
		t.Fatalf("the release proven was written as %+v", tags)
	}
	if tags.ReleaseID != uuid.Nil || tags.Date != "" {
		t.Fatalf("another release's edition was written: %+v", tags)
	}
}

// Where a proof names the release group the want was resolved through, the two
// are talking about the same release and the catalogue is the one that can date
// it. The name it holds is written with the date and the identifier that belong
// to it, rather than a name from the proof standing beside another edition's
// identifier and filing one album under two names.
func TestTheCataloguesNameIsWrittenWhereTheProofAgreesWithTheGroup(t *testing.T) {
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	release := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "Anetha",
		EntryTitle:                "Candy from Strangers",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		AlbumTitle:                "Mata Hari",
		ReleaseID:                 uuid.NullUUID{UUID: release, Valid: true},
		ReleaseDate:               "2019-11-15",
	}, &identity.Identity{
		ReleaseGroupID: group, ReleaseTitle: "Mata Hari (Japanese edition)",
	})

	if tags.Album != "Mata Hari" {
		t.Fatalf("the album was named %q, not as the catalogue holds it", tags.Album)
	}
	if tags.ReleaseID != release || tags.Date != "2019-11-15" {
		t.Fatalf("the edition written was %+v", tags)
	}
}

// Where nothing has named the release, the entry's own account of it stands.
// The playlist said which album the track is from, and that is the entry
// speaking, the same as the artist and the title beside it — not an album made
// up out of the track's name.
func TestTheAlbumTheEntryNamedIsWrittenWhereNothingElseNamesOne(t *testing.T) {
	tags := copyTags(db.TargetImportRow{
		EntryArtist:            "Himera",
		EntryTitle:             "Tears",
		EntryAlbum:             "Sleepless",
		MusicBrainzRecordingID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
	}, nil)

	if tags.Album != "Sleepless" {
		t.Fatalf("the album the entry named was written as %q", tags.Album)
	}
	if tags.Date != "" || tags.ReleaseID != uuid.Nil {
		t.Fatalf("the entry's word was taken for an edition: %+v", tags)
	}
}

// A track fetched one at a time is numbered where the catalogue's album puts
// it, in the same numbers the whole release would have given the same file. A
// file that arrives unnumbered beside numbered siblings is a track on no disc,
// and a music server shows that as a disc of its own.
func TestACopyIsNumberedWhereTheCatalogueSaysItSitsOnTheRelease(t *testing.T) {
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	release := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "AgonyOST",
		EntryTitle:                "Walk to the Closed Bodega",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		AlbumTitle:                "Halfway House Near Me",
		ReleaseID:                 uuid.NullUUID{UUID: release, Valid: true},
		ReleaseDate:               "2023-12-24",
		DiscNumber:                2,
		TrackNumber:               4,
	}, nil)

	whole := releaseTags(db.DownloadImportRow{
		ArtistName:     "AgonyOST",
		AlbumTitle:     "Halfway House Near Me",
		ReleaseID:      uuid.NullUUID{UUID: release, Valid: true},
		ReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		ReleaseDate:    "2023-12-24",
	}, db.ImportTrack{
		Title: "Walk to the Closed Bodega", DiscNumber: 2, TrackNumber: 4,
	})

	if tags.DiscNumber != whole.DiscNumber || tags.TrackNumber != whole.TrackNumber {
		t.Fatalf("the copy was numbered %+v, the whole release %+v", tags, whole)
	}
}

// A copy fetched one at a time is credited as the catalogue credits the track it
// answers — the same list, in the same order, that a whole release's import
// writes into the same file.
func TestACopyIsCreditedAsTheCatalogueCreditsTheTrack(t *testing.T) {
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	anetha := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	mandar := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	credited := []db.ArtistCredit{
		{Name: "Anetha", ArtistID: anetha}, {Name: "Mandar", ArtistID: mandar},
	}

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "anetha, mandar",
		EntryTitle:                "Candy from Strangers",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		AlbumTitle:                "Mata Hari",
		TrackCredits:              credited,
		AlbumCredits:              []db.ArtistCredit{{Name: "Anetha", ArtistID: anetha}},
	}, nil)

	whole := releaseTags(db.DownloadImportRow{
		ArtistName:   "Anetha",
		AlbumTitle:   "Mata Hari",
		AlbumCredits: []db.ArtistCredit{{Name: "Anetha", ArtistID: anetha}},
	}, db.ImportTrack{Title: "Candy from Strangers", Credits: credited})

	if !slices.Equal(tags.Artists, whole.Artists) {
		t.Fatalf("the copy was credited %#v, the whole release %#v", tags.Artists, whole.Artists)
	}
	if !slices.Equal(tags.AlbumArtists, whole.AlbumArtists) {
		t.Fatalf("the album was credited %#v, the whole release %#v",
			tags.AlbumArtists, whole.AlbumArtists)
	}
}

// An album the entry itself named is the playlist's account of where the track
// comes from, and a credit on it is not part of that account. A credit is a
// credit on a release, and this file is not on a release the catalogue answered
// for — the words the playlist typed stay one string rather than being cut into
// artists MusicBrainz never named.
func TestNoCreditIsWrittenWhereTheAlbumCameFromTheEntry(t *testing.T) {
	tags := copyTags(db.TargetImportRow{
		EntryArtist:            "porter robinson, ninajirachi",
		EntryTitle:             "Everything to Me",
		EntryAlbum:             "SMILE! :D",
		MusicBrainzRecordingID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		TrackCredits:           []db.ArtistCredit{{Name: "Porter Robinson"}},
		AlbumCredits:           []db.ArtistCredit{{Name: "Porter Robinson"}},
	}, nil)

	if tags.Album != "SMILE! :D" {
		t.Fatalf("the album the entry named was written as %q", tags.Album)
	}
	if len(tags.Artists) != 0 || len(tags.AlbumArtists) != 0 {
		t.Fatalf("another release's credit was written: %#v %#v", tags.Artists, tags.AlbumArtists)
	}
	if tags.Artist != "porter robinson, ninajirachi" {
		t.Fatalf("the credit as one string reads %q", tags.Artist)
	}
}

// A proof that names another release group is describing a different release,
// and the credit on the release the want was looked for under belongs to that
// other release no more than a stranger's would.
func TestNoCreditIsWrittenWhereTheProofNamesAnotherGroup(t *testing.T) {
	wanted := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	proven := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "Anetha",
		EntryTitle:                "Candy from Strangers",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: wanted, Valid: true},
		AlbumTitle:                "Mata Hari",
		TrackCredits:              []db.ArtistCredit{{Name: "Anetha"}, {Name: "Mandar"}},
		AlbumCredits:              []db.ArtistCredit{{Name: "Anetha"}},
	}, &identity.Identity{ReleaseGroupID: proven, ReleaseTitle: "Mother Ep"})

	if tags.Album != "Mother Ep" {
		t.Fatalf("the release proven was written as %q", tags.Album)
	}
	if len(tags.Artists) != 0 || len(tags.AlbumArtists) != 0 {
		t.Fatalf("another release's credit was written: %#v %#v", tags.Artists, tags.AlbumArtists)
	}
}

// A recording the catalogued album holds no track for has no position on it.
// The album is still the album — the want was resolved through its release
// group — but where the file sits on it is not something the catalogue said, so
// the file keeps the numbering it arrived with rather than one made up here.
func TestNoPositionIsWrittenForAWantTheCataloguedAlbumHasNoTrackFor(t *testing.T) {
	group := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "AgonyOST",
		EntryTitle:                "Walk to the Closed Bodega",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: group, Valid: true},
		AlbumTitle:                "Halfway House Near Me",
		ReleaseDate:               "2023-12-24",
	}, nil)

	if tags.Album != "Halfway House Near Me" {
		t.Fatalf("the album the catalogue holds was written as %q", tags.Album)
	}
	if tags.DiscNumber != 0 || tags.TrackNumber != 0 {
		t.Fatalf("a position was invented: %+v", tags)
	}
}

// An album the entry itself named is the playlist's account of where the track
// comes from, and a position on it is not part of that account. Numbering a
// file against a release Schall never looked up would put it at a place on that
// release nobody has checked.
func TestNoPositionIsWrittenWhereTheAlbumCameFromTheEntry(t *testing.T) {
	tags := copyTags(db.TargetImportRow{
		EntryArtist:            "Himera",
		EntryTitle:             "Tears",
		EntryAlbum:             "Sleepless",
		MusicBrainzRecordingID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		DiscNumber:             1,
		TrackNumber:            3,
	}, nil)

	if tags.Album != "Sleepless" {
		t.Fatalf("the album the entry named was written as %q", tags.Album)
	}
	if tags.DiscNumber != 0 || tags.TrackNumber != 0 {
		t.Fatalf("the entry's album was numbered from another release: %+v", tags)
	}
}

// A proof that names another release group is describing a different release,
// and a position on the release the want was looked for under says nothing
// about where the file sits on the one it turned out to be from.
func TestNoPositionIsWrittenWhereTheProofNamesAnotherGroup(t *testing.T) {
	wanted := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	proven := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	tags := copyTags(db.TargetImportRow{
		EntryArtist:               "Anetha",
		EntryTitle:                "Candy from Strangers",
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: wanted, Valid: true},
		AlbumTitle:                "Mata Hari",
		DiscNumber:                1,
		TrackNumber:               7,
	}, &identity.Identity{
		ReleaseGroupID: proven, ReleaseTitle: "Mother Ep",
	})

	if tags.Album != "Mother Ep" {
		t.Fatalf("the release proven was written as %q", tags.Album)
	}
	if tags.DiscNumber != 0 || tags.TrackNumber != 0 {
		t.Fatalf("another release's position was written: %+v", tags)
	}
}
