package accuraterip

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// skippedFrames is how much of a disc's first and last track AccurateRip
// leaves out of the checksum: five sectors, which drives with different read
// offsets cannot all reach.
const skippedFrames = 5 * FramesPerSector

// Sums is one pass over a track's samples, kept in the parts the two edges of
// a disc leave out, so one decode answers for the track wherever it sits.
type Sums struct {
	// Frames is how many stereo sample frames the audio held.
	Frames int
	whole  sum
	head   sum
	tail   sum
}

// sum is the two running totals both checksum versions are built from: the
// low and high halves of every frame multiplied by its position.
type sum struct{ low, high uint32 }

func (total *sum) add(frame uint32, position uint64) {
	product := uint64(frame) * position
	total.low += uint32(product)
	total.high += uint32(product >> 32)
}

func (total sum) minus(other sum) sum {
	return sum{low: total.low - other.low, high: total.high - other.high}
}

// ErrNotCDAudio reports audio that cannot be a track ripped from a CD: an odd
// number of bytes, or too few frames to leave anything after the edges are
// skipped.
var ErrNotCDAudio = errors.New("the audio is not a CD track")

// Sum reads 16-bit little-endian stereo samples, as a CD holds them, and keeps
// what both checksum versions need.
func Sum(samples io.Reader) (Sums, error) {
	reader := bufio.NewReaderSize(samples, 1<<16)
	var sums Sums
	// The last five sectors are only known to be the last once the audio ends,
	// so they are kept until then and added to the tail afterwards.
	last := make([]uint32, skippedFrames)
	var frame [4]byte
	position := uint64(0)
	for {
		if _, err := io.ReadFull(reader, frame[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return Sums{}, ErrNotCDAudio
			}
			return Sums{}, err
		}
		position++
		value := binary.LittleEndian.Uint32(frame[:])
		sums.whole.add(value, position)
		if position < skippedFrames {
			sums.head.add(value, position)
		}
		last[(position-1)%skippedFrames] = value
	}
	if position <= 2*skippedFrames {
		return Sums{}, ErrNotCDAudio
	}
	sums.Frames = int(position)
	for at := position - skippedFrames + 1; at <= position; at++ {
		sums.tail.add(last[(at-1)%skippedFrames], at)
	}
	return sums, nil
}

// Checksums are the two versions of a track's AccurateRip checksum. The
// database does not say which version a submission used, so a match on
// either is a match.
type Checksums struct {
	V1 uint32
	V2 uint32
}

// Checksums is the checksum of the audio as the track it would be on a disc:
// the first track leaves out its opening five sectors but one frame, and the
// last leaves out its closing five.
func (sums Sums) Checksums(first, last bool) Checksums {
	total := sums.whole
	if first {
		total = total.minus(sums.head)
	}
	if last {
		total = total.minus(sums.tail)
	}
	return Checksums{V1: total.low, V2: total.low + total.high}
}

// Matches reports whether a submitted checksum is one of these.
func (checksums Checksums) Matches(submitted uint32) bool {
	return submitted == checksums.V1 || submitted == checksums.V2
}
