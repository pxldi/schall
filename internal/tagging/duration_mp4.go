package tagging

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// How long an MP4 is, read off the boxes that say so.
//
// MP4 is a tree of boxes, each one four bytes of length and four of name, and
// the length is what makes this cheap: a reader that wants `moov` reads eight
// bytes, jumps that far, and reads eight more. Nothing between is touched. The
// numbers themselves are a duration against a timescale, both written by the
// muxer once it knew the whole thing, so every division here is exact.
//
// Which box is asked is the entire difficulty, and it is the same difficulty
// Opus has. `mdhd` is the audio track's media duration and it includes the
// priming samples an AAC decoder has to run through before its output is
// correct -- audio that is in the file and is not part of the recording. So a
// one-second file measures 1.128 seconds if `mdhd` is believed, in the same way
// it measures 1.006 seconds if an Opus pre-skip is left on.
//
// What states the trim is the edit list. `elst` gives the presentation
// segments: how much of the media is actually played and for how long, in the
// movie's timescale, which is exactly the length with the priming taken off. It
// is asked first. A track with no edit list has nothing trimmed, and there
// `mdhd` is the whole truth. A file with no audio track this can walk to falls
// back to `mvhd`, which is a real number about the same file -- the length of
// its longest track -- rather than an inference about this one.
//
// The other trap is the version byte. Version 1 widened the timestamps to 64
// bits and moved the timescale twelve bytes further in; reading a version 1 box
// with version 0 offsets yields a number that is not nonsense-looking enough to
// notice. And the all-ones duration is the reserved value for unknown, written
// by a muxer that was still writing. That is a refusal, not four billion
// timescale units.
func readMP4Duration(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("size %s: %w", path, err)
	}

	milliseconds, ok, err := mp4Duration(file, info.Size())
	if err != nil {
		return 0, fmt.Errorf("walk the boxes of %s: %w", path, err)
	}
	if !ok {
		return noDuration(path)
	}
	return milliseconds, nil
}

func mp4Duration(file *os.File, size int64) (int, bool, error) {
	movie, ok, err := findBox(file, 0, size, "moov")
	if err != nil || !ok {
		return 0, false, err
	}
	header, hasHeader, err := findBox(file, movie.at, movie.end, "mvhd")
	if err != nil {
		return 0, false, err
	}
	var movieTimescale, movieDuration uint64
	if hasHeader {
		movieTimescale, movieDuration, _, err = headerBoxFields(file, header)
		if err != nil {
			return 0, false, err
		}
	}

	track, hasTrack, err := audioTrack(file, movie)
	if err != nil {
		return 0, false, err
	}
	if hasTrack {
		if played, ok, err := presentedDuration(file, track, movieTimescale); err != nil {
			return 0, false, err
		} else if ok {
			return played, true, nil
		}
		media, hasMedia, err := findBox(file, track.at, track.end, "mdia")
		if err != nil {
			return 0, false, err
		}
		if hasMedia {
			if box, ok, err := findBox(file, media.at, media.end, "mdhd"); err != nil {
				return 0, false, err
			} else if ok {
				timescale, duration, ok, err := headerBoxFields(file, box)
				if err != nil {
					return 0, false, err
				}
				if ok {
					return milliseconds(duration, timescale)
				}
			}
		}
	}
	if hasHeader && movieDuration > 0 {
		return milliseconds(movieDuration, movieTimescale)
	}
	return 0, false, nil
}

// extent is where a box's contents begin and end. Boxes are addressed rather
// than read: nothing in this file loads a box body it did not need.
type extent struct {
	at  int64
	end int64
}

// A malformed tree must end rather than be followed, so the number of boxes
// read at one level is capped. The limit is not reachable by a file a muxer
// wrote.
const mp4BoxLimit = 4096

// findBox walks one level of the tree and returns the extent of the first box
// with this name.
func findBox(file *os.File, from, to int64, kind string) (extent, bool, error) {
	header := make([]byte, 16)
	at := from
	for boxes := 0; boxes < mp4BoxLimit && at+8 <= to; boxes++ {
		if _, err := file.ReadAt(header[:8], at); err != nil {
			if ranOut(err) {
				return extent{}, false, nil
			}
			return extent{}, false, err
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		name := string(header[4:8])
		content := at + 8
		switch size {
		case 0:
			// The last box may declare no size at all and run to the end of
			// whatever contains it.
			size = to - at
		case 1:
			// A box too big for 32 bits carries its real size in the eight
			// bytes after the name.
			if _, err := file.ReadAt(header[8:16], content); err != nil {
				if ranOut(err) {
					return extent{}, false, nil
				}
				return extent{}, false, err
			}
			size = int64(binary.BigEndian.Uint64(header[8:16]))
			content = at + 16
		}
		if size < content-at || at+size > to {
			// A length that overruns its parent is not a box. What follows it
			// cannot be trusted to be one either, so the level ends here.
			return extent{}, false, nil
		}
		if name == kind {
			return extent{at: content, end: at + size}, true, nil
		}
		at += size
	}
	return extent{}, false, nil
}

// audioTrack is the first `trak` whose handler says sound.
//
// A track whose tree is missing a piece is passed over rather than guessed at.
func audioTrack(file *os.File, movie extent) (extent, bool, error) {
	at := movie.at
	for tracks := 0; tracks < mp4BoxLimit && at < movie.end; tracks++ {
		track, ok, err := findBox(file, at, movie.end, "trak")
		if err != nil || !ok {
			return extent{}, false, err
		}
		at = track.end

		media, ok, err := findBox(file, track.at, track.end, "mdia")
		if err != nil {
			return extent{}, false, err
		}
		if !ok {
			continue
		}
		handler, ok, err := findBox(file, media.at, media.end, "hdlr")
		if err != nil {
			return extent{}, false, err
		}
		if !ok {
			continue
		}
		// Four bytes of version and flags, four the standard reserves, then
		// the four that name the handler.
		kind := make([]byte, 4)
		if _, err := file.ReadAt(kind, handler.at+8); err != nil {
			if ranOut(err) {
				continue
			}
			return extent{}, false, err
		}
		if string(kind) == "soun" {
			return track, true, nil
		}
	}
	return extent{}, false, nil
}

// presentedDuration sums this track's edit list: the parts of the media that
// are actually played, stated in the movie's timescale.
//
// This is where the decoder priming goes. A muxer writes the whole encoded
// media into the track and then an edit saying where the music starts, so the
// edit list is the only place in the file that distinguishes the audio from the
// run-up to it.
func presentedDuration(file *os.File, track extent, movieTimescale uint64) (int, bool, error) {
	if movieTimescale == 0 {
		return 0, false, nil
	}
	edits, ok, err := findBox(file, track.at, track.end, "edts")
	if err != nil || !ok {
		return 0, false, err
	}
	list, ok, err := findBox(file, edits.at, edits.end, "elst")
	if err != nil || !ok {
		return 0, false, err
	}

	head := make([]byte, 8)
	if _, err := file.ReadAt(head, list.at); err != nil {
		if ranOut(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	entrySize, segmentSize := int64(12), 4
	if head[0] == 1 {
		entrySize, segmentSize = 20, 8
	}
	count := int64(binary.BigEndian.Uint32(head[4:8]))
	if count <= 0 || list.at+8+count*entrySize > list.end {
		return 0, false, nil
	}

	entry := make([]byte, entrySize)
	var total uint64
	for index := int64(0); index < count; index++ {
		if _, err := file.ReadAt(entry, list.at+8+index*entrySize); err != nil {
			if ranOut(err) {
				return 0, false, nil
			}
			return 0, false, err
		}
		if segmentSize == 8 {
			total += binary.BigEndian.Uint64(entry[:8])
			continue
		}
		total += uint64(binary.BigEndian.Uint32(entry[:4]))
	}
	if total == 0 {
		return 0, false, nil
	}
	return milliseconds(total, movieTimescale)
}

// headerBoxFields reads the timescale and duration out of an `mvhd` or an
// `mdhd`, which for these two fields are laid out identically.
func headerBoxFields(file *os.File, box extent) (uint64, uint64, bool, error) {
	body := make([]byte, 32)
	if box.end-box.at < int64(len(body)) {
		body = body[:box.end-box.at]
	}
	if _, err := file.ReadAt(body, box.at); err != nil && !ranOut(err) {
		return 0, 0, false, err
	}

	switch {
	case len(body) >= 32 && body[0] == 1:
		// Version 1: the two timestamps ahead of the timescale are eight bytes
		// each rather than four, and the duration behind it is eight as well.
		duration := binary.BigEndian.Uint64(body[24:32])
		if duration == math.MaxUint64 {
			return 0, 0, false, nil
		}
		return uint64(binary.BigEndian.Uint32(body[20:24])), duration, true, nil
	case len(body) >= 20 && body[0] == 0:
		duration := uint64(binary.BigEndian.Uint32(body[16:20]))
		if duration == math.MaxUint32 {
			return 0, 0, false, nil
		}
		return uint64(binary.BigEndian.Uint32(body[12:16])), duration, true, nil
	}
	return 0, 0, false, nil
}

func milliseconds(duration, timescale uint64) (int, bool, error) {
	if timescale == 0 || duration == 0 || duration > math.MaxInt64/1000 {
		return 0, false, nil
	}
	return int(duration * 1000 / timescale), true, nil
}
