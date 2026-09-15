// Package waveform reads the shape of a file's audio: two hundred numbers, one
// per slice of the track, each the loudest sample in that slice.
//
// It is drawn on a copy's card in the review queue so that a person can see
// where the music is before pressing play. It says nothing about identity and
// nothing about quality, and no verdict reads it.
package waveform

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable reports that the decoder could not be found, so nothing was
// read. It is a look that could not happen rather than a file with a flat shape.
var ErrUnavailable = errors.New("the audio decoder is unavailable")

const (
	// Bars is how many numbers one waveform holds. It is fixed, so a waveform
	// stored today is the same picture when it is read back however wide the card
	// drawing it has become.
	Bars = 200
	// sampleRateHz is what the whole file is decoded at. A sample per eighth of a
	// millisecond is far more than two hundred bars need, and it makes the decode
	// of a five-minute track a fraction of a second.
	sampleRateHz = 8000
	readLimit    = 120 * time.Second
)

// Reader reads waveforms with ffmpeg.
type Reader struct {
	ffmpeg string
	run    func(ctx context.Context, name string, args ...string) ([]byte, error)
	real   bool
}

// Options configures a Reader.
type Options struct {
	// FFmpegPath is where ffmpeg is. Empty means look for it on the PATH under
	// its own name.
	FFmpegPath string
	// Run executes a program. Tests replace it; nothing else should.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func NewReader(options Options) *Reader {
	reader := &Reader{ffmpeg: strings.TrimSpace(options.FFmpegPath), run: options.Run}
	if reader.ffmpeg == "" {
		reader.ffmpeg = "ffmpeg"
	}
	if reader.run == nil {
		reader.run, reader.real = runCommand, true
	}
	return reader
}

// Available reports whether the decoder can be run at all, so a machine without
// one says so once rather than once per file.
func (reader *Reader) Available() bool {
	if !reader.real {
		return true
	}
	_, err := exec.LookPath(reader.ffmpeg)
	return err == nil
}

// Binary names the program the read runs, for a log line that has to say what
// could not be found.
func (reader *Reader) Binary() string { return reader.ffmpeg }

// Peaks reads one file's shape.
//
// A file the decoder produced no audio for comes back nil with no error. That is
// a fact about the file, and the caller writes down that there is no waveform
// rather than treating it as a failure.
func (reader *Reader) Peaks(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()

	raw, err := reader.run(ctx, reader.ffmpeg,
		"-nostdin", "-loglevel", "error",
		"-i", path,
		// Audio only, first stream. Cover art in a FLAC is a video stream.
		"-vn", "-map", "0:a:0",
		"-ac", "1", "-ar", strconv.Itoa(sampleRateHz),
		"-f", "s16le", "-",
	)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s could not be found", ErrUnavailable, reader.ffmpeg)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// ffmpeg prints what it decoded before it complains, so a run that
		// produced audio produced audio. One that produced less than a sample a
		// bar is a file this cannot draw, and it falls through to the check below.
	}
	if len(raw) < Bars*2 {
		return nil, nil
	}
	return Peaks(pcm(raw)), nil
}

// Peaks turns samples into the Bars numbers the card draws: the loudest sample
// in each equal slice of the track, scaled so the loudest slice reads 255.
//
// A quiet stretch inside loud music is a real answer and comes back small. A
// file that is silent throughout comes back all zeros: nothing is scaled up out
// of nothing.
func Peaks(samples []int16) []byte {
	peaks := make([]byte, Bars)
	if len(samples) == 0 {
		return peaks
	}
	buckets := make([]int, Bars)
	loudest := 0
	for bar := range buckets {
		// Both edges are computed from the bar rather than accumulated, so every
		// sample lands in exactly one bucket whatever the remainder is.
		start := bar * len(samples) / Bars
		end := (bar + 1) * len(samples) / Bars
		highest := 0
		for _, sample := range samples[start:end] {
			// int16's floor has no positive twin, so the magnitude is taken in a
			// wider type.
			value := int(sample)
			if value < 0 {
				value = -value
			}
			if value > highest {
				highest = value
			}
		}
		buckets[bar] = highest
		if highest > loudest {
			loudest = highest
		}
	}
	if loudest == 0 {
		return peaks
	}
	for bar, highest := range buckets {
		peaks[bar] = byte((highest*255 + loudest/2) / loudest)
	}
	return peaks
}

// pcm reads signed 16-bit little-endian samples.
func pcm(raw []byte) []int16 {
	samples := make([]int16, len(raw)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(raw[index*2:]))
	}
	return samples
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	return output, err
}
