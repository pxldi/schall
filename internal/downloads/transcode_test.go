package downloads

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/transcode"
	"github.com/rs/zerolog"
)

// The import tests that involve a real re-encode need ffmpeg. Without one an
// installation files every file as it arrived, which is what
// TestImporterImportsTheFileAsItArrivedWhenTheEncoderIsMissing asserts anyway.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
}

// writeTone puts one second of audio at path and answers how big it is, so the
// download request can state the size validation checks the bytes against.
func writeTone(t *testing.T, path string) int64 {
	t.Helper()
	command := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-y", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("write the downloaded file: %v: %s", err, output)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// shrinkTo is the policy under test, wired to the encoder named by binary. An
// empty binary means ffmpeg is looked for on PATH.
func shrinkTo(binary string) *transcode.Service {
	return transcode.NewService(func(context.Context) (transcode.Policy, error) {
		return transcode.Policy{
			Enabled: true, Target: transcode.TargetMP3,
			Bitrate: "320", When: transcode.WhenLossless,
		}, nil
	}, binary, zerolog.Nop())
}

// oneLosslessRelease is a download of a single uncompressed track that validation will
// accept, together with the store that claims it.
func oneLosslessRelease(t *testing.T, inbox string) (*fakeImportStore, uuid.UUID, int64) {
	t.Helper()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	size := writeTone(t, filepath.Join(folder, "01.wav"))
	requestID := uuid.New()
	return &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files: []db.DownloadRequestFile{
			{Path: `Album\01.wav`, Name: "01.wav", SizeBytes: size},
		},
		Tracks: []db.ImportTrack{
			{Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000},
		},
	}}, requestID, size
}

func firstTrackTags(string) library.AudioMetadata {
	return library.AudioMetadata{
		Artist: "Artist", Album: "Album", Title: "First",
		DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000,
	}
}

// The order the whole feature rests on: the file is identified as it arrived,
// and only then written smaller. Nothing the encoder produces is ever asked
// what music it is.
func TestImporterIdentifiesTheFileThatArrivedAndFilesTheSmallerCopy(t *testing.T) {
	requireFFmpeg(t)
	inbox, libraryPath := t.TempDir(), t.TempDir()
	store, requestID, original := oneLosslessRelease(t, inbox)

	var asked []string
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop()).
		WithTranscoding(shrinkTo(""))
	importer.inspect = func(path string) library.AudioMetadata {
		asked = append(asked, path)
		return firstTrackTags(path)
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	// Every question about what this file is was asked of the copy in the
	// inbox, in the format it arrived in.
	if len(asked) == 0 {
		t.Fatal("nothing was ever asked what the file is")
	}
	for _, path := range asked {
		if !strings.HasPrefix(path, inbox) || filepath.Ext(path) != ".wav" {
			t.Fatalf("the file was identified at %q rather than as it arrived", path)
		}
	}

	local := store.localPaths[`Album\01.wav`].LocalPath
	if filepath.Ext(local) != ".mp3" {
		t.Fatalf("the library holds %q, wanted an MP3", local)
	}
	info, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= original {
		t.Fatalf("the copy is %d bytes and the WAV was %d", info.Size(), original)
	}

	if len(store.transcoded) != 1 {
		t.Fatalf("recorded re-encodes = %+v", store.transcoded)
	}
	written := store.transcoded[0]
	if written.LocalPath != local || written.SourceFormat != "wav" ||
		written.SourceSizeBytes != original || written.TargetFormat != "mp3" {
		t.Fatalf("recorded %+v", written)
	}
	if len(store.transcodeRefused) != 0 {
		t.Fatalf("refusals = %v", store.transcodeRefused)
	}
}

// Never lose the music to save room on it. An encoder that is not installed
// imports the file exactly as it arrived and says so.
func TestImporterImportsTheFileAsItArrivedWhenTheEncoderIsMissing(t *testing.T) {
	requireFFmpeg(t)
	inbox, libraryPath := t.TempDir(), t.TempDir()
	store, requestID, original := oneLosslessRelease(t, inbox)

	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop()).
		WithTranscoding(shrinkTo(filepath.Join(t.TempDir(), "no-such-encoder")))
	importer.inspect = firstTrackTags

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	local := store.localPaths[`Album\01.wav`].LocalPath
	if filepath.Ext(local) != ".wav" {
		t.Fatalf("the library holds %q, wanted the WAV that arrived", local)
	}
	info, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != original {
		t.Fatalf("the imported file is %d bytes and the WAV was %d", info.Size(), original)
	}
	if len(store.transcoded) != 0 {
		t.Fatalf("a re-encode was recorded that never happened: %+v", store.transcoded)
	}
	if len(store.transcodeRefused) != 1 {
		t.Fatalf("refusals = %v, wanted one saying why nothing was re-encoded",
			store.transcodeRefused)
	}
}

// Removing the provider's copy is confirmed against the file that was written,
// which after a re-encode is deliberately not the size of the file that
// arrived. Measuring it against the arrival would refuse every removal.
func TestRetentionRemovesTheProviderCopyOfAReEncodedFile(t *testing.T) {
	requireFFmpeg(t)
	inbox, libraryPath := t.TempDir(), t.TempDir()
	store, requestID, _ := oneLosslessRelease(t, inbox)
	store.retention = db.SourceRetentionDelete

	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop()).
		WithTranscoding(shrinkTo(""))
	importer.inspect = firstTrackTags

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}

	if !store.removed {
		t.Fatalf("the provider copy was kept: %q", store.retentionDetail)
	}
	if _, err := os.Stat(filepath.Join(inbox, "Album", "01.wav")); !os.IsNotExist(err) {
		t.Fatalf("the provider copy is still there: %v", err)
	}
}

// The safety-critical order, from the other side: a file that is still an open
// question is never handed to the encoder at all, whether or not one is
// installed. Nothing here needs ffmpeg to prove that — validate returning
// before copyRelease is what proves it, and this asserts the encoder was never
// asked to do anything about it.
func TestAFileThatNeedsReviewIsNeverTranscoded(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files:           []db.DownloadRequestFile{{Path: "01.flac", Name: "01.flac", SizeBytes: 3}},
		Tracks:          []db.ImportTrack{{Title: "Expected", DiscNumber: 1, TrackNumber: 1}},
	}}
	// An encoder that would prove the point if it were ever run: it leaves a
	// marker behind and fails loudly, so a bug that reordered the two steps
	// would fail this test instead of passing it by accident.
	binary, marker := writeFakeEncoder(t)
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop()).
		WithTranscoding(shrinkTo(binary))
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Artist", Album: "Different album", Title: "Expected", TrackNumber: 1,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.review, "is not conclusively any track of this edition") {
		t.Fatalf("review = %q", store.review)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the encoder was run on a file that was never proven")
	}
	if len(store.transcoded) != 0 {
		t.Fatalf("recorded re-encodes = %+v", store.transcoded)
	}
}

// writeFakeEncoder writes a script that marks the returned path and exits with
// a failure, standing in for ffmpeg wherever a test needs to prove the encoder
// was or was not reached rather than what it produced.
func writeFakeEncoder(t *testing.T) (binary, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "asked")
	binary = filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch \""+marker+"\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, marker
}
