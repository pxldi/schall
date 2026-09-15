package tagging

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// How long an AIFF is, read off COMM.
//
// AIFF is chunked like WAV but big-endian, and it is the better of the two
// here: instead of a byte count to be divided by a byte rate, COMM states the
// number of sample frames outright. That is a count of moments rather than of
// bytes, so it stays exact for AIFF-C as well, where the samples are compressed
// and a byte rate would mean nothing.
//
// The trap is the sample rate, which Apple wrote as an 80-bit IEEE extended
// float: one sign bit, fifteen of exponent, and sixty-four of mantissa with its
// leading bit written out rather than implied. No language has the type and no
// library here decodes it, so it is decoded below by hand. Getting the bias
// wrong by one puts every AIFF in the library out by a factor of two, which is
// the kind of error that looks like a different recording.
func readAIFFDuration(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	header := make([]byte, 12)
	if _, err := io.ReadFull(file, header); err != nil {
		if ranOut(err) {
			return noDuration(path)
		}
		return 0, fmt.Errorf("read the header of %s: %w", path, err)
	}
	// AIFC is the compressed variant and carries the same COMM fields ahead of
	// the compression it adds.
	form := string(header[8:12])
	if string(header[:4]) != "FORM" || (form != "AIFF" && form != "AIFC") {
		return noDuration(path)
	}

	var (
		frames float64
		rate   float64
	)
	err = eachAIFFChunk(file, 12, func(kind string, at, size int64) error {
		if kind != "COMM" || size < 18 {
			return nil
		}
		// Two bytes of channel count, four of frame count, two of sample size,
		// then the ten the rate is written in.
		body := make([]byte, 18)
		if _, err := file.ReadAt(body, at); err != nil && !ranOut(err) {
			return err
		}
		frames = float64(binary.BigEndian.Uint32(body[2:6]))
		rate = extended80(body[8:18])
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk the chunks of %s: %w", path, err)
	}
	if frames <= 0 || rate <= 0 {
		return noDuration(path)
	}
	return int(frames*1000/rate + 0.5), nil
}

// extended80 decodes the 80-bit IEEE extended float AIFF states its sample rate
// in.
//
// The exponent is biased by 16383 and the mantissa is a plain 64-bit integer
// whose top bit is the one an ordinary float would have left implied. So the
// value is that integer scaled down by the 63 bits below its top one and up by
// the exponent: an exponent of 16414 and a mantissa with only its top bit set
// is 44100, and there is no rounding anywhere in getting there.
func extended80(raw []byte) float64 {
	exponent := int(raw[0]&0x7f)<<8 | int(raw[1])
	mantissa := binary.BigEndian.Uint64(raw[2:10])
	// All-ones exponent is an infinity or a not-a-number, and all-zero with an
	// empty mantissa is zero. Neither is a sample rate.
	if exponent == 0 || exponent == 0x7fff || mantissa == 0 {
		return 0
	}
	value := float64(mantissa) * math.Exp2(float64(exponent-16383-63))
	if raw[0]&0x80 != 0 {
		return -value
	}
	return value
}

// eachAIFFChunk is the RIFF walk with the bytes the other way round. Same cap,
// same reason, same refusal to read a chunk body it was not asked for.
func eachAIFFChunk(file *os.File, from int64, visit func(kind string, at, size int64) error) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	limit := info.Size()

	header := make([]byte, 8)
	at := from
	for chunks := 0; chunks < riffChunkLimit && at+8 <= limit; chunks++ {
		if _, err := file.ReadAt(header, at); err != nil {
			if ranOut(err) {
				return nil
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[4:8]))
		content := at + 8
		if content+size > limit {
			size = limit - content
		}
		if err := visit(string(header[:4]), content, size); err != nil {
			return err
		}
		at = content + size + size&1
	}
	return nil
}
