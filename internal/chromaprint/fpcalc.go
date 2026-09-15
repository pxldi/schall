package chromaprint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNoFingerprint reports that fpcalc could not produce a fingerprint of the
// file. It is a fact about the bytes on disk, which will not decode differently
// next time, so it is never worth retrying and never evidence about identity.
var ErrNoFingerprint = errors.New("the file could not be fingerprinted")

// ErrNoFpcalc reports that the fpcalc program is not installed. Nothing was
// learned about the audio: it is a check that could not run, and a caller must
// record it as one rather than as a refusal.
var ErrNoFpcalc = errors.New("fpcalc is not installed")

const (
	defaultFpcalc = "fpcalc"
	// How long one fpcalc run may take. Decoding is many times faster than
	// playing, so even the fifteen minutes a candidate is read for finishes well
	// inside this; the limit is here to stop a wedged decoder holding a job.
	fpcalcLimit = 2 * time.Minute
)

// Options configures a Fingerprinter.
type Options struct {
	// FpcalcPath is where the fpcalc program is. Empty means look for it on the
	// PATH under its own name.
	FpcalcPath string
	// Run executes fpcalc. Tests replace it; nothing else should.
	Run func(ctx context.Context, path string, args ...string) ([]byte, error)
}

// Fingerprinter computes fingerprints of files with fpcalc.
//
// internal/acoustid also runs fpcalc, and this is deliberately not that. That
// client fingerprints a file in order to ask AcoustID who it is, and reads only
// the opening two minutes, which is all AcoustID wants and all its answer needs.
// A comparison here reads a stated length instead, because a preview cut from
// the middle of a song is not inside the opening two minutes of a copy of it —
// see BitErrorRate. The two runs answer different questions and must not share
// one fingerprint.
type Fingerprinter struct {
	fpcalcPath string
	run        func(ctx context.Context, path string, args ...string) ([]byte, error)
	// Whether run is the real program. A test that supplies its own runner is
	// always able to answer, and must not be told the program is missing because
	// the machine running the test does not have it.
	real bool
}

func NewFingerprinter(options Options) *Fingerprinter {
	print := &Fingerprinter{
		fpcalcPath: strings.TrimSpace(options.FpcalcPath),
		run:        options.Run,
	}
	if print.fpcalcPath == "" {
		print.fpcalcPath = defaultFpcalc
	}
	if print.run == nil {
		print.run, print.real = runCommand, true
	}
	return print
}

// Available reports whether fpcalc can be run at all. It is asked before a
// preview is fetched, so that a missing program is one recorded sentence rather
// than a download per want and a failure per file.
func (print *Fingerprinter) Available() bool {
	if !print.real {
		return true
	}
	_, err := exec.LookPath(print.fpcalcPath)
	return err == nil
}

// Compute returns the compressed fingerprint of the first lengthSeconds of a
// file, and how long the audio it read actually ran.
//
// The compressed form is what comes back, and what a caller stores, because it
// is the form every fingerprint in Schall is already kept in and it decodes back
// to frames exactly. Storing frames instead would be a second format holding the
// same fact.
//
// lengthSeconds is not optional and has no default here on purpose. It is the
// one number that decides whether a later comparison means anything, so a caller
// states it — ReferenceLengthSeconds for a preview, CandidateLengthSeconds for a
// whole file.
func (print *Fingerprinter) Compute(
	ctx context.Context, file string, lengthSeconds int,
) (string, int, error) {
	if lengthSeconds <= 0 {
		return "", 0, fmt.Errorf("%w: no length was stated", ErrNoFingerprint)
	}
	ctx, cancel := context.WithTimeout(ctx, fpcalcLimit)
	defer cancel()

	output, runErr := print.run(ctx, print.fpcalcPath,
		"-json", "-length", strconv.Itoa(lengthSeconds), file)
	if runErr != nil && ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	if runErr != nil && errors.Is(runErr, exec.ErrNotFound) {
		return "", 0, fmt.Errorf("%w: %s", ErrNoFpcalc, print.fpcalcPath)
	}

	var payload struct {
		Duration    float64 `json:"duration"`
		Fingerprint string  `json:"fingerprint"`
	}
	// fpcalc reads past a damaged frame, prints what it computed and then exits
	// non-zero to complain, so the output is read before the exit code is. A run
	// that printed a fingerprint produced one.
	if err := json.Unmarshal(output, &payload); err != nil || payload.Fingerprint == "" {
		if runErr != nil {
			return "", 0, fmt.Errorf("%w: %s", ErrNoFingerprint, firstLine(runErr.Error()))
		}
		return "", 0, fmt.Errorf("%w: fpcalc printed no fingerprint", ErrNoFingerprint)
	}
	return payload.Fingerprint, int(payload.Duration + 0.5), nil
}

// Frames is Compute followed by Decode: the numbers a comparison reads, for a
// file that has not been fingerprinted before.
func (print *Fingerprinter) Frames(
	ctx context.Context, file string, lengthSeconds int,
) ([]uint32, error) {
	value, _, err := print.Compute(ctx, file, lengthSeconds)
	if err != nil {
		return nil, err
	}
	return Decode(value)
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return output, fmt.Errorf("%s: %s", err, firstLine(string(exit.Stderr)))
		}
		return output, err
	}
	return output, nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}
