package chromaprint

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The golden vectors prove the decoder against two recorded answers. This
// proves it against fpcalc itself, which is the part that breaks silently if
// the tool ever changes what it packs — the compressed form is the only form
// Schall stores, so a decoder that drifts would quietly compare the wrong
// numbers rather than fail.
func TestDecodeAgreesWithFpcalcOnFreshAudio(t *testing.T) {
	if _, err := exec.LookPath("fpcalc"); err != nil {
		t.Skip("fpcalc is not installed; skipping the real-binary contract test")
	}

	path := writeBusyAudio(t, 40)
	compressed := fingerprint(t, path)
	raw := fingerprint(t, path, "-raw")

	want := make([]uint32, 0, 400)
	for _, field := range strings.Split(raw, ",") {
		if field == "" {
			continue
		}
		value, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			t.Fatalf("fpcalc printed %q, which is not a frame: %v", field, err)
		}
		want = append(want, uint32(value))
	}
	if len(want) == 0 {
		t.Fatal("fpcalc printed no raw frames")
	}

	got, err := Decode(compressed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d frames, fpcalc printed %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("frame %d of %d is %d, fpcalc printed %d", index, len(want), got[index], want[index])
		}
	}
}

func fingerprint(t *testing.T, path string, extra ...string) string {
	t.Helper()
	output, err := exec.Command("fpcalc", append(extra, "-length", "900", path)...).Output()
	if err != nil {
		t.Fatalf("fpcalc %v: %v", extra, err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if after, ok := strings.CutPrefix(line, "FINGERPRINT="); ok {
			return strings.TrimSpace(after)
		}
	}
	t.Fatalf("fpcalc printed no fingerprint: %s", output)
	return ""
}

// writeBusyAudio writes a WAV file of several tones moving against each other.
// It is generated rather than checked in so the test needs no fixture and no
// encoder. It is busier than a single tone on purpose: a flat note packs into
// small numbers only, and the part of the format most worth testing is the
// exception section, which is reached only when a frame's numbers are large.
func writeBusyAudio(t *testing.T, seconds int) string {
	t.Helper()
	const rate = 44100
	// A fixed generator, so a failure is the same failure every time.
	noise := uint32(0x5eed1e55)

	var body bytes.Buffer
	for index := 0; index < rate*seconds; index++ {
		at := float64(index) / rate
		value := 0.35 * math.Sin(2*math.Pi*(220+40*math.Sin(2*math.Pi*at/5))*at)
		value += 0.25 * math.Sin(2*math.Pi*(587+90*math.Sin(2*math.Pi*at/3))*at)
		value += 0.20 * math.Sin(2*math.Pi*(1046+300*math.Sin(2*math.Pi*at/1.7))*at)
		noise = noise*1664525 + 1013904223
		value += 0.10 * (float64(noise>>8)/float64(1<<24) - 0.5)
		_ = binary.Write(&body, binary.LittleEndian, int16(math.Tanh(value)*math.MaxInt16*0.7))
	}

	var file bytes.Buffer
	file.WriteString("RIFF")
	_ = binary.Write(&file, binary.LittleEndian, uint32(36+body.Len()))
	file.WriteString("WAVEfmt ")
	_ = binary.Write(&file, binary.LittleEndian, uint32(16))
	_ = binary.Write(&file, binary.LittleEndian, uint16(1))    // PCM
	_ = binary.Write(&file, binary.LittleEndian, uint16(1))    // mono
	_ = binary.Write(&file, binary.LittleEndian, uint32(rate)) // sample rate
	_ = binary.Write(&file, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(&file, binary.LittleEndian, uint16(2))
	_ = binary.Write(&file, binary.LittleEndian, uint16(16))
	file.WriteString("data")
	_ = binary.Write(&file, binary.LittleEndian, uint32(body.Len()))
	file.Write(body.Bytes())

	path := filepath.Join(t.TempDir(), "busy.wav")
	if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	return path
}
