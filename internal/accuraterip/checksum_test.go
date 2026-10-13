package accuraterip

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// frames is ten thousand pseudo-random stereo frames, the same ones the
// expected checksums below were computed over with ARver's C routine
// (arver/audio/_audio.c) transcribed line by line.
func frames(count int) []byte {
	samples := make([]byte, 0, count*4)
	state := uint32(12345)
	for range count {
		state = state*1103515245 + 12345
		samples = binary.LittleEndian.AppendUint32(samples, state)
	}
	return samples
}

func TestTheChecksumsMatchARversForATrackInTheMiddle(t *testing.T) {
	assertChecksums(t, false, false, Checksums{V1: 0x8915f270, V2: 0x8a90b61b})
}

func TestTheFirstTrackLeavesOutItsOpeningSectors(t *testing.T) {
	assertChecksums(t, true, false, Checksums{V1: 0xb59935e0, V2: 0xb6f25adc})
}

func TestTheLastTrackLeavesOutItsClosingSectors(t *testing.T) {
	assertChecksums(t, false, true, Checksums{V1: 0x78b83b44, V2: 0x7975de9a})
}

func TestTheOnlyTrackLeavesOutBothEdges(t *testing.T) {
	assertChecksums(t, true, true, Checksums{V1: 0xa53b7eb4, V2: 0xa5d7835b})
}

func assertChecksums(t *testing.T, first, last bool, want Checksums) {
	t.Helper()
	sums, err := Sum(bytes.NewReader(frames(10000)))
	if err != nil {
		t.Fatalf("Sum() error = %v", err)
	}
	if sums.Frames != 10000 {
		t.Errorf("frames = %d, want 10000", sums.Frames)
	}
	if got := sums.Checksums(first, last); got != want {
		t.Errorf("checksums = %08x/%08x, want %08x/%08x", got.V1, got.V2, want.V1, want.V2)
	}
}

func TestAudioCutInsideAFrameIsNotCDAudio(t *testing.T) {
	if _, err := Sum(bytes.NewReader(frames(10000)[:39999])); !errors.Is(err, ErrNotCDAudio) {
		t.Fatalf("Sum() error = %v, want ErrNotCDAudio", err)
	}
}

func TestAudioShorterThanBothEdgesIsNotCDAudio(t *testing.T) {
	if _, err := Sum(bytes.NewReader(frames(2 * skippedFrames))); !errors.Is(err, ErrNotCDAudio) {
		t.Fatalf("Sum() error = %v, want ErrNotCDAudio", err)
	}
}
