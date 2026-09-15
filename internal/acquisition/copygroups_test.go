package acquisition

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
)

// The two fingerprints below are real. Each is one piece of audio fingerprinted
// by fpcalc 1.6.1 in the compressed form Schall stores, and they are the same
// two internal/chromaprint's own tests are built on, so what these tests read is
// what the tool printed rather than what this code believes it should have.

// Twelve seconds of audio.
const audioA = "AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wdaD_y4XikD69yNJeCHzZqFX-E6QT1oxf24GmMM3rwOMWR7PBzpLeC3DmEI0TRPMGDI0eyP_ABny6u4_gvHGGO59ARUzmFXsdbotYS5M0J_Xh2RPqLM4J_MCuPPAr6EIrPCLp4lDy-4ccvnMdUpYaeSEg1sWAkAMMAIgQwAhhBVAoLhjBOIAOBAkw4I4ihBAEECDUOMQWUAM4EIBEWAhEhAQAMMUGMA8oRQAgzAhIDGUQA"

// Eight seconds of different audio, starting three seconds in.
const audioB = "AQAAK0mi5EuyJEEW8gd1wsd5eIlOnOmP_njRB86XB9OFC0KZXvAqPKnwdoaTcoEjMsKz482F4zwuTbjooC-e40GoQ9SRcsePP8MH__hiHB81-HB-_Ec3AIKAEA4wAQAhQgADkKCMCaRAA0ZZpqQkghJgHDIOOWUUIwQ"

// copy is one held file, as the queue reads it: an identifier, a size, and what
// its audio sounds like.
func copyOf(fingerprint string, size int64) db.AcquisitionTargetFileRow {
	row := db.AcquisitionTargetFileRow{ID: uuid.New(), Verdict: db.AcquiredFileHeld}
	if size > 0 {
		row.SizeBytes = pgtype.Int8{Int64: size, Valid: true}
	}
	if fingerprint != "" {
		row.Evidence = &db.AcquiredFileEvidence{AudioFingerprint: fingerprint}
	}
	return row
}

// The point of the whole thing: copies that are one piece of audio are one
// question. Two peers sent the same master under different names and different
// sizes, and a person reads one row rather than two.
func TestCopiesOfOneMasterAreOneGroup(t *testing.T) {
	first, second := copyOf(audioA, 9218048), copyOf(audioA, 39845888)

	groups := GroupCopies([]db.AcquisitionTargetFileRow{first, second})

	if len(groups) != 1 {
		t.Fatalf("groups = %d, want the two copies of one master read as one", len(groups))
	}
	if len(groups[0].CopyIDs) != 2 {
		t.Errorf("copies = %v, want both", groups[0].CopyIDs)
	}
	if groups[0].Rips != 2 {
		t.Errorf("rips = %d, want two sizes counted as two rips", groups[0].Rips)
	}
}

// Two masters of one recording are two questions. Both may be correct — the
// calibration behind the thresholds found exactly that — so nothing here decides
// between them; they are simply not shown as one row.
func TestDifferentAudioStaysTwoGroups(t *testing.T) {
	groups := GroupCopies([]db.AcquisitionTargetFileRow{
		copyOf(audioA, 9218048), copyOf(audioB, 5120000),
	})

	if len(groups) != 2 {
		t.Fatalf("groups = %d, want audio that is not the same audio kept apart", len(groups))
	}
}

// One upload that spread. Five files of identical size are not five people
// agreeing, so the group says one rip however many copies of it arrived.
func TestOneRipThatSpreadIsCountedOnce(t *testing.T) {
	groups := GroupCopies([]db.AcquisitionTargetFileRow{
		copyOf(audioA, 39845888), copyOf(audioA, 39845888), copyOf(audioA, 39845888),
	})

	if len(groups) != 1 {
		t.Fatalf("groups = %d, want one", len(groups))
	}
	if len(groups[0].CopyIDs) != 3 {
		t.Errorf("copies = %v, want all three kept in the group", groups[0].CopyIDs)
	}
	if groups[0].Rips != 1 {
		t.Errorf("rips = %d, want one upload counted once", groups[0].Rips)
	}
}

// A copy nobody could measure. No fingerprint, an unreadable one, and audio too
// short to hold the copy it is compared against are all silence, and silence
// joins a copy to nothing. Being the odd one out costs it a row of its own and
// can never let it in.
func TestACopyThatCannotBeComparedStandsAlone(t *testing.T) {
	for name, odd := range map[string]db.AcquisitionTargetFileRow{
		"no fingerprint":        copyOf("", 1024),
		"not a fingerprint":     copyOf("this is not a fingerprint", 1024),
		"no evidence at all":    {ID: uuid.New(), Verdict: db.AcquiredFileHeld},
		"nothing to compare to": copyOf(audioB, 0),
	} {
		t.Run(name, func(t *testing.T) {
			rows := []db.AcquisitionTargetFileRow{copyOf(audioA, 1), copyOf(audioA, 2), odd}

			groups := GroupCopies(rows)

			if len(groups) != 2 {
				t.Fatalf("groups = %d, want the two alike copies together and the odd one alone",
					len(groups))
			}
			if len(groups[1].CopyIDs) != 1 || groups[1].CopyIDs[0] != odd.ID {
				t.Errorf("second group = %v, want only the copy nothing could compare",
					groups[1].CopyIDs)
			}
		})
	}
}

// Every copy is in exactly one group, whatever it is. A copy that fell out of
// the answer would be a file a person never sees, which is worse than a row too
// many.
func TestEveryCopyIsInExactlyOneGroup(t *testing.T) {
	rows := []db.AcquisitionTargetFileRow{
		copyOf(audioA, 10), copyOf(audioB, 20), copyOf(audioA, 10), copyOf("", 30),
	}

	groups := GroupCopies(rows)

	seen := make(map[uuid.UUID]int)
	for _, group := range groups {
		for _, id := range group.CopyIDs {
			seen[id]++
		}
	}
	if len(seen) != len(rows) {
		t.Fatalf("copies placed = %d, want %d", len(seen), len(rows))
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("copy %s is in %d groups, want one", id, count)
		}
	}
}

// Nothing to group. An empty list is an empty answer rather than a group of
// nothing, because a group with no copies in it would draw a row with no file.
func TestNoCopiesAreNoGroups(t *testing.T) {
	if groups := GroupCopies(nil); len(groups) != 0 {
		t.Errorf("groups = %v, want none", groups)
	}
}
