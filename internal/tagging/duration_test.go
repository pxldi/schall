package tagging

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Every fixture below is one second of digital silence, made with ffmpeg,
// mono, at 8 kHz where the format allows a rate that low. One second is the
// known truth each reader is checked against: it is what ffmpeg was asked for
// and what ffprobe reads back out of the finished file.
//
// The tolerance is one millisecond, and it is a rounding allowance rather than
// a tolerance. Every one of these containers states its length exactly; what is
// inexact is only that a sample count divided by a sample rate does not always
// land on a whole millisecond. Anything wider than this would stop the tests
// noticing the errors that actually happen here -- an Opus pre-skip left on is
// six milliseconds, an mvhd read at the wrong version offset is a factor, an
// 80-bit exponent off by one is a factor of two.
const (
	fixtureMS  = 1000
	roundingMS = 1
)

func TestCountedDurationReadsEveryFormatTheLibraryHolds(t *testing.T) {
	for _, fixture := range []struct {
		name string
		want int
	}{
		// MPEG is the one format whose file is honestly longer than the second
		// that went into it. There is no length field to trim against, so the
		// encoder's own run-up is in the stream as frames and the frames are
		// what there is to count. At 8 kHz a Layer III frame of MPEG 2.5 holds
		// 576 samples, which is 72 ms: a second needs fourteen of them and lame
		// writes two more for its delay, so sixteen frames, so 1152 ms. The
		// count is exact; it is the file that is long. At 44.1 kHz the same
		// overshoot is about 26 ms, and the tolerance a match is judged at is
		// five seconds.
		{"silence.mp3", 1152},
		{"silence.flac", fixtureMS},
		{"silence.ogg", fixtureMS},
		{"silence.opus", fixtureMS},
		{"silence.m4a", fixtureMS},
		{"silence.wav", fixtureMS},
		{"silence.aiff", fixtureMS},
		// DSD is one bit per sample at sixty-four times a CD's rate, so a
		// second of it is a third of a megabyte and no fixture in a repository
		// is going to be one. This is a single 4096-byte block: 32768 samples
		// at 2822400 Hz, which is 11.61 ms and is a real DSF header saying so.
		{"silence.dsf", 11},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			counted, err := CountedDurationMS(filepath.Join("testdata", fixture.name))
			if err != nil {
				t.Fatal(err)
			}
			if counted < fixture.want-roundingMS || counted > fixture.want+roundingMS {
				t.Fatalf("counted = %dms, want %dms", counted, fixture.want)
			}
		})
	}
}

// The extensions that share a reader with a fixture still have to reach it. A
// dispatcher that routes .m4a and forgets .m4b stores nothing for half the
// audiobooks in the library and says nothing about it.
func TestCountedDurationReachesEveryExtensionOfAFamily(t *testing.T) {
	for _, family := range []struct {
		fixture    string
		extensions []string
	}{
		{"silence.ogg", []string{".oga"}},
		{"silence.m4a", []string{".m4b", ".m4p", ".mp4"}},
		{"silence.aiff", []string{".aif"}},
	} {
		for _, extension := range family.extensions {
			t.Run(extension, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "track"+extension)
				copyFixture(t, family.fixture, path)

				counted, err := CountedDurationMS(path)
				if err != nil {
					t.Fatal(err)
				}
				if counted < fixtureMS-roundingMS || counted > fixtureMS+roundingMS {
					t.Fatalf("counted = %dms, want %dms", counted, fixtureMS)
				}
			})
		}
	}
}

// The Opus trap, pinned on its own because it is the one that produces a
// plausible wrong answer rather than an obvious one. The fixture's last granule
// is 48312 at 48 kHz; read naively that is 1006 ms, and 1006 is close enough to
// right to survive any review and wrong enough to matter six milliseconds at a
// time.
func TestAnOpusFileIsNotAsLongAsItsGranule(t *testing.T) {
	path := filepath.Join("testdata", "silence.opus")

	counted, err := CountedDurationMS(path)
	if err != nil {
		t.Fatal(err)
	}
	if counted != fixtureMS {
		t.Fatalf("counted = %dms, want %dms with the pre-skip taken off", counted, fixtureMS)
	}
}

// A granule of -1 is not a position, it is a page on which no packet finished,
// and it is legal at the end of a file. The page before it is asked instead.
// The fixture's second-to-last page ends at 7936 samples of 8000, so the file
// reads 992 ms rather than refusing and rather than reporting a length of minus
// one sample.
func TestAnOggPageThatFinishedNothingIsSteppedOver(t *testing.T) {
	whole, err := os.ReadFile(filepath.Join("testdata", "silence.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	last := 0
	for at := 0; at+27 <= len(whole); at++ {
		if string(whole[at:at+4]) == "OggS" {
			last = at
		}
	}
	for offset := range 8 {
		whole[last+6+offset] = 0xff
	}
	path := filepath.Join(t.TempDir(), "track.ogg")
	if err := os.WriteFile(path, whole, 0o644); err != nil {
		t.Fatal(err)
	}

	counted, err := CountedDurationMS(path)
	if err != nil {
		t.Fatal(err)
	}
	if counted != 992 {
		t.Fatalf("counted = %dms, want the 992ms the page before it finished", counted)
	}
}

// The two VBR fixtures are the same three seconds of audio, encoded twice by
// the same encoder at the same setting: once with the header frame that
// declares the length, once with `lame -t`, which withholds it.
//
// That pair is the whole argument for walking. A reader that believes the
// header is right about one of them and guesses at the other; a walk counts
// the frames and cannot tell the two files apart, because as audio they are
// not different.
const vbrFixtureMS = 3030

func TestCountedDurationMeasuresTheSameAudioTheSameWay(t *testing.T) {
	for _, name := range []string{"vbr-xing.mp3", "vbr-no-xing.mp3"} {
		t.Run(name, func(t *testing.T) {
			counted, err := CountedDurationMS(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			// A millisecond of slack, for the rounding of a frame count into a
			// unit that does not divide it. Not a tolerance: the counts are
			// equal, only their expression in milliseconds is not exact.
			if counted < vbrFixtureMS-1 || counted > vbrFixtureMS+1 {
				t.Fatalf("counted = %dms, want %dms", counted, vbrFixtureMS)
			}
		})
	}
}

// What the walk is for, stated as a test rather than as a comment: the header
// reader answers differently for the two fixtures. Which of its answers is
// wrong does not matter and is not asserted -- taglib may improve, and this
// test should still hold. What matters is that one file's length is knowable to
// it and the other's is not, and that Schall stopped depending on which.
func TestTheHeaderReaderCannotTellTheFixturesApartCorrectly(t *testing.T) {
	withHeader, err := ReadProperties(filepath.Join("testdata", "vbr-xing.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	without, err := ReadProperties(filepath.Join("testdata", "vbr-no-xing.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if withHeader.DurationMS == without.DurationMS {
		t.Skip("the header reader now measures both; the walk is still the thing that guarantees it")
	}
	t.Logf("header reader: %dms with a header, %dms without; counted: %dms for both",
		withHeader.DurationMS, without.DurationMS, vbrFixtureMS)
}

// A short mono file at 8kHz, which is MPEG 2.5 -- the version whose bits are 0
// in the header and whose Layer III frames carry half the samples of MPEG 1's.
// Both of those are places the walk can be wrong in a way that only shows up on
// a file nobody makes on purpose, so the fixture is kept.
func TestCountedDurationReadsTheLowRateVersions(t *testing.T) {
	counted, err := CountedDurationMS(filepath.Join("testdata", "silence.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if counted < 1000 || counted > 1300 {
		t.Fatalf("counted = %dms, want about a second", counted)
	}
}

// A music extension on something that is not music is not a failure of the read
// and must not stop a scan. It is answered with nothing, which grades as
// silence, which has never been an agreement.
func TestCountedDurationRefusesWhatIsNotAudio(t *testing.T) {
	for _, extension := range []string{
		".mp3", ".flac", ".ogg", ".oga", ".opus",
		".m4a", ".m4b", ".m4p", ".mp4", ".dsf", ".aiff", ".aif", ".wav",
	} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "not-audio"+extension)
			sentence := []byte("this is not a stream, it is a sentence, and it goes on for a while")
			if err := os.WriteFile(path, sentence, 0o644); err != nil {
				t.Fatal(err)
			}
			counted, err := CountedDurationMS(path)
			if !errors.Is(err, ErrNoDuration) {
				t.Fatalf("err = %v, want ErrNoDuration", err)
			}
			if counted != 0 {
				t.Fatalf("counted = %d, want 0", counted)
			}
		})
	}
}

// A file cut off partway through its own header says nothing. Reading the bytes
// that did arrive as a length would put an invented number in front of a
// decision that turns on five seconds.
func TestCountedDurationRefusesAStreamThatStopsMidHeader(t *testing.T) {
	for _, fixture := range []string{
		"silence.mp3", "silence.flac", "silence.ogg", "silence.opus",
		"silence.m4a", "silence.wav", "silence.aiff", "silence.dsf",
	} {
		t.Run(fixture, func(t *testing.T) {
			whole, err := os.ReadFile(filepath.Join("testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), fixture)
			if err := os.WriteFile(path, whole[:12], 0o644); err != nil {
				t.Fatal(err)
			}
			if counted, err := CountedDurationMS(path); counted != 0 || err == nil {
				t.Fatalf("counted = %d, err = %v; want nothing measured", counted, err)
			}
		})
	}
}

// An extension with no reader behind it is answered the same way as a file that
// will not say, rather than with a zero nobody can tell from a measurement.
func TestCountedDurationRefusesAnExtensionItDoesNotKnow(t *testing.T) {
	if CanCount("track.ape") {
		t.Fatal("CanCount says .ape has a reader")
	}
	if _, err := CountedDurationMS("track.ape"); !errors.Is(err, ErrNoDuration) {
		t.Fatalf("err = %v, want ErrNoDuration", err)
	}
}

func TestCountedDurationRefusesAFileThatIsNotThere(t *testing.T) {
	if _, err := CountedDurationMS(filepath.Join(t.TempDir(), "gone.mp3")); err == nil {
		t.Fatal("err = nil, want a failure to open")
	}
}

// STREAMINFO is required to be the first metadata block, so a FLAC whose first
// block is something else is not one this can measure. It says so rather than
// reading another block's bytes as a sample rate.
func TestAFLACWhoseFirstBlockIsNotStreamInfoSaysNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	data := make([]byte, 42)
	copy(data, "fLaC")
	data[4] = 4 // a Vorbis comment block where STREAMINFO must be
	data[7] = 34
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CountedDurationMS(path); !errors.Is(err, ErrNoDuration) {
		t.Fatalf("err = %v, want ErrNoDuration", err)
	}
}

// Zero total samples is STREAMINFO's way of saying it does not know, which an
// encoder writes when it is still streaming. Reported as a number it would be
// an instant long.
func TestAFLACThatSaysItDoesNotKnowIsBelieved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	data := make([]byte, 42)
	copy(data, "fLaC")
	data[7] = 34
	const sampleRate = 48000
	data[18] = byte(sampleRate >> 12 & 0xff)
	data[19] = byte(sampleRate >> 4 & 0xff)
	data[20] = byte(sampleRate & 0x0f << 4)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CountedDurationMS(path); !errors.Is(err, ErrNoDuration) {
		t.Fatalf("err = %v, want ErrNoDuration", err)
	}
}

// A WAV whose samples are not laid out plainly has a byte rate that is an
// average somebody nominated, not a rate the file keeps. Dividing by it gives a
// number that is close, and close is what this whole change exists to stop.
func TestAWAVOfCompressedAudioSaysNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.wav")
	whole, err := os.ReadFile(filepath.Join("testdata", "silence.wav"))
	if err != nil {
		t.Fatal(err)
	}
	at := 12 + 8 // past RIFF/WAVE and the `fmt ` chunk header
	// 0x0055 is MP3 carried inside a WAV, which players handle and this will
	// not put a length on.
	binary.LittleEndian.PutUint16(whole[at:at+2], 0x0055)
	if err := os.WriteFile(path, whole, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CountedDurationMS(path); !errors.Is(err, ErrNoDuration) {
		t.Fatalf("err = %v, want ErrNoDuration", err)
	}
}

// An MP4 with an edit list is as long as the list says it is played for, not as
// long as the media behind it. The fixture's own media is 1128 ms -- one second
// of silence plus the 1024 priming samples an AAC decoder discards -- and the
// edit list is what says so. This is the same trap as the Opus pre-skip and it
// is 128 ms wide at 8 kHz, about 48 ms at 44.1 kHz.
func TestAnMP4IsAsLongAsItIsPlayedForNotAsLongAsItsMedia(t *testing.T) {
	counted, err := CountedDurationMS(filepath.Join("testdata", "silence.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	if counted != fixtureMS {
		t.Fatalf("counted = %dms, want %dms with the priming taken off", counted, fixtureMS)
	}
}

// The all-ones duration is MP4's reserved value for a muxer that did not know
// yet. It is four billion timescale units and it is not a length.
func TestAnMP4WhoseDurationIsUnknownSaysNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.m4a")
	whole, err := os.ReadFile(filepath.Join("testdata", "silence.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	// The edit list first, because a file that still has one is answered from
	// it and would never reach the header boxes this test is about.
	edits := 0
	for at := 0; at+4 <= len(whole); at++ {
		if string(whole[at:at+4]) == "elst" {
			copy(whole[at:at+4], "xxxx")
			edits++
		}
	}
	if edits == 0 {
		t.Fatal("the fixture holds no edit list")
	}
	blanked := 0
	// Both header boxes, because either one answering would hide the other.
	for _, box := range []string{"mvhd", "mdhd"} {
		for at := 0; at+24 <= len(whole); at++ {
			if string(whole[at:at+4]) != box || whole[at+4] != 0 {
				continue
			}
			for offset := range 4 {
				whole[at+20+offset] = 0xff
			}
			blanked++
		}
	}
	if blanked == 0 {
		t.Fatal("the fixture holds no version 0 header box to blank")
	}
	if err := os.WriteFile(path, whole, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CountedDurationMS(path); !errors.Is(err, ErrNoDuration) {
		t.Fatalf("err = %v, want ErrNoDuration", err)
	}
}

// The 80-bit extended float, decoded by hand and therefore worth checking
// against the rates it will actually meet. An exponent read one out puts every
// AIFF in the library out by a factor of two.
func TestTheEightyBitFloatDecodesTheRatesAIFFUses(t *testing.T) {
	for _, rate := range []struct {
		raw  []byte
		want float64
	}{
		{[]byte{0x40, 0x0e, 0xac, 0x44, 0, 0, 0, 0, 0, 0}, 44100},
		{[]byte{0x40, 0x0e, 0xbb, 0x80, 0, 0, 0, 0, 0, 0}, 48000},
		{[]byte{0x40, 0x0b, 0xfa, 0x00, 0, 0, 0, 0, 0, 0}, 8000},
		{[]byte{0x40, 0x0f, 0xbb, 0x80, 0, 0, 0, 0, 0, 0}, 96000},
	} {
		if got := extended80(rate.raw); got != rate.want {
			t.Errorf("extended80 = %v, want %v", got, rate.want)
		}
	}
}

func copyFixture(t *testing.T, name, to string) {
	t.Helper()
	whole, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, whole, 0o644); err != nil {
		t.Fatal(err)
	}
}
