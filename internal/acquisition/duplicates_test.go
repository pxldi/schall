package acquisition

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

// heldCopy is one held file with a name, a bit rate and an arrival time.
func keptOrNot(fingerprint, name string, bitRate int, arrived time.Time) db.AcquisitionTargetFileRow {
	row := copyOf(fingerprint, 0)
	row.FileName = name
	row.DecidedAt = arrived
	if row.Evidence != nil {
		row.Evidence.BitRate = bitRate
	}
	return row
}

var arrival = time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)

func TestALosslessCopyIsKeptOverALossyOneOfTheSameAudio(t *testing.T) {
	lossy := keptOrNot(audioA, "a.mp3", 320, arrival)
	lossless := keptOrNot(audioA, "a.flac", 0, arrival.Add(time.Minute))

	got := Duplicates([]db.AcquisitionTargetFileRow{lossy, lossless})

	if !slices.Equal(got, []uuid.UUID{lossy.ID}) {
		t.Fatalf("duplicates = %v, want only the mp3 %v", got, lossy.ID)
	}
}

func TestTheHigherBitRateIsKeptAmongLossyCopiesOfTheSameAudio(t *testing.T) {
	low := keptOrNot(audioA, "a.mp3", 128, arrival)
	high := keptOrNot(audioA, "b.mp3", 320, arrival.Add(time.Minute))

	got := Duplicates([]db.AcquisitionTargetFileRow{low, high})

	if !slices.Equal(got, []uuid.UUID{low.ID}) {
		t.Fatalf("duplicates = %v, want only the 128 kbps copy %v", got, low.ID)
	}
}

func TestTheFirstArrivalIsKeptAmongEqualCopies(t *testing.T) {
	first := keptOrNot(audioA, "a.mp3", 320, arrival)
	second := keptOrNot(audioA, "b.mp3", 320, arrival.Add(time.Minute))

	got := Duplicates([]db.AcquisitionTargetFileRow{first, second})

	if !slices.Equal(got, []uuid.UUID{second.ID}) {
		t.Fatalf("duplicates = %v, want only the later copy %v", got, second.ID)
	}
}

// Two masters can both be the recording, so each keeps a copy to ask about.
func TestDifferentAudioIsNeverADuplicate(t *testing.T) {
	got := Duplicates([]db.AcquisitionTargetFileRow{
		keptOrNot(audioA, "a.mp3", 320, arrival),
		keptOrNot(audioB, "b.mp3", 128, arrival.Add(time.Minute)),
	})

	if len(got) != 0 {
		t.Fatalf("duplicates = %v, want none", got)
	}
}

// A copy with no fingerprint cannot be shown to sound like anything.
func TestACopyWithoutAFingerprintIsNeverADuplicate(t *testing.T) {
	got := Duplicates([]db.AcquisitionTargetFileRow{
		keptOrNot(audioA, "a.flac", 0, arrival),
		keptOrNot("", "b.mp3", 0, arrival.Add(time.Minute)),
		keptOrNot("", "c.mp3", 0, arrival.Add(2*time.Minute)),
	})

	if len(got) != 0 {
		t.Fatalf("duplicates = %v, want none", got)
	}
}
