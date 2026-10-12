package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// AcoustID's leading cluster can name two rows that are not one registered
// track: an original and its remix, or a track and the edit a compilation
// carries, both close enough to share a fingerprint. The audio then says the
// file is one of the two and cannot say which. The file's own identifier can,
// and these tests pin when it is let to. A copy for a want is admitted only on
// audio, so Audio is what they read; Proven alone can rest on tags.

// "Glory Box" and the remix MusicBrainz links to the same fingerprint: other
// titles, other lengths, an ISRC each.
func clusterOfTwo() (wanted, other musicbrainz.Recording) {
	wanted = creditedTo(
		recording(firstID, "Glory Box", "Portishead", 306000, "Dummy"), []string{"Portishead"})
	wanted.ISRCs = []string{"GBF079400305"}
	other = creditedTo(
		recording(secondID, "Glory Box (Remix)", "Portishead", 280000, "Glory Box"),
		[]string{"Portishead"})
	other.ISRCs = []string{"GBF079400999"}
	return wanted, other
}

func copyHeardAsBoth() Evidence {
	return Evidence{
		Subject: SubjectFile, Artist: "Portishead", Title: "Glory Box", DurationMS: 306100,
		Acoustic: heard(cluster(0.96, firstID, secondID)),
	}
}

func verifyAgainstClusterOfTwo(t *testing.T, evidence Evidence, rows ...musicbrainz.Recording) Verification {
	t.Helper()
	verification, err := NewResolver(rowProvider(rows...)).Verify(
		context.Background(), evidence, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	return verification
}

func TestAClusterOfTwoRowsAloneProvesNothing(t *testing.T) {
	wanted, other := clusterOfTwo()
	verification := verifyAgainstClusterOfTwo(t, copyHeardAsBoth(), wanted, other)
	if verification.Audio {
		t.Fatalf("admitted by audio with no identifier: %q", verification.Summary)
	}
}

func TestARecordingIDPicksTheWantOutOfTheCluster(t *testing.T) {
	wanted, other := clusterOfTwo()
	copied := copyHeardAsBoth()
	copied.RecordingID = uuid.MustParse(firstID)

	verification := verifyAgainstClusterOfTwo(t, copied, wanted, other)
	if !verification.Proven() || !verification.Audio {
		t.Fatalf("not proven by audio: %q", verification.Summary)
	}
	if verification.Identity.Method != "fingerprint-and-identifier" {
		t.Errorf("method = %q, want fingerprint-and-identifier", verification.Identity.Method)
	}
	if !strings.Contains(verification.Summary, "recording ID names this one") {
		t.Errorf("summary = %q, want it to say the ID picked the row", verification.Summary)
	}
}

func TestAnISRCOnlyTheWantCarriesPicksItOutOfTheCluster(t *testing.T) {
	wanted, other := clusterOfTwo()
	copied := copyHeardAsBoth()
	copied.ISRC = "GBF079400305"

	verification := verifyAgainstClusterOfTwo(t, copied, wanted, other)
	if !verification.Proven() || !verification.Audio {
		t.Fatalf("not proven by audio: %q", verification.Summary)
	}
}

// MusicBrainz files one ISRC on genuinely different audio too, so a code both
// rows carry says nothing about which of them the file is.
func TestAnISRCBothRowsCarryPicksNothing(t *testing.T) {
	wanted, other := clusterOfTwo()
	other.ISRCs = append(other.ISRCs, "GBF079400305")
	copied := copyHeardAsBoth()
	copied.ISRC = "GBF079400305"

	verification := verifyAgainstClusterOfTwo(t, copied, wanted, other)
	if verification.Audio {
		t.Fatalf("admitted by audio on a shared code: %q", verification.Summary)
	}
}

// The other row could not be looked up, so whether it carries the code is
// unknown, and unknown is not "no".
func TestAnISRCIsNotReadWhenTheOtherRowCannotBeAsked(t *testing.T) {
	wanted, other := clusterOfTwo()
	copied := copyHeardAsBoth()
	copied.ISRC = "GBF079400305"
	provider := rowProvider(wanted, other)
	answer := provider.answer
	provider.answer = func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return musicbrainz.Recording{}, context.DeadlineExceeded
		}
		return answer(id)
	}

	verification, err := NewResolver(provider).Verify(
		context.Background(), copied, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Audio {
		t.Fatalf("admitted by audio without asking about the other row: %q", verification.Summary)
	}
}

func TestAContradictionVoidsTheIdentifiersPick(t *testing.T) {
	wanted, other := clusterOfTwo()
	copied := copyHeardAsBoth()
	copied.RecordingID = uuid.MustParse(firstID)
	copied.DurationMS = 200000

	verification := verifyAgainstClusterOfTwo(t, copied, wanted, other)
	if verification.Audio {
		t.Fatalf("admitted by audio while the length contradicts: %q", verification.Summary)
	}
}

func TestAClusterBelowTheFloorIsNotPickedFrom(t *testing.T) {
	wanted, other := clusterOfTwo()
	copied := copyHeardAsBoth()
	copied.RecordingID = uuid.MustParse(firstID)
	copied.Acoustic = heard(cluster(acousticFloor-0.01, firstID, secondID))

	verification := verifyAgainstClusterOfTwo(t, copied, wanted, other)
	if verification.Audio {
		t.Fatalf("admitted by audio below the likeness floor: %q", verification.Summary)
	}
}

func TestAClusterTiedWithAnotherIsNotPickedFrom(t *testing.T) {
	wanted, other := clusterOfTwo()
	copied := copyHeardAsBoth()
	copied.RecordingID = uuid.MustParse(firstID)
	copied.Acoustic = heard(cluster(0.96, firstID, secondID), cluster(0.96, secondID))

	verification := verifyAgainstClusterOfTwo(t, copied, wanted, other)
	if verification.Audio {
		t.Fatalf("admitted by audio from a tied cluster: %q", verification.Summary)
	}
}
