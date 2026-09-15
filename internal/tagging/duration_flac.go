package tagging

import (
	"fmt"
	"io"
	"os"
)

// How long a FLAC is, read off STREAMINFO.
//
// FLAC is the easy case and always was: the format requires STREAMINFO to be
// the first metadata block after the magic, and STREAMINFO carries the total
// number of samples in the stream against the rate they play at. That is a
// count of what is there, written by the encoder that put it there, so the
// division is exact rather than close.
//
// The one value that is not a number is zero. The spec spends it on "unknown",
// for an encoder streaming audio it has not finished reading, and a file that
// says unknown is refused rather than reported as an instant long.
func readFLACStreamInfo(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	// Four bytes of magic, four of block header, thirty-four of STREAMINFO --
	// a fixed read, whatever the file weighs.
	header := make([]byte, 42)
	if _, err := io.ReadFull(file, header); err != nil {
		if ranOut(err) {
			return noDuration(path)
		}
		return 0, fmt.Errorf("read the STREAMINFO of %s: %w", path, err)
	}
	if string(header[:4]) != "fLaC" {
		return noDuration(path)
	}
	// The low seven bits of the block header are its type, and STREAMINFO is
	// type 0. Anything else here is not a FLAC stream this can read.
	if header[4]&0x7f != 0 {
		return noDuration(path)
	}
	if int(header[5])<<16|int(header[6])<<8|int(header[7]) < 34 {
		return noDuration(path)
	}

	// The fields are packed against bit boundaries rather than byte ones: the
	// sample rate is twenty bits and the sample count thirty-six, which is why
	// byte 12 is shared between the rate and the channel count above it, and
	// byte 13 between the bit depth and the top of the count.
	streamInfo := header[8:42]
	sampleRate := int(streamInfo[10])<<12 | int(streamInfo[11])<<4 | int(streamInfo[12])>>4
	totalSamples := uint64(streamInfo[13]&0x0f)<<32 |
		uint64(streamInfo[14])<<24 | uint64(streamInfo[15])<<16 |
		uint64(streamInfo[16])<<8 | uint64(streamInfo[17])
	if sampleRate <= 0 || totalSamples == 0 {
		return noDuration(path)
	}
	return int(totalSamples * 1000 / uint64(sampleRate)), nil
}
