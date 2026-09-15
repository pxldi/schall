package tagging

import (
	"slices"
	"testing"

	"go.senan.xyz/taglib"
)

// measured is a file Schall proved and also measured the loudness of.
func measured() Tags {
	tags := proved()
	tags.ReplayGain = &ReplayGain{TrackGainDB: -7.25, TrackPeak: 0.966051}
	return tags
}

// shipWith puts tags on a fixture the way the peer who sent it would have, so a
// test can say what the file arrived carrying.
func shipWith(t *testing.T, path string, tags map[string][]string) {
	t.Helper()
	if err := taglib.WriteTags(path, tags, 0); err != nil {
		t.Fatalf("put the peer's tags on %q: %v", path, err)
	}
}

// peerGain is a replay gain a stranger measured and wrote, in his own precision
// rather than Schall's.
var peerGain = map[string][]string{
	"REPLAYGAIN_TRACK_GAIN": {"-3.4 dB"},
	"REPLAYGAIN_TRACK_PEAK": {"0.812345"},
	"REPLAYGAIN_ALBUM_GAIN": {"-2.1 dB"},
	"REPLAYGAIN_ALBUM_PEAK": {"0.998000"},
}

func TestAMeasuredFileCarriesItsLoudnessInTheFormAPlayerReads(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)
			if err := Write(path, measured()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}
			present := read(t, path)
			if got := present["REPLAYGAIN_TRACK_GAIN"]; !slices.Equal(got, []string{"-7.25 dB"}) {
				t.Fatalf("track gain is %q, want %q", got, "-7.25 dB")
			}
			if got := present["REPLAYGAIN_TRACK_PEAK"]; !slices.Equal(got, []string{"0.966051"}) {
				t.Fatalf("track peak is %q, want %q", got, "0.966051")
			}
		})
	}
}

func TestAWholeRecordMeasuredCarriesTheAlbumsLoudnessToo(t *testing.T) {
	path := fixture(t, "silence.flac")
	tags := measured()
	tags.ReplayGain.Album = &AlbumGain{GainDB: -6.5, Peak: 1.001234}
	if err := Write(path, tags); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	present := read(t, path)
	if got := present["REPLAYGAIN_ALBUM_GAIN"]; !slices.Equal(got, []string{"-6.50 dB"}) {
		t.Fatalf("album gain is %q, want %q", got, "-6.50 dB")
	}
	// A true peak above full scale is real and is written as it is. A player
	// reads it to decide whether the gain would clip, and rounding it down to
	// 1.0 would tell it there is headroom where there is none.
	if got := present["REPLAYGAIN_ALBUM_PEAK"]; !slices.Equal(got, []string{"1.001234"}) {
		t.Fatalf("album peak is %q, want %q", got, "1.001234")
	}
}

func TestAPartlyMeasuredRecordLosesTheStrangersAlbumGain(t *testing.T) {
	// Schall's track gain beside a peer's album gain is two reference
	// loudnesses written into one file, and a player following the album tag
	// would level the record by a stranger's measurement of another mastering.
	path := fixture(t, "silence.flac")
	shipWith(t, path, peerGain)
	if err := Write(path, measured()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	present := read(t, path)
	if got := present["REPLAYGAIN_ALBUM_GAIN"]; len(got) != 0 {
		t.Fatalf("album gain is %q, want it gone", got)
	}
	if got := present["REPLAYGAIN_ALBUM_PEAK"]; len(got) != 0 {
		t.Fatalf("album peak is %q, want it gone", got)
	}
}

func TestAFileNobodyMeasuredKeepsTheGainThePeerWrote(t *testing.T) {
	// An installation with no ffmpeg tags its library exactly as before, and a
	// peer who did the measuring is not undone by that.
	path := fixture(t, "silence.flac")
	shipWith(t, path, peerGain)
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	present := read(t, path)
	for name, want := range peerGain {
		if got := present[name]; !slices.Equal(got, want) {
			t.Fatalf("%s is %q, want the peer's %q", name, got, want)
		}
	}
}

func TestARevertGivesBackTheGainThePeerWrote(t *testing.T) {
	for _, name := range formats {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)
			shipWith(t, path, peerGain)
			if err := Write(path, measured()); err != nil {
				t.Fatalf("write the tags: %v", err)
			}
			restored, err := Revert(path)
			if err != nil {
				t.Fatalf("revert: %v", err)
			}
			if !restored {
				t.Fatal("the file had nothing to give back")
			}
			present := read(t, path)
			for tag, want := range peerGain {
				if got := present[tag]; !slices.Equal(got, want) {
					t.Fatalf("%s is %q, want the peer's %q back", tag, got, want)
				}
			}
		})
	}
}

func TestARevertTakesBackAGainThePeerNeverWrote(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := Write(path, measured()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	if _, err := Revert(path); err != nil {
		t.Fatalf("revert: %v", err)
	}
	present := read(t, path)
	for _, tag := range []string{"REPLAYGAIN_TRACK_GAIN", "REPLAYGAIN_TRACK_PEAK"} {
		if got := present[tag]; len(got) != 0 {
			t.Fatalf("%s is %q, want it gone with everything else Schall wrote", tag, got)
		}
	}
}

func TestAFileMeasuredOnlyOnItsSecondWriteStillGivesThePeersGainBack(t *testing.T) {
	// A library tagged before there was any measuring, then measured and tagged
	// again. The first write kept an account with nothing about loudness in it,
	// because at that moment Schall had no claim on those tags. The second
	// write is the last moment the peer's gain is still standing in the file,
	// so that is when it has to be written down.
	path := fixture(t, "silence.flac")
	shipWith(t, path, peerGain)
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags the first time: %v", err)
	}
	if err := Write(path, measured()); err != nil {
		t.Fatalf("write the tags the second time: %v", err)
	}
	if _, err := Revert(path); err != nil {
		t.Fatalf("revert: %v", err)
	}
	present := read(t, path)
	for tag, want := range peerGain {
		if got := present[tag]; !slices.Equal(got, want) {
			t.Fatalf("%s is %q, want the peer's %q back", tag, got, want)
		}
	}
}

func TestARevertOfAFileTaggedBeforeAnyMeasuringLeavesItsGainAlone(t *testing.T) {
	// The account in a file Schall tagged before this feature existed says
	// nothing about loudness, and cannot: those tags were never Schall's. What
	// stands in the file is the stranger's, and a revert that cleared the four
	// names would delete his work in the name of undoing Schall's.
	path := fixture(t, "silence.flac")
	if err := Write(path, proved()); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	shipWith(t, path, peerGain)
	if _, err := Revert(path); err != nil {
		t.Fatalf("revert: %v", err)
	}
	present := read(t, path)
	for tag, want := range peerGain {
		if got := present[tag]; !slices.Equal(got, want) {
			t.Fatalf("%s is %q, want the peer's %q left where it is", tag, got, want)
		}
	}
}

func TestAMeasuredFileAlreadySayingItNeedsNoWriting(t *testing.T) {
	path := fixture(t, "silence.flac")
	tags := measured()
	tags.ReplayGain.Album = &AlbumGain{GainDB: -6.5, Peak: 0.99}
	if err := Write(path, tags); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	if !AlreadySays(path, tags) {
		t.Fatal("a file that says exactly this reads as needing a rewrite")
	}
	// A file measured again to a different loudness does need writing: a
	// rewrite costs a copy of the whole file, so the check has to be able to
	// tell the two apart.
	louder := measured()
	louder.ReplayGain.TrackGainDB = -9
	if AlreadySays(path, louder) {
		t.Fatal("a file measured to another loudness reads as up to date")
	}
}
