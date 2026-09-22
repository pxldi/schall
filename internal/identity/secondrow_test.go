package identity

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// MusicBrainz sometimes enters one performance as two recordings and never
// merges them: the same title, the same length, the same artist credit and the
// same ISRC, differing only in which release each hangs off. A want is keyed to
// one of the rows and a fingerprint names the other.
//
// Read as a difference that is the audio naming other music, which is final and
// throws the right copy away. These tests pin when it is not read that way, and
// they matter most where they refuse to help: the rule turns on a proof, and
// without that proof the refusal stands. See ADR 0025.

// Both rows of "Racks" by 88GLAM, as MusicBrainz actually holds them: one on
// "88GLAM 2.5" and one on "88GLAM2", under one ISRC and with nothing else
// between them.
func twoRowsOfOneTrack() (wanted, named musicbrainz.Recording) {
	wanted = creditedTo(
		recording(firstID, "Racks", "88GLAM", 208525, "88GLAM 2.5"), []string{"88GLAM"})
	wanted.ISRCs = []string{"USUM71819697"}
	named = creditedTo(
		recording(secondID, "Racks", "88GLAM", 208525, "88GLAM2"), []string{"88GLAM"})
	named.ISRCs = []string{"USUM71819697"}
	return wanted, named
}

// Two rows under one registration code that are not one track: "1992" and the
// sped-up mixes edition MusicBrainz files the same ISRC on. The code is all
// they share, so only 0025's test can speak about them.
func twoRowsUnderOneCode() (wanted, named musicbrainz.Recording) {
	wanted = creditedTo(
		recording(firstID, "1992", "Princess Nokia", 190000, "1992"),
		[]string{"Princess Nokia"})
	wanted.ISRCs = []string{"USA2P1728301"}
	named = creditedTo(
		recording(secondID, "1992 (sped Up + slowed mixes)", "Princess Nokia", 152000,
			"1992 Deluxe"), []string{"Princess Nokia"})
	named.ISRCs = []string{"USA2P1728301"}
	return wanted, named
}

// A copy of "1992", reproducing the sample Deezer publishes for it, with the
// audio identified as the sped-up row.
func copyUnderOneCode() Evidence {
	return Evidence{
		Subject: SubjectFile, Artist: "Princess Nokia", Title: "1992", DurationMS: 190001,
		Acoustic: heard(cluster(0.97, secondID)),
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.066, Source: AnchorSourceDeezer,
		},
	}
}

func oneCodeProvider(wanted, named musicbrainz.Recording) *stubProvider {
	return &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return named, nil
		}
		return wanted, nil
	}}
}

// A copy of that track, reproducing the sample the want is anchored to, with
// the audio identified as the other row.
func copyOfOneTrack() Evidence {
	return Evidence{
		Subject: SubjectFile, Artist: "88GLAM", Title: "Racks", DurationMS: 208524,
		Acoustic: heard(cluster(0.97, secondID)),
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.066, Source: AnchorSourceDeezer,
		},
	}
}

func twoRowProvider() *stubProvider {
	return oneCodeProvider(twoRowsOfOneTrack())
}

// The copy is the wanted music and it is let in. The audio was identified as the
// catalogue's other row for the same registered track, which is a statement
// about MusicBrainz's bookkeeping and not about what the music is.
func TestACopyNamedAsTheOtherRowOfOneTrackIsNotRefused(t *testing.T) {
	resolver := NewResolver(twoRowProvider())

	verification, err := resolver.Verify(
		context.Background(), copyOfOneTrack(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the second row set aside")
	}
	if !verification.AudioSetAside {
		t.Error("the second row is not reported as set aside")
	}
	if !verification.Proven() {
		t.Fatalf("nothing proven: %q", verification.Summary)
	}
}

// What proved it is the sample, and the record says so. AcoustID identified a
// row this want is not keyed to, so recording it as having identified this
// recording would write down a thing AcoustID never said.
func TestTheSampleIsRecordedAsWhatProvedSuchACopy(t *testing.T) {
	resolver := NewResolver(twoRowProvider())

	verification, err := resolver.Verify(
		context.Background(), copyOfOneTrack(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Identity == nil || verification.Identity.Method != "published-sample" {
		t.Fatalf("identity = %+v, want the sample recorded as what proved it",
			verification.Identity)
	}
}

// Without the sample the refusal stands, and this is the whole safety of the
// rule. MusicBrainz puts one ISRC on genuinely different audio too — the ISRC on
// "1992" is the ISRC on "1992 (sped Up + slowed mixes)" — so a shared code on
// its own would let a fingerprint of the sped-up edit answer for a want for the
// original.
func TestASharedISRCAloneDoesNotSetTheOtherRowAside(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(twoRowsUnderOneCode()))
	copied := copyUnderOneCode()
	copied.Anchor = nil

	verification, err := resolver.Verify(
		context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
	if verification.Proven() {
		t.Errorf("identity = %+v, want nothing proven without the sample", verification.Identity)
	}
}

// An upload found by the want's name does not set a row aside on the shared
// code alone. It is audio a search returned under this name, and the sibling
// edit that shares the ISRC is exactly what such a search returns, so reading it
// here would cancel the fingerprint's refusal with the audio being refused
// (ADR 0029). The other test, on the two rows' own names and lengths,
// does admit such an upload, and it is the length agreement that separates the
// two cases (ADR 0031).
func TestAnUploadFoundByNameDoesNotSetTheOtherRowAside(t *testing.T) {
	resolver := NewResolver(oneCodeProvider(twoRowsUnderOneCode()))
	copied := copyUnderOneCode()
	copied.Anchor.Source = AnchorByName

	verification, err := resolver.Verify(
		context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
	if verification.Proven() {
		t.Errorf("identity = %+v, want nothing proven on an upload found by name",
			verification.Identity)
	}
}

// The named recording is registered under a different code, so nothing says the
// catalogue holds one track twice. The audio is naming other music and the copy
// is refused, sample or no sample.
func TestAnUnrelatedRecordingIsStillTheAudioNamingOtherMusic(t *testing.T) {
	wanted, _ := twoRowsOfOneTrack()
	other := recording(secondID, "Some Other Song", "Somebody Else", 208525)
	other.ISRCs = []string{"GBAYE0601498"}
	resolver := NewResolver(&stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return other, nil
		}
		return wanted, nil
	}})

	verification, err := resolver.Verify(
		context.Background(), copyOfOneTrack(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
}

// The wanted recording has no ISRC at all. An absent code is silence and shares
// nothing with anybody, so there is no proof the two rows are one track.
func TestAWantWithNoISRCSharesNothing(t *testing.T) {
	wanted, named := twoRowsUnderOneCode()
	wanted.ISRCs = nil
	resolver := NewResolver(oneCodeProvider(wanted, named))

	verification, err := resolver.Verify(
		context.Background(), copyUnderOneCode(), uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the audio is not reported as naming other music, want the refusal to stand")
	}
}

// The extra lookup is paid only where a copy was about to be refused for good.
// MusicBrainz serves one request a second and a want is offered by many peers,
// so a copy the audio agrees about must cost exactly what it always cost.
func TestACopyTheAudioAgreesAboutCostsNoExtraLookup(t *testing.T) {
	wanted, _ := twoRowsOfOneTrack()
	provider := &stubProvider{recording: wanted}
	copied := copyOfOneTrack()
	copied.Acoustic = heard(cluster(0.97, firstID))

	if _, err := NewResolver(provider).Verify(
		context.Background(), copied, uuid.MustParse(firstID)); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if provider.lookups != 1 {
		t.Errorf("lookups = %d, want the want's own recording alone", provider.lookups)
	}
}

// AcoustID lumps two $uicideboy$ songs into one cluster: the leading cluster
// names the other row of the wanted track and a second, unrelated song. Seen on
// the live instance on 2026-09-05, where the leftover name refused a copy the
// sample had proven. A cluster that names this recording under another row is
// not naming other music, whatever else it carries; it admits nothing by
// itself, and the sample decides.
func TestAClusterNamingASiblingRowAndAnotherSongIsNotOtherMusic(t *testing.T) {
	const thirdID = "4bdc99d6-5202-413b-bf36-c2dcd443b575"
	wanted, named := twoRowsOfOneTrack()
	another := creditedTo(
		recording(thirdID, "…and to Those I Love, Thanks for Sticking Around", "88GLAM",
			168489, "Some Album"), []string{"88GLAM"})
	another.ISRCs = []string{"USUM71819999"}
	resolver := NewResolver(&stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		switch id {
		case uuid.MustParse(secondID):
			return named, nil
		case uuid.MustParse(thirdID):
			return another, nil
		}
		return wanted, nil
	}})
	copied := copyOfOneTrack()
	copied.Acoustic = heard(cluster(0.99, secondID, thirdID))

	verification, err := resolver.Verify(
		context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the sibling row to keep it silent")
	}
	if !verification.Proven() {
		t.Fatalf("nothing proven: %q", verification.Summary)
	}
	if verification.SameRegistrationUnchecked {
		t.Error("the check is reported as not run")
	}
}

// A name MusicBrainz no longer has a row for is a stale link, not other music.
func TestAClusterNamingADeletedRowIsNotOtherMusic(t *testing.T) {
	const goneID = "0e0e0e0e-0e0e-4e0e-8e0e-0e0e0e0e0e0e"
	wanted, _ := twoRowsOfOneTrack()
	resolver := NewResolver(&stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(goneID) {
			return musicbrainz.Recording{}, musicbrainz.ErrNotFound
		}
		return wanted, nil
	}})
	copied := copyOfOneTrack()
	copied.Acoustic = heard(cluster(0.99, goneID))

	verification, err := resolver.Verify(
		context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want a deleted row set aside")
	}
	if !verification.Proven() {
		t.Fatalf("nothing proven: %q", verification.Summary)
	}
}
