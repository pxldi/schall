package chromaprint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPlayableFramesCountsCoverageFromTheFrames(t *testing.T) {
	var seen []string
	prints := NewFingerprinter(Options{
		FpcalcPath: "/bin/fpcalc", FFmpegPath: "/bin/ffmpeg",
		Pipe: func(_ context.Context, ffmpeg, fpcalc, file string, length int) ([]byte, error) {
			seen = append(seen, ffmpeg, fpcalc, file)
			if length != CandidateLengthSeconds {
				t.Fatalf("length = %d", length)
			}
			// fpcalc prints a duration of 0 for a stream; the coverage must
			// come from the frames.
			return []byte(`{"duration":0.0,"fingerprint":[1,2,3,4,5,6,7,8,9]}`), nil
		},
	})
	frames, seconds, err := prints.PlayableFrames(context.Background(), "/music/a.mp3", CandidateLengthSeconds)
	if err != nil {
		t.Fatalf("playable frames: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	if seconds != CoveredSeconds(len(frames)) {
		t.Fatalf("covered %ds for %d frames", seconds, len(frames))
	}
	if len(seen) != 3 || seen[0] != "/bin/ffmpeg" || seen[1] != "/bin/fpcalc" || seen[2] != "/music/a.mp3" {
		t.Fatalf("ran %v", seen)
	}
}

func TestPlayableFramesWithNothingPrintedIsAFailure(t *testing.T) {
	prints := NewFingerprinter(Options{
		Pipe: func(context.Context, string, string, string, int) ([]byte, error) {
			return nil, errors.New("exit status 3")
		},
	})
	_, _, err := prints.PlayableFrames(context.Background(), "/music/a.mp3", CandidateLengthSeconds)
	if !errors.Is(err, ErrNoFingerprint) {
		t.Fatalf("reported %v", err)
	}
}

func TestCoveredSecondsReadsEightFramesASecond(t *testing.T) {
	if got := CoveredSeconds(705); got != 87 {
		t.Fatalf("705 frames covered %ds, want 87", got)
	}
	if got := CoveredSeconds(0); got != 0 {
		t.Fatalf("no frames covered %ds", got)
	}
}

// fpcalc alone stops at the first frame it cannot decode. Read through ffmpeg,
// the same damaged file is fingerprinted to its end.
func TestPlayableFramesReadsPastADamagedFrame(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("fpcalc"); err != nil {
		t.Skip("fpcalc is not installed")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "damaged.mp3")
	if output, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i",
		"aevalsrc=0.3*sin(2*PI*(220+110*floor(mod(t*3\\,8)))*t)+0.2*sin(2*PI*(330+55*floor(mod(t*2\\,5)))*t):s=44100:d=60",
		"-b:a", "192k", path).CombinedOutput(); err != nil {
		t.Fatalf("make audio: %v: %s", err, output)
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	middle := len(audio) / 2
	copy(audio[middle:middle+3000], make([]byte, 3000))
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		t.Fatal(err)
	}

	prints := NewFingerprinter(Options{})
	stopped, err := prints.Frames(ctx, path, CandidateLengthSeconds)
	if err != nil {
		t.Fatalf("fpcalc alone: %v", err)
	}
	frames, seconds, err := prints.PlayableFrames(ctx, path, CandidateLengthSeconds)
	if err != nil {
		t.Fatalf("playable frames: %v", err)
	}
	if CoveredSeconds(len(stopped)) > 40 {
		t.Fatalf("fpcalc alone covered %ds; the fixture is not damaged where it should be",
			CoveredSeconds(len(stopped)))
	}
	if seconds < 55 {
		t.Fatalf("read through ffmpeg covered %ds of 60 (%d frames)", seconds, len(frames))
	}
}
