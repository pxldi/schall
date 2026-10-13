// Package accuraterip checks a copy against the checksums people who ripped a
// CD submitted to AccurateRip.
//
// AccurateRip is a database of ripped CD tracks. Every program that rips a CD
// with it computes a checksum of each track's audio and submits it under the
// disc's table of contents: where each track starts, to the sector. A copy
// whose audio sums to a checksum submitted for track N of a disc is that
// track, bit for bit, because nothing but the same samples in the same order
// reaches the same sum over millions of samples.
//
// The table of contents comes from MusicBrainz, which keeps the ones people
// read off their discs on the medium they belong to. That is what makes the
// checksum say something about a recording: MusicBrainz says which recording
// is track N of that medium, AccurateRip says the copy is track N of a disc
// with that table of contents.
//
// Nothing here is a similarity. A checksum is equal or it is not, and an
// unequal one is silence: a rip with its drive's offset left uncorrected, a
// different mastering and a copy bought as a download all sum differently and
// are still the recording. Only an equal sum is evidence.
package accuraterip

import "fmt"

// leadIn is the two seconds of sectors before the first track that MusicBrainz
// counts in every offset and AccurateRip does not.
const leadIn = 150

// FramesPerSector is how many stereo sample frames one CD sector holds.
const FramesPerSector = 588

// TOC is a disc's table of contents as MusicBrainz keeps it: the sector each
// audio track starts at and the sector the lead-out starts at, both counted
// from the start of the disc including the two-second lead-in.
type TOC struct {
	Offsets []int
	Sectors int
}

// Valid reports whether the offsets rise and end before the lead-out. A table
// that does not cannot have been read off a disc.
func (toc TOC) Valid() bool {
	if len(toc.Offsets) == 0 || toc.Offsets[0] < leadIn {
		return false
	}
	for index := 1; index < len(toc.Offsets); index++ {
		if toc.Offsets[index] <= toc.Offsets[index-1] {
			return false
		}
	}
	return toc.Sectors > toc.Offsets[len(toc.Offsets)-1]
}

// Frames is how many stereo sample frames track number track holds on this
// disc, counted from one. A rip of the track holds exactly this many: a ripper
// writes each track from its own start to the next one's, with nothing added.
func (toc TOC) Frames(track int) int {
	if track < 1 || track > len(toc.Offsets) {
		return 0
	}
	end := toc.Sectors
	if track < len(toc.Offsets) {
		end = toc.Offsets[track]
	}
	return (end - toc.Offsets[track-1]) * FramesPerSector
}

// IDs are the three numbers AccurateRip files a disc under.
type IDs struct {
	Tracks int
	ID1    uint32
	ID2    uint32
	CDDB   uint32
}

// IDs computes the numbers AccurateRip files this disc under, the way every
// ripper that submits to it does.
func (toc TOC) IDs() IDs {
	var id1, id2 uint32
	for index, offset := range toc.Offsets {
		sector := uint32(offset - leadIn)
		id1 += sector
		id2 += max(sector, 1) * uint32(index+1)
	}
	leadOut := uint32(toc.Sectors - leadIn)
	id1 += leadOut
	id2 += max(leadOut, 1) * uint32(len(toc.Offsets)+1)

	digits := 0
	for _, offset := range toc.Offsets {
		for seconds := offset / 75; seconds > 0; seconds /= 10 {
			digits += seconds % 10
		}
	}
	length := toc.Sectors/75 - toc.Offsets[0]/75
	cddb := uint32(digits%0xff)<<24 | uint32(length)<<8 | uint32(len(toc.Offsets))
	return IDs{Tracks: len(toc.Offsets), ID1: id1, ID2: id2, CDDB: cddb}
}

// path is where AccurateRip keeps the checksums for a disc with these numbers.
func (ids IDs) path() string {
	hex := fmt.Sprintf("%08x", ids.ID1)
	return fmt.Sprintf("/accuraterip/%c/%c/%c/dBAR-%03d-%08x-%08x-%08x.bin",
		hex[7], hex[6], hex[5], ids.Tracks, ids.ID1, ids.ID2, ids.CDDB)
}
