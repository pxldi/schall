package tagging

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// How long an MP3 is, counted rather than claimed.
//
// Every other container Schall holds says its own length exactly. FLAC's
// STREAMINFO holds a sample count, WAV a data size against a byte rate, AIFF a
// frame count in COMM, MP4 a duration against a timescale, and Ogg the granule
// position of its last page. A reader that opens those files gets the truth for
// nothing.
//
// MP3 has no such field. The format is a bare sequence of frames, so the only
// honest length is the sum of the frames, and a decoder that wants one without
// reading them all has to guess: bitrate times file size, which is exact for a
// constant rate and wrong for a variable one. Encoders papered over this by
// writing a Xing or VBRI frame at the front carrying the count -- but it is
// optional, plenty of files predate it or were cut by tools that dropped it,
// and being a claim rather than a measurement it can be wrong even when it is
// there. taglib reads it and is therefore exactly as right as the file is
// honest.
//
// So MP3 is walked. Not decoded -- no audio is turned into samples -- but read
// frame header to frame header, each one saying how long its own frame is, from
// the start of the audio to the end of the file. It costs one sequential pass
// and it cannot be wrong about a file it managed to read.

// mpegVersion is which of the three MPEG audio generations a frame belongs to.
// They differ in sample rate, in bitrate table, and in how many samples a Layer
// III frame carries, which is why the version has to be read before anything
// else can be worked out.
type mpegVersion int

// The order is the header's own, which is not the order the versions were
// invented in: 2.5 came last and was given the spare value 0.
const (
	mpeg25 mpegVersion = iota
	mpegReserved
	mpeg2
	mpeg1
)

// The bitrate tables, in kbit/s, indexed by the four bits the header carries.
// Index 0 is the "free" format -- a stream whose rate is agreed out of band --
// and index 15 is invalid. Neither can be turned into a frame length, so both
// end the walk rather than being guessed at.
var (
	bitratesV1L1 = [16]int{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}
	bitratesV1L2 = [16]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0}
	bitratesV1L3 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	bitratesV2L1 = [16]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0}
	bitratesV2L2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
)

var sampleRates = map[mpegVersion][3]int{
	mpeg1:  {44100, 48000, 32000},
	mpeg2:  {22050, 24000, 16000},
	mpeg25: {11025, 12000, 8000},
}

// frame is one MPEG audio frame's header, read for the two things the walk
// needs from it: how many bytes to step over, and how much time it holds.
type frame struct {
	sizeBytes  int
	samples    int
	sampleRate int
	// mono decides where a Xing tag would sit inside the frame, because the
	// side information it follows is half the length for one channel.
	mono bool
	// mpeg1 has the same job for the same reason.
	mpeg1 bool
}

// carriesVBRTag reports whether this frame is the header frame an encoder
// writes at the front of a variable-rate file rather than a frame of audio.
//
// It has to be recognised because it holds no music and every decoder skips it.
// Counting it would make a file measure a frame longer than the same audio
// encoded without one, and the whole reason for walking is that two encodings
// of one recording should agree about how long it is.
//
// Note what this is not: the tag's own claimed frame count is right here and is
// still not read. It is a claim, it is the claim taglib already believes, and
// believing it is what this function exists to stop.
func (header frame) carriesVBRTag(body []byte) bool {
	sideInfo := 32
	switch {
	case header.mpeg1 && header.mono:
		sideInfo = 17
	case !header.mpeg1 && header.mono:
		sideInfo = 9
	case !header.mpeg1:
		sideInfo = 17
	}
	if len(body) >= sideInfo+4 {
		switch string(body[sideInfo : sideInfo+4]) {
		case "Xing", "Info":
			return true
		}
	}
	// VBRI sits at a fixed offset instead of after the side information.
	const vbriOffset = 32
	return len(body) >= vbriOffset+4 && string(body[vbriOffset:vbriOffset+4]) == "VBRI"
}

// countMPEGFrames walks the frames of an MP3 and returns how long they add up
// to, in milliseconds.
//
// It reads the whole file once, sequentially, and decodes nothing. A file it
// cannot make sense of answers ErrNoDuration rather than a number: a length
// nobody counted is silence here, exactly as it is everywhere else, and silence
// has never been an agreement.
func countMPEGFrames(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 64*1024)
	if err := skipID3v2(reader); err != nil {
		return 0, fmt.Errorf("read the start of %s: %w", path, err)
	}

	// Time is accumulated rather than samples, because a file that turned out
	// to be two files concatenated can change sample rate halfway and a single
	// division at the end would then be wrong about both halves.
	var (
		milliseconds float64
		counted      int
		inspected    bool
	)
	for {
		header, err := nextFrame(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("walk the frames of %s: %w", path, err)
		}
		if header == nil {
			// A frame the header rules out -- a free-format or invalid rate --
			// ends the walk. What has been counted so far is a prefix of the
			// file rather than its length, and a prefix is not an answer.
			return noDuration(path)
		}
		// Only the first frame is inspected for a header tag. A later frame
		// holding those four bytes at that offset is audio that happens to
		// spell them, and skipping it would lose real time.
		if !inspected {
			inspected = true
			if body, err := reader.Peek(header.sizeBytes - 4); err == nil && header.carriesVBRTag(body) {
				if _, err := reader.Discard(header.sizeBytes - 4); err != nil {
					break
				}
				continue
			}
		}
		milliseconds += float64(header.samples) * 1000 / float64(header.sampleRate)
		counted++
		if _, err := reader.Discard(header.sizeBytes - 4); err != nil {
			// The last frame of a file is routinely short of its own declared
			// length, because encoders pad and taggers append. It was still a
			// frame and its time still counts.
			break
		}
	}
	if counted == 0 {
		return noDuration(path)
	}
	return int(milliseconds + 0.5), nil
}

// skipID3v2 steps over a tag at the front of the file, so that the walk starts
// at audio rather than at somebody's cover art. The size is stored seven bits
// to the byte, so that a tag can never contain a byte sequence a decoder would
// mistake for a frame sync.
func skipID3v2(reader *bufio.Reader) error {
	header, err := reader.Peek(10)
	if err != nil || string(header[:3]) != "ID3" {
		// No tag, a file too short to hold one, or an unreadable start. The
		// walk is the thing that decides whether this is audio, not this.
		return nil
	}
	size := int(header[6]&0x7f)<<21 | int(header[7]&0x7f)<<14 |
		int(header[8]&0x7f)<<7 | int(header[9]&0x7f)
	// Bit 4 of the flags says a footer is present, which is ten further bytes
	// the size does not include.
	if header[5]&0x10 != 0 {
		size += 10
	}
	_, err = reader.Discard(10 + size)
	return err
}

// nextFrame finds the next frame header and reads it.
//
// It resyncs rather than giving up: a stray tag, a block of junk between
// frames, or an APE tag at the end will all break the chain, and a file that
// plays fine in every player is not one Schall should refuse to measure. A nil
// frame with no error means a header was found and its own contents rule it
// out, which is different from not finding one at all.
func nextFrame(reader *bufio.Reader) (*frame, error) {
	// Bounded so that a file of noise ends rather than being scanned to its
	// last byte in one-byte steps for every frame it does not have.
	const resyncLimit = 1 << 16

	for skipped := 0; skipped < resyncLimit; skipped++ {
		header, err := reader.Peek(4)
		if err != nil {
			return nil, io.EOF
		}
		if header[0] == 0xff && header[1]&0xe0 == 0xe0 {
			if parsed := parseFrame(binary.BigEndian.Uint32(header)); parsed != nil {
				if _, err := reader.Discard(4); err != nil {
					return nil, io.EOF
				}
				return parsed, nil
			}
		}
		if _, err := reader.Discard(1); err != nil {
			return nil, io.EOF
		}
	}
	return nil, io.EOF
}

// parseFrame turns a four-byte header into the two numbers the walk needs, or
// nil if the header is not one. Every reserved combination is a nil: a byte
// pair that happens to look like a sync is common in tag data and in album art,
// and reading one as a frame is how a walk ends up measuring a picture.
func parseFrame(header uint32) *frame {
	version := mpegVersion(header >> 19 & 0x3)
	layer := int(header >> 17 & 0x3)
	bitrateIndex := int(header >> 12 & 0xf)
	rateIndex := int(header >> 10 & 0x3)

	if version == mpegReserved || layer == 0 || rateIndex == 3 ||
		bitrateIndex == 0 || bitrateIndex == 15 {
		return nil
	}
	sampleRate := sampleRates[version][rateIndex]

	// The layer bits count downwards: 3 is Layer I, 1 is Layer III.
	var (
		bitrate int
		samples int
	)
	switch {
	case layer == 3 && version == mpeg1:
		bitrate, samples = bitratesV1L1[bitrateIndex], 384
	case layer == 3:
		bitrate, samples = bitratesV2L1[bitrateIndex], 384
	case layer == 2 && version == mpeg1:
		bitrate, samples = bitratesV1L2[bitrateIndex], 1152
	case layer == 2:
		bitrate, samples = bitratesV2L2[bitrateIndex], 1152
	case version == mpeg1:
		bitrate, samples = bitratesV1L3[bitrateIndex], 1152
	default:
		// Layer III at MPEG 2 or 2.5 halves the frame, which is the one place
		// the sample count cannot be read off the layer alone.
		bitrate, samples = bitratesV2L2[bitrateIndex], 576
	}
	if bitrate == 0 {
		return nil
	}

	padding := int(header >> 9 & 0x1)
	var size int
	if layer == 3 {
		size = (12*bitrate*1000/sampleRate + padding) * 4
	} else {
		size = samples/8*bitrate*1000/sampleRate + padding
	}
	if size <= 4 {
		return nil
	}
	return &frame{
		sizeBytes:  size,
		samples:    samples,
		sampleRate: sampleRate,
		mono:       header>>6&0x3 == 3,
		mpeg1:      version == mpeg1,
	}
}
