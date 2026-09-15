package acquisition

import (
	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
)

// CopyGroup is the copies of one want that are the same piece of audio.
//
// A want is one recording somebody asked for. A copy is one file a peer sent for
// it, and a want commonly holds many copies that nothing could decide about: one
// want in the queue on 2026-08-16 held eleven, which were five copies of one
// master and six of another, and nothing in the file names said so. Grouping
// them is what turns eleven rows a person has to read into two.
//
// This is presentation and never proof. A group says these files sound alike; it
// says nothing about which recording any of them is, and no copy is admitted,
// refused or reordered because of the group it landed in.
type CopyGroup struct {
	// CopyIDs are the copies in this group, in the order they were handed in.
	CopyIDs []uuid.UUID
	// Rips is how many separate rips the group holds. Copies of identical size
	// are one rip that spread rather than several people agreeing, so they count
	// once: five files that are byte-for-byte one upload are one piece of
	// evidence, not five.
	//
	// Size is the whole of the test today. The encoder header would be the other
	// half of it and is not stored for a copy yet — that arrives with ROADMAP
	// 10d — so two different rips that happen to weigh the same are counted as
	// one. It undercounts and never overcounts, which is the safe direction for a
	// number that only ever says how much agreement to read into a group.
	Rips int
}

// GroupCopies gathers the copies that are one piece of audio.
//
// Each copy carries the fingerprint computed from its own decoded audio. Two are
// the same audio when the share of bits between them is below the threshold
// already measured for this comparison, chromaprint.AdmitBelow — the same number
// the published-sample rule reads and not a new one invented here.
//
// A copy that cannot be compared is its own group, and that is deliberate: no
// fingerprint, an unreadable one, or audio too short to hold another copy is
// silence, and silence never joins a copy to anything. Being the odd copy out
// can only cost a copy a row of its own. It can never let one in.
//
// The order is the order the copies arrived in, and a copy joins the first group
// it matches. Two copies in one group are therefore each within the threshold of
// the copy that opened it rather than of one another, which is exactly the claim
// the interface makes: the group is a way of reading a list, not a proof about
// its members.
func GroupCopies(rows []db.AcquisitionTargetFileRow) []CopyGroup {
	type group struct {
		frames []uint32
		copies []db.AcquisitionTargetFileRow
	}
	var groups []*group

	for _, row := range rows {
		var frames []uint32
		if row.Evidence != nil && row.Evidence.AudioFingerprint != "" {
			decoded, err := chromaprint.Decode(row.Evidence.AudioFingerprint)
			if err == nil {
				frames = decoded
			}
		}
		joined := false
		if frames != nil {
			for _, existing := range groups {
				if existing.frames == nil || !sameAudio(existing.frames, frames) {
					continue
				}
				existing.copies = append(existing.copies, row)
				joined = true
				break
			}
		}
		if !joined {
			groups = append(groups, &group{frames: frames, copies: []db.AcquisitionTargetFileRow{row}})
		}
	}

	result := make([]CopyGroup, 0, len(groups))
	for _, found := range groups {
		ids := make([]uuid.UUID, 0, len(found.copies))
		for _, row := range found.copies {
			ids = append(ids, row.ID)
		}
		result = append(result, CopyGroup{CopyIDs: ids, Rips: ripsIn(found.copies)})
	}
	return result
}

// sameAudio reports whether two fingerprints are of the same piece of audio.
//
// The shorter side is the reference, because the measurement slides one across
// the other and reads only where it sits wholly inside: the longer side has to
// be able to contain the shorter. A comparison that cannot run at all — a
// fingerprint of nothing, one side too short to hold the other — answers no,
// which leaves both copies where they were.
func sameAudio(left, right []uint32) bool {
	reference, candidate := left, right
	if len(candidate) < len(reference) {
		reference, candidate = candidate, reference
	}
	rate, err := chromaprint.BitErrorRate(reference, candidate)
	if err != nil {
		return false
	}
	return rate < chromaprint.AdmitBelow
}

// ripsIn counts the separate rips in a group. Copies of identical size are one
// upload that spread and count once; a copy whose size nobody recorded is
// counted on its own, because an unknown size is not evidence of sameness.
func ripsIn(copies []db.AcquisitionTargetFileRow) int {
	rips, seen := 0, make(map[int64]bool, len(copies))
	for _, row := range copies {
		if !row.SizeBytes.Valid || row.SizeBytes.Int64 <= 0 {
			rips++
			continue
		}
		if seen[row.SizeBytes.Int64] {
			continue
		}
		seen[row.SizeBytes.Int64] = true
		rips++
	}
	return rips
}
