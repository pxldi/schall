// Package loudness measures how loud the audio in a file is, so that a music
// player can even out the volume across a collection.
//
// Records are mastered at whatever loudness their era and their label favoured,
// and two files of the same collection can sit ten decibels apart. A library
// played on shuffle therefore lurches: the listener reaches for the volume
// control between one song and the next. The remedy every player already
// understands is ReplayGain — measure a track once, write the result into the
// file as a tag, and the player turns that track down or up by exactly that
// much when it plays it.
//
// What is measured here is loudness and nothing else. Nothing in this package
// says what music a file holds, and no number it produces may ever be read as
// evidence that two files hold the same music: two unrelated recordings
// mastered by the same engineer measure the same. It is the quality family that
// internal/tagging's AudioProperties belongs to, not the identity family that
// internal/identity belongs to.
//
// The samples are never touched. ReplayGain is metadata: the file keeps the
// audio it arrived with, and the player applies the number at playback.
package loudness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable reports that ffmpeg could not be found. Measuring loudness is
// the only thing it costs: everything else Schall does with a file still works,
// so this is reported to the caller rather than failing anything at startup —
// the same bargain internal/preview makes for the same binary.
var ErrUnavailable = errors.New("loudness cannot be measured")

// ErrNotMeasurable reports a file ffmpeg read without arriving at a loudness.
// A file can be audio Schall is glad to hold and still say nothing here: a
// stream too short for the measurement's first window, or one ffmpeg decoded
// into silence. It is not a failure to retry, so the caller records that the
// question was asked and leaves the answer empty.
var ErrNotMeasurable = errors.New("this file's loudness could not be measured")

// referenceLUFS is the loudness every measured track is levelled to.
//
// ReplayGain 2.0 (hydrogenaud.io/index.php/ReplayGain_2.0_specification) fixes
// the reference at -18 LUFS, where LUFS is the loudness unit of ITU-R BS.1770 —
// the same measurement EBU R128 broadcasts to, offset by 5 units because music
// is listened to louder than television. So the gain a player must apply is the
// distance from the track's measured loudness to -18.
//
// ReplayGain 1.0 measured something else entirely (an equal-loudness curve
// against a 89 dB SPL reference) and is what ffmpeg's own `replaygain` filter
// implements. This package deliberately does not use that filter: it is the
// older definition, and it refuses sample rates outside 8-48 kHz, which is most
// of a modern collection.
const referenceLUFS = -18.0

// measureCap bounds one measurement. Loudness is measured by decoding the whole
// stream, which for a long file on a busy machine is not instant; a file that
// takes longer than this is a file something is wrong with.
const measureCap = 5 * time.Minute

// Measured is one file's loudness, in the two numbers a ReplayGain tag carries.
type Measured struct {
	// GainDB is what a player adds to the volume to bring this track to the
	// reference loudness. A loud master measures above the reference and gets a
	// negative gain; a quiet one gets a positive gain.
	GainDB float64
	// Peak is the highest sample in the stream, as a fraction of full scale.
	// 1.0 is full scale. It can exceed 1.0 and legitimately does: this is the
	// true peak of the reconstructed waveform, which passes between samples and
	// so can be higher than any sample is. A player reads it to decide whether
	// applying the gain would clip.
	Peak float64
}

// LoudnessLUFS is the measurement the gain was derived from. It is kept as a
// derivation rather than a field because a stored gain is the only thing the
// database holds, and an album's loudness has to be worked back out of its
// tracks' gains. See AlbumOf.
func (measured Measured) LoudnessLUFS() float64 { return referenceLUFS - measured.GainDB }

// TrackOf is one measured track as an album is worked out from its tracks.
type TrackOf struct {
	Measured
	// DurationMS is how long the track is. An album is levelled as one
	// programme, so a two-minute track counts for less of it than a ten-minute
	// one; a track with no length is one the album cannot be worked out from.
	DurationMS int
}

// Measurer runs the measurement.
type Measurer struct {
	binary string
}

// NewMeasurer prepares one. binary may be empty, meaning ffmpeg is looked for
// on PATH; a binary that cannot be found is reported when a measurement is
// asked for rather than here, because Schall runs perfectly well without one.
func NewMeasurer(binary string) *Measurer {
	if binary == "" {
		binary = "ffmpeg"
	}
	return &Measurer{binary: binary}
}

// Binary names the program the measurement runs, for a log line that has to say
// what could not be found.
func (measurer *Measurer) Binary() string { return measurer.binary }

// Measure decodes the file and reports how loud it is.
//
// ffmpeg's ebur128 filter is what does the measuring. It implements ITU-R
// BS.1770 directly, which is the measurement ReplayGain 2.0 is defined in terms
// of, and `peak=true` adds the true peak beside it. The output goes nowhere —
// `-f null -` decodes the stream and discards it — so nothing is written and
// the file is never opened for writing.
//
// Only the first audio stream is read. Cover art in a FLAC is a video stream to
// ffmpeg, and a file with two audio streams is not two loudnesses.
func (measurer *Measurer) Measure(ctx context.Context, path string) (Measured, error) {
	runCtx, cancel := context.WithTimeout(ctx, measureCap)
	defer cancel()

	// The summary this parses is printed at ffmpeg's default log level. It is
	// not asked down to `error` the way the preview transcoder asks it down,
	// because at `error` the summary is one of the things that stops being
	// printed and the measurement comes back empty.
	command := exec.CommandContext(runCtx, measurer.binary,
		"-nostdin", "-hide_banner", "-nostats",
		"-i", path,
		"-map", "0:a:0",
		"-af", "ebur128=peak=true",
		"-f", "null", "-",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		// Not found comes back two ways: as ErrNotFound when the name was
		// looked for on PATH, and as a missing file when it was given as a
		// path.
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return Measured{}, fmt.Errorf("%w: %s could not be found", ErrUnavailable, measurer.binary)
		}
		return Measured{}, fmt.Errorf("measure the loudness of %s: %w: %s",
			path, err, truncate(string(output)))
	}
	return Parse(string(output))
}

// The two lines the summary is read from. ebur128 prints a block of them at the
// end of the run, indented under headings:
//
//	Integrated loudness:
//	  I:         -9.4 LUFS
//	  Threshold: -20.0 LUFS
//	...
//	True peak:
//	  Peak:      -0.3 dBFS
//
// Every line before that block is a running reading of the same names, printed
// once per hundred milliseconds, so the last match is the summary's and the
// earlier ones are the track being measured. Both patterns anchor on the name
// at the start of a trimmed line, which the running readings never do — they
// put a timestamp first.
var (
	integratedPattern = regexp.MustCompile(`^I:\s+(-?[0-9]+(?:\.[0-9]+)?)\s+LUFS$`)
	peakPattern       = regexp.MustCompile(`^Peak:\s+(-?[0-9]+(?:\.[0-9]+)?)\s+dBFS$`)
)

// Parse reads a loudness out of what ffmpeg printed.
//
// It is exported so the parsing can be tested against recorded output without
// ffmpeg being installed, which is the half of this package that has rules in
// it. A run that printed no integrated loudness is ErrNotMeasurable rather than
// an error: ffmpeg exited happily, so nothing went wrong, the file simply had
// no answer to give.
func Parse(output string) (Measured, error) {
	var loudness, peakDBFS float64
	var haveLoudness, havePeak bool

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if found := integratedPattern.FindStringSubmatch(line); found != nil {
			if value, err := strconv.ParseFloat(found[1], 64); err == nil {
				loudness, haveLoudness = value, true
			}
			continue
		}
		if found := peakPattern.FindStringSubmatch(line); found != nil {
			if value, err := strconv.ParseFloat(found[1], 64); err == nil {
				peakDBFS, havePeak = value, true
			}
		}
	}
	if !haveLoudness || math.IsInf(loudness, 0) {
		return Measured{}, ErrNotMeasurable
	}
	// A stream ffmpeg decoded into silence has no loudness at all, and ebur128
	// says so with a very large negative number rather than by saying nothing.
	// Levelling silence up by ninety decibels is not what anybody wants, so it
	// is treated as the non-answer it is.
	if loudness <= -70 {
		return Measured{}, ErrNotMeasurable
	}

	measured := Measured{GainDB: referenceLUFS - loudness}
	if havePeak && !math.IsInf(peakDBFS, 0) {
		measured.Peak = math.Pow(10, peakDBFS/20)
	}
	return measured, nil
}

// AlbumOf works out one loudness for a whole release from its tracks', and
// reports whether it could.
//
// Album gain exists because a record is one performance. Levelling every track
// of it on its own flattens the quiet track the artist put after the loud one,
// which is a decision somebody made; album gain moves the whole record by one
// number and leaves the shape of it alone. A player that has both tags lets the
// listener choose which to follow.
//
// The album's loudness is the energy-weighted mean of its tracks' — each
// track's loudness converted back out of decibels, weighted by how long the
// track is, and converted back. This approximates measuring the record played
// end to end as one programme, which is what the specification defines and what
// a dedicated tool such as loudgain does. It is an approximation because
// BS.1770 gates quiet passages out of the measurement per track here rather
// than once across the record, so a record with a long quiet track is levelled
// a fraction of a decibel differently than a single pass would level it. That
// is loudness, not identity: a fraction of a decibel is inaudible, and nothing
// here decides anything about what a file is.
//
// The album's peak is the loudest sample anywhere on the record, because a
// player reads it to decide whether applying the album gain would clip, and the
// answer to that is set by the loudest moment, not by an average of them.
//
// It answers false where the record cannot be worked out: no tracks, or a track
// with no length to weigh it by. A partial answer is not offered — an album
// gain worked out from some of an album is a number for a record that does not
// exist, and the caller writes the track values alone.
func AlbumOf(tracks []TrackOf) (Measured, bool) {
	if len(tracks) == 0 {
		return Measured{}, false
	}
	var energy, span float64
	var peak float64
	for _, track := range tracks {
		if track.DurationMS <= 0 {
			return Measured{}, false
		}
		seconds := float64(track.DurationMS) / 1000
		energy += seconds * math.Pow(10, track.LoudnessLUFS()/10)
		span += seconds
		peak = math.Max(peak, track.Peak)
	}
	if span <= 0 || energy <= 0 {
		return Measured{}, false
	}
	loudness := 10 * math.Log10(energy/span)
	return Measured{GainDB: referenceLUFS - loudness, Peak: peak}, true
}

// FormatGain writes a gain the way every player expects to read it: a number of
// decibels with the unit after it, as "-7.25 dB". Two decimal places is what
// the specification's own examples carry and is finer than anybody can hear.
func FormatGain(gain float64) string {
	return strconv.FormatFloat(gain, 'f', 2, 64) + " dB"
}

// FormatPeak writes a peak as the bare fraction of full scale it is, with no
// unit. Six decimal places, because the number a player cares about is how
// close to 1.0 it is and the difference between 0.999 and 1.000 is the whole
// question.
func FormatPeak(peak float64) string {
	return strconv.FormatFloat(peak, 'f', 6, 64)
}

func truncate(value string) string {
	const limit = 200
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
