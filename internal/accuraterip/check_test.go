package accuraterip

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/pxldi/schall/internal/musicbrainz"
)

var (
	wanted  = uuid.MustParse("9f9cf187-d6f9-437f-9d98-d59cdbd52757")
	release = uuid.MustParse("c7569949-0f67-4682-a0d8-75c4290c52dc")
	discID  = "rdosqrxASN.ZqF.d.5pPhHtgVyA-"
)

// threeTracks is a three-track disc whose second track holds frameCount
// frames, rounded down to whole sectors.
func threeTracks(frameCount int) musicbrainz.Disc {
	sectors := frameCount / FramesPerSector
	return musicbrainz.Disc{
		ID: discID, Offsets: []int{150, 1150, 1150 + sectors}, Sectors: 1150 + sectors + 900,
	}
}

type fakeCatalogue struct {
	placements []musicbrainz.DiscPlacement
	recordings []uuid.UUID
}

func (catalogue *fakeCatalogue) RecordingDiscs(context.Context, uuid.UUID) ([]musicbrainz.DiscPlacement, error) {
	return catalogue.placements, nil
}

func (catalogue *fakeCatalogue) RecordingsAtDiscTrack(context.Context, string, int) ([]uuid.UUID, error) {
	return catalogue.recordings, nil
}

type fakeDatabase struct {
	pressings []Pressing
	err       error
	asked     int
}

func (database *fakeDatabase) Pressings(context.Context, IDs) ([]Pressing, error) {
	database.asked++
	return database.pressings, database.err
}

type fakeSummer struct{ audio []byte }

func (fakeSummer) Available() bool { return true }

func (summer fakeSummer) Sum(context.Context, string) (Sums, error) {
	return Sum(bytes.NewReader(summer.audio))
}

// copyOfTrackTwo is audio exactly as long as track two of threeTracks, and
// the checksum it reaches there.
func copyOfTrackTwo(t *testing.T) ([]byte, Checksums) {
	t.Helper()
	audio := frames(17 * FramesPerSector)
	sums, err := Sum(bytes.NewReader(audio))
	if err != nil {
		t.Fatalf("Sum() error = %v", err)
	}
	return audio, sums.Checksums(false, false)
}

func checker(audio []byte, checksum uint32, confidence int) (*Checker, *fakeCatalogue, *fakeDatabase) {
	catalogue := &fakeCatalogue{
		placements: []musicbrainz.DiscPlacement{{
			ReleaseID: release, Medium: 1, Track: 2, Discs: []musicbrainz.Disc{threeTracks(len(audio) / 4)},
		}},
		recordings: []uuid.UUID{wanted, wanted},
	}
	database := &fakeDatabase{pressings: []Pressing{{Tracks: []Submission{
		{Confidence: 9, Checksum: 1}, {Confidence: confidence, Checksum: checksum}, {Confidence: 9, Checksum: 3},
	}}}}
	return NewChecker(catalogue, database, fakeSummer{audio: audio}), catalogue, database
}

func TestACopyWithTheChecksumSubmittedForTheTrackIsThatTrack(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, _, _ := checker(audio, checksums.V2, 4)

	answer, err := check.Check(context.Background(), wanted, "copy.flac")
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	want := Match{ReleaseID: release, Medium: 1, Track: 2, DiscID: discID, Confidence: 4}
	if answer.Match == nil || *answer.Match != want {
		t.Fatalf("answer = %+v, want %+v", answer, want)
	}
}

func TestACopyWithAnotherChecksumIsNotMatched(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, _, _ := checker(audio, checksums.V1+1, 4)

	answer, err := check.Check(context.Background(), wanted, "copy.flac")
	if err != nil || answer.Match != nil || answer.Silence != noChecksum {
		t.Fatalf("answer = %+v, %v; want silence %q", answer, err, noChecksum)
	}
}

func TestAChecksumNobodyStandsBehindIsNotMatched(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, _, _ := checker(audio, checksums.V1, 0)

	answer, _ := check.Check(context.Background(), wanted, "copy.flac")
	if answer.Match != nil {
		t.Fatalf("answer = %+v, want no match", answer)
	}
}

func TestACopyOfAnotherLengthIsNotLookedUp(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, catalogue, database := checker(audio[:len(audio)-4], checksums.V1, 4)
	// The disc is built for the full copy, so the copy is a frame short of it.
	catalogue.placements[0].Discs = []musicbrainz.Disc{threeTracks(len(audio) / 4)}

	answer, err := check.Check(context.Background(), wanted, "copy.flac")
	if err != nil || answer.Match != nil || answer.Silence != noTrackOfThatLength {
		t.Fatalf("answer = %+v, %v; want silence %q", answer, err, noTrackOfThatLength)
	}
	if database.asked != 0 {
		t.Errorf("AccurateRip asked %d times, want 0", database.asked)
	}
}

func TestADiscWithTooFewTracksIsNotLookedUp(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, catalogue, database := checker(audio, checksums.V1, 4)
	disc := threeTracks(len(audio) / 4)
	disc.Offsets, disc.Sectors = disc.Offsets[:2], disc.Offsets[2]
	catalogue.placements[0].Discs = []musicbrainz.Disc{disc}

	answer, _ := check.Check(context.Background(), wanted, "copy.flac")
	if answer.Match != nil || database.asked != 0 {
		t.Fatalf("answer = %+v after %d lookups, want nothing asked", answer, database.asked)
	}
}

func TestAReleaseNamingAnotherRecordingOnTheDiscAdmitsNothing(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, catalogue, _ := checker(audio, checksums.V1, 4)
	catalogue.recordings = []uuid.UUID{wanted, uuid.New()}

	answer, err := check.Check(context.Background(), wanted, "copy.flac")
	if err != nil || answer.Match != nil || answer.Silence != otherRecordingOnTheDisc {
		t.Fatalf("answer = %+v, %v; want silence %q", answer, err, otherRecordingOnTheDisc)
	}
}

func TestAnAccurateRipThatCouldNotBeAskedIsAnError(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, _, database := checker(audio, checksums.V1, 4)
	database.err = errors.New("connection refused")

	if _, err := check.Check(context.Background(), wanted, "copy.flac"); err == nil {
		t.Fatal("Check() error = nil, want the failure")
	}
}

func TestARecordingOnNoCDWithATableIsSilence(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, catalogue, _ := checker(audio, checksums.V1, 4)
	catalogue.placements = nil

	answer, err := check.Check(context.Background(), wanted, "copy.flac")
	if err != nil || answer.Silence != noDiscs {
		t.Fatalf("answer = %+v, %v; want silence %q", answer, err, noDiscs)
	}
}

func TestACDNobodySubmittedIsToldApartFromAChecksumThatDiffers(t *testing.T) {
	audio, checksums := copyOfTrackTwo(t)
	check, _, database := checker(audio, checksums.V1, 4)
	database.err = ErrNotListed

	answer, err := check.Check(context.Background(), wanted, "copy.flac")
	if err != nil || answer.Match != nil || answer.Silence != notInAccurateRip {
		t.Fatalf("answer = %+v, %v; want silence %q", answer, err, notInAccurateRip)
	}
}
