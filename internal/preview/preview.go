// Package preview turns a file Schall is holding into something a browser will
// play, so a copy can be listened to before anybody decides about it.
//
// It exists because the last question in the review queue is one no identifier
// can answer. A copy reaches that queue precisely when nothing could identify it,
// and at that point the only remaining evidence is the audio itself. A player is
// not a convenience here; it is the evidence.
package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ErrUnavailable reports that no transcoder could be found. It costs the preview
// and nothing else: every other way of deciding about a copy still works, so this
// is reported to the caller rather than failing anything at startup.
var ErrUnavailable = errors.New("audio preview is unavailable")

// The encode. Constant bitrate MP3 rather than anything better, because the only
// property that matters here is that every browser can seek in it: a variable
// stream makes the scrubber guess, and a preview you cannot scrub through is a
// preview that only answers questions about the first ten seconds.
const (
	bitRate      = "128k"
	sampleRate   = "44100"
	contentType  = "audio/mpeg"
	transcodeCap = 3 * time.Minute
)

// keepFor bounds how long a transcode is kept. It is a cache of something
// reproducible, and a review session is measured in minutes, so a day is
// generous. Nothing depends on a hit.
const keepFor = 24 * time.Hour

// cacheBudget bounds the cache by size as well as by age. Age alone was enough
// while a transcode only happened because somebody pressed play; warming encodes
// a whole queue before anybody presses anything, and the cache directory is
// scratch space with a limit on it — filling that is not a slow cache but an
// evicted pod. Oldest goes first, which for a cache of something reproducible is
// the least anybody loses.
const cacheBudget = 512 << 20

// warmWorkers bounds how many transcodes run ahead of the user at once. ffmpeg is
// CPU-bound and warming is work nobody is waiting on, so it is deliberately
// narrower than the machine: a queue that warms slightly slower costs nothing,
// and one that starves the request somebody is actually waiting for costs the
// thing warming was for.
const warmWorkers = 2

// Transcoder produces playable copies and remembers them.
type Transcoder struct {
	binary   string
	cacheDir string
	logger   zerolog.Logger

	// budget is cacheBudget, held rather than read so a test can set one it can
	// actually fill.
	budget int64

	// encoding serialises work on one source. Two requests for the same copy are
	// the ordinary case — a player asks for the start, then seeks — and without
	// this they would both run ffmpeg over the same file.
	mu       sync.Mutex
	encoding map[string]*sync.WaitGroup

	// warming counts the workers Warm has running. Nothing in the server waits
	// on it; the tests do, because a worker still renaming a copy into a
	// directory the test is about to remove is a cleanup that fails.
	warming sync.WaitGroup
}

// New prepares a transcoder. binary may be empty, meaning ffmpeg is looked for on
// PATH; a binary that cannot be found is reported when a preview is asked for
// rather than here, because Schall runs perfectly well without one.
func New(binary, cacheDir string, logger zerolog.Logger) *Transcoder {
	if binary == "" {
		binary = "ffmpeg"
	}
	return &Transcoder{
		binary:   binary,
		cacheDir: cacheDir,
		logger:   logger,
		budget:   cacheBudget,
		encoding: make(map[string]*sync.WaitGroup),
	}
}

// ContentType names what Playable produces.
func (transcoder *Transcoder) ContentType() string { return contentType }

// Playable returns the path of a browser-playable copy of source, encoding one if
// there is not one already.
//
// The whole file is encoded rather than the thirty seconds the queue starts
// playback at. A clip would be smaller and would answer one question; a whole
// file served from disk gets range requests, a duration, and a working scrubber
// for free, which is what somebody who is unsure actually needs.
func (transcoder *Transcoder) Playable(ctx context.Context, source string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("read the copy to play: %w", err)
	}
	cached := filepath.Join(transcoder.cacheDir, transcoder.key(source, info)+".mp3")

	// Wait for whoever is already encoding this, then look again: the file they
	// were producing is the file this request wanted.
	if inFlight := transcoder.claim(cached); inFlight != nil {
		inFlight.Wait()
	} else {
		defer transcoder.release(cached)
	}
	if _, err := os.Stat(cached); err == nil {
		return cached, nil
	}

	if err := os.MkdirAll(transcoder.cacheDir, 0o750); err != nil {
		return "", fmt.Errorf("prepare the preview cache: %w", err)
	}
	transcoder.prune()

	if err := transcoder.encode(ctx, source, cached); err != nil {
		return "", err
	}
	return cached, nil
}

// Warm encodes sources ahead of anybody asking for them and returns immediately.
//
// The review queue is worked through rather than dipped into: somebody opening it
// is about to play most of what is on the page, one copy after another, and the
// first few seconds of every one of them were spent watching a spinner. Warming
// moves that work to the moment the page is read, where nobody is waiting on it.
//
// It is best-effort in every direction. A source that cannot be encoded is not
// reported, because nothing asked for it yet and the request that eventually does
// will surface the failure properly. It does not take the caller's context
// either: a warm started while a response is being written must outlive that
// response, or it would be cancelled the instant it began.
func (transcoder *Transcoder) Warm(sources []string) {
	if len(sources) == 0 {
		return
	}
	queue := make(chan string)
	workers := min(warmWorkers, len(sources))
	transcoder.warming.Add(workers)
	for range workers {
		go func() {
			defer transcoder.warming.Done()
			for source := range queue {
				ctx, cancel := context.WithTimeout(context.Background(), transcodeCap)
				if _, err := transcoder.Playable(ctx, source); err != nil {
					transcoder.logger.Debug().Err(err).Str("source", source).
						Msg("warm a preview nobody has asked for yet")
				}
				cancel()
			}
		}()
	}
	go func() {
		defer close(queue)
		for _, source := range sources {
			queue <- source
		}
	}()
}

// settle blocks until every warm started so far has finished, one way or the
// other.
func (transcoder *Transcoder) settle() { transcoder.warming.Wait() }

// encode runs ffmpeg into a temporary name and renames it into place, so a
// transcode interrupted halfway is never served as a complete one.
func (transcoder *Transcoder) encode(ctx context.Context, source, destination string) error {
	runCtx, cancel := context.WithTimeout(ctx, transcodeCap)
	defer cancel()

	partial := destination + ".partial"
	command := exec.CommandContext(runCtx, transcoder.binary,
		"-nostdin", "-loglevel", "error",
		"-i", source,
		// Audio only, first stream. Cover art in a FLAC is a video stream to
		// ffmpeg, and encoding it into an MP3 is not what anybody asked for.
		"-vn", "-map", "0:a:0",
		"-c:a", "libmp3lame", "-b:a", bitRate, "-ar", sampleRate, "-ac", "2",
		"-f", "mp3", "-y", partial,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		os.Remove(partial)
		// Not found comes back two ways: as ErrNotFound when the name was looked
		// for on PATH, and as a missing file when it was given as a path.
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s could not be found", ErrUnavailable, transcoder.binary)
		}
		return fmt.Errorf("transcode %s for playback: %w: %s",
			filepath.Base(source), err, truncate(string(output)))
	}
	if err := os.Rename(partial, destination); err != nil {
		os.Remove(partial)
		return fmt.Errorf("store the transcoded copy: %w", err)
	}
	return nil
}

// key names the encode of one file at one moment. Size and modification time are
// in it because the inbox is a place where files are replaced: the same path an
// hour later may be a different copy entirely, and serving the old transcode
// would be playing somebody the wrong audio while asking them to judge it.
func (transcoder *Transcoder) key(source string, info os.FileInfo) string {
	sum := sha256.Sum256([]byte(source + "\x00" +
		strconv.FormatInt(info.Size(), 10) + "\x00" +
		strconv.FormatInt(info.ModTime().UnixNano(), 10)))
	return hex.EncodeToString(sum[:16])
}

// claim reports the encode already running for this destination, or registers
// this caller as the one running it.
func (transcoder *Transcoder) claim(destination string) *sync.WaitGroup {
	transcoder.mu.Lock()
	defer transcoder.mu.Unlock()
	if running, ok := transcoder.encoding[destination]; ok {
		return running
	}
	mine := &sync.WaitGroup{}
	mine.Add(1)
	transcoder.encoding[destination] = mine
	return nil
}

func (transcoder *Transcoder) release(destination string) {
	transcoder.mu.Lock()
	defer transcoder.mu.Unlock()
	if running, ok := transcoder.encoding[destination]; ok {
		running.Done()
		delete(transcoder.encoding, destination)
	}
}

// prune drops transcodes nobody has asked for in a day, and then drops the
// oldest of what is left until the cache is inside its budget. It is best-effort
// by design: a cache that cannot be tidied is still a working cache, and refusing
// to play something because an old file could not be deleted would be absurd.
//
// The budget matters more than the day does now that previews are warmed. Age
// bounds a cache that only grew when somebody pressed play; size is what bounds
// one that fills itself from a queue.
func (transcoder *Transcoder) prune() {
	entries, err := os.ReadDir(transcoder.cacheDir)
	if err != nil {
		return
	}

	type cached struct {
		name  string
		size  int64
		aged  time.Time
		stale bool
	}
	cutoff := time.Now().Add(-keepFor)
	kept := make([]cached, 0, len(entries))
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			continue
		}
		file := cached{
			name: entry.Name(), size: info.Size(), aged: info.ModTime(),
			stale: info.ModTime().Before(cutoff),
		}
		if file.stale {
			transcoder.drop(file.name)
			continue
		}
		total += file.size
		kept = append(kept, file)
	}

	if total <= transcoder.budget {
		return
	}
	slices.SortFunc(kept, func(a, b cached) int { return a.aged.Compare(b.aged) })
	for _, file := range kept {
		if total <= transcoder.budget {
			return
		}
		if transcoder.drop(file.name) {
			total -= file.size
		}
	}
}

// drop removes one cached transcode, reporting whether it went. A file that will
// not delete is left alone rather than retried: it is reproducible, and the next
// prune will find it again.
func (transcoder *Transcoder) drop(name string) bool {
	if err := os.Remove(filepath.Join(transcoder.cacheDir, name)); err != nil {
		transcoder.logger.Debug().Err(err).Str("file", name).Msg("prune preview cache")
		return false
	}
	return true
}

func truncate(value string) string {
	const limit = 200
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
