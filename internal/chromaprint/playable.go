package chromaprint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// FramesPerSecond is how many fingerprint frames fpcalc writes for one second
// of audio. The default algorithm resamples to 11025 Hz and moves a third of
// its 4096-sample frame each step, so one frame is 1365⅓ samples.
const FramesPerSecond = 11025.0 / (4096.0 / 3.0)

// CoveredSeconds is how much audio a fingerprint of this many frames was
// computed from. It is read off the frames because fpcalc prints a file's
// container duration even when it stopped reading at a damaged frame, and
// prints 0 when it read a stream.
func CoveredSeconds(frames int) int {
	if frames <= 0 {
		return 0
	}
	return int(float64(frames)/FramesPerSecond + 0.5)
}

// PipeFunc decodes a file with ffmpeg and fingerprints the decoded stream with
// fpcalc, and returns what fpcalc printed: JSON with the raw frames. Tests
// replace it; nothing else should.
type PipeFunc func(ctx context.Context, ffmpeg, fpcalc, file string, lengthSeconds int) ([]byte, error)

// PlayableFrames fingerprints the audio a player would play from a file, read
// past any damaged frame.
//
// fpcalc stops at the first frame it cannot decode, so a damaged file read by
// fpcalc alone gives a fingerprint of its opening part only. ffmpeg decodes
// past such a frame, and fpcalc reads ffmpeg's output instead. A skipped frame
// shifts the rest of the audio by a fraction of a frame, which can only raise
// the measured difference, never lower it.
//
// It returns the frames and how many seconds of audio they cover, counted from
// the frames.
func (print *Fingerprinter) PlayableFrames(
	ctx context.Context, file string, lengthSeconds int,
) ([]uint32, int, error) {
	if lengthSeconds <= 0 {
		return nil, 0, fmt.Errorf("%w: no length was stated", ErrNoFingerprint)
	}
	ctx, cancel := context.WithTimeout(ctx, fpcalcLimit)
	defer cancel()

	output, runErr := print.pipe(ctx, print.ffmpegPath, print.fpcalcPath, file, lengthSeconds)
	if runErr != nil && ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	if runErr != nil && errors.Is(runErr, exec.ErrNotFound) {
		return nil, 0, fmt.Errorf("%w: %s", ErrNoFpcalc, runErr)
	}
	// The frames are asked for raw: nothing stores this fingerprint, so there
	// is no reason to compress it and decode it again.
	var payload struct {
		Fingerprint []uint32 `json:"fingerprint"`
	}
	if err := json.Unmarshal(output, &payload); err != nil || len(payload.Fingerprint) == 0 {
		if runErr != nil {
			return nil, 0, fmt.Errorf("%w: %s", ErrNoFingerprint, firstLine(runErr.Error()))
		}
		return nil, 0, fmt.Errorf("%w: fpcalc printed no fingerprint", ErrNoFingerprint)
	}
	return payload.Fingerprint, CoveredSeconds(len(payload.Fingerprint)), nil
}

func runPipe(ctx context.Context, ffmpeg, fpcalc, file string, lengthSeconds int) ([]byte, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	decoder := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-v", "error", "-i", file, "-map", "0:a:0", "-f", "wav", "-")
	decoder.Stdout = writer
	printer := exec.CommandContext(ctx, fpcalc,
		"-raw", "-json", "-length", strconv.Itoa(lengthSeconds), "-")
	printer.Stdin = reader
	var output, complaint bytes.Buffer
	printer.Stdout, printer.Stderr = &output, &complaint

	if err := decoder.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	if err := printer.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = decoder.Process.Kill()
		_ = decoder.Wait()
		return nil, err
	}
	// Both children hold their own copies of the pipe. Closing ours lets fpcalc
	// see the end of the stream when ffmpeg exits.
	_ = reader.Close()
	_ = writer.Close()
	printErr := printer.Wait()
	// ffmpeg exits non-zero on a damaged file, and on a closed pipe once fpcalc
	// has read its length. Neither says anything about the fingerprint.
	_ = decoder.Wait()
	if printErr != nil && complaint.Len() > 0 {
		printErr = fmt.Errorf("%s: %s", printErr, firstLine(strings.TrimSpace(complaint.String())))
	}
	return output.Bytes(), printErr
}
