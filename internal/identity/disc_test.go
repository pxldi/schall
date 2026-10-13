package identity

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

// A disc match is a copy whose AccurateRip checksum is one submitted for the
// track this recording sits at on a CD. These tests pin what it may do: admit a
// copy, answer only for the recording at that place on the disc, and never
// refuse or outweigh a witness that says the audio is other music.

func cdTrack(recordingID string) *DiscComparison {
	return &DiscComparison{RecordingID: uuid.MustParse(recordingID), Track: 6, Confidence: 12}
}

func TestACDChecksumAdmitsACopyNothingElseCouldPlace(t *testing.T) {
	copied := Evidence{
		Title: "06 karma police", DurationMS: 263000, Disc: cdTrack(firstID),
	}
	known := recording(firstID, "Karma Police", "Radiohead", 263000, "OK Computer")

	outcome := decide(copied, []musicbrainz.Recording{known})
	if outcome.Identity == nil || outcome.Identity.Method != "cd-checksum" {
		t.Fatalf("identity = %+v, want it recorded as proven by a CD checksum", outcome.Identity)
	}
}

func TestACDChecksumForAnotherRecordingSaysNothing(t *testing.T) {
	copied := Evidence{Title: "Karma Police", DurationMS: 263000, Disc: cdTrack(secondID)}
	known := recording(firstID, "Karma Police", "Radiohead", 263000, "OK Computer")

	if pair := compare(copied, known); pair.disc != tagmatch.Unknown {
		t.Fatalf("disc = %v, want silence about another recording", pair.disc)
	}
}

func TestACDChecksumDoesNotOverrideAudioNamingOtherMusic(t *testing.T) {
	copied := Evidence{
		Title: "Karma Police", DurationMS: 263000, Disc: cdTrack(firstID),
		Acoustic: heard(cluster(0.97, secondID)),
	}
	known := recording(firstID, "Karma Police", "Radiohead", 263000, "OK Computer")

	if level := gradeOf(compare(copied, known)); level.conclusive() {
		t.Fatalf("grade = %v, want AcoustID naming other music to void the pair", level)
	}
}

func TestACopyACDChecksumAdmittedIsProvenForItsWant(t *testing.T) {
	if !ProvenForItsWant("cd-checksum") {
		t.Fatal("ProvenForItsWant(cd-checksum) = false, want true")
	}
}
