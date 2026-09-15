package tagging

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

// How long an Ogg stream is, read off the granule position of its last page.
//
// Ogg does not carry a length in its header, because it was designed to be
// streamed by something that did not know one yet. What it carries instead is
// better: every page ends with a granule position, the position in the stream
// of the last sample that finished on that page. The final page's granule is
// therefore the sample count of the whole thing, written by the encoder after
// it had encoded all of it. Nothing needs decoding and nothing needs a pass
// over the file -- the first page says what the stream is, the last says how
// much of it there was.
//
// Two traps live here.
//
// The first is Opus. Its granule is always counted at 48 kHz whatever rate the
// audio was given at, and it starts not at zero but at pre-skip: the samples
// the decoder has to run through before its output is correct, which are in the
// file and are not part of the recording. OpusHead states that number and it
// has to come off, or every Opus file reads a few hundredths of a second long.
// A file of exactly one second of silence measures 1006 ms if you forget.
//
// The second is that a granule of -1 is not a position. It is a page on which
// no packet finished, which is legal and happens at the end of a file whose
// last packet is continued nowhere. That page is stepped over and the one
// before it asked instead.
func readOggGranule(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("size %s: %w", path, err)
	}

	serial, packet, err := firstOggPacket(file)
	if err != nil {
		if ranOut(err) {
			return noDuration(path)
		}
		return 0, fmt.Errorf("read the first page of %s: %w", path, err)
	}
	if packet == nil {
		return noDuration(path)
	}

	granule, found, err := lastOggGranule(file, info.Size(), serial)
	if err != nil {
		return 0, fmt.Errorf("read the last page of %s: %w", path, err)
	}
	// The granule is sixty-four bits out of the file, so a corrupted one can be
	// large enough that turning it into milliseconds would wrap. A number that
	// wrapped is not a length.
	if !found || granule <= 0 || granule > math.MaxInt64/1000 {
		return noDuration(path)
	}

	switch {
	case len(packet) >= 19 && string(packet[:8]) == "OpusHead":
		// Opus counts at 48 kHz regardless of what it was handed, so the rate
		// is not read from the file at all -- reading it would be the mistake.
		preSkip := int64(binary.LittleEndian.Uint16(packet[10:12]))
		if granule <= preSkip {
			return noDuration(path)
		}
		return int((granule - preSkip) * 1000 / 48000), nil

	case len(packet) >= 16 && packet[0] == 0x01 && string(packet[1:7]) == "vorbis":
		rate := int64(binary.LittleEndian.Uint32(packet[12:16]))
		if rate <= 0 {
			return noDuration(path)
		}
		return int(granule * 1000 / rate), nil
	}
	// FLAC, Speex or video in an Ogg wrapper. The granule is there but it is
	// counted in that codec's units, and a unit nobody read is not a length.
	return noDuration(path)
}

// An Ogg page is at most 27 bytes of header, 255 of segment table, and 255
// segments of 255 bytes: 65307 in all. Everything here is bounded by that, so
// no allocation ever depends on how big the file is.
const (
	oggHeaderBytes = 27
	oggPageMax     = 65307
	// The tail is searched a page at a time, and only a handful of pages back,
	// because the answer is on the last page or the one behind it. A file where
	// it is not is a file that will not say.
	oggScanSteps = 4
)

// firstOggPacket reads the identification header, which by construction is
// alone on the first page of the stream: the serial number the rest of the
// stream is keyed by, and the packet that says which codec wrote it.
func firstOggPacket(file *os.File) (uint32, []byte, error) {
	header := make([]byte, oggHeaderBytes)
	if _, err := io.ReadFull(file, header); err != nil {
		return 0, nil, err
	}
	if string(header[:4]) != "OggS" || header[4] != 0 {
		return 0, nil, nil
	}
	serial := binary.LittleEndian.Uint32(header[14:18])

	table := make([]byte, int(header[26]))
	if _, err := io.ReadFull(file, table); err != nil {
		return 0, nil, err
	}
	// A lace of 255 means the packet carries on into the next segment. The
	// first one under 255 ends it; a table that never gets there is a packet
	// spanning pages, which an identification header never does.
	length := 0
	for _, lace := range table {
		length += int(lace)
		if lace < 255 {
			packet := make([]byte, length)
			if _, err := io.ReadFull(file, packet); err != nil {
				return 0, nil, err
			}
			return serial, packet, nil
		}
	}
	return 0, nil, nil
}

// lastOggGranule scans backwards from the end of the file for the last page of
// this stream that finished a packet.
//
// Backwards, because the answer is at the end and the file may be long. The
// candidate is checked against more than its capture pattern -- the version
// byte, the flag bits, and above all the serial number -- because "OggS" is
// four bytes that audio data is entitled to spell, and a page invented out of
// packet contents would be a length invented out of nothing.
func lastOggGranule(file *os.File, size int64, serial uint32) (int64, bool, error) {
	buffer := make([]byte, oggPageMax+oggHeaderBytes)
	end := size
	for step := 0; step < oggScanSteps && end >= oggHeaderBytes; step++ {
		start := max(end-int64(len(buffer)), 0)
		window := buffer[:end-start]
		if _, err := file.ReadAt(window, start); err != nil && !ranOut(err) {
			return 0, false, err
		}
		for at := len(window) - oggHeaderBytes; at >= 0; at-- {
			if string(window[at:at+4]) != "OggS" || window[at+4] != 0 {
				continue
			}
			// Only continuation, beginning and end of stream are defined; a
			// page with any other bit set is not one.
			if window[at+5]&0xf8 != 0 {
				continue
			}
			if binary.LittleEndian.Uint32(window[at+14:at+18]) != serial {
				continue
			}
			// -1 as an unsigned field, which is the page that finished nothing.
			granule := int64(binary.LittleEndian.Uint64(window[at+6 : at+14]))
			if granule < 0 {
				continue
			}
			return granule, true, nil
		}
		if start == 0 {
			break
		}
		// Overlap by one header short of a page, so a header straddling the
		// boundary between two windows is seen whole by the second of them.
		end = start + oggHeaderBytes - 1
	}
	return 0, false, nil
}
