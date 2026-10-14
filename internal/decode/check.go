// Package decode decodes a file's whole audio stream and reports the first
// error the decoder met. A file with an error is damaged: a player will skip,
// click or stop where the error is.
//
// What it finds is a fact about the copy and never about its identity. A
// damaged file of the right recording is still the right recording, so nothing
// that admits a file reads it. It decides only which copies the upgrade sweep
// asks to replace, and which fetched copies are refused before they reach the
// library.
package decode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ErrUnavailable reports that ffmpeg could not be found. Nothing was learned
// about the file.
var ErrUnavailable = errors.New("the audio decoder is unavailable")

// checkLimit bounds one decode. A two-hour FLAC decodes in well under a minute,
// so a decode still running after this is stuck rather than slow.
const checkLimit = 10 * time.Minute

// maxErrorLength keeps one stored error to a line a person can read.
const maxErrorLength = 300

// Checker decodes files with ffmpeg.
type Checker struct {
	ffmpeg string
	run    RunFunc
	real   bool
}

// RunFunc runs a program and returns what it wrote to stderr and its exit
// status. err is set only when the program could not be run or was stopped.
type RunFunc func(ctx context.Context, name string, args ...string) (stderr []byte, exitCode int, err error)

// Options configures a Checker.
type Options struct {
	// FFmpegPath is where ffmpeg is. Empty means look for it on the PATH.
	FFmpegPath string
	// Run executes a program. Tests replace it; nothing else should.
	Run RunFunc
}

func NewChecker(options Options) *Checker {
	checker := &Checker{ffmpeg: strings.TrimSpace(options.FFmpegPath), run: options.Run}
	if checker.ffmpeg == "" {
		checker.ffmpeg = "ffmpeg"
	}
	if checker.run == nil {
		checker.run, checker.real = runCommand, true
	}
	return checker
}

// Available reports whether ffmpeg can be run at all.
func (checker *Checker) Available() bool {
	if !checker.real {
		return true
	}
	_, err := exec.LookPath(checker.ffmpeg)
	return err == nil
}

// Binary names the program the check runs, for the log line said when it
// cannot be found.
func (checker *Checker) Binary() string { return checker.ffmpeg }

// Check decodes the first audio stream of one file to nowhere and returns the
// first audio error, or "" when the audio decoded cleanly. The error it
// returns is a check that could not run, never a fact about the file.
//
// Only the first audio stream is mapped, so an attached picture is never
// decoded on purpose. ffmpeg still reads the picture while it probes the file
// and can complain about it; see classify.
func (checker *Checker) Check(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, checkLimit)
	defer cancel()
	stderr, exitCode, err := checker.run(ctx, checker.ffmpeg,
		"-nostdin", "-v", "error", "-xerror", "-i", path, "-map", "0:a:0", "-f", "null", "-")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrUnavailable
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("decode %s: %w", path, ctx.Err())
		}
		return "", fmt.Errorf("decode %s: %w", path, err)
	}
	return classify(string(stderr), exitCode), nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stderr = &stderr
	err := command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 && ctx.Err() == nil {
		return stderr.Bytes(), exit.ExitCode(), nil
	}
	return stderr.Bytes(), 0, err
}

// pictureOnly matches the error lines an embedded picture produces. The live
// library had three kinds: a JPEG whose APP fields are broken, a JPEG labelled
// as a PNG, and a picture with no MIME type. All three exit 0 and leave the
// audio alone, so they say nothing about whether the file plays.
var pictureOnly = regexp.MustCompile(
	`^\[(mjpeg|png|bmp|gif|webp|tiff|jpeg2000|jpegls) @ |attached picture`)

// prefix is one "[component @ 0x…]" group ffmpeg puts in front of a message.
// They nest, as in "[aist#0:0/flac @ 0x…] [dec:flac @ 0x…] Decoding error".
var prefix = regexp.MustCompile(`^\[[^\]]*\]\s*`)

// classify reads one decode's stderr and exit status. A file is damaged when
// ffmpeg gave up on it, or when it printed any error that did not come from
// an embedded picture. The first such line is what gets stored.
func classify(stderr string, exitCode int) string {
	first := ""
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || pictureOnly.MatchString(line) {
			continue
		}
		first = line
		break
	}
	if first == "" {
		if exitCode == 0 {
			return ""
		}
		return fmt.Sprintf("ffmpeg could not decode the audio (exit status %d)", exitCode)
	}
	for prefix.MatchString(first) {
		first = prefix.ReplaceAllString(first, "")
	}
	if first == "" {
		first = fmt.Sprintf("ffmpeg could not decode the audio (exit status %d)", exitCode)
	}
	if len(first) > maxErrorLength {
		first = first[:maxErrorLength]
	}
	return first
}
