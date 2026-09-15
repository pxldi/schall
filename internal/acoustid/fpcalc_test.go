package acoustid

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeTone writes a WAV file of a sine tone. It is generated rather than
// checked in so the test needs no fixture and no encoder: fpcalc decodes WAV
// directly, and what is being tested is the plumbing around it, not the audio.
func writeTone(t *testing.T, seconds int) string {
	t.Helper()
	const rate = 44100
	samples := rate * seconds

	var body bytes.Buffer
	for index := 0; index < samples; index++ {
		value := math.Sin(2 * math.Pi * 440 * float64(index) / rate)
		// A little movement over time, so the fingerprint is not one flat note.
		value *= 0.6 + 0.4*math.Sin(2*math.Pi*float64(index)/(rate*3))
		_ = binary.Write(&body, binary.LittleEndian, int16(value*math.MaxInt16*0.5))
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

	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
		t.Fatalf("write tone: %v", err)
	}
	return path
}

// The fake runner proves the parsing; this proves the contract with the real
// binary, which is the part that breaks silently when fpcalc changes its output
// or is missing from the image altogether.
func TestFingerprintUsesTheRealFpcalcBinary(t *testing.T) {
	if _, err := exec.LookPath(defaultFpcalc); err != nil {
		t.Skip("fpcalc is not installed; skipping the real-binary contract test")
	}

	client := NewClient(Options{})
	print, err := client.Fingerprint(context.Background(), writeTone(t, 12))
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if print.Value == "" {
		t.Fatal("fpcalc returned no fingerprint for decodable audio")
	}
	if print.DurationSeconds != 12 {
		t.Fatalf("duration = %d, want the 12 seconds that were written", print.DurationSeconds)
	}
}

func TestFingerprintRefusesAFileThatIsNotAudio(t *testing.T) {
	if _, err := exec.LookPath(defaultFpcalc); err != nil {
		t.Skip("fpcalc is not installed; skipping the real-binary contract test")
	}

	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("this is not audio"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := NewClient(Options{}).Fingerprint(context.Background(), path); err == nil {
		t.Fatal("a text file was fingerprinted")
	}
}
