package waveform

import (
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// signed16 is the stream ffmpeg would have written, so everything between the
// command line and the peaks is the code that runs in production.
func signed16(samples []int16) []byte {
	raw := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(raw[index*2:], uint16(sample))
	}
	return raw
}

// ramp is one sample a bar, rising from nothing to full scale.
func ramp() []int16 {
	samples := make([]int16, Bars)
	for index := range samples {
		samples[index] = int16(index * 32767 / (Bars - 1))
	}
	return samples
}

func TestPeaksTakesTheLoudestSampleInEachBucket(t *testing.T) {
	// Four samples a bar, the loudest one in a different place each time and
	// negative on every other bar. A bucket that read the first sample, the mean,
	// or the signed maximum would come out differently.
	samples := make([]int16, Bars*4)
	for bar := range Bars {
		loudest := int16(bar * 32767 / (Bars - 1))
		if bar%2 == 1 {
			loudest = -loudest
		}
		samples[bar*4+bar%4] = loudest
	}

	peaks := Peaks(samples)

	if len(peaks) != Bars {
		t.Fatalf("peaks = %d bars, want %d", len(peaks), Bars)
	}
	for bar, peak := range peaks {
		highest := bar * 32767 / (Bars - 1)
		want := byte((highest*255 + 32767/2) / 32767)
		if peak != want {
			t.Fatalf("bar %d = %d, want %d", bar, peak, want)
		}
	}
}

func TestPeaksScaleTheLoudestBucketToFull(t *testing.T) {
	// A quiet file. The shape is what is drawn, so the loudest bar reads 255
	// whether the music was mastered loud or not.
	samples := make([]int16, Bars*8)
	for index := range samples {
		samples[index] = int16(index % 5)
	}
	samples[len(samples)-1] = 400

	peaks := Peaks(samples)

	if peaks[Bars-1] != 255 {
		t.Fatalf("the loudest bar = %d, want 255", peaks[Bars-1])
	}
	if peaks[0] > 10 {
		t.Fatalf("a bar at a hundredth of the loudest = %d, want it near nothing", peaks[0])
	}
}

func TestPeaksOfSilenceAreZeros(t *testing.T) {
	peaks := Peaks(make([]int16, Bars*10))

	if len(peaks) != Bars {
		t.Fatalf("peaks = %d bars, want %d", len(peaks), Bars)
	}
	for bar, peak := range peaks {
		if peak != 0 {
			t.Fatalf("bar %d of a silent file = %d, want 0", bar, peak)
		}
	}
}

func TestPeaksOfNoSamplesAreZeros(t *testing.T) {
	if peaks := Peaks(nil); len(peaks) != Bars {
		t.Fatalf("peaks = %d bars, want %d", len(peaks), Bars)
	}
}

func TestReaderDecodesTheWholeFileAsMono(t *testing.T) {
	var called []string
	reader := NewReader(Options{Run: func(
		_ context.Context, name string, args ...string,
	) ([]byte, error) {
		called = append([]string{name}, args...)
		return signed16(ramp()), nil
	}})

	peaks, err := reader.Peaks(context.Background(), "/inbox/folder/track.flac")
	if err != nil {
		t.Fatalf("peaks: %v", err)
	}
	if len(peaks) != Bars || peaks[Bars-1] != 255 || peaks[0] != 0 {
		t.Fatalf("peaks = %v, want %d bars running 0 to 255", peaks, Bars)
	}

	// The whole file, one channel, at the rate the buckets assume. A -ss or a -t
	// would draw the shape of a fragment under the whole track's card.
	command := strings.Join(called, " ") + " "
	for _, want := range []string{"-ac 1 ", "-ar 8000 ", "-f s16le ", "/inbox/folder/track.flac "} {
		if !strings.Contains(command, want) {
			t.Fatalf("command %q is missing %q", command, want)
		}
	}
	for _, unwanted := range []string{" -ss ", " -t "} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("command %q reads only part of the file (%q)", command, unwanted)
		}
	}
}

func TestReaderReportsAMissingDecoder(t *testing.T) {
	reader := NewReader(Options{Run: func(
		context.Context, string, ...string,
	) ([]byte, error) {
		return nil, exec.ErrNotFound
	}})

	_, err := reader.Peaks(context.Background(), "/inbox/f/t.mp3")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestReaderSaysNothingAboutAFileWithNoAudio(t *testing.T) {
	reader := NewReader(Options{Run: func(
		context.Context, string, ...string,
	) ([]byte, error) {
		return []byte{0, 0, 0, 0}, errors.New("Output file does not contain any stream")
	}})

	peaks, err := reader.Peaks(context.Background(), "/inbox/f/t.mp3")
	if err != nil {
		t.Fatalf("err = %v, want a file nothing could be read from to be silence", err)
	}
	if peaks != nil {
		t.Fatalf("peaks = %v, want nil", peaks)
	}
}
