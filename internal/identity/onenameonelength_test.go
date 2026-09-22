package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// Most of the rows MusicBrainz enters twice carry no ISRC on one side, so
// ADR 0025's test cannot speak about them: 1244 copies on 83 wants
// were discarded because the audio named the catalogue's other row and nothing
// could say the two rows were one track. These tests pin the second test, on the
// rows' own names and lengths, and they matter most where they refuse to help.
// See ADR 0031.

// Both rows of "Dónde" by Souly, as MusicBrainz holds them: one on the release
// "Dónde" with no ISRC, one on "traence" with one. The names, the credits and
// the lengths are the same.
func twoRowsOfOneName() (wanted, named musicbrainz.Recording) {
	wanted = creditedTo(
		recording(firstID, "Dónde", "Souly", 160000, "Dónde"), []string{"Souly"})
	named = creditedTo(
		recording(secondID, "Dónde", "Souly", 160000, "traence"), []string{"Souly"})
	named.ISRCs = []string{"DEUM72300761"}
	return wanted, named
}

// A copy of that track, reproducing the upload the want is anchored to, with the
// audio identified as the other row.
func copyOfOneName() Evidence {
	return Evidence{
		Subject: SubjectFile, Artist: "Souly", Title: "Dónde", DurationMS: 160000,
		Acoustic: heard(cluster(0.97, secondID)),
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.04, Source: AnchorByName,
		},
	}
}

// The copy is the wanted music and it is let in. The two rows name one artist,
// hold one title and run one length, so the audio naming the other of them is a
// statement about MusicBrainz's bookkeeping and not about what the music is.
func TestACopyNamedAsARowOfOneNameAndOneLengthIsNotRefused(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(twoRowsOfOneName()))

	verification, err := resolver.Verify(
		context.Background(), copyOfOneName(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the second row set aside")
	}
	if !verification.Proven() {
		t.Fatalf("nothing proven: %q", verification.Summary)
	}
}

// The row set aside leaves nothing in the verdicts, so the ticks a person reads
// stop saying the audio disagrees.
func TestSuchACopyNoLongerListsTheAudioAsDisagreeing(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(twoRowsOfOneName()))

	verification, err := resolver.Verify(
		context.Background(), copyOfOneName(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	for _, differs := range verification.Differs {
		if strings.HasPrefix(differs, "audio") {
			t.Errorf("differs = %v, want the audio off it", verification.Differs)
		}
	}
}

// What AcoustID said is not lost with the tick. The summary names the row it
// identified, because that is the only place somebody can now read it.
func TestSuchACopySaysWhichRowTheAudioNamed(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(twoRowsOfOneName()))

	verification, err := resolver.Verify(
		context.Background(), copyOfOneName(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !strings.Contains(verification.Summary, "same registered track") {
		t.Errorf("summary = %q, want the row AcoustID named", verification.Summary)
	}
}

// Three seconds apart is two different recordings as far as this test goes. Both
// numbers are the catalogue's own, taken from the same audio if it is the same
// audio, so the tolerance absorbs a typo and nothing more.
func TestRowsThreeSecondsApartAreNotOneRegistration(t *testing.T) {
	wanted, named := twoRowsOfOneName()
	*named.DurationMS = 163000
	resolver := NewResolver(oneCodeProvider(wanted, named))

	verification, err := resolver.Verify(
		context.Background(), copyOfOneName(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
}

// A sped-up edit is what this rule has to keep out, and its title says so. The
// upload the want is anchored to was found by name, and a search for a name
// returns the edit as readily as the original.
func TestARowTitledAsASpedUpEditIsNotOneRegistration(t *testing.T) {
	wanted, named := twoRowsOfOneName()
	named.Title = "Dónde (sped up)"
	resolver := NewResolver(oneCodeProvider(wanted, named))

	verification, err := resolver.Verify(
		context.Background(), copyOfOneName(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
}

// Two rows credited to different artists are two recordings, whatever they are
// called. "Who Am I" by Travis Scott and "Who Am I" by The Graduates are the
// concrete pair, three seconds apart as well.
func TestRowsWithDifferentCreditsAreNotOneRegistration(t *testing.T) {
	wanted := creditedTo(
		recording(firstID, "Who Am I", "Travis Scott", 210000), []string{"Travis Scott"})
	named := creditedTo(
		recording(secondID, "Who Am I", "The Graduates", 207000), []string{"The Graduates"})
	copied := Evidence{
		Subject: SubjectFile, Artist: "Travis Scott", Title: "Who Am I", DurationMS: 210000,
		Acoustic: heard(cluster(0.97, secondID)),
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.04, Source: AnchorByName,
		},
	}
	resolver := NewResolver(oneCodeProvider(wanted, named))

	verification, err := resolver.Verify(context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
}

// One row credits "$uicideboy$" with a left-to-right embedding inside the name.
// The character prints nothing, so the two rows credit one artist.
func TestACreditDifferingByAnInvisibleCharacterIsOneRegistration(t *testing.T) {
	title := "I Will Celebrate for Stepping on Broken Glass"
	wanted := creditedTo(
		recording(firstID, title, "$‪uicideboy$", 138000), []string{"$‪uicideboy$"})
	named := creditedTo(
		recording(secondID, title, "$uicideboy$", 138000), []string{"$uicideboy$"})
	named.ISRCs = []string{"QZFYX2000123"}
	copied := Evidence{
		Subject: SubjectFile, Artist: "$uicideboy$", Title: title, DurationMS: 138000,
		Acoustic: heard(cluster(0.97, secondID)),
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.04, Source: AnchorByName,
		},
	}
	resolver := NewResolver(oneCodeProvider(wanted, named))

	verification, err := resolver.Verify(context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the second row set aside")
	}
}

// A bracketed producer credit names who made the track. "Resin [Prod. GeeKey]"
// and "Resin" are one recording under two naming conventions.
func TestABracketedProducerCreditIsOneRegistration(t *testing.T) {
	wanted := creditedTo(
		recording(firstID, "Resin [Prod. GeeKey]", "$uicideboy$", 139000),
		[]string{"$uicideboy$"})
	named := creditedTo(
		recording(secondID, "Resin", "$uicideboy$", 139000), []string{"$uicideboy$"})
	named.ISRCs = []string{"QZFYX2000456"}
	copied := Evidence{
		Subject: SubjectFile, Artist: "$uicideboy$", Title: "Resin", DurationMS: 139000,
		Acoustic: heard(cluster(0.97, secondID)),
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.04, Source: AnchorByName,
		},
	}
	resolver := NewResolver(oneCodeProvider(wanted, named))

	verification, err := resolver.Verify(context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the second row set aside")
	}
}

// ziplocRows are the two rows of "Ziploc" by 88GLAM: MusicBrainz knows no length
// for the one the want is keyed to.
func ziplocRows() (wanted, named musicbrainz.Recording) {
	wanted = creditedTo(recording(firstID, "Ziploc", "88GLAM", 0), []string{"88GLAM"})
	named = creditedTo(recording(secondID, "Ziploc", "88GLAM", 157933), []string{"88GLAM"})
	named.ISRCs = []string{"CA6D21800012"}
	return wanted, named
}

func ziplocCopy(durationMS int) Evidence {
	return Evidence{
		Subject: SubjectFile, Artist: "88GLAM", Title: "Ziploc", DurationMS: durationMS,
		Acoustic: heard(cluster(0.97, secondID)),
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.04, Source: AnchorByName,
		},
	}
}

// A row MusicBrainz knows no length for is compared through the copy. The copy
// runs 157900 ms and the row that has a length says 157933, so the audio in hand
// is the audio both rows describe.
func TestARowWithNoLengthIsComparedThroughTheCopy(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(ziplocRows()))

	verification, err := resolver.Verify(
		context.Background(), ziplocCopy(157900), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the second row set aside")
	}
}

// The same pair with a copy eight seconds short of the row that has a length.
// Nothing then ties the copy to the row the audio named, and the refusal stands.
func TestACopyOutsideTheKnownLengthIsNotOneRegistration(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(ziplocRows()))

	verification, err := resolver.Verify(
		context.Background(), ziplocCopy(150000), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
}

// Neither row has a length. The title and the credit alone are what a cover
// version also has, so nothing here says the catalogue holds one track twice.
func TestTwoRowsWithNoLengthAreNotOneRegistration(t *testing.T) {
	wanted, named := ziplocRows()
	*named.DurationMS = 0
	resolver := NewResolver(oneCodeProvider(wanted, named))

	verification, err := resolver.Verify(
		context.Background(), ziplocCopy(157900), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
}

// With no anchor the copy is a question. The two rows are one registration
// whatever measured the audio, so the refusal that was final does not stand, and
// nothing about the music has proven the copy either.
func TestACopyWithNoAnchorIsNotRefused(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(twoRowsOfOneName()))
	copied := copyOfOneName()
	copied.Anchor = nil

	verification, err := resolver.Verify(context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the second row set aside")
	}
	if verification.Audio {
		t.Error("the audio is reported as having proven the copy, want nothing to have listened")
	}
}

// unreachableRow describes the want's own recording and fails the lookup of the
// row the audio named. That is the shape an outage takes on this path: which
// recording an identifier names now is a narrow question the client remembers
// for an hour, and reading the row itself is a fresh request.
type unreachableRow struct{ wanted musicbrainz.Recording }

func (provider unreachableRow) Recording(
	_ context.Context, id uuid.UUID,
) (musicbrainz.Recording, error) {
	if id == uuid.MustParse(secondID) {
		return musicbrainz.Recording{}, errors.New("musicbrainz is unreachable")
	}
	return provider.wanted, nil
}

func (provider unreachableRow) SearchRecordings(
	_ context.Context, _ musicbrainz.RecordingQuery, _ int,
) ([]musicbrainz.Recording, error) {
	return nil, nil
}

func (provider unreachableRow) ResolveRecordingID(
	_ context.Context, id uuid.UUID,
) (uuid.UUID, error) {
	return id, nil
}

// A lookup that could not be made has answered nothing. The refusal rests on
// that answer, so the copy is a question and the summary says which question.
func TestAFailedLookupLeavesTheCopyUnrefused(t *testing.T) {
	wanted, _ := twoRowsOfOneName()
	resolver := NewResolver(unreachableRow{wanted: wanted})

	verification, err := resolver.Verify(
		context.Background(), copyOfOneName(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.SameRegistrationUnchecked {
		t.Error("the check is reported as having run, want it reported as unavailable")
	}
	if !strings.Contains(verification.Summary, "could not be asked") {
		t.Errorf("summary = %q, want it to say the check could not run", verification.Summary)
	}
}

// A failed lookup proves nothing either. The copy waits for the check rather
// than being let in on what is left standing.
func TestAFailedLookupProvesNothing(t *testing.T) {
	wanted, _ := twoRowsOfOneName()
	resolver := NewResolver(unreachableRow{wanted: wanted})

	verification, err := resolver.Verify(
		context.Background(), copyOfOneName(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() {
		t.Errorf("identity = %+v, want nothing proven while the check is unavailable",
			verification.Identity)
	}
}
