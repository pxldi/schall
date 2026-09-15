package tagging

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures are a second of silence encoded four ways. What matters is not
// the exact numbers — those belong to ffmpeg — but that every format the
// library holds answers at all, and that an MP3 gives up a length here where
// the scanner can measure none.
func TestReadPropertiesReadsEveryFormat(t *testing.T) {
	for _, name := range []string{"silence.flac", "silence.mp3", "silence.m4a", "silence.wav"} {
		t.Run(name, func(t *testing.T) {
			properties, err := ReadProperties(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			if !properties.Known() {
				t.Fatalf("properties = %+v, want a sample rate", properties)
			}
			if properties.Channels == 0 {
				t.Fatalf("channels = 0, want the stream's own count")
			}
			if properties.DurationMS < 500 || properties.DurationMS > 2000 {
				t.Fatalf("duration = %dms, want about a second", properties.DurationMS)
			}
		})
	}
}

// A file that is not audio says nothing, and says it without complaining:
// taglib hands back an empty answer rather than an error. So the caller cannot
// tell the two apart by the error and must not try — Known is the whole test,
// and it is why the comparison that reads these numbers checks for a sample
// rate rather than for a successful read.
func TestReadPropertiesSaysNothingAboutWhatIsNotAudio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-audio.mp3")
	if err := os.WriteFile(path, []byte("this is not a stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	properties, err := ReadProperties(path)
	if err != nil {
		t.Fatal(err)
	}
	if properties.Known() {
		t.Fatalf("properties = %+v, want nothing said", properties)
	}
}
