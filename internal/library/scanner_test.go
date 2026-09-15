package library

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/pxldi/schall/internal/tagging"
)

func TestSupportedAudioExtensions(t *testing.T) {
	for _, path := range []string{"track.MP3", "track.flac", "track.ogg", "track.opus", "track.m4a", "track.dsf"} {
		if !isSupported(path) {
			t.Errorf("%s should be supported", path)
		}
	}
	for _, path := range []string{"cover.jpg", "notes.txt", "track.ape"} {
		if isSupported(path) {
			t.Errorf("%s should not be supported", path)
		}
	}
}

// Uncompressed lossless is music the library holds like any other. It carries no
// tags this parser reads, which is silence rather than damage — and excluding it
// meant a copy fetched for a want was imported into a directory no scan would
// ever look at again, leaving the want reporting progress that could not happen.
func TestTheLibraryHoldsUncompressedLossless(t *testing.T) {
	for _, path := range []string{
		"/music/Introversion/Dystopia/01 Dystopia.aiff",
		"track.AIF",
		"track.wav",
	} {
		if !CanHold(path) {
			t.Errorf("CanHold(%q) = false, want the library able to hold it", path)
		}
	}
}

// What a peer is offering is asked of the same list a scan reads by, so the two
// can never drift apart.
func TestWhatTheLibraryCannotHoldIsNotHoldable(t *testing.T) {
	for _, path := range []string{`Music\Peer\05 Glory Box.ape`, "sleeve.jpg", "readme.nfo"} {
		if CanHold(path) {
			t.Errorf("CanHold(%q) = true, want a file no scan would index refused", path)
		}
	}
}

func TestReadTagsKeepsUnreadableAudioAsScanError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.mp3")
	if err := os.WriteFile(path, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := readTags(path)
	if result.Error == "" {
		t.Fatal("expected tag extraction error")
	}
}

func TestReadTagsExtractsCoreID3v1Fields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagged.mp3")
	data := make([]byte, 256)
	copy(data[128:131], "TAG")
	copy(data[131:161], "Roads")
	copy(data[161:191], "Portishead")
	copy(data[191:221], "Dummy")
	data[253] = 0
	data[254] = 7
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	result := readTags(path)
	if result.Error != "" {
		t.Fatalf("readTags error = %q", result.Error)
	}
	if result.Title != "Roads" || result.Artist != "Portishead" || result.Album != "Dummy" || result.TrackNumber != 7 {
		t.Fatalf("tags = %#v", result)
	}
}

// writeFLAC lays down a FLAC carrying a STREAMINFO block of the given length and
// the given Vorbis comments, which is enough for both the tag reader and the
// stream parser to have something to say about how long the audio is.
func writeFLAC(t *testing.T, path string, sampleRate, totalSamples int, comments ...string) {
	t.Helper()
	stream := make([]byte, 34)
	stream[10] = byte((sampleRate >> 12) & 0xff)
	stream[11] = byte((sampleRate >> 4) & 0xff)
	stream[12] = byte((sampleRate & 0x0f) << 4)
	stream[13] = byte((totalSamples >> 32) & 0x0f)
	stream[14] = byte((totalSamples >> 24) & 0xff)
	stream[15] = byte((totalSamples >> 16) & 0xff)
	stream[16] = byte((totalSamples >> 8) & 0xff)
	stream[17] = byte(totalSamples & 0xff)

	tags := make([]byte, 0, 16)
	tags = binary.LittleEndian.AppendUint32(tags, 0) // no vendor string
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len(comments)))
	for _, comment := range comments {
		tags = binary.LittleEndian.AppendUint32(tags, uint32(len(comment)))
		tags = append(tags, comment...)
	}

	data := []byte("fLaC")
	data = append(data, 0, 0, 0, byte(len(stream)))
	data = append(data, stream...)
	data = append(data, 0x80|4, 0, 0, byte(len(tags)))
	data = append(data, tags...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A duration is one of the things a match may rest on, and more than five
// seconds out voids a pair whatever else agrees. A TLEN frame is a number
// somebody typed; STREAMINFO is the stream counting its own samples, and it is
// sitting in the same file.
func TestAFLACIsTimedByItsStreamRatherThanByItsTLEN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000, "TLEN=180000")

	if got := readTags(path).DurationMS; got != 3000 {
		t.Fatalf("duration = %d, want the 3000 ms the stream itself holds", got)
	}
}

// Nothing measured this file and nothing claimed a length either. Zero is
// silence, which is never agreement — the alternative is an invented number
// deciding whether a file is the recording somebody wanted.
func TestAFileNothingMeasuredAndNothingClaimedHasNoDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagged.mp3")
	data := make([]byte, 256)
	copy(data[128:131], "TAG")
	copy(data[131:161], "Roads")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := readTags(path).DurationMS; got != 0 {
		t.Fatalf("duration = %d, want silence rather than a length", got)
	}
}

// A FLAC whose header says nothing usable has no duration at all. It used to
// fall back to its own TLEN, and that fallback is what this change removed: a
// number that can void a match cannot come from the file's account of itself.
func TestAFLACWithNoUsableStreamInfoHasNoDurationAtAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 0, "TLEN=180000")

	if got := readTags(path).DurationMS; got != 0 {
		t.Fatalf("duration = %d, want silence rather than the file's own claim", got)
	}
}

// The same removal stated for the format it mattered on. An MP3 whose frames
// cannot be walked used to be as long as its TLEN said, and a wrong TLEN was
// therefore evidence. Now nobody counted it, so nobody knows.
func TestAnMP3WithNothingWalkableHasNoDurationAtAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	writeID3v2WithTLEN(t, path, 180000)

	if got := readTags(path).DurationMS; got != 0 {
		t.Fatalf("duration = %d, want silence rather than the file's own claim", got)
	}
}

// Every type the library indexes has to be able to say how long it is. A format
// added to supportedExtensions with no reader behind it would be stored with no
// duration forever, silently, and the only symptom would be matches that never
// happen — so it is a test failure instead.
func TestEveryFormatTheLibraryHoldsCanSayHowLongItIs(t *testing.T) {
	// Nothing is listed here. The entry exists so that a format which genuinely
	// cannot state its own length is written down as a decision rather than
	// discovered as a gap.
	cannotSay := map[string]string{}

	for extension := range supportedExtensions {
		if tagging.CanCount("track" + extension) {
			continue
		}
		if reason, listed := cannotSay[extension]; listed {
			t.Logf("%s says nothing about its length: %s", extension, reason)
			continue
		}
		t.Errorf("the library holds %s and nothing here counts its duration", extension)
	}
}

// The scanner and import validation read the same file through the same parser,
// so what one indexes is exactly what the other judged.
func TestInspectAudioReportsWhatTheParserRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000, "TITLE=Roads", "ARTIST=Portishead", "ALBUM=Dummy")

	metadata := InspectAudio(path)

	if metadata.Error != "" {
		t.Fatalf("error = %q", metadata.Error)
	}
	if metadata.Title != "Roads" || metadata.Artist != "Portishead" || metadata.Album != "Dummy" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.DurationMS != 3000 {
		t.Fatalf("duration = %d, want the length the stream holds", metadata.DurationMS)
	}
}

// A file that has gone by the time it is read is a scan error rather than a
// crash or a row claiming the file said nothing. A permission the scanner does
// not have arrives here as the same refusal from the same call.
func TestAFileThatCannotBeOpenedIsRecordedAsAScanError(t *testing.T) {
	metadata := InspectAudio(filepath.Join(t.TempDir(), "gone.flac"))

	if metadata.Error == "" {
		t.Fatal("a file that could not be opened was read as silence")
	}
	if metadata.TagsAbsent {
		t.Fatal("a file nobody could open must not read as one with no tags")
	}
}

// Uncompressed lossless carries no tags this parser reads. That is silence
// rather than damage, and the difference is what lets a fetched WAV be imported
// at all: an absent tag never agrees with anything, and never contradicts.
func TestAFileWithNoTagsAtAllReadsAsSilenceRatherThanDamage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.wav")
	data := make([]byte, 1024)
	copy(data, "RIFF")
	copy(data[8:], "WAVE")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	metadata := InspectAudio(path)

	if metadata.Error == "" {
		t.Fatal("a file with no tags said nothing about why it said nothing")
	}
	if !metadata.TagsAbsent {
		t.Fatal("absent tags were recorded as unreadable bytes")
	}
}

// An ISRC identifies a recording outright, so it has to survive being read even
// when the tagger wrote it under the other of its two names.
func TestAnISRCIsReadFromEitherOfItsTagNames(t *testing.T) {
	directory := t.TempDir()
	for name, comment := range map[string]string{
		"isrc.flac": "ISRC=GBAYE0601498",
		"tsrc.flac": "TSRC=GBAYE0601498",
	} {
		path := filepath.Join(directory, name)
		writeFLAC(t, path, 48000, 144000, comment)
		if got := readTags(path).ISRC; got != "GBAYE0601498" {
			t.Errorf("%s ISRC = %q, want it read", name, got)
		}
	}
}

// A tag the file carries more than once is still that tag. Reading a repeated
// value as nothing would turn an identifier into silence.
func TestARepeatedTagIsReadAtItsFirstValue(t *testing.T) {
	if got := rawString(map[string]any{"ISRC": []string{"GBAYE0601498", "USRC17607839"}}, "isrc"); got != "GBAYE0601498" {
		t.Fatalf("ISRC = %q, want the first value of a repeated tag", got)
	}
}

// A tag that is present and holds nothing is silence. There is no value to
// disagree with, and inventing one would be worse than having none.
func TestATagWithNoValuesAtAllReadsAsNothing(t *testing.T) {
	if got := rawString(map[string]any{"ISRC": []string{}}, "isrc"); got != "" {
		t.Fatalf("ISRC = %q, want an empty tag to say nothing", got)
	}
}

// A MusicBrainz recording ID is proof of identity on its own, so a file that
// carries one has to be indexed with it.
func TestAMusicBrainzRecordingIDIsReadFromTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	const recordingID = "6f1a4d7f-2f2c-4d3f-9d6a-2a9d4e6b1c11"
	writeFLAC(t, path, 48000, 144000, "musicbrainz_recordingid="+recordingID)

	result := readTags(path)

	if result.MusicBrainzRecordingID == nil {
		t.Fatal("the recording ID the file carries was not read")
	}
	if got := result.MusicBrainzRecordingID.String(); got != recordingID {
		t.Fatalf("recording = %q, want %q", got, recordingID)
	}
}

// A recording ID that is not a UUID identifies nothing, and recording it would
// key a file's whole identity on a value MusicBrainz has never issued.
func TestARecordingIDThatIsNotAUUIDIsNotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000, "musicbrainz_recordingid=not-a-uuid")

	if result := readTags(path); result.MusicBrainzRecordingID != nil {
		t.Fatalf("recording = %v, want a value that is not a UUID ignored", result.MusicBrainzRecordingID)
	}
}

// The album artist is what a release is filed under; the track artist stands in
// only where there is no album artist to read.
func TestTheTrackArtistStandsInWhenThereIsNoAlbumArtist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000, "ARTIST=Portishead")

	if got := readTags(path).Artist; got != "Portishead" {
		t.Fatalf("artist = %q, want the track artist where no album artist was written", got)
	}
}

// The rule this arrangement exists for: a match is provable only from what
// somebody other than Schall said. Schall writes into the files it imports what
// it proved them to be, and a scan reading those tags back would be Schall
// agreeing with itself — a recording identifier it wrote is nobody's
// corroboration of anything.
func TestAFileSchallTaggedIsStillReadAsTheTagsItArrivedWith(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000,
		"TITLE=walk to the closed bodega", "ARTIST=Peer Artist", "ALBUM=Peer Album", "TRACKNUMBER=7")
	arrived := readTags(path)

	if err := tagging.Write(path, tagging.Tags{
		Title:       "Walk to the Closed Bodega",
		Album:       "Halfway House Near Me",
		Artist:      "AgonyOST",
		AlbumArtist: "AgonyOST",
		TrackNumber: 4,
		RecordingID: uuid.MustParse("44444444-4444-4444-4444-444444444444"),
	}); err != nil {
		t.Fatalf("write what Schall proved: %v", err)
	}

	after := readTags(path)
	if after.Title != arrived.Title || after.Artist != arrived.Artist ||
		after.Album != arrived.Album || after.TrackNumber != arrived.TrackNumber {
		t.Fatalf("a scan read Schall's own account back as %#v, where the file arrived as %#v",
			after, arrived)
	}
}

// A recording identifier is proof of identity on its own, read with no audio
// consulted. Schall never writes the one it proved into a file, so however that
// file is later rewritten — by a converter, by a tagger, by anything that keeps
// the fields it recognises and drops the ones it does not — the only recording
// it can ever be read as claiming is the one a stranger put there.
func TestTheOnlyRecordingIdentifierAScanReadsIsThePeersOwn(t *testing.T) {
	const peerRecording = "6f1a4d7f-2f2c-4d3f-9d6a-2a9d4e6b1c11"
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000,
		"TITLE=peer title", "ARTIST=Peer Artist", "musicbrainz_trackid="+peerRecording)

	if err := tagging.Write(path, tagging.Tags{
		Title:       "Walk to the Closed Bodega",
		Artist:      "AgonyOST",
		RecordingID: uuid.MustParse("44444444-4444-4444-4444-444444444444"),
	}); err != nil {
		t.Fatalf("write what Schall proved: %v", err)
	}

	got := readTags(path).MusicBrainzRecordingID
	if got == nil || got.String() != peerRecording {
		t.Fatalf("recording = %v, want the peer's own %s", got, peerRecording)
	}
}

// How long the audio is comes from the audio, so a tag write cannot change it,
// and a tagged file keeps the one piece of evidence no tag can lie about.
func TestTaggingAFileDoesNotChangeHowLongTheAudioIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000, "TITLE=peer title", "ARTIST=Peer Artist")
	before := readTags(path).DurationMS

	if err := tagging.Write(path, tagging.Tags{Title: "Tears", Artist: "Himera"}); err != nil {
		t.Fatalf("write what Schall proved: %v", err)
	}

	if after := readTags(path).DurationMS; after != before {
		t.Fatalf("the audio was %dms and now reads %dms", before, after)
	}
}

// writeID3v2WithTLEN writes a file that is nothing but a tag: an ID3v2 header,
// a TLEN frame, and not one byte of audio behind it. It is the shape the
// deleted fallback used to find a length in.
func writeID3v2WithTLEN(t *testing.T, path string, milliseconds int) {
	t.Helper()
	claim := []byte(fmt.Sprintf("\x00%d", milliseconds))
	frame := append([]byte("TLEN"), 0, 0, 0, byte(len(claim)), 0, 0)
	frame = append(frame, claim...)

	tag := append([]byte("ID3"), 3, 0, 0)
	// The size is stored seven bits to the byte, so no byte of a tag can be
	// mistaken for the start of a frame.
	tag = append(tag, 0, 0, byte(len(frame)>>7&0x7f), byte(len(frame)&0x7f))
	tag = append(tag, frame...)
	if err := os.WriteFile(path, tag, 0o644); err != nil {
		t.Fatal(err)
	}
}
