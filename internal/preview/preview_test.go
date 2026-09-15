package preview

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// sourceTrack writes a short real audio file to encode from. ffmpeg generates it,
// so the test needs no fixture in the repository and no assumptions about what a
// decoder will accept.
func sourceTrack(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "source.flac")
	command := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-y", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("write the source track: %v: %s", err, output)
	}
	return path
}

// A copy is playable at all: what comes back is a real file with audio in it, not
// an empty placeholder a player would silently refuse.
func TestAHeldCopyIsTurnedIntoSomethingPlayable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source := sourceTrack(t)

	transcoder := New("", t.TempDir(), zerolog.Nop())
	playable, err := transcoder.Playable(ctx, source)
	if err != nil {
		t.Fatalf("Playable() error = %v", err)
	}
	info, err := os.Stat(playable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("the playable copy is empty")
	}
}

// The same copy is encoded once. A player asks for the start and then seeks, so
// re-encoding per request would run ffmpeg over the file every time somebody
// moved the scrubber.
func TestAskingForTheSameCopyTwiceEncodesItOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source := sourceTrack(t)

	transcoder := New("", t.TempDir(), zerolog.Nop())
	first, err := transcoder.Playable(ctx, source)
	if err != nil {
		t.Fatalf("Playable() error = %v", err)
	}
	encoded, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}

	second, err := transcoder.Playable(ctx, source)
	if err != nil {
		t.Fatalf("Playable() a second time error = %v", err)
	}
	if second != first {
		t.Fatalf("second = %s, want the copy already encoded at %s", second, first)
	}
	again, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(encoded.ModTime()) {
		t.Fatal("the copy was encoded again rather than reused")
	}
}

// A file replaced in the inbox is different audio at the same path. Serving the
// transcode of what used to be there would play somebody one recording while
// asking them to judge another.
func TestAReplacedCopyIsEncodedAgainRatherThanServedFromTheOldOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source := sourceTrack(t)

	transcoder := New("", t.TempDir(), zerolog.Nop())
	first, err := transcoder.Playable(ctx, source)
	if err != nil {
		t.Fatalf("Playable() error = %v", err)
	}

	replacement := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=880:duration=4", "-y", source)
	if output, err := replacement.CombinedOutput(); err != nil {
		t.Fatalf("replace the source track: %v: %s", err, output)
	}

	second, err := transcoder.Playable(ctx, source)
	if err != nil {
		t.Fatalf("Playable() after the replacement error = %v", err)
	}
	if second == first {
		t.Fatal("the replaced copy was served as the transcode of the old one")
	}
}

// A missing transcoder costs the preview and says so. Everything else about a
// copy still decides it, so this must be a sentence rather than a failure.
func TestAMissingTranscoderIsReportedRatherThanFailing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source := sourceTrack(t)

	transcoder := New(filepath.Join(t.TempDir(), "no-such-ffmpeg"), t.TempDir(), zerolog.Nop())
	if _, err := transcoder.Playable(ctx, source); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Playable() error = %v, want ErrUnavailable", err)
	}
}

// The cache is scratch space with a limit on it, and warming fills it from a
// queue rather than from what somebody pressed play on. Filling it is not a slow
// cache but an evicted pod, so size bounds it and the oldest goes first.
func TestTheCacheDropsTheOldestTranscodesToStayInsideItsBudget(t *testing.T) {
	cacheDir := t.TempDir()
	transcoder := New("", cacheDir, zerolog.Nop())
	transcoder.budget = 300

	aged := time.Now().Add(-time.Hour)
	for index, name := range []string{"oldest.mp3", "middle.mp3", "newest.mp3"} {
		path := filepath.Join(cacheDir, name)
		if err := os.WriteFile(path, make([]byte, 200), 0o600); err != nil {
			t.Fatal(err)
		}
		when := aged.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}

	transcoder.prune()

	left := cachedNames(t, cacheDir)
	if !slices.Equal(left, []string{"newest.mp3"}) {
		t.Errorf("cache = %v, want only the newest kept inside a 300-byte budget", left)
	}
}

// A cache inside its budget is left alone. Dropping something reproducible costs
// only an encode, but dropping it for no reason costs one every time.
func TestTheCacheKeepsEverythingThatFitsInTheBudget(t *testing.T) {
	cacheDir := t.TempDir()
	transcoder := New("", cacheDir, zerolog.Nop())
	transcoder.budget = 1000

	for _, name := range []string{"one.mp3", "two.mp3"} {
		if err := os.WriteFile(filepath.Join(cacheDir, name), make([]byte, 200), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	transcoder.prune()

	if left := cachedNames(t, cacheDir); len(left) != 2 {
		t.Errorf("cache = %v, want both kept", left)
	}
}

// The type a preview is served as has to be the type it actually is, or the
// browser refuses the bytes and the review queue loses its only evidence.
func TestAPreviewIsServedAsTheTypeItActuallyIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source := sourceTrack(t)

	transcoder := New("", t.TempDir(), zerolog.Nop())
	if got := transcoder.ContentType(); got != "audio/mpeg" {
		t.Fatalf("ContentType() = %q, want audio/mpeg", got)
	}
	playable, err := transcoder.Playable(ctx, source)
	if err != nil {
		t.Fatalf("Playable() error = %v", err)
	}
	header, err := os.ReadFile(playable)
	if err != nil {
		t.Fatal(err)
	}
	// An MP3 file starts either with an ID3 tag or with a frame sync.
	if !bytes.HasPrefix(header, []byte("ID3")) && !(len(header) > 1 && header[0] == 0xFF && header[1]&0xE0 == 0xE0) {
		t.Fatalf("the playable copy does not begin like an MP3: % x", header[:min(4, len(header))])
	}
}

// A copy that is no longer where the library said it was cannot be played, and
// the reason has to reach the caller rather than becoming an empty preview.
func TestACopyThatIsNoLongerThereIsReportedRatherThanPlayed(t *testing.T) {
	transcoder := New("", t.TempDir(), zerolog.Nop())

	_, err := transcoder.Playable(context.Background(), filepath.Join(t.TempDir(), "gone.flac"))

	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Playable() error = %v, want the missing copy reported", err)
	}
}

// The very first preview asked for arrives before the cache directory exists.
func TestTheFirstPreviewCreatesTheCacheItIsStoredIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source := sourceTrack(t)
	cacheDir := filepath.Join(t.TempDir(), "previews")

	playable, err := New("", cacheDir, zerolog.Nop()).Playable(ctx, source)
	if err != nil {
		t.Fatalf("Playable() error = %v", err)
	}
	if filepath.Dir(playable) != cacheDir {
		t.Fatalf("playable = %s, want it stored in %s", playable, cacheDir)
	}
}

// A cache that cannot be created is reported instead of being transcoded into
// nowhere.
func TestACacheThatCannotBeCreatedIsReported(t *testing.T) {
	source := sourceTrack(t)
	occupied := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(occupied, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New("", filepath.Join(occupied, "previews"), zerolog.Nop()).
		Playable(context.Background(), source)

	if err == nil || !strings.Contains(err.Error(), "prepare the preview cache") {
		t.Fatalf("Playable() error = %v, want the cache failure reported", err)
	}
}

// A player asks for the start and then seeks, and the review queue warms ahead
// of both. Two requests for the same copy arriving together must not each run a
// transcode over the same file.
func TestTwoRequestsForTheSameCopyRunOneTranscode(t *testing.T) {
	cacheDir := t.TempDir()
	runs := filepath.Join(t.TempDir(), "runs")
	transcoder := New(fakeTranscoder(t, runs, 0, ""), cacheDir, zerolog.Nop())
	source := filepath.Join(t.TempDir(), "source.flac")
	if err := os.WriteFile(source, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	results := make([]string, 2)
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			playable, err := transcoder.Playable(context.Background(), source)
			if err != nil {
				t.Errorf("Playable() error = %v", err)
				return
			}
			results[index] = playable
		}()
	}
	wait.Wait()

	if results[0] != results[1] {
		t.Fatalf("the two requests were answered with %q and %q", results[0], results[1])
	}
	if got := invocations(t, runs); got != 1 {
		t.Fatalf("the transcoder ran %d times, want the second request to wait for the first", got)
	}
}

// A transcode that failed says what the transcoder said, because that is the
// only place the reason exists. A whole ffmpeg log in an API response is not
// that reason, so it is cut.
func TestATranscodeThatFailedReportsWhatTheTranscoderSaidWithoutAllOfIt(t *testing.T) {
	noise := strings.Repeat("z", 4096)
	transcoder := New(fakeTranscoder(t, "", 1, noise), t.TempDir(), zerolog.Nop())
	source := filepath.Join(t.TempDir(), "source.flac")
	if err := os.WriteFile(source, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := transcoder.Playable(context.Background(), source)

	if err == nil || !strings.Contains(err.Error(), "transcode source.flac for playback") {
		t.Fatalf("Playable() error = %v, want the failure named", err)
	}
	if len(err.Error()) > 500 {
		t.Fatalf("the error is %d characters long; the transcoder's whole log reached the caller",
			len(err.Error()))
	}
}

// A transcode interrupted halfway must never be served as a complete one, so the
// partial file is not left behind for the next request to find.
func TestAFailedTranscodeLeavesNothingBehindToBeServed(t *testing.T) {
	cacheDir := t.TempDir()
	transcoder := New(fakeTranscoder(t, "", 1, "broken"), cacheDir, zerolog.Nop())
	source := filepath.Join(t.TempDir(), "source.flac")
	if err := os.WriteFile(source, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := transcoder.Playable(context.Background(), source); err == nil {
		t.Fatal("Playable() reported success on a failed transcode")
	}

	if left := cachedNames(t, cacheDir); len(left) != 0 {
		t.Fatalf("cache = %v, want a failed transcode to leave nothing", left)
	}
}

// Warming is what moves the wait to the moment the review page is read, so a
// copy nobody has pressed play on yet is encoded anyway.
func TestWarmingEncodesCopiesNobodyHasAskedForYet(t *testing.T) {
	cacheDir := t.TempDir()
	transcoder := New(fakeTranscoder(t, "", 0, ""), cacheDir, zerolog.Nop())
	sources := make([]string, 0, 3)
	folder := t.TempDir()
	for _, name := range []string{"a.flac", "b.flac", "c.flac"} {
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, path)
	}

	transcoder.Warm(sources)

	deadline := time.Now().Add(10 * time.Second)
	for len(cachedNames(t, cacheDir)) < len(sources) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if left := cachedNames(t, cacheDir); len(left) != len(sources) {
		t.Fatalf("cache = %v, want every warmed copy encoded", left)
	}
}

// Warming nothing is nothing to do, and must not leave workers waiting on a
// queue that never fills.
func TestWarmingNothingDoesNothing(t *testing.T) {
	cacheDir := t.TempDir()

	New(fakeTranscoder(t, "", 0, ""), cacheDir, zerolog.Nop()).Warm(nil)

	if left := cachedNames(t, cacheDir); len(left) != 0 {
		t.Fatalf("cache = %v, want nothing encoded", left)
	}
}

// A copy that could not be warmed is not reported: nothing asked for it, and the
// request that eventually does will surface the failure properly.
func TestACopyThatCouldNotBeWarmedIsNotReported(t *testing.T) {
	cacheDir := t.TempDir()
	transcoder := New(fakeTranscoder(t, "", 1, "no"), cacheDir, zerolog.Nop())
	source := filepath.Join(t.TempDir(), "source.flac")
	if err := os.WriteFile(source, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	transcoder.Warm([]string{source})

	time.Sleep(100 * time.Millisecond)
	if left := cachedNames(t, cacheDir); len(left) != 0 {
		t.Fatalf("cache = %v, want the failed warm to leave nothing", left)
	}
}

// A transcode nobody has asked for in a day is dropped whatever the cache is
// using, because a cache of something reproducible is not an archive.
func TestATranscodeNobodyHasAskedForInADayIsDropped(t *testing.T) {
	cacheDir := t.TempDir()
	transcoder := New("", cacheDir, zerolog.Nop())

	for name, age := range map[string]time.Duration{
		"stale.mp3": keepFor + time.Hour,
		"fresh.mp3": time.Hour,
	} {
		path := filepath.Join(cacheDir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}

	transcoder.prune()

	if left := cachedNames(t, cacheDir); !slices.Equal(left, []string{"fresh.mp3"}) {
		t.Fatalf("cache = %v, want only the transcode somebody asked for recently", left)
	}
}

// Only transcodes are the cache's to remove. Anything else that turns up in the
// directory is left where it is.
func TestPruningLeavesDirectoriesInTheCacheAlone(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(cacheDir, "somebody-elses"), 0o750); err != nil {
		t.Fatal(err)
	}
	transcoder := New("", cacheDir, zerolog.Nop())
	transcoder.budget = 0

	transcoder.prune()

	if left := cachedNames(t, cacheDir); !slices.Equal(left, []string{"somebody-elses"}) {
		t.Fatalf("cache = %v, want the directory left alone", left)
	}
}

// A cached entry that will not delete is left alone rather than retried: it is
// reproducible, and the next prune finds it again.
func TestAnEntryThatWillNotDeleteIsReportedAsStillThere(t *testing.T) {
	cacheDir := t.TempDir()
	// A directory with something in it is the one thing os.Remove refuses to
	// anybody, whatever the process is allowed to do.
	if err := os.MkdirAll(filepath.Join(cacheDir, "stuck", "inside"), 0o750); err != nil {
		t.Fatal(err)
	}
	transcoder := New("", cacheDir, zerolog.Nop())

	if transcoder.drop("stuck") {
		t.Fatal("an entry that could not be removed was reported as dropped")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "stuck")); err != nil {
		t.Fatalf("the entry that would not delete is gone: %v", err)
	}
}

// The cache is scratch space, and scratch space is something else's to clear. A
// transcode whose output vanished before it was renamed into place must be
// reported, never served as a complete one.
func TestATranscodeWhoseCacheDisappearedIsReportedRatherThanServed(t *testing.T) {
	cacheDir := t.TempDir()
	transcoder := New(fakeTranscoder(t, "", 0, "", "rm -rf \"$(dirname \"$destination\")\""),
		cacheDir, zerolog.Nop())
	source := filepath.Join(t.TempDir(), "source.flac")
	if err := os.WriteFile(source, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := transcoder.Playable(context.Background(), source)

	if err == nil || !strings.Contains(err.Error(), "store the transcoded copy") {
		t.Fatalf("Playable() error = %v, want the copy that could not be stored reported", err)
	}
}

// A cache directory that is not there yet has nothing in it to prune, and
// pruning must not conjure one either — the request that needs it makes it.
func TestPruningACacheThatIsNotThereYetDoesNothing(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "not-yet")

	New("", cacheDir, zerolog.Nop()).prune()

	if _, err := os.Stat(cacheDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat = %v, want the cache still not there", err)
	}
}

// fakeTranscoder writes a stand-in for ffmpeg: it produces the output file it
// was told to write, records that it ran, and can be made to fail with as much
// noise as a real one does. Mocking here is mocking the process boundary — the
// tests above are about what happens around a transcode, not inside one.
func fakeTranscoder(t *testing.T, runs string, exit int, noise string, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg")
	script := "#!/bin/sh\n"
	if runs != "" {
		script += "echo ran >> " + strconv.Quote(runs) + "\n"
	}
	if noise != "" {
		script += "echo " + strconv.Quote(noise) + " >&2\n"
	}
	// The destination is the last argument ffmpeg is given.
	script += "for argument in \"$@\"; do destination=\"$argument\"; done\n" +
		"sleep 0.05\n"
	if exit == 0 {
		script += "printf 'ID3\\4\\0\\0\\0\\0\\0\\0' > \"$destination\"\n"
	}
	for _, line := range extra {
		script += line + "\n"
	}
	script += "exit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func invocations(t *testing.T, runs string) int {
	t.Helper()
	recorded, err := os.ReadFile(runs)
	if err != nil {
		t.Fatalf("read the transcoder's record: %v", err)
	}
	return len(strings.Fields(string(recorded)))
}

func cachedNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}
