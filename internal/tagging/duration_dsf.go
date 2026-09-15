package tagging

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// How long a DSF is, read off its format chunk.
//
// DSF is three chunks in a fixed order -- `DSD `, `fmt `, `data` -- and the
// middle one states the sampling frequency and the number of samples per
// channel outright. One divided by the other is the length, and it is exact
// because DSD is one bit per sample with nothing between the samples: there is
// no frame, no packet and no header for a count to be approximate about.
//
// The numbers look wrong until you remember what DSD is. The lowest rate in use
// is 2822400 Hz, sixty-four times a CD's, and each of those samples is a single
// bit, so a second of mono audio is a third of a megabyte. A file that measures
// as a handful of milliseconds here is not a misread; it is a handful of
// milliseconds.
func readDSFDuration(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	// Twenty-eight bytes of `DSD ` chunk then fifty-two of `fmt `: a fixed
	// read, and the only read this makes.
	header := make([]byte, 80)
	if _, err := io.ReadFull(file, header); err != nil {
		if ranOut(err) {
			return noDuration(path)
		}
		return 0, fmt.Errorf("read the format chunk of %s: %w", path, err)
	}
	if string(header[:4]) != "DSD " || string(header[28:32]) != "fmt " {
		return noDuration(path)
	}
	// Everything in DSF is little-endian, unlike every other big-endian-ish
	// container here, because the format came out of a Windows toolchain.
	if binary.LittleEndian.Uint64(header[32:40]) != 52 {
		return noDuration(path)
	}

	rate := uint64(binary.LittleEndian.Uint32(header[56:60]))
	// The count is per channel, which is the count of moments in the audio
	// rather than of the bits it took to write them down.
	samples := binary.LittleEndian.Uint64(header[64:72])
	// The count is sixty-four bits wide and comes out of the file, so a
	// corrupted one can be large enough that turning it into milliseconds would
	// wrap. A number that wrapped is not a length.
	if rate == 0 || samples == 0 || samples > math.MaxInt64/1000 {
		return noDuration(path)
	}
	return int(samples * 1000 / rate), nil
}
