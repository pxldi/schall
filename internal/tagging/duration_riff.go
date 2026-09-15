package tagging

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// How long a WAV is, read off the size of its samples.
//
// A WAV is chunks: a four-byte name, a four-byte length, that many bytes, and a
// pad byte if the length was odd. Two of them matter. `fmt ` says how many
// bytes a second of this audio occupies, and `data` says how many bytes of it
// there are. The division is exact for uncompressed audio because there is
// nothing between a byte and its moment in time -- the samples are laid end to
// end at a fixed width and no header lies between them.
//
// That last sentence is the whole reason for the refusal below. WAV is a
// container and people have put compressed audio in it -- ADPCM, MP3, GSM --
// and for those the byte rate in `fmt ` is an average the encoder nominated,
// not a rate the file keeps. Dividing by it gives a number that is close, which
// is exactly what this change exists to stop producing. So a WAV whose samples
// are not laid out plainly is refused and stored with no length at all.
func readRIFFDuration(path string) (int, error) {
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
	if string(header[:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return noDuration(path)
	}

	var (
		byteRate  int64
		dataBytes int64
		plain     bool
	)
	err = eachRIFFChunk(file, 12, func(kind string, at, size int64) error {
		switch kind {
		case "fmt ":
			// Two bytes of format tag, two of channel count, four of sample
			// rate, then the four this wants.
			body := make([]byte, 26)
			if size < 16 {
				return nil
			}
			if int64(len(body)) > size {
				body = body[:size]
			}
			if _, err := file.ReadAt(body, at); err != nil && !ranOut(err) {
				return err
			}
			format := binary.LittleEndian.Uint16(body[:2])
			// Extensible defers the real format to a GUID at the end of the
			// chunk, whose first two bytes are the tag it stands in for. It is
			// what any WAV above two channels or 16 bits is written as, so
			// refusing it outright would refuse most modern lossless WAVs.
			if format == 0xfffe && len(body) >= 26 {
				format = binary.LittleEndian.Uint16(body[24:26])
			}
			// 1 is integer PCM and 3 is IEEE float. Both are laid out plainly.
			plain = format == 1 || format == 3
			byteRate = int64(binary.LittleEndian.Uint32(body[8:12]))
		case "data":
			dataBytes = size
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk the chunks of %s: %w", path, err)
	}
	if !plain || byteRate <= 0 || dataBytes <= 0 {
		return noDuration(path)
	}
	return int(dataBytes * 1000 / byteRate), nil
}

// A file of noise can spell a chunk name and declare a length, so the walk is
// capped. No real file comes close.
const riffChunkLimit = 4096

// eachRIFFChunk steps through chunk headers, never their contents. The visitor
// reads what it wants where it is told it is, which keeps every allocation the
// size of a header rather than the size of the audio.
//
// The declared size of the RIFF chunk is deliberately not used as the end: it
// is written before the audio exists and tools that append are careless with
// it. The file's real size is the honest limit.
func eachRIFFChunk(file *os.File, from int64, visit func(kind string, at, size int64) error) error {
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
		size := int64(binary.LittleEndian.Uint32(header[4:8]))
		content := at + 8
		if content+size > limit {
			// A `data` chunk truncated by a copy that stopped early. What is
			// actually there is the honest length of what is actually there.
			size = limit - content
		}
		if err := visit(string(header[:4]), content, size); err != nil {
			return err
		}
		// Chunks start on even offsets, so an odd length is followed by a pad
		// byte that belongs to nobody.
		at = content + size + size&1
	}
	return nil
}
