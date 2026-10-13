package accuraterip

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNoDecoder reports that ffmpeg could not be found, so no copy can be
// summed.
var ErrNoDecoder = errors.New("ffmpeg could not be found")

// Decoder sums a file's audio with ffmpeg.
type Decoder struct {
	ffmpeg string
}

// NewDecoder registers the ffmpeg to decode with. Empty means "find ffmpeg on
// PATH".
func NewDecoder(path string) Decoder {
	if strings.TrimSpace(path) == "" {
		path = "ffmpeg"
	}
	return Decoder{ffmpeg: path}
}

// Available reports whether ffmpeg can be found.
func (decoder Decoder) Available() bool {
	_, err := exec.LookPath(decoder.ffmpeg)
	return err == nil
}

// Sum decodes a file's first audio stream to the samples a CD holds and sums
// them. Nothing is resampled or remixed: audio that is not 44.1 kHz stereo is
// turned away before this is asked, and a file that was converted on its way
// here sums to something no disc holds.
func (decoder Decoder) Sum(ctx context.Context, path string) (Sums, error) {
	if !decoder.Available() {
		return Sums{}, ErrNoDecoder
	}
	command := exec.CommandContext(ctx, decoder.ffmpeg,
		"-nostdin", "-v", "error", "-i", path,
		"-map", "0:a:0", "-f", "s16le", "-c:a", "pcm_s16le", "-")
	output, err := command.StdoutPipe()
	if err != nil {
		return Sums{}, err
	}
	var failure strings.Builder
	command.Stderr = &failure
	if err := command.Start(); err != nil {
		return Sums{}, fmt.Errorf("start ffmpeg: %w", err)
	}
	sums, sumErr := Sum(output)
	if sumErr != nil {
		// Whatever is left unread would block ffmpeg on a full pipe.
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if sumErr != nil {
		return Sums{}, sumErr
	}
	if waitErr != nil {
		return Sums{}, fmt.Errorf("decode %s: %w: %s", path, waitErr, strings.TrimSpace(failure.String()))
	}
	return sums, nil
}
