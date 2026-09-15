package spectrum

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable reports that the decoder could not be found. Nothing was
// learned about the audio: it is a look that could not happen, and the caller
// records it as one rather than as a file with nothing to say.
var ErrUnavailable = errors.New("the audio decoder is unavailable")

// How much audio one look reads, and from where.
//
// The middle of the track is read rather than the start, because a fade-in
// carries no high frequencies in any copy and a first look at one would call
// every file a transcode. Six seconds is far more than the measurement needs and
// still a fraction of a second of decoding.
const (
	segmentSeconds = 6
	readLimit      = 90 * time.Second
)

// cliffDB is how much louder the music immediately below the cutoff has to be
// than the silence immediately above it before the stop counts as a cliff.
//
// This is the whole guard against slandering a quiet recording. A lossy encoder
// cuts with a brick wall: full-strength music in one band, nothing at all in the
// next. An old or dark recording fades out instead, so the bands around where it
// stops are all faint and the step between them is small. Only a cliff is ever
// called a transcode, and a detection that misses some fakes is preferred over
// one that ever accuses an honest recording (issue #386).
const cliffDB = 20.0

// transcodeCutoffHz is the frequency below which a stop is evidence the audio
// was squeezed once. Every lossy encoder worth the name keeps more than this at
// any rate somebody would call good, and no lossless encoder removes anything at
// all, so a lossless container that stops here was lossy earlier in its life.
const transcodeCutoffHz = 16000

// wideBitRateKbps is the rate at or above which a bit rate contradicts a low
// cutoff. A 256 or 320 kbps encode keeps content to nineteen or twenty
// kilohertz; one that stops at sixteen is spending its bits re-encoding
// something that was already squeezed harder.
const wideBitRateKbps = 256

// losslessExtensions are the containers that promise the audio was never
// squeezed. The promise is about the container, which is exactly why it can be
// broken by what is inside it.
var losslessExtensions = map[string]bool{
	".flac": true, ".wav": true, ".wave": true, ".aif": true, ".aiff": true,
	".ape": true, ".wv": true, ".tta": true, ".shn": true,
}

// Reading is everything one look at a file's audio produced.
type Reading struct {
	Measurement
	// Encoder is the program the file says made it — "LAME3.100" on an MP3, a
	// libFLAC version on a FLAC. It is what the file claims about itself and is
	// shown beside the measurement, never read as proof of anything: a transcode
	// is free to carry any encoder string at all.
	Encoder string
	// TranscodeSuspected is the suspicion, and it is only meaningful where
	// Judged is true. It is shown to a person and read by nothing that admits or
	// refuses a file.
	TranscodeSuspected bool
}

// Reader looks at files with ffmpeg.
type Reader struct {
	ffmpeg  string
	ffprobe string
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	real    bool
}

// Options configures a Reader.
type Options struct {
	// FFmpegPath is where ffmpeg is. Empty means look for it on the PATH under
	// its own name; ffprobe is looked for beside it.
	FFmpegPath string
	// Run executes a program. Tests replace it; nothing else should.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func NewReader(options Options) *Reader {
	reader := &Reader{ffmpeg: strings.TrimSpace(options.FFmpegPath), run: options.Run}
	if reader.ffmpeg == "" {
		reader.ffmpeg = "ffmpeg"
	}
	// ffprobe ships with ffmpeg and sits beside it, whether that is a name on the
	// PATH or a path somebody configured.
	reader.ffprobe = filepath.Join(filepath.Dir(reader.ffmpeg), "ffprobe")
	if !strings.ContainsRune(reader.ffmpeg, filepath.Separator) {
		reader.ffprobe = "ffprobe"
	}
	if reader.run == nil {
		reader.run, reader.real = runCommand, true
	}
	return reader
}

// Available reports whether the decoder can be run at all, so that a machine
// without one says so once for a pass rather than once per file.
func (reader *Reader) Available() bool {
	if !reader.real {
		return true
	}
	_, err := exec.LookPath(reader.ffmpeg)
	return err == nil
}

// Binary names the program the measurement runs, for a log line that has to say
// what could not be found.
func (reader *Reader) Binary() string { return reader.ffmpeg }

// Read looks at one file.
//
// sampleRateHz and durationMS are what the scan already read off the file's
// headers. The sample rate is handed to the decoder unchanged so that no
// resampling happens: a resampler removes the top of the range itself, and a
// measurement of that would be a measurement of Schall rather than of the file.
//
// A file that will not decode, or whose middle is silent, comes back with Judged
// false and no error. That is a fact about the file, not a failure, and the
// caller writes it down as a look that concluded nothing.
func (reader *Reader) Read(
	ctx context.Context, path string, sampleRateHz, durationMS int,
) (Reading, error) {
	if sampleRateHz <= 0 {
		return Reading{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()

	samples, err := reader.decode(ctx, path, sampleRateHz, durationMS)
	if err != nil {
		return Reading{}, err
	}
	reading := Reading{Measurement: Analyse(samples, sampleRateHz)}
	if !reading.Judged {
		return reading, nil
	}
	reading.Encoder = reader.encoder(ctx, path)
	reading.TranscodeSuspected = Suspected(reading.Measurement, path, 0)
	return reading, nil
}

// ReadWithBitRate is Read where the caller also knows what bit rate the file
// claims, which is a second way the audio can contradict the file (see
// Suspected).
func (reader *Reader) ReadWithBitRate(
	ctx context.Context, path string, sampleRateHz, durationMS, bitRateKbps int,
) (Reading, error) {
	reading, err := reader.Read(ctx, path, sampleRateHz, durationMS)
	if err != nil || !reading.Judged {
		return reading, err
	}
	reading.TranscodeSuspected = Suspected(reading.Measurement, path, bitRateKbps)
	return reading, nil
}

// Suspected reports whether this measurement is evidence the audio was squeezed
// before it reached the container it is in now.
//
// Two things say so, and both need the stop to be a cliff:
//
//  1. A lossless container that stops below transcodeCutoffHz. Nothing lossless
//     removes anything, so the removal happened earlier.
//  2. A bit rate wide enough to carry the whole range, spent on audio that stops
//     well short of it. The file is paying for bits it has nothing to put in.
//
// Everything else is not suspected, which is a different sentence from "clean":
// this looks for one specific kind of damage and finds nothing else.
func Suspected(measurement Measurement, path string, bitRateKbps int) bool {
	if !measurement.Judged || measurement.CutoffHz > transcodeCutoffHz {
		return false
	}
	if measurement.CliffHeightDB < cliffDB {
		return false
	}
	if losslessExtensions[strings.ToLower(filepath.Ext(path))] {
		return true
	}
	return bitRateKbps >= wideBitRateKbps
}

// decode reads a stretch from the middle of the file as mono samples.
func (reader *Reader) decode(
	ctx context.Context, path string, sampleRateHz, durationMS int,
) ([]float64, error) {
	start := 0
	if middle := durationMS/2 - segmentSeconds*1000/2; middle > 0 {
		start = middle / 1000
	}
	raw, err := reader.run(ctx, reader.ffmpeg,
		"-nostdin", "-loglevel", "error",
		// Seeking before the input is the cheap seek: the decoder jumps rather
		// than decoding everything up to the point that was asked for.
		"-ss", strconv.Itoa(start),
		"-i", path,
		// Audio only, first stream. Cover art in a FLAC is a video stream.
		"-vn", "-map", "0:a:0",
		"-t", strconv.Itoa(segmentSeconds),
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
		// produced audio produced audio. One that produced none is a file this
		// cannot read, which is recorded rather than raised.
		if len(raw) < frameSize*2 {
			return nil, nil
		}
	}
	return pcm(raw), nil
}

// pcm turns signed 16-bit little-endian samples into the range -1 to 1.
func pcm(raw []byte) []float64 {
	samples := make([]float64, len(raw)/2)
	for index := range samples {
		samples[index] = float64(int16(binary.LittleEndian.Uint16(raw[index*2:]))) / 32768
	}
	return samples
}

// encoder asks the file what made it. An answer is a nicety beside the
// measurement and never a reason to conclude anything, so a probe that fails,
// or a container that carries no such tag, leaves it empty.
func (reader *Reader) encoder(ctx context.Context, path string) string {
	raw, err := reader.run(ctx, reader.ffprobe,
		"-v", "error",
		"-show_entries", "format_tags=encoder:stream_tags=encoder",
		"-of", "json", path,
	)
	if err != nil && len(raw) == 0 {
		return ""
	}
	var payload struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	for _, tags := range payload.Streams {
		if named := clean(tags.Tags["encoder"]); named != "" {
			return named
		}
	}
	return clean(payload.Format.Tags["encoder"])
}

// clean trims what a file says about its encoder down to something worth
// showing. A tag is written by whoever made the file and can hold anything at
// all, so it is bounded before it is stored.
func clean(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) > 120 {
		value = strings.TrimSpace(value[:120])
	}
	return value
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	return output, err
}
