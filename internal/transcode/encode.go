package transcode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnavailable reports that no encoder could be found. It costs the smaller
// copy and nothing else: the music is imported as it arrived, which is what an
// installation without ffmpeg has always done.
var ErrUnavailable = errors.New("no audio encoder is available")

// encodeCap bounds one file. A long lossless track takes tens of seconds on a
// small machine; ten minutes is far past that and still short enough that a
// wedged encoder does not hold an import open all day.
const encodeCap = 10 * time.Minute

// ToleranceMS is how far the re-encoded copy may be from the length of what it
// was made from. An encoder pads the last frame, so the two are never exactly
// equal; a whole second is generous for that and nowhere near a truncated file.
const ToleranceMS = 1000

// Encoder runs ffmpeg. binary may be empty, meaning ffmpeg is looked for on
// PATH; not finding it is reported when a copy is encoded rather than at
// startup, because Schall imports perfectly well without one.
type Encoder struct {
	binary string
}

func NewEncoder(binary string) *Encoder {
	if binary == "" {
		binary = "ffmpeg"
	}
	return &Encoder{binary: binary}
}

// Encode writes source into destination as the policy asks.
//
// It encodes beside the name and renames it on, so an encode that stopped half
// way is never taken for a finished one. The caller's context bounds it as well
// as encodeCap does: an import that was cancelled must not keep a CPU busy.
func (encoder *Encoder) Encode(ctx context.Context, policy Policy, source, destination string) error {
	runCtx, cancel := context.WithTimeout(ctx, encodeCap)
	defer cancel()

	partial := destination + ".part"
	arguments := []string{
		"-nostdin", "-loglevel", "error",
		"-i", source,
		// Audio only, first stream. Cover art in a FLAC is a video stream to
		// ffmpeg, and an import that turned an album cover into a track is not
		// what anybody asked for.
		"-vn", "-map", "0:a:0",
		"-c:a", "libmp3lame",
	}
	if policy.Bitrate == BitrateV0 {
		arguments = append(arguments, "-q:a", "0")
	} else {
		arguments = append(arguments, "-b:a", policy.Bitrate+"k")
	}
	arguments = append(arguments,
		// The peer's tags travel with the audio. What the file said about itself
		// is part of how it was judged, and the record of that has to survive
		// into the copy the library keeps — the tagging pass runs after this and
		// rewrites what Schall knows on top.
		"-map_metadata", "0",
		"-id3v2_version", "3",
		"-f", "mp3", "-y", partial,
	)

	command := exec.CommandContext(runCtx, encoder.binary, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		_ = os.Remove(partial)
		// Not found comes back two ways: as ErrNotFound when the name was looked
		// for on PATH, and as a missing file when it was given as a path.
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s could not be found", ErrUnavailable, encoder.binary)
		}
		return fmt.Errorf("re-encode %s: %w: %s",
			filepath.Base(source), err, truncate(string(output)))
	}
	if err := os.Rename(partial, destination); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("store the re-encoded copy: %w", err)
	}
	return nil
}

// Available reports whether the encoder can be found, without running it. It
// is the same check the settings page makes before letting the policy be
// turned on, exposed here so a pass over the whole library can make it once
// per batch instead of once per file — the difference between one failed
// lookup and ten thousand of them on a machine with no ffmpeg at all.
func (encoder *Encoder) Available() bool {
	binary := encoder.binary
	if strings.ContainsRune(binary, os.PathSeparator) {
		info, err := os.Stat(binary)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(binary)
	return err == nil
}

func truncate(value string) string {
	const limit = 200
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
