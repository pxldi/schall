package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// OneRegistration is the same question secondRowOfSameRegistration asks, put
// about a pair: a want's copy was proven, imported, and then filed by the
// library under a second MusicBrainz row. It used to read ADR 0025's
// registration code alone, which left 32 wants stopped on 2026-09-05 with their
// own music on the disc. These tests pin what the two tests of 0031 answer here.
//
// Every one of them holds the copy's own proof fixed: the audio identified it as
// the recording the want names. What the registration test may add to that proof
// is the subject, and what it may do without one is pinned separately, below.

// A Deezer excerpt this copy reproduces, measured against the row the want
// names.
func excerptOf(rate float64) *AnchorComparison {
	return &AnchorComparison{
		RecordingID: uuid.MustParse(firstID), Rate: rate, Source: AnchorSourceDeezer,
	}
}

func askOneRegistration(
	t *testing.T, wanted, filed musicbrainz.Recording,
	anchor *AnchorComparison, copyDurationMS int,
) bool {
	t.Helper()
	return askFiling(t, oneCodeProvider(wanted, filed), FilingEvidence{
		Anchor: anchor, CopyDurationMS: copyDurationMS, Method: "fingerprint",
	}) == FilingOneRegistration
}

func askFiling(
	t *testing.T, provider Provider, evidence FilingEvidence,
) FilingVerdict {
	t.Helper()
	verdict, err := NewResolver(provider).OneRegistration(
		context.Background(), uuid.MustParse(firstID), uuid.MustParse(secondID), evidence)
	if err != nil {
		t.Fatalf("OneRegistration() error = %v", err)
	}
	return verdict
}

// The two rows of "Dónde" by Souly: one title, one credit, one length, and a
// registration code on one side only. The file on the disc is the music the
// want asked for, and nothing further is needed to say so. This is the case the
// 32 stopped wants are.
func TestAFiledRowOfOneNameAndOneLengthIsOneRegistration(t *testing.T) {
	wanted, filed := twoRowsOfOneName()

	if !askOneRegistration(t, wanted, filed, nil, 160000) {
		t.Error("the two rows are read as two registrations, want one")
	}
}

// An excerpt found by searching for the want's name never carries the
// registration-code test. A search by name returns the sibling edit that shares
// the code as readily as the original, so reading it there would answer a want
// for "1992" with the sped-up mixes (ADR 0029, 0031).
func TestASharedCodeWithAnUploadFoundByNameIsNotOneRegistration(t *testing.T) {
	wanted, filed := twoRowsUnderOneCode()
	anchor := &AnchorComparison{
		RecordingID: uuid.MustParse(firstID), Rate: 0.04, Source: AnchorByName,
	}

	if askOneRegistration(t, wanted, filed, anchor, 190000) {
		t.Error("an upload found by name carried the code test, want it read by Deezer alone")
	}
}

// The same pair with the excerpt Deezer publishes for the want's own code. That
// is 0025 unchanged, and it still settles the want.
func TestASharedCodeWithTheDistributorsExcerptIsOneRegistration(t *testing.T) {
	wanted, filed := twoRowsUnderOneCode()

	if !askOneRegistration(t, wanted, filed, excerptOf(0.04), 190000) {
		t.Error("the two rows are read as two registrations, want 0025's answer")
	}
}

// The clean edit and the explicit one. MusicBrainz holds "Timeless" by The
// Weeknd twice under one title, one credit and one length, with a registration
// code on each row, and they are different audio. Both rows carrying codes and
// sharing none is the label's own statement that it registered two tracks, and
// it voids the pair whatever the names and the lengths say.
func TestTwoRowsWithDifferentCodesAreNotOneRegistration(t *testing.T) {
	credit := []string{"The Weeknd"}
	wanted := creditedTo(recording(firstID, "Timeless", "The Weeknd", 256000), credit)
	wanted.ISRCs = []string{"USUG12406537"}
	filed := creditedTo(recording(secondID, "Timeless", "The Weeknd", 256000), credit)
	filed.ISRCs = []string{"USUG12406536"}

	if askOneRegistration(t, wanted, filed, excerptOf(0.04), 256000) {
		t.Error("two rows the label registered separately are read as one")
	}
}

// A credit that names one more artist is another recording. "The Road to Hell
// Is Highway 59" by $uicideboy$ and the same title by $uicideboy$ x RVMIRXZ are
// the pair, and the want stops for a person.
func TestAFiledRowCreditingAnotherArtistIsNotOneRegistration(t *testing.T) {
	title := "The Road to Hell Is Highway 59"
	wanted := creditedTo(
		recording(firstID, title, "$uicideboy$", 133000), []string{"$uicideboy$"})
	filed := creditedTo(recording(secondID, title, "$uicideboy$ x RVMIRXZ", 133000),
		[]string{"$uicideboy$"}, []string{"RVMIRXZ"})

	if askOneRegistration(t, wanted, filed, nil, 133000) {
		t.Error("a credit naming another artist is read as one registration")
	}
}

// A bracketed producer credit is a naming convention. "A Girl Named Drool and a
// Pack of Kools [Prod. JCKIE JEWLZ]" and the same title without it are one
// recording, and both rows run 174 seconds.
func TestAProducerLabelOnOneTitleIsOneRegistration(t *testing.T) {
	title := "A Girl Named Drool and a Pack of Kools"
	wanted := creditedTo(
		recording(firstID, title+" [Prod. JCKIE JEWLZ]", "$uicideboy$", 174000),
		[]string{"$uicideboy$"})
	filed := creditedTo(
		recording(secondID, title, "$uicideboy$", 174000), []string{"$uicideboy$"})
	filed.ISRCs = []string{"QM8DG1600123"}

	if !askOneRegistration(t, wanted, filed, nil, 174000) {
		t.Error("a bracketed producer credit is read as two registrations, want one")
	}
}

// MusicBrainz knows no length for the row the want is keyed to, so the file on
// the disc stands in for it: 157900 ms against the 157933 the other row holds.
func TestARowWithNoLengthIsComparedThroughTheFileOnTheDisc(t *testing.T) {
	wanted, filed := ziplocRows()

	if !askOneRegistration(t, wanted, filed, nil, 157900) {
		t.Error("the file on the disc did not stand in for the length nobody has")
	}
}

// The same pair with a file eight seconds short of the row that has a length.
// Nothing then ties the audio in hand to the row the library filed it under.
func TestAFileOutsideTheKnownLengthIsNotOneRegistration(t *testing.T) {
	wanted, filed := ziplocRows()

	if askOneRegistration(t, wanted, filed, nil, 150000) {
		t.Error("a file outside the tolerance is read as one registration")
	}
}

// kanyeID is the artist MusicBrainz renamed from Kanye West to Ye in 2021. The
// rows entered before the rename go on printing the old name, so one recording
// entered twice prints two credits that point at this one artist.
const kanyeID = "164f0d73-1234-4e2c-8743-d77bf2191051"

// renamedArtistRows are two rows of "Ghost Town": the want's row credited under
// the name MusicBrainz printed in 2018, the filed row under the name it prints
// now. One title, one length, and no registration code on either.
func renamedArtistRows() (wanted, filed musicbrainz.Recording) {
	artist := uuid.MustParse(kanyeID)
	wanted = creditedToArtists(
		recording(firstID, "Ghost Town", "Kanye West", 205000),
		musicbrainz.Credit{ArtistID: artist, Name: "Kanye West", ArtistName: "Ye"})
	filed = creditedToArtists(
		recording(secondID, "Ghost Town", "Ye", 205000),
		musicbrainz.Credit{ArtistID: artist, Name: "Ye", ArtistName: "Ye"})
	return wanted, filed
}

// The names disagree and the artist does not. Both credits carry the same
// MusicBrainz artist, which is MusicBrainz saying the two rows credit one
// person, so the pair falls to the same tests as any other.
func TestRowsCreditingOneRenamedArtistAreOneRegistration(t *testing.T) {
	wanted, filed := renamedArtistRows()

	if !askOneRegistration(t, wanted, filed, nil, 205000) {
		t.Error("two names for one MusicBrainz artist are read as two registrations")
	}
}

// Two artists MusicBrainz spells alike are two artists. The names agree and the
// identifiers do not, and the identifiers are what MusicBrainz actually says
// about who is credited.
func TestRowsCreditingTwoArtistsOfOneNameAreNotOneRegistration(t *testing.T) {
	wanted := creditedToArtists(
		recording(firstID, "Sunrise", "Nova", 205000),
		musicbrainz.Credit{
			ArtistID: uuid.MustParse(firstID), Name: "Nova", ArtistName: "Nova"})
	filed := creditedToArtists(
		recording(secondID, "Sunrise", "Nova", 205000),
		musicbrainz.Credit{
			ArtistID: uuid.MustParse(secondID), Name: "Nova", ArtistName: "Nova"})

	if askOneRegistration(t, wanted, filed, nil, 205000) {
		t.Error("two MusicBrainz artists of one name are read as one artist")
	}
}

// A credit MusicBrainz holds no artist entity for says nothing about who it
// names, so the names are compared as they always were. These two agree.
func TestARowWithNoArtistIDIsComparedByName(t *testing.T) {
	wanted, filed := renamedArtistRows()
	wanted.ArtistCredit = "Ye"
	wanted.Credits[0] = musicbrainz.Credit{Name: "Ye"}

	if !askOneRegistration(t, wanted, filed, nil, 205000) {
		t.Error("a credit with no artist identifier stopped the name comparison")
	}
}

// The same row with no identifier, printing the name it was entered under. The
// names are all there is and they disagree, so the pair stays a person's
// question.
func TestARenamedArtistWithNoArtistIDIsNotOneRegistration(t *testing.T) {
	wanted, filed := renamedArtistRows()
	wanted.Credits[0] = musicbrainz.Credit{Name: "Kanye West"}

	if askOneRegistration(t, wanted, filed, nil, 205000) {
		t.Error("two names with nothing tying them together are read as one artist")
	}
}

// What the two rows say about each other never files a copy on its own.
//
// "Dónde" by Souly is entered twice under one title, one credit and one length,
// with a code on one row and none on the other. That is what MusicBrainz's
// duplicates look like, and it is also what a re-recording and a live take of
// the same length look like. Whether the file on the disc is this want's music
// is answered by whatever admitted the file, so a copy nothing admitted stays a
// question for a person however alike the rows read.
func TestRowsAlikeWithNothingProvingTheCopyAreNotFiled(t *testing.T) {
	wanted, filed := twoRowsOfOneName()

	verdict := askFiling(t, oneCodeProvider(wanted, filed),
		FilingEvidence{CopyDurationMS: 160000})

	if verdict != FilingOtherRecording {
		t.Errorf("verdict = %v, want the pair left for a person", verdict)
	}
}

// The tags are not the audio. A copy admitted because its own tags carried a
// recording ID says nothing a stranger could not have written, so it cannot
// carry the second row either (ADR 0002).
func TestATagMethodDoesNotFileACopyUnderASecondRow(t *testing.T) {
	wanted, filed := twoRowsOfOneName()

	verdict := askFiling(t, oneCodeProvider(wanted, filed),
		FilingEvidence{CopyDurationMS: 160000, Method: "recording-id"})

	if verdict != FilingOtherRecording {
		t.Errorf("verdict = %v, want the pair left for a person", verdict)
	}
}

// The copy the library already holds a witness for. The audio answered for the
// recording the want names, and the second row was the only thing left standing
// in the way, so the want settles against it.
func TestACopyProvenByAWitnessIsFiledUnderTheWantsRow(t *testing.T) {
	wanted, filed := twoRowsOfOneName()

	verdict := askFiling(t, oneCodeProvider(wanted, filed),
		FilingEvidence{CopyDurationMS: 160000, Method: "library-witness"})

	if verdict != FilingOneRegistration {
		t.Errorf("verdict = %v, want the copy filed under the want's row", verdict)
	}
}

// A person who listened is a decision and not a resemblance. Their answer is
// that this copy is the wanted music, which is what the registration test needed
// alongside it.
func TestACopyAcceptedByHandIsFiledUnderTheWantsRow(t *testing.T) {
	wanted, filed := twoRowsOfOneName()

	verdict := askFiling(t, oneCodeProvider(wanted, filed),
		FilingEvidence{CopyDurationMS: 160000, Method: "manual"})

	if verdict != FilingOneRegistration {
		t.Errorf("verdict = %v, want the copy filed under the want's row", verdict)
	}
}

// mergedRowsProvider answers both identifiers with one row, which is what
// MusicBrainz does once the two have been merged: a lookup on the retired
// identifier redirects to the surviving recording.
func mergedRowsProvider(surviving musicbrainz.Recording) *stubProvider {
	return &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		row := surviving
		if id != row.ID {
			row.KnownAs = append([]uuid.UUID{}, id)
		}
		return row, nil
	}}
}

// recordedAliases is what a merge is written to. It is the provider's own answer
// being copied down, and nothing else may put a row in it.
type recordedAliases struct{ pairs [][2]uuid.UUID }

func (aliases *recordedAliases) RecordRecordingAlias(
	_ context.Context, retired, surviving uuid.UUID,
) error {
	aliases.pairs = append(aliases.pairs, [2]uuid.UUID{retired, surviving})
	return nil
}

// failingAliases cannot write a merge down.
type failingAliases struct{}

func (failingAliases) RecordRecordingAlias(context.Context, uuid.UUID, uuid.UUID) error {
	return errors.New("the alias table is unreachable")
}

// A merge MusicBrainz reported but Schall could not write down files nothing.
// The record is what wakes every other want held on the retired row, so the
// filing waits for the sweep that can make it.
func TestAMergeThatCannotBeWrittenDownFilesNothing(t *testing.T) {
	_, filed := twoRowsOfOneName()
	resolver := NewResolver(mergedRowsProvider(filed)).WithAliases(failingAliases{})

	verdict, err := resolver.OneRegistration(
		context.Background(), uuid.MustParse(firstID), uuid.MustParse(secondID),
		FilingEvidence{CopyDurationMS: 160000})
	if err == nil {
		t.Fatal("OneRegistration() error = nil, want the alias write's failure")
	}
	if verdict == FilingMerged {
		t.Error("verdict = FilingMerged, want no filing while the merge is unrecorded")
	}
}

// MusicBrainz merged the two rows. A lookup on the want's identifier arrives at
// the row the library filed the copy under, which is the catalogue's own
// statement that they name one recording, and it files the copy whatever else is
// or is not known about it.
func TestRowsMusicBrainzMergedAreFiledWithoutAnyOtherProof(t *testing.T) {
	_, filed := twoRowsOfOneName()
	aliases := &recordedAliases{}
	resolver := NewResolver(mergedRowsProvider(filed)).WithAliases(aliases)

	verdict, err := resolver.OneRegistration(
		context.Background(), uuid.MustParse(firstID), uuid.MustParse(secondID),
		FilingEvidence{CopyDurationMS: 160000})
	if err != nil {
		t.Fatalf("OneRegistration() error = %v", err)
	}

	if verdict != FilingMerged {
		t.Errorf("verdict = %v, want the merge read as the answer", verdict)
	}
	want := [2]uuid.UUID{uuid.MustParse(firstID), uuid.MustParse(secondID)}
	if len(aliases.pairs) != 1 || aliases.pairs[0] != want {
		t.Errorf("aliases recorded = %v, want the one merge %v", aliases.pairs, want)
	}
}
