package transcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/tagging"
	"github.com/rs/zerolog"
	"go.senan.xyz/taglib"
)

// The encoder tests need ffmpeg, which is how the feature works at all: a
// machine without one imports every file as it arrived, and there is nothing to
// assert about an encode that cannot happen.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
}

// makeSource writes one second of a tone into name, with a title and an artist
// on it, and returns the path. It is generated rather than checked in because a
// second of audio is a second of audio and the repository does not need to
// carry one.
func makeSource(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	command := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-metadata", "title=A Tone",
		"-metadata", "artist=The Generator",
		"-y", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("write the source file: %v: %s", err, output)
	}
	return path
}

func planFor(policy Policy, binary string) *Service {
	return NewService(func(context.Context) (Policy, error) {
		return policy, nil
	}, binary, zerolog.Nop())
}

func request(t *testing.T, path string) Request {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("read the source file: %v", err)
	}
	return Request{Source: path, Name: filepath.Base(path), SizeBytes: info.Size()}
}

func TestALosslessFileIsWrittenAsAnMP3OfTheSameLength(t *testing.T) {
	requireFFmpeg(t)
	source := makeSource(t, t.TempDir(), "01 A Tone.wav")
	service := planFor(Policy{
		Enabled: true, Target: TargetMP3, Bitrate: "320", When: WhenLossless,
	}, "")

	placed, cleanup := service.Plan(context.Background(), []Request{request(t, source)})
	defer cleanup()

	if len(placed) != 1 || !placed[0].Transcoded() {
		t.Fatalf("the WAV was not re-encoded: %+v", placed)
	}
	if placed[0].Name != "01 A Tone.mp3" {
		t.Fatalf("name = %q", placed[0].Name)
	}
	if placed[0].FromFormat != "wav" {
		t.Fatalf("from = %q", placed[0].FromFormat)
	}
	written, err := tagging.ReadProperties(placed[0].Source)
	if err != nil {
		t.Fatalf("read the smaller copy: %v", err)
	}
	if written.BitRateKbps < 280 || written.BitRateKbps > 340 {
		t.Fatalf("bit rate = %d kbit/s, wanted about 320", written.BitRateKbps)
	}
	if difference(written.DurationMS, 1000) > ToleranceMS {
		t.Fatalf("duration = %d ms, wanted about 1000", written.DurationMS)
	}
}

// The peer's tags travel with the audio. What a file said about itself is part
// of how it was judged, and the record of that has to survive into the copy the
// library keeps.
func TestTheTagsOnTheFileTravelIntoTheSmallerCopy(t *testing.T) {
	requireFFmpeg(t)
	source := makeSource(t, t.TempDir(), "01 A Tone.wav")
	service := planFor(Policy{
		Enabled: true, Target: TargetMP3, Bitrate: "320", When: WhenLossless,
	}, "")

	placed, cleanup := service.Plan(context.Background(), []Request{request(t, source)})
	defer cleanup()

	tags, err := taglib.ReadTags(placed[0].Source)
	if err != nil {
		t.Fatalf("read the tags on the smaller copy: %v", err)
	}
	if got := strings.Join(tags[taglib.Title], ""); got != "A Tone" {
		t.Fatalf("title = %q", got)
	}
	if got := strings.Join(tags[taglib.Artist], ""); got != "The Generator" {
		t.Fatalf("artist = %q", got)
	}
}

// A variable-rate encode is a different ffmpeg argument, and getting it wrong
// produces a file at the default rate rather than a failure anybody would see.
func TestAVariableRateSettingProducesASmallerCopy(t *testing.T) {
	requireFFmpeg(t)
	source := makeSource(t, t.TempDir(), "01 A Tone.wav")
	service := planFor(Policy{
		Enabled: true, Target: TargetMP3, Bitrate: BitrateV0, When: WhenLossless,
	}, "")

	placed, cleanup := service.Plan(context.Background(), []Request{request(t, source)})
	defer cleanup()

	if !placed[0].Transcoded() {
		t.Fatalf("the WAV was not re-encoded: %+v", placed[0])
	}
	if placed[0].SizeBytes >= placed[0].OriginalSizeBytes {
		t.Fatalf("the copy is %d bytes and the WAV was %d",
			placed[0].SizeBytes, placed[0].OriginalSizeBytes)
	}
}

// The whole point of the fallback: no encoder, no smaller copy, and the music
// is imported exactly as it arrived.
func TestAMissingEncoderImportsTheFileAsItArrived(t *testing.T) {
	requireFFmpeg(t)
	source := makeSource(t, t.TempDir(), "01 A Tone.flac")
	service := planFor(Policy{
		Enabled: true, Target: TargetMP3, Bitrate: "320", When: WhenLossless,
	}, filepath.Join(t.TempDir(), "no-such-encoder"))

	placed, cleanup := service.Plan(context.Background(), []Request{request(t, source)})
	defer cleanup()

	if placed[0].Transcoded() {
		t.Fatal("a copy was reported without an encoder to make one")
	}
	if placed[0].Source != source || placed[0].Name != "01 A Tone.flac" {
		t.Fatalf("placement = %+v, wanted the file that arrived", placed[0])
	}
	if placed[0].Note == "" {
		t.Fatal("nothing was recorded about the re-encode that did not happen")
	}
}

// Two files whose re-encoded names would be the same name. One of them is left
// as it arrived, because a release with two files under one name imports one of
// two different recordings and silently drops the other.
func TestAFileIsLeftAloneWhenItsNewNameIsAlreadyTaken(t *testing.T) {
	requireFFmpeg(t)
	directory := t.TempDir()
	arrivedLossy := makeSource(t, directory, "01 A Tone.mp3")
	arrivedLossless := makeSource(t, directory, "01 A Tone.flac")
	service := planFor(Policy{
		Enabled: true, Target: TargetMP3, Bitrate: "320", When: WhenLossless,
	}, "")

	placed, cleanup := service.Plan(context.Background(),
		[]Request{request(t, arrivedLossy), request(t, arrivedLossless)})
	defer cleanup()

	if placed[1].Transcoded() {
		t.Fatal("the FLAC was written over the MP3's name")
	}
	if placed[0].Name == placed[1].Name {
		t.Fatalf("both files are called %q", placed[0].Name)
	}
	if !strings.Contains(placed[1].Note, "name of another file") {
		t.Fatalf("note = %q", placed[1].Note)
	}
}

func TestAPolicyThatIsOffPlansTheFilesAsTheyArrived(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "01 A Tone.flac")
	if err := os.WriteFile(path, []byte("not really audio"), 0o600); err != nil {
		t.Fatalf("write the source file: %v", err)
	}
	service := planFor(Default(), "")

	placed, cleanup := service.Plan(context.Background(),
		[]Request{{Source: path, Name: "01 A Tone.flac", SizeBytes: 16}})
	defer cleanup()

	if placed[0].Transcoded() || placed[0].Note != "" {
		t.Fatalf("placement = %+v, wanted the file untouched and nothing said about it", placed[0])
	}
}

// A build with no transcoder wired in at all is the state every installation
// was in before this existed, and it has to keep working.
func TestNoTranscoderPlansTheFilesAsTheyArrived(t *testing.T) {
	var service *Service

	placed, cleanup := service.Plan(context.Background(),
		[]Request{{Source: "/music/01.flac", Name: "01.flac", SizeBytes: 42}})
	defer cleanup()

	if len(placed) != 1 || placed[0].Source != "/music/01.flac" || placed[0].SizeBytes != 42 {
		t.Fatalf("placement = %+v", placed)
	}
}

// A lossless file that is already small -- quiet or sparse music -- comes out of
// a fixed-rate encoder bigger than it went in. Re-encoding it would cost a
// generation of quality and buy no room, so it is left as it is.
func TestAFileThatWouldNotShrinkIsLeftAlone(t *testing.T) {
	requireFFmpeg(t)
	source := makeSource(t, t.TempDir(), "01 A Tone.flac")
	service := planFor(Policy{
		Enabled: true, Target: TargetMP3, Bitrate: "320", When: WhenLossless,
	}, "")

	placed, cleanup := service.Plan(context.Background(), []Request{request(t, source)})
	defer cleanup()

	if placed[0].Transcoded() {
		t.Fatal("a copy was kept that is bigger than the file it was made from")
	}
	if !strings.Contains(placed[0].Note, "no smaller") {
		t.Fatalf("note = %q", placed[0].Note)
	}
}
