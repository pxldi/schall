package spectrum

import (
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// A reader whose ffmpeg and ffprobe are this function rather than the programs.
// The audio comes back as the signed sixteen-bit stream ffmpeg would have
// written, so everything between the command line and the measurement is the
// code that runs in production.
func readerOver(samples []float64, encoder string, t *testing.T) (*Reader, *[][]string) {
	t.Helper()
	calls := &[][]string{}
	reader := NewReader(Options{Run: func(
		_ context.Context, name string, args ...string,
	) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if strings.HasSuffix(name, "ffprobe") {
			return []byte(`{"streams":[{"tags":{"encoder":"` + encoder + `"}}],"format":{}}`), nil
		}
		return signed16(samples), nil
	}})
	return reader, calls
}

func signed16(samples []float64) []byte {
	raw := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(raw[index*2:], uint16(int16(sample*32767)))
	}
	return raw
}

func TestReadMeasuresWhatTheDecoderProduced(t *testing.T) {
	reader, calls := readerOver(mix(cutBands, 1e-6), "LAME3.100", t)

	reading, err := reader.ReadWithBitRate(
		context.Background(), "/music/track.flac", testSampleRate, 240_000, 900)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !reading.Judged {
		t.Fatal("the audio the decoder produced was not judged")
	}
	if reading.CutoffHz > 15500 {
		t.Fatalf("cutoff = %d Hz, want the audio to stop by 15 kHz", reading.CutoffHz)
	}
	if !reading.TranscodeSuspected {
		t.Fatal("a FLAC whose audio stops at 15 kHz was not suspected")
	}
	if reading.Encoder != "LAME3.100" {
		t.Fatalf("encoder = %q, want what the file says made it", reading.Encoder)
	}

	// The decode must not resample. A resampler removes the top of the range
	// itself, and the measurement would then be of Schall rather than of the
	// file.
	decode := strings.Join((*calls)[0], " ")
	if !strings.Contains(decode, "-ar 44100") {
		t.Fatalf("the decode did not keep the file's own sample rate: %s", decode)
	}
	// And it must read from the middle rather than the opening, because a
	// fade-in carries no high frequencies in any copy at all.
	if !strings.Contains(decode, "-ss 117") {
		t.Fatalf("the decode did not start in the middle of the file: %s", decode)
	}
}

// A file with no sample rate is one nothing could read the headers of. It is not
// decoded at all, because a decode would have to resample to some rate of its
// own choosing and would measure that choice.
func TestReadDeclinesAFileWithNoSampleRate(t *testing.T) {
	reader, calls := readerOver(mix(wideBands, 1e-6), "", t)

	reading, err := reader.Read(context.Background(), "/music/track.flac", 0, 240_000)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reading.Judged {
		t.Fatal("a file with no sample rate was judged")
	}
	if len(*calls) != 0 {
		t.Fatalf("the decoder was run %d times for a file that could not be read", len(*calls))
	}
}

// A decoder that produced nothing is a file this cannot read. It is a fact about
// the file and never an error, so the caller writes it down as a look that
// concluded nothing rather than retrying it forever.
func TestReadConcludesNothingWhenTheDecoderProducedNothing(t *testing.T) {
	reader := NewReader(Options{Run: func(
		context.Context, string, ...string,
	) ([]byte, error) {
		return nil, errors.New("Invalid data found when processing input")
	}})

	reading, err := reader.Read(context.Background(), "/music/track.flac", testSampleRate, 1000)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reading.Judged {
		t.Fatal("audio nothing could decode was judged")
	}
}

// A machine with no ffmpeg is a check that could not run. It is reported to the
// caller rather than written down as a file with nothing to say.
func TestReadReportsAMissingDecoder(t *testing.T) {
	reader := NewReader(Options{Run: func(
		context.Context, string, ...string,
	) ([]byte, error) {
		return nil, exec.ErrNotFound
	}})

	_, err := reader.Read(context.Background(), "/music/track.flac", testSampleRate, 1000)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want the decoder to be reported as unavailable", err)
	}
}
