package chromaprint

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func runner(output string, err error) (
	func(context.Context, string, ...string) ([]byte, error), *[]string,
) {
	var seen []string
	return func(_ context.Context, path string, args ...string) ([]byte, error) {
		seen = append([]string{path}, args...)
		return []byte(output), err
	}, &seen
}

// The length is the one number that decides whether a later comparison means
// anything: fpcalc reads only the opening two minutes unless it is told
// otherwise, and a preview cut from the middle of a song is not inside the
// opening two minutes of a copy of it.
func TestTheStatedLengthIsPassedToFpcalc(t *testing.T) {
	run, seen := runner(`{"duration":30.0,"fingerprint":"AQAB"}`, nil)
	prints := NewFingerprinter(Options{Run: run})

	if _, _, err := prints.Compute(context.Background(), "/music/a.mp3", 900); err != nil {
		t.Fatalf("compute: %v", err)
	}

	if !slices.Contains(*seen, "-length") || !slices.Contains(*seen, "900") {
		t.Fatalf("ran %v, which does not state a length", *seen)
	}
}

func TestAFingerprintWithNoStatedLengthIsRefused(t *testing.T) {
	run, seen := runner(`{"duration":30.0,"fingerprint":"AQAB"}`, nil)
	prints := NewFingerprinter(Options{Run: run})

	_, _, err := prints.Compute(context.Background(), "/music/a.mp3", 0)
	if !errors.Is(err, ErrNoFingerprint) {
		t.Fatalf("reported %v", err)
	}
	if len(*seen) != 0 {
		t.Fatal("fpcalc was run without a length to read to")
	}
}

// fpcalc reads past a damaged frame, prints what it computed, and then exits
// non-zero to complain. A run that printed a fingerprint produced one, so the
// output is read before the exit code is believed.
func TestAComplaintWithAFingerprintBesideItIsStillAFingerprint(t *testing.T) {
	run, _ := runner(`{"duration":30.0,"fingerprint":"AQAB"}`,
		errors.New("ERROR: could not decode frame 41"))
	prints := NewFingerprinter(Options{Run: run})

	value, seconds, err := prints.Compute(context.Background(), "/music/a.mp3", 60)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if value != "AQAB" || seconds != 30 {
		t.Fatalf("read %q of %ds", value, seconds)
	}
}

func TestAComplaintWithNothingBesideItIsAFailure(t *testing.T) {
	run, _ := runner("", errors.New("ERROR: unable to open the file"))
	prints := NewFingerprinter(Options{Run: run})

	_, _, err := prints.Compute(context.Background(), "/music/a.mp3", 60)
	if !errors.Is(err, ErrNoFingerprint) {
		t.Fatalf("reported %v", err)
	}
}

// A missing program is a check that could not run, and it is said with its own
// sentinel so that a caller records it as one rather than as a fact about the
// audio.
func TestAMissingProgramSaysSoInItsOwnWords(t *testing.T) {
	run, _ := runner("", exec.ErrNotFound)
	prints := NewFingerprinter(Options{Run: run})

	_, _, err := prints.Compute(context.Background(), "/music/a.mp3", 60)
	if !errors.Is(err, ErrNoFpcalc) {
		t.Fatalf("reported %v", err)
	}
}

func TestAFingerprinterWithItsOwnRunnerIsAlwaysAvailable(t *testing.T) {
	run, _ := runner(`{"duration":30.0,"fingerprint":"AQAB"}`, nil)
	if !NewFingerprinter(Options{Run: run}).Available() {
		t.Fatal("a fingerprinter that can answer said it could not")
	}
}

func TestAFingerprinterPointedAtNothingIsNotAvailable(t *testing.T) {
	prints := NewFingerprinter(Options{FpcalcPath: "/nowhere/fpcalc-does-not-exist"})
	if prints.Available() {
		t.Fatal("a fingerprinter with no program said it could answer")
	}
}

// The contract against the real binary: what Compute returns is what Decode
// reads, at the length that was asked for.
func TestComputeAndDecodeAgreeOnRealAudio(t *testing.T) {
	if _, err := exec.LookPath("fpcalc"); err != nil {
		t.Skip("fpcalc is not installed; skipping the real-binary contract test")
	}

	path := writeBusyAudio(t, 40)
	prints := NewFingerprinter(Options{})

	value, seconds, err := prints.Compute(context.Background(), path, CandidateLengthSeconds)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if seconds != 40 {
		t.Fatalf("measured %ds of a 40s file", seconds)
	}

	frames, err := prints.Frames(context.Background(), path, CandidateLengthSeconds)
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	decoded, err := Decode(value)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(frames, decoded) {
		t.Fatal("the frames read directly and the frames decoded from the stored value differ")
	}

	// The audio ran for forty seconds, so a fingerprint of it is a few hundred
	// frames. A handful would mean the length was ignored.
	if len(frames) < 200 {
		t.Fatalf("40 seconds of audio came to %d frames", len(frames))
	}
	rate, err := BitErrorRate(frames, frames)
	if err != nil || rate != 0 {
		t.Fatalf("audio did not agree with itself: %v at %v", rate, err)
	}
}

func TestAFingerprinterUsesFpcalcByDefault(t *testing.T) {
	prints := NewFingerprinter(Options{})
	if !strings.HasSuffix(prints.fpcalcPath, "fpcalc") {
		t.Fatalf("looks for %q", prints.fpcalcPath)
	}
}
