package uploads

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

type fakeImportStore struct {
	uploadID   uuid.UUID
	rootPath   string
	importPath string
	err        error
}

func (store *fakeImportStore) CompleteUploadImport(
	_ context.Context, uploadID uuid.UUID, rootPath, importPath string,
) error {
	if store.err != nil {
		return store.err
	}
	store.uploadID, store.rootPath, store.importPath = uploadID, rootPath, importPath
	return nil
}

// taggedMP3 is a real file the real parser reads: an ID3v1 trailer is the
// smallest thing that carries an artist and an album.
func taggedMP3(artist, album, title string) []byte {
	data := make([]byte, 256)
	copy(data[128:131], "TAG")
	copy(data[131:161], title)
	copy(data[161:191], artist)
	copy(data[191:221], album)
	data[253] = 0
	data[254] = 1
	return data
}

// brokenFLAC claims to be FLAC and then stops, which is what a file that did
// not survive its journey looks like. The parser reports damage rather than
// absent tags, which is the distinction validation turns on.
func brokenFLAC() []byte {
	data := make([]byte, 11)
	copy(data, "fLaC")
	return data
}

// taggedFLAC is a real, minimal, decodable FLAC: a STREAMINFO block and a
// VORBIS_COMMENT block naming an artist, album and title, and no audio
// frames at all — the shortest file library.InspectAudio reads as lossless
// music rather than as damage. It exists to prove the upload path never
// re-encodes: a re-encode would turn this into an MP3, which is the one
// thing this file's own extension says it must not become before a
// resolved identity says otherwise.
func taggedFLAC(t *testing.T, artist, album, title string) []byte {
	t.Helper()
	const sampleRate, totalSamples = 44100, 44100

	stream := make([]byte, 34)
	stream[10] = byte((sampleRate >> 12) & 0xff)
	stream[11] = byte((sampleRate >> 4) & 0xff)
	stream[12] = byte((sampleRate & 0x0f) << 4)
	stream[13] = byte((totalSamples >> 32) & 0x0f)
	stream[14] = byte((totalSamples >> 24) & 0xff)
	stream[15] = byte((totalSamples >> 16) & 0xff)
	stream[16] = byte((totalSamples >> 8) & 0xff)
	stream[17] = byte(totalSamples & 0xff)

	comments := []string{"ARTIST=" + artist, "ALBUM=" + album, "TITLE=" + title}
	tags := make([]byte, 0, 64)
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
	return data
}

func newImporter(t *testing.T) (*Importer, *Staging, *fakeImportStore, string) {
	t.Helper()
	base := t.TempDir()
	staging, err := NewStaging(filepath.Join(base, "staging"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	libraryPath := filepath.Join(base, "music")
	store := &fakeImportStore{}
	return NewImporter(staging, libraryPath, store, zerolog.Nop()), staging, store, libraryPath
}

// stage puts files in the staging folder the way an accepted upload leaves
// them, so that importing can be asked about on its own.
func stage(t *testing.T, staging *Staging, files map[string][]byte) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := os.MkdirAll(staging.Path(id), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(staging.Path(id), name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// The whole of what roadmap 8a's first half is for: an album chosen in a
// browser is staged, validated, and in the library without anybody touching the
// filesystem by hand.
func TestAnUploadedAlbumIsStagedValidatedAndImported(t *testing.T) {
	importer, staging, store, libraryPath := newImporter(t)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, title := range map[string]string{
		"01 Mysterons.mp3": "Mysterons", "02 Sour Times.mp3": "Sour Times",
	} {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(taggedMP3("Portishead", "Dummy", title)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	upload, _, err := staging.Accept(context.Background(),
		multipart.NewReader(&buffer, writer.Boundary()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), upload.ID); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(libraryPath, "Portishead", "Dummy ["+upload.ID.String()[:8]+"]")
	if store.importPath != destination {
		t.Fatalf("importPath = %q, want %q", store.importPath, destination)
	}
	for _, name := range []string{"01 Mysterons.mp3", "02 Sour Times.mp3"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("stat %q = %v", name, err)
		}
	}
	staged, err := staging.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("staging holds %d uploads after the import", len(staged))
	}
}

func TestImportFilesAnUploadUnderTheArtistAndAlbumItsFilesAgreeOn(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3":  taggedMP3("Portishead", "Dummy", "Mysterons"),
		"02 Sour Times.mp3": taggedMP3("Portishead", "Dummy", "Sour Times"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Portishead", "Dummy ["+id.String()[:8]+"]")
	for _, name := range []string{"01 Mysterons.mp3", "02 Sour Times.mp3"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("stat %q = %v", name, err)
		}
	}
}

// A folder is where bytes live, never evidence. Where the files do not agree on
// an album, that field is silent and the template names the folder anyway — the
// upload's own identifier is what keeps it apart from another one.
func TestImportFilesAnUploadUnderTheArtistWhenOnlyItsAlbumsDisagree(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
		"02 Glory Box.mp3": taggedMP3("Portishead", "Roseland NYC Live", "Glory Box"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Portishead", "["+id.String()[:8]+"]")
	if _, err := os.Stat(filepath.Join(destination, "01 Mysterons.mp3")); err != nil {
		t.Fatalf("stat = %v, want the upload filed under the artist it agrees on", err)
	}
}

func TestImportFilesAnUploadUnderAnUnnamedFolderWhenItsFilesSayNothing(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	// A WAV off a disc carries no tags this parser reads. That is silence rather
	// than damage: the copy is still worth keeping.
	untagged := make([]byte, 512)
	copy(untagged, "RIFF....WAVEfmt ")
	id := stage(t, staging, map[string][]byte{"track01.wav": untagged})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Unknown", "["+id.String()[:8]+"]")
	if _, err := os.Stat(filepath.Join(destination, "track01.wav")); err != nil {
		t.Fatalf("stat = %v, want an untagged copy kept", err)
	}
}

// A path component is escaped rather than dropped, so two albums whose titles
// differ only there stay two folders.
func TestImportEscapesASeparatorInATag(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Untitled.mp3": taggedMP3("AC/DC", "Back in Black", "Hells Bells"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(libraryPath, "AC_DC")); err != nil {
		t.Fatalf("stat = %v, want the separator escaped", err)
	}
}

// One broken track holds the whole upload back. A release half in the library
// and half in staging is worse than one still waiting, because the half that
// landed is indistinguishable from music somebody put there on purpose.
func TestImportRefusesTheWholeUploadWhenOneFileIsNotAudio(t *testing.T) {
	importer, staging, store, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3":   taggedMP3("Portishead", "Dummy", "Mysterons"),
		"02 Sour Times.flac": brokenFLAC(),
	})

	_, err := importer.Import(context.Background(), id)
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
	if !strings.Contains(err.Error(), "02 Sour Times.flac") {
		t.Fatalf("err = %q, want it to name the file", err)
	}
	if _, statErr := os.Stat(libraryPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stat = %v, want nothing imported", statErr)
	}
	if store.importPath != "" {
		t.Fatalf("a refused upload was recorded as imported to %q", store.importPath)
	}
}

// The folder is deliberately kept, so that the reason and the files it names
// are both still there to look at.
func TestImportLeavesARefusedUploadInStaging(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{"01 Mysterons.flac": brokenFLAC()})

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("a broken upload was imported")
	}
	if _, err := staging.Open(id); err != nil {
		t.Fatalf("open = %v, want the refused upload still staged", err)
	}
}

func TestImportRefusesAnEmptyFile(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{"01 Mysterons.flac": {}})

	_, err := importer.Import(context.Background(), id)
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
	if !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("err = %q, want it to say the file is empty", err)
	}
}

// The extension is refused at the door, so a file of a type no scan would index
// only reaches here if something put it in the folder by hand.
func TestImportRefusesAFileTypeTheLibraryCannotHold(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{"cover.jpg": []byte("picture")})

	_, err := importer.Import(context.Background(), id)
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
}

func TestImportReportsAnUploadThatIsNotStaged(t *testing.T) {
	importer, _, _, _ := newImporter(t)

	if _, err := importer.Import(context.Background(), uuid.New()); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("err = %v, want ErrNotStaged", err)
	}
}

func TestImportReportsAnUploadWithNothingInIt(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	id := stage(t, staging, nil)

	if _, err := importer.Import(context.Background(), id); !errors.Is(err, ErrNoFiles) {
		t.Fatalf("err = %v, want ErrNoFiles", err)
	}
}

func TestImportStopsWhenTheJobIsCancelled(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := importer.Import(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A scan is what turns copied bytes into a row the resolver can be asked about,
// so the managed library becoming a music folder is part of the import.
func TestImportRecordsWhereTheUploadLandedAndAsksForAScan(t *testing.T) {
	importer, staging, store, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if store.uploadID != id || store.rootPath != libraryPath {
		t.Fatalf("recorded %s under %q", store.uploadID, store.rootPath)
	}
	want := filepath.Join(libraryPath, "Portishead", "Dummy ["+id.String()[:8]+"]")
	if store.importPath != want {
		t.Fatalf("importPath = %q, want %q", store.importPath, want)
	}
}

func TestImportClearsTheStagingFolderOfAnImportedUpload(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := staging.Open(id); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("open = %v, want the staging folder cleared", err)
	}
}

// The copy is renamed into place, so a destination that already exists is an
// import that already happened. A second attempt finishes what follows it
// rather than refusing music that is already on the disc.
func TestImportFinishesAnImportThatAlreadyCopiedItsFiles(t *testing.T) {
	importer, staging, store, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	store.err = errors.New("the database went away")
	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("a failed record was reported as an import")
	}
	if store.importPath != "" {
		t.Fatalf("a failed record left %q behind", store.importPath)
	}

	store.err = nil
	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if store.importPath == "" {
		t.Fatal("the second attempt recorded nothing, so the import can never be finished")
	}
}

// Nothing was made durable, so the bytes stay where a retry can find them.
func TestImportKeepsTheStagingFolderWhenTheRecordFails(t *testing.T) {
	importer, staging, store, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	store.err = errors.New("the database went away")

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("a failed record was reported as an import")
	}
	if _, err := staging.Open(id); err != nil {
		t.Fatalf("open = %v, want the upload still staged", err)
	}
}

func TestImportReportsADestinationThatIsNotAFolder(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	occupied := filepath.Join(libraryPath, "Portishead")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	blocking := filepath.Join(occupied, "Dummy ["+id.String()[:8]+"]")
	if err := os.WriteFile(blocking, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("a destination that is not a folder was imported into")
	}
}

func TestImportReportsALibraryItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	if err := os.MkdirAll(libraryPath, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(libraryPath, 0o755) })

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("an upload was imported into a library nobody may write to")
	}
}

func TestImportReportsAFileItCouldNotCopy(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	// The file the validation saw is gone by the time the copy reaches it, which
	// is a failure of the filesystem rather than an answer about the upload.
	importer.inspect = func(path string) library.AudioMetadata {
		metadata := library.InspectAudio(path)
		_ = os.Remove(path)
		return metadata
	}

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("a file that vanished was reported as imported")
	}
}

func TestImportFilesAnUploadUnderTheAlbumWhenOnlyItsArtistsDisagree(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3":  taggedMP3("Portishead", "Dummy", "Mysterons"),
		"02 Sour Times.mp3": taggedMP3("Beth Gibbons", "Dummy", "Sour Times"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Unknown", "Dummy ["+id.String()[:8]+"]")
	if _, err := os.Stat(filepath.Join(destination, "01 Mysterons.mp3")); err != nil {
		t.Fatalf("stat = %v, want the upload filed under the album it agrees on", err)
	}
}

// The managed copy is committed by the time this runs, so a staging folder that
// will not go away costs disc space and nothing else.
func TestImportSucceedsWhenTheStagingFolderCannotBeCleared(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	if err := os.Chmod(staging.Root(), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(staging.Root(), 0o755) })

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Portishead", "Dummy ["+id.String()[:8]+"]")
	if _, err := os.Stat(filepath.Join(destination, "01 Mysterons.mp3")); err != nil {
		t.Fatalf("stat = %v, want the file imported anyway", err)
	}
}

// Not being able to tell whether the destination is there is not the same as it
// not being there, and importing on that basis would copy over an album.
func TestImportReportsADestinationItCannotLookAt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	closed := filepath.Join(libraryPath, "Portishead")
	if err := os.MkdirAll(closed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("a destination nobody could look at was imported into")
	}
}

func TestImportReportsAnIntermediateFolderItCannotClearAway(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	parent := filepath.Join(libraryPath, "Portishead")
	leftover := filepath.Join(parent, ".schall-upload-Dummy ["+id.String()[:8]+"]")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "half.mp3"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("an import ran with a half-copied folder in the way")
	}
}

func TestImportReportsAnIntermediateFolderItCannotCreate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	parent := filepath.Join(libraryPath, "Portishead")
	if err := os.MkdirAll(libraryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	if _, err := importer.Import(context.Background(), id); err == nil {
		t.Fatal("an import ran with nowhere to assemble the copy")
	}
}

// A shutdown between validating an upload and copying it stops the copy rather
// than leaving a half-written folder for the next scan.
func TestImportStopsWhenTheJobIsCancelledBeforeTheCopy(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	importer.inspect = func(path string) library.AudioMetadata {
		metadata := library.InspectAudio(path)
		cancel()
		return metadata
	}

	if _, err := importer.Import(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	destination := filepath.Join(libraryPath, "Portishead", "Dummy ["+id.String()[:8]+"]")
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat = %v, want no folder a scan could walk into", err)
	}
	// And nothing half-assembled beside it either.
	entries, err := os.ReadDir(filepath.Join(libraryPath, "Portishead"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the library holds %d entries after a cancelled copy", len(entries))
	}
}

// filedBy is the layout an installation chose, in the shape the importer reads
// it: a function, because the template is read per import rather than held.
func filedBy(t *testing.T, text string) func(context.Context) (library.Template, error) {
	t.Helper()
	template, err := library.ParseTemplate(text)
	if err != nil {
		t.Fatal(err)
	}
	return func(context.Context) (library.Template, error) { return template, nil }
}

// The whole point of the setting: an upload arriving after a library was
// migrated to a new layout lands in that layout rather than rebuilding the one
// the migration replaced.
func TestImportFilesAnUploadWhereTheLayoutTemplateSays(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Portishead", "Dummy")
	if _, err := os.Stat(filepath.Join(destination, "01 Mysterons.mp3")); err != nil {
		t.Fatalf("stat = %v, want the upload filed where the template says", err)
	}
}

// The bytes are staged and validated by this point, so a setting nobody can
// read is not a reason to lose them.
func TestImportFilesAnUploadTheDefaultWayWhenTheLayoutCannotBeRead(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	importer.WithLayout(func(context.Context) (library.Template, error) {
		return library.Template{}, errors.New("the database went away")
	})
	id := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})

	if _, err := importer.Import(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Portishead", "Dummy ["+id.String()[:8]+"]")
	if _, err := os.Stat(filepath.Join(destination, "01 Mysterons.mp3")); err != nil {
		t.Fatalf("stat = %v, want the upload filed the way it always was", err)
	}
}

// A template need not name the upload, and then a second upload of the same
// album finds the first one's folder. What it holds is added to rather than
// taken for an import that already happened.
func TestImportAddsASecondUploadToTheFolderTheTemplateNames(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	first := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	second := stage(t, staging, map[string][]byte{
		"02 Sour Times.mp3": taggedMP3("Portishead", "Dummy", "Sour Times"),
	})

	if _, err := importer.Import(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(libraryPath, "Portishead", "Dummy")
	for _, name := range []string{"01 Mysterons.mp3", "02 Sour Times.mp3"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("stat %q = %v, want both uploads in the folder", name, err)
		}
	}
}

// Which of two files under one name the library keeps is a question for a
// person. Nothing here answers it by overwriting.
func TestImportRefusesToWriteOverAFileOfTheSameName(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	first := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	other := taggedMP3("Portishead", "Dummy", "Mysterons")
	// The same music by its tags and a different file by its bytes, so the two
	// render one folder and collide inside it. The padding goes in front because
	// the tags an ID3v1 trailer carries are the last thing in the file.
	second := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": append(make([]byte, 128), other...),
	})

	if _, err := importer.Import(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), second); err == nil {
		t.Fatal("a file already in the library was written over")
	}
	landed := filepath.Join(libraryPath, "Portishead", "Dummy", "01 Mysterons.mp3")
	info, err := os.Stat(landed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(other)) {
		t.Fatalf("the file in the library is %d bytes, want the %d it was imported at",
			info.Size(), len(other))
	}
}

// Two encodes of one track can be the same length, and skipping the second as
// a copy that already landed would report an upload that imported nothing.
func TestImportRefusesAFileOfTheSameNameAndSizeThatIsNotTheSameFile(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	first := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	// The same tags, the same name and the same length, and different audio.
	other := taggedMP3("Portishead", "Dummy", "Mysterons")
	copy(other, "ID3 different audio")
	second := stage(t, staging, map[string][]byte{"01 Mysterons.mp3": other})

	if _, err := importer.Import(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), second); err == nil {
		t.Fatal("a different file of the same size was taken for a copy already imported")
	}
}

// Import checks every missing file before placing one, so a later conflict
// leaves an existing destination unchanged.
func TestImportLeavesAnExistingDestinationUnchangedWhenASecondFileConflicts(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	first := stage(t, staging, map[string][]byte{
		"02 Sour Times.mp3": taggedMP3("Portishead", "Dummy", "Sour Times"),
	})
	if _, err := importer.Import(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	conflicting := taggedMP3("Portishead", "Dummy", "Sour Times")
	copy(conflicting, "different audio")
	second := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3":  taggedMP3("Portishead", "Dummy", "Mysterons"),
		"02 Sour Times.mp3": conflicting,
	})

	if _, err := importer.Import(context.Background(), second); err == nil {
		t.Fatal("an import with a later conflicting file succeeded")
	}
	destination := filepath.Join(libraryPath, "Portishead", "Dummy")
	if _, err := os.Stat(filepath.Join(destination, "01 Mysterons.mp3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the first file was placed after a later conflict: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "02 Sour Times.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	want := taggedMP3("Portishead", "Dummy", "Sour Times")
	if !bytes.Equal(got, want) {
		t.Fatal("the existing file changed after a later conflict")
	}
}

// longTaggedMP3 is a file of a real size carrying the tags of a real file: the
// ID3v1 trailer is the last 128 bytes, whatever comes before it.
func longTaggedMP3(artist, album, title string, size int) []byte {
	data := make([]byte, size)
	copy(data[size-128:], taggedMP3(artist, album, title)[128:])
	return data
}

// A file is compared to its end rather than to the end of the first chunk read
// of it. Two rips of one track share far more than the opening of a frame.
func TestImportComparesAFilePastItsFirstChunk(t *testing.T) {
	importer, staging, _, _ := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	long := longTaggedMP3("Portishead", "Dummy", "Mysterons", 200*1024)
	first := stage(t, staging, map[string][]byte{"01 Mysterons.mp3": long})
	diverging := append([]byte(nil), long...)
	diverging[150*1024] = 1
	second := stage(t, staging, map[string][]byte{"01 Mysterons.mp3": diverging})

	if _, err := importer.Import(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), second); err == nil {
		t.Fatal("two files that differ past the first chunk were taken for one")
	}
}

// Shutting Schall down midway through filling a folder that already holds music
// must leave nothing half written there either.
func TestImportLeavesNothingBehindWhenAddingToAFolderIsCancelled(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	first := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	second := stage(t, staging, map[string][]byte{
		"02 Sour Times.mp3": taggedMP3("Portishead", "Dummy", "Sour Times"),
	})
	if _, err := importer.Import(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	importer.inspect = func(path string) library.AudioMetadata {
		metadata := library.InspectAudio(path)
		cancel()
		return metadata
	}

	if _, err := importer.Import(ctx, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	entries, err := os.ReadDir(filepath.Join(libraryPath, "Portishead", "Dummy"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the folder holds %d entries after a cancelled copy, want only the file that landed",
			len(entries))
	}
}

// Nothing a scan would index is left behind by a copy that did not finish.
func TestImportLeavesNoHalfWrittenFileInAFolderItFailedToAddTo(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	importer.WithLayout(filedBy(t, "{artist}/{album}"))
	first := stage(t, staging, map[string][]byte{
		"01 Mysterons.mp3": taggedMP3("Portishead", "Dummy", "Mysterons"),
	})
	second := stage(t, staging, map[string][]byte{
		"02 Sour Times.mp3": taggedMP3("Portishead", "Dummy", "Sour Times"),
	})
	if _, err := importer.Import(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// The file validation saw is gone by the time the copy reaches it.
	importer.inspect = func(path string) library.AudioMetadata {
		metadata := library.InspectAudio(path)
		_ = os.Remove(path)
		return metadata
	}

	if _, err := importer.Import(context.Background(), second); err == nil {
		t.Fatal("a file that vanished was reported as imported")
	}
	entries, err := os.ReadDir(filepath.Join(libraryPath, "Portishead", "Dummy"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the folder holds %d entries after a failed copy, want only the file that landed",
			len(entries))
	}
}

// TestImportNeverReEncodesAnUpload is the regression this package's whole
// removal of transcoding is for. An upload has no verdict of its own —
// nothing about it is checked against a catalogue edition — so the acoustic
// check and the resolver that run after the scan read whatever bytes are on
// disk, and those must be the bytes that arrived: transcoding on the way in
// would mean every later identification of this file reads Schall's own
// re-encode rather than the file the user actually uploaded.
//
// There is no WithTranscoding on Importer any more, which is what makes this
// true rather than merely configured to be true today. The transcoding
// setting still reaches this music, once it has a resolved identity, through
// the library sweep (internal/library/transcode.go) — the same sweep every
// other proven file in the library goes through.
func TestImportNeverReEncodesAnUpload(t *testing.T) {
	importer, staging, _, libraryPath := newImporter(t)
	original := taggedFLAC(t, "Portishead", "Dummy", "Mysterons")

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("files", "01 Mysterons.flac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(original); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	upload, _, err := staging.Accept(context.Background(),
		multipart.NewReader(&buffer, writer.Boundary()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), upload.ID); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(libraryPath, "Portishead", "Dummy ["+upload.ID.String()[:8]+"]")
	written, err := os.ReadFile(filepath.Join(destination, "01 Mysterons.flac"))
	if err != nil {
		t.Fatalf("the file is not filed as the FLAC it arrived as: %v", err)
	}
	if !bytes.Equal(written, original) {
		t.Fatal("the uploaded FLAC's bytes were changed on the way into the library")
	}
}
