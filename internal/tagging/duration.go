package tagging

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// How long a file is, counted rather than claimed.
//
// A duration is one of the few things that can void a match: a pair whose two
// lengths are more than five seconds apart is refused whatever else agrees. So
// the number has to come from the audio. Until now it mostly did not — a FLAC
// was measured, and everything else fell back to a `TLEN` frame, which is the
// file's own claim about itself and is wrong as often as whatever wrote it was
// careless. A decision that can void a match cannot rest on a claim.
//
// Every container the library holds says its length exactly, somewhere in its
// own structure, and every one says it differently. FLAC counts samples in
// STREAMINFO, WAV divides a data size by a byte rate, AIFF holds a frame count
// against a rate written as an 80-bit float nobody else uses, MP4 a duration
// against a timescale, DSF a sample count against a frequency, and Ogg the
// granule position of its last page. MP3 says nothing at all and has to be
// walked frame by frame. What they share is that none of it is a decode: these
// readers step through headers and chunk boundaries and never turn a byte into
// a sample.
//
// The one rule they all keep is the one that matters. A container that will not
// say answers ErrNoDuration and the file is stored with none, because a length
// nobody counted is silence, and silence has never been an agreement.

// ErrNoDuration is what a file that does not say how long it is answers with.
// It is not a failure of the read: a music extension on something that is not
// music, a stream cut off mid-header, a container whose length field is the
// reserved value meaning unknown — all of those are things the library holds
// and things a scan must survive.
var ErrNoDuration = errors.New("this file does not say how long it is")

// durationReaders is one reader per container family, keyed by the extensions
// the library indexes. It is deliberately the same shape as the scanner's
// supportedExtensions and is checked against it by a test, so that adding a
// format to the library and forgetting to teach it to say its length fails
// rather than quietly storing nothing.
var durationReaders = map[string]func(path string) (int, error){
	".mp3":  countMPEGFrames,
	".flac": readFLACStreamInfo,
	".ogg":  readOggGranule,
	".oga":  readOggGranule,
	".opus": readOggGranule,
	".m4a":  readMP4Duration,
	".m4b":  readMP4Duration,
	".m4p":  readMP4Duration,
	".mp4":  readMP4Duration,
	".dsf":  readDSFDuration,
	".aiff": readAIFFDuration,
	".aif":  readAIFFDuration,
	".wav":  readRIFFDuration,
}

// CanCount reports whether there is a reader here for a file with this name.
// Nothing is opened, only the extension is read — the same question CanHold
// asks of the library, asked of this package.
func CanCount(name string) bool {
	_, ok := durationReaders[strings.ToLower(filepath.Ext(name))]
	return ok
}

// CountedDurationMS is how long the audio in this file is, in milliseconds,
// according to the file's own structure.
//
// Nothing is decoded and nothing is estimated. A file whose container will not
// say answers 0 alongside ErrNoDuration, which is the honest answer and the one
// the caller must be able to survive: a scan stores it as NULL and matching
// reads NULL as silence rather than as agreement.
func CountedDurationMS(path string) (int, error) {
	read, ok := durationReaders[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return 0, fmt.Errorf("%s: %w", path, ErrNoDuration)
	}
	return read(path)
}

// noDuration is the refusal, wrapped so the message says which file refused.
func noDuration(path string) (int, error) {
	return 0, fmt.Errorf("%s: %w", path, ErrNoDuration)
}

// ranOut distinguishes a file that stopped from a disk that failed. A header
// that ends halfway through is not an I/O problem to be reported and retried,
// it is a file that does not hold what its name says, and the answer to that is
// a refusal.
func ranOut(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
