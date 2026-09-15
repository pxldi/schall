package loudness

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// summary is what ffmpeg printed for a real file, kept verbatim so the parsing
// can be tested on a machine with no ffmpeg on it. The running readings above
// the summary are part of the sample on purpose: they carry the same names, and
// a parser that took the first one would report the loudness of the first
// tenth of a second.
const summary = `[Parsed_ebur128_0 @ 0x7fe094003c00] t: 0.099979   TARGET:-23 LUFS    M: -30.1 S: -30.1     I: -30.1 LUFS       LRA:   0.0 LU  FTPK: -27.1 dBFS  TPK: -27.1 dBFS
[Parsed_ebur128_0 @ 0x7fe094003c00] t: 0.199979   TARGET:-23 LUFS    M: -21.1 S: -21.1     I: -21.1 LUFS       LRA:   0.0 LU  FTPK: -18.1 dBFS  TPK: -18.1 dBFS
[Parsed_ebur128_0 @ 0x7fe094003c00] Summary:

  Integrated loudness:
    I:          -9.43 LUFS
    Threshold: -20.0 LUFS

  Loudness range:
    LRA:         5.6 LU
    Threshold: -30.1 LUFS
    LRA low:   -12.3 LUFS
    LRA high:   -6.7 LUFS

  True peak:
    Peak:       -0.30 dBFS

[out#0/null @ 0x564761ad1000] video:0KiB audio:469KiB subtitle:0KiB other streams:0KiB
`

func TestParseReadsTheSummaryAndNotTheRunningReadings(t *testing.T) {
	measured, err := Parse(summary)
	if err != nil {
		t.Fatalf("parse the summary: %v", err)
	}
	// -18 - (-9.43) = -8.57.
	if math.Abs(measured.GainDB-(-8.57)) > 0.001 {
		t.Fatalf("gain is %v, want -8.57", measured.GainDB)
	}
	// -0.30 dBFS is 10^(-0.30/20) of full scale.
	if math.Abs(measured.Peak-0.966051) > 0.00001 {
		t.Fatalf("peak is %v, want about 0.966051", measured.Peak)
	}
}

func TestParseKeepsATruePeakAboveFullScale(t *testing.T) {
	// A true peak passes between samples, so a stream whose samples all fit
	// inside full scale can still reconstruct above it. Clamping that to 1.0
	// would tell a player there is headroom where there is none.
	measured, err := Parse("  I:  -12.0 LUFS\n  Peak:  1.50 dBFS\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if measured.Peak <= 1 {
		t.Fatalf("peak is %v, want it kept above full scale", measured.Peak)
	}
}

func TestParseRefusesOutputWithNoLoudnessInIt(t *testing.T) {
	for name, output := range map[string]string{
		"nothing at all":  "",
		"an error only":   "[in#0 @ 0x1] Error opening input: Invalid data found\n",
		"digital silence": "  I:      -70.0 LUFS\n  Peak:   -inf dBFS\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(output); !errors.Is(err, ErrNotMeasurable) {
				t.Fatalf("error is %v, want ErrNotMeasurable", err)
			}
		})
	}
}

func TestFormatsAreWhatAPlayerReads(t *testing.T) {
	if got := FormatGain(-7.25); got != "-7.25 dB" {
		t.Fatalf("gain is %q, want %q", got, "-7.25 dB")
	}
	if got := FormatGain(3); got != "3.00 dB" {
		t.Fatalf("gain is %q, want %q", got, "3.00 dB")
	}
	if got := FormatPeak(0.9660509); got != "0.966051" {
		t.Fatalf("peak is %q, want %q", got, "0.966051")
	}
}

func TestAlbumOfWeighsEachTrackByHowLongItIs(t *testing.T) {
	// Two tracks at the same loudness make an album at that loudness, however
	// long either of them is.
	album, ok := AlbumOf([]TrackOf{
		{Measured: Measured{GainDB: -6, Peak: 0.5}, DurationMS: 60_000},
		{Measured: Measured{GainDB: -6, Peak: 0.9}, DurationMS: 600_000},
	})
	if !ok {
		t.Fatal("the album could not be worked out")
	}
	if math.Abs(album.GainDB-(-6)) > 0.001 {
		t.Fatalf("album gain is %v, want -6", album.GainDB)
	}
	if album.Peak != 0.9 {
		t.Fatalf("album peak is %v, want the loudest track's 0.9", album.Peak)
	}

	// A loud minute beside a quiet hour is an album that is mostly quiet, so
	// the album's gain sits nearer the quiet track's than halfway between.
	mixed, ok := AlbumOf([]TrackOf{
		{Measured: Measured{GainDB: -12}, DurationMS: 60_000},
		{Measured: Measured{GainDB: 0}, DurationMS: 3_600_000},
	})
	if !ok {
		t.Fatal("the album could not be worked out")
	}
	if mixed.GainDB < -6 || mixed.GainDB > -0.001 {
		t.Fatalf("album gain is %v, want it between the two and nearer the long track's", mixed.GainDB)
	}
}

func TestAlbumOfRefusesWhatItCannotWeigh(t *testing.T) {
	if _, ok := AlbumOf(nil); ok {
		t.Fatal("an album of no tracks was worked out anyway")
	}
	if _, ok := AlbumOf([]TrackOf{
		{Measured: Measured{GainDB: -6}, DurationMS: 60_000},
		{Measured: Measured{GainDB: -6}, DurationMS: 0},
	}); ok {
		t.Fatal("an album with an unmeasured track was worked out anyway")
	}
}

// TestMeasureASineIsTheGainTheSpecificationAsksFor runs the real thing.
//
// A 1 kHz sine is the one signal whose loudness can be worked out on paper. The
// K-weighting BS.1770 applies is flat at 1 kHz to within a tenth of a decibel,
// and its two channels sum, so a stereo sine of peak amplitude a measures
// 10*log10(2 * a^2/2) = 20*log10(a) LUFS — where the -0.691 offset in the
// specification's formula is cancelled by the weighting curve's own gain at
// that frequency. At a = 0.5 that is -6.02 LUFS, so the gain to the -18 LUFS
// reference is -11.98 dB.
//
// Halving the amplitude is checked beside it. It must move the gain by exactly
// 6.02 dB the other way, which is what catches a sign the wrong way round or a
// decimal point read out of the wrong column.
func TestMeasureASineIsTheGainTheSpecificationAsksFor(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	measurer := NewMeasurer("")
	dir := t.TempDir()

	loud := filepath.Join(dir, "loud.wav")
	writeSine(t, loud, 0.5)
	quiet := filepath.Join(dir, "quiet.wav")
	writeSine(t, quiet, 0.25)

	measuredLoud, err := measurer.Measure(context.Background(), loud)
	if err != nil {
		t.Fatalf("measure the loud sine: %v", err)
	}
	if math.Abs(measuredLoud.GainDB-(-11.98)) > 0.5 {
		t.Fatalf("gain is %v dB, want -11.98 dB within half a decibel", measuredLoud.GainDB)
	}
	if math.Abs(measuredLoud.Peak-0.5) > 0.01 {
		t.Fatalf("peak is %v, want 0.5", measuredLoud.Peak)
	}

	measuredQuiet, err := measurer.Measure(context.Background(), quiet)
	if err != nil {
		t.Fatalf("measure the quiet sine: %v", err)
	}
	if math.Abs((measuredQuiet.GainDB-measuredLoud.GainDB)-6.02) > 0.2 {
		t.Fatalf("halving the amplitude moved the gain by %v dB, want 6.02 dB",
			measuredQuiet.GainDB-measuredLoud.GainDB)
	}
}

func TestMeasureSaysWhenFfmpegIsNotThere(t *testing.T) {
	measurer := NewMeasurer("schall-no-such-ffmpeg")
	_, err := measurer.Measure(context.Background(), filepath.Join(t.TempDir(), "any.flac"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error is %v, want ErrUnavailable", err)
	}
}

// writeSine puts one second of a stereo 1 kHz sine at the given peak amplitude
// into a WAV file, written here rather than asked of ffmpeg so that the test's
// expected value rests on the samples it wrote and not on a filter's defaults.
func writeSine(t *testing.T, path string, amplitude float64) {
	t.Helper()
	const rate = 44100
	const channels = 2
	const frames = rate

	var samples bytes.Buffer
	for frame := range frames {
		value := amplitude * math.Sin(2*math.Pi*1000*float64(frame)/rate)
		// Full scale for a 16-bit sample is 32767 rather than 32768, so an
		// amplitude of 1.0 writes the loudest sample the format has and not one
		// that wraps around to the quietest.
		sample := int16(math.Round(value * 32767))
		for range channels {
			_ = binary.Write(&samples, binary.LittleEndian, sample)
		}
	}

	var file bytes.Buffer
	data := samples.Bytes()
	byteRate := rate * channels * 2
	file.WriteString("RIFF")
	_ = binary.Write(&file, binary.LittleEndian, uint32(36+len(data)))
	file.WriteString("WAVEfmt ")
	_ = binary.Write(&file, binary.LittleEndian, uint32(16))
	_ = binary.Write(&file, binary.LittleEndian, uint16(1))        // PCM
	_ = binary.Write(&file, binary.LittleEndian, uint16(channels)) //nolint:gosec // constant
	_ = binary.Write(&file, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&file, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&file, binary.LittleEndian, uint16(channels*2))
	_ = binary.Write(&file, binary.LittleEndian, uint16(16))
	file.WriteString("data")
	_ = binary.Write(&file, binary.LittleEndian, uint32(len(data)))
	file.Write(data)

	if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
		t.Fatalf("write the sine to %s: %v", path, err)
	}
}
