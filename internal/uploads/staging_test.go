package uploads

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// newStaging is a staging folder in a directory of the test's own, with the
// volume reported as having room. Every test that cares about space says so.
func newStaging(t *testing.T) *Staging {
	t.Helper()
	staging, err := NewStaging(filepath.Join(t.TempDir(), "staging"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	return staging
}

// multipartBody is one upload as a browser sends it: a form of file parts.
func multipartBody(t *testing.T, files map[string]string) (*multipart.Reader, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return multipart.NewReader(&buffer, writer.Boundary()), writer.Boundary()
}

// multipartBodyWithAddress is multipartBody plus the plain form fields a
// browser sends beside a one-file upload's bytes once somebody pastes a
// SoundCloud address.
func multipartBodyWithAddress(
	t *testing.T, files map[string]string, address map[string]string,
) *multipart.Reader {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for field, value := range address {
		if err := writer.WriteField(field, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return multipart.NewReader(&buffer, writer.Boundary())
}

// The address rides beside the bytes as plain fields, and Accept reads them
// without mistaking either for a file.
func TestAcceptReadsTheSoundCloudAddressSentBesideAFile(t *testing.T) {
	staging := newStaging(t)
	parts := multipartBodyWithAddress(t,
		map[string]string{"Illegal.flac": "audio"},
		map[string]string{
			"sourceUrl": "https://soundcloud.com/abo/illegal",
			"artist":    "PinkPantheress", "title": "Illegal", "remixer": "Abo",
		},
	)

	upload, address, err := staging.Accept(context.Background(), parts)
	if err != nil {
		t.Fatal(err)
	}
	if names := upload.Names(); len(names) != 1 || names[0] != "Illegal.flac" {
		t.Fatalf("names = %v, want the one file staged", names)
	}
	want := Address{
		URL: "https://soundcloud.com/abo/illegal", Artist: "PinkPantheress", Title: "Illegal", Remixer: "Abo",
	}
	if address != want {
		t.Fatalf("address = %+v, want %+v", address, want)
	}
	if !address.Sent() {
		t.Fatal("Sent() = false for an address that was sent")
	}
}

// Nothing sent is what every upload got before addresses existed.
func TestAcceptReadsNoAddressWhenNoneWasSent(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{"Illegal.flac": "audio"})

	_, address, err := staging.Accept(context.Background(), parts)
	if err != nil {
		t.Fatal(err)
	}
	if address.Sent() {
		t.Fatalf("address = %+v, want nothing sent", address)
	}
}

func TestStagingIsCreatedWhereItIsConfigured(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "staging")

	staging, err := NewStaging(root, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("stat %q = %v, %v", root, info, err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if staging.Root() != resolved {
		t.Fatalf("root = %q, want %q", staging.Root(), resolved)
	}
}

// A staging path reached through a symlink into the library would otherwise
// pass a containment check it plainly fails, so it is resolved the way a music
// folder is before anything is compared.
func TestStagingResolvesASymlinkedPath(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-staging")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "staging")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	staging, err := NewStaging(link, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if staging.Root() != real {
		t.Fatalf("root = %q, want %q", staging.Root(), real)
	}
}

func TestStagingRefusesAnEmptyPath(t *testing.T) {
	if _, err := NewStaging("   ", zerolog.Nop()); err == nil {
		t.Fatal("an empty staging path was accepted")
	}
}

func TestStagingRefusesARelativePath(t *testing.T) {
	if _, err := NewStaging("staging", zerolog.Nop()); err == nil {
		t.Fatal("a relative staging path was accepted")
	}
}

func TestStagingRefusesAPathThatIsNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "staging")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStaging(file, zerolog.Nop()); err == nil {
		t.Fatal("a file was accepted as the staging folder")
	}
}

// An installation should learn that its staging volume is read-only at startup
// rather than from the first upload somebody spends ten minutes on.
func TestStagingRefusesAFolderItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	root := filepath.Join(t.TempDir(), "staging")
	if err := os.Mkdir(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	if _, err := NewStaging(root, zerolog.Nop()); err == nil {
		t.Fatal("a read-only staging folder was accepted")
	}
}

func TestStagingRefusesAFolderInsideAMusicFolder(t *testing.T) {
	err := EnsureOutsideLibrary("/music/incoming", []string{"/music"})
	if err == nil {
		t.Fatal("a staging folder inside a music folder was accepted")
	}
	if !strings.Contains(err.Error(), "/music") {
		t.Fatalf("error = %q, want it to name the music folder", err)
	}
}

func TestStagingRefusesAFolderThatContainsAMusicFolder(t *testing.T) {
	if err := EnsureOutsideLibrary("/data", []string{"/data/music"}); err == nil {
		t.Fatal("a staging folder containing a music folder was accepted")
	}
}

func TestStagingIsAcceptedBesideTheMusicFolders(t *testing.T) {
	if err := EnsureOutsideLibrary("/data/staging", []string{"  ", "/music", "/srv/music"}); err != nil {
		t.Fatal(err)
	}
}

// A path that cannot be compared at all is not evidence that staging is safe,
// but it is also not a containment: the check reports what it can prove.
func TestStagingIgnoresAMusicFolderItCannotCompare(t *testing.T) {
	if err := EnsureOutsideLibrary("/data/staging", []string{"relative/music"}); err != nil {
		t.Fatal(err)
	}
}

// A music folder reached through a symlink is an ordinary container layout, and
// the staging root arrives here already resolved. Comparing only the configured
// spelling would let a staging folder physically inside the library start —
// which is the one thing this check exists to stop.
func TestStagingRefusesAFolderInsideASymlinkedMusicFolder(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "library")
	if err := os.MkdirAll(filepath.Join(real, "incoming"), 0o755); err != nil {
		t.Fatal(err)
	}
	music := filepath.Join(base, "music")
	if err := os.Symlink(real, music); err != nil {
		t.Fatal(err)
	}

	staging, err := NewStaging(filepath.Join(real, "incoming"), zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureOutsideLibrary(staging.Root(), []string{music}); err == nil {
		t.Fatal("a staging folder inside a symlinked music folder was accepted")
	}
}

// And the other way round: a music folder that resolves inside the staging root
// is the same hazard read from the other end.
func TestStagingRefusesAFolderContainingASymlinkedMusicFolder(t *testing.T) {
	base := t.TempDir()
	stagingRoot := filepath.Join(base, "staging")
	if err := os.MkdirAll(filepath.Join(stagingRoot, "library"), 0o755); err != nil {
		t.Fatal(err)
	}
	music := filepath.Join(base, "music")
	if err := os.Symlink(filepath.Join(stagingRoot, "library"), music); err != nil {
		t.Fatal(err)
	}

	staging, err := NewStaging(stagingRoot, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureOutsideLibrary(staging.Root(), []string{music}); err == nil {
		t.Fatal("a staging folder containing a symlinked music folder was accepted")
	}
}

func TestHeadroomRefusesAnUploadTheVolumeHasNoRoomFor(t *testing.T) {
	staging := newStaging(t)
	staging.free = func(string) (int64, error) { return 500 << 20, nil }

	err := staging.CheckHeadroom(400 << 20)
	if !errors.Is(err, ErrNoHeadroom) {
		t.Fatalf("err = %v, want ErrNoHeadroom", err)
	}
}

func TestHeadroomAcceptsAnUploadTheVolumeHasRoomFor(t *testing.T) {
	staging := newStaging(t)
	staging.free = func(string) (int64, error) { return 8 << 30, nil }

	if err := staging.CheckHeadroom(400 << 20); err != nil {
		t.Fatal(err)
	}
}

// A request that did not say how large it is still has to leave the reserve
// behind; the copy itself fails honestly if the volume fills up under it.
func TestHeadroomAsksOnlyForTheReserveWhenTheSizeIsUnknown(t *testing.T) {
	staging := newStaging(t)
	staging.free = func(string) (int64, error) { return headroomReserve + 1, nil }

	if err := staging.CheckHeadroom(-1); err != nil {
		t.Fatal(err)
	}
}

func TestHeadroomReportsAVolumeItCannotRead(t *testing.T) {
	staging := newStaging(t)
	staging.free = func(string) (int64, error) { return 0, errors.New("no such volume") }

	if err := staging.CheckHeadroom(0); err == nil {
		t.Fatal("an unreadable volume was treated as having room")
	}
}

// The default reader is the real one, so a folder that exists reports the space
// its volume actually has.
func TestHeadroomReadsTheVolumeTheStagingFolderIsOn(t *testing.T) {
	staging := newStaging(t)

	if err := staging.CheckHeadroom(0); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeFreeReportsAPathThatIsNotThere(t *testing.T) {
	if _, err := volumeFree(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a path that is not there reported free space")
	}
}

func TestAcceptStagesEveryFileItWasSent(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{
		"02 Sour Times.flac": "second",
		"01 Mysterons.flac":  "first",
	})

	upload, _, err := staging.Accept(context.Background(), parts)
	if err != nil {
		t.Fatal(err)
	}
	if names := upload.Names(); len(names) != 2 || names[0] != "01 Mysterons.flac" {
		t.Fatalf("names = %v, want them sorted", names)
	}
	if upload.TotalBytes() != int64(len("first")+len("second")) {
		t.Fatalf("total = %d", upload.TotalBytes())
	}
	written, err := os.ReadFile(filepath.Join(staging.Path(upload.ID), "01 Mysterons.flac"))
	if err != nil || string(written) != "first" {
		t.Fatalf("staged file = %q, %v", written, err)
	}
}

// Nothing is sanitised. A name carrying a path is refused, because guessing at
// what a caller meant by "../" is how a staging folder stops being one.
func TestAcceptRefusesAFilenameCarryingAPath(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{"../../escape.flac": "audio"})

	_, _, err := staging.Accept(context.Background(), parts)
	if !errors.Is(err, ErrUnsafeName) {
		t.Fatalf("err = %v, want ErrUnsafeName", err)
	}
}

func TestAcceptRefusesAHiddenFilename(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{"._01 Mysterons.flac": "resource fork"})

	_, _, err := staging.Accept(context.Background(), parts)
	if !errors.Is(err, ErrUnsafeName) {
		t.Fatalf("err = %v, want ErrUnsafeName", err)
	}
}

// A name a MIME header cannot even carry never reaches Accept, so the rule is
// asked directly. It is kept because the reason a name is refused should not
// depend on which layer happens to notice first.
func TestAFilenameIsAFilenameOrItIsRefused(t *testing.T) {
	for _, name := range []string{
		"01 Mysterons.flac", "Étude n° 3.flac", "a b - c (1994).flac",
	} {
		if !safeName(name) {
			t.Errorf("safeName(%q) = false, want a plain name accepted", name)
		}
	}
	for _, name := range []string{
		"", ".", "..", "../escape.flac", `disc 1\01.flac`, "disc 1/01.flac",
		".hidden.flac", "track\x00.flac", "track\x07.flac",
	} {
		if safeName(name) {
			t.Errorf("safeName(%q) = true, want it refused", name)
		}
	}
}

// A part with no Content-Disposition at all is not a file, and is passed over
// rather than staged under a name nobody sent.
func TestAcceptPassesOverAPartThatNamesNoFile(t *testing.T) {
	staging := newStaging(t)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if _, err := writer.CreatePart(textproto.MIMEHeader{}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err := staging.Accept(context.Background(),
		multipart.NewReader(&buffer, writer.Boundary()))
	if !errors.Is(err, ErrNoFiles) {
		t.Fatalf("err = %v, want ErrNoFiles", err)
	}
}

// The library's own list decides, so a type no scan would ever index cannot
// reach the staging folder at all.
func TestAcceptRefusesAFileTypeTheLibraryCannotHold(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{"cover.jpg": "picture"})

	_, _, err := staging.Accept(context.Background(), parts)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestAcceptRefusesTheSameFilenameTwice(t *testing.T) {
	staging := newStaging(t)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for range 2 {
		part, err := writer.CreateFormFile("files", "01 Mysterons.flac")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("audio")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err := staging.Accept(context.Background(),
		multipart.NewReader(&buffer, writer.Boundary()))
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("err = %v, want ErrDuplicateName", err)
	}
}

func TestAcceptRefusesARequestWithNoFilesInIt(t *testing.T) {
	staging := newStaging(t)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err := writer.WriteField("note", "just a field"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err := staging.Accept(context.Background(),
		multipart.NewReader(&buffer, writer.Boundary()))
	if !errors.Is(err, ErrNoFiles) {
		t.Fatalf("err = %v, want ErrNoFiles", err)
	}
}

// A folder half of an album deep looks exactly like one that finished, so
// nothing partial is left behind for the importer to find.
func TestAcceptLeavesNothingBehindWhenItRefuses(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{"../escape.flac": "audio"})

	if _, _, err := staging.Accept(context.Background(), parts); err == nil {
		t.Fatal("the upload was accepted")
	}
	entries, err := os.ReadDir(staging.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging holds %d entries after a refusal", len(entries))
	}
}

// The browser going away mid-transfer is the ordinary case, not an exception:
// the folder goes with it.
func TestAcceptLeavesNothingBehindWhenTheClientDisconnects(t *testing.T) {
	staging := newStaging(t)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("files", "01 Mysterons.flac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a"), 64)); err != nil {
		t.Fatal(err)
	}
	// The body stops mid-part, which is what a dropped connection looks like to
	// the multipart reader.
	truncated := buffer.Bytes()[:buffer.Len()-16]

	_, _, err = staging.Accept(context.Background(),
		multipart.NewReader(bytes.NewReader(truncated), writer.Boundary()))
	if err == nil {
		t.Fatal("a truncated upload was accepted")
	}
	entries, err := os.ReadDir(staging.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging holds %d entries after a disconnect", len(entries))
	}
}

func TestAcceptStopsWhenTheRequestIsCancelled(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{"01 Mysterons.flac": "audio"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := staging.Accept(ctx, parts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

type cancelOnRead struct {
	cancel context.CancelFunc
	read   bool
}

func (reader *cancelOnRead) Read(buffer []byte) (int, error) {
	if reader.read {
		return 0, errors.New("the reader was called after cancellation")
	}
	reader.read = true
	copy(buffer, "audio")
	reader.cancel()
	return len("audio"), nil
}

func TestWriteStopsAndRemovesAPartialFileWhenTheContextIsCancelled(t *testing.T) {
	staging := newStaging(t)
	directory := filepath.Join(staging.Root(), "upload")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelOnRead{cancel: cancel}

	_, err := staging.write(ctx, directory, "01 Mysterons.flac", map[string]bool{}, reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "01 Mysterons.flac")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file = %v, want it removed", err)
	}
}

func TestAcceptReportsAFileItCouldNotWrite(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{
		strings.Repeat("a", 300) + ".flac": "audio",
	})

	if _, _, err := staging.Accept(context.Background(), parts); err == nil {
		t.Fatal("a file the filesystem refused was reported as staged")
	}
}

func TestAcceptReportsAFolderItCouldNotCreate(t *testing.T) {
	staging := newStaging(t)
	if err := os.RemoveAll(staging.Root()); err != nil {
		t.Fatal(err)
	}
	parts, _ := multipartBody(t, map[string]string{"01 Mysterons.flac": "audio"})

	if _, _, err := staging.Accept(context.Background(), parts); err == nil {
		t.Fatal("an upload with nowhere to go was accepted")
	}
}

// A part whose bytes stop early is a transfer that did not finish, and the file
// it was writing must not be reported as staged.
func TestAcceptReportsAPartThatStoppedShort(t *testing.T) {
	staging := newStaging(t)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("files", "01 Mysterons.flac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a"), 4096)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body := io.MultiReader(
		bytes.NewReader(buffer.Bytes()[:512]),
		failingReader{errors.New("connection reset by peer")},
	)

	_, _, err = staging.Accept(context.Background(), multipart.NewReader(body, writer.Boundary()))
	if err == nil {
		t.Fatal("a part that stopped short was accepted")
	}
}

type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }

func TestOpenReadsAStagedUploadBackOffTheFilesystem(t *testing.T) {
	staging := newStaging(t)
	parts, _ := multipartBody(t, map[string]string{"01 Mysterons.flac": "audio"})
	staged, _, err := staging.Accept(context.Background(), parts)
	if err != nil {
		t.Fatal(err)
	}

	upload, err := staging.Open(staged.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(upload.Files) != 1 || upload.Files[0].Name != "01 Mysterons.flac" {
		t.Fatalf("files = %#v", upload.Files)
	}
	if upload.TotalBytes() != int64(len("audio")) {
		t.Fatalf("total = %d", upload.TotalBytes())
	}
}

func TestOpenReportsAnUploadThatIsNotStaged(t *testing.T) {
	staging := newStaging(t)

	if _, err := staging.Open(uuid.New()); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("err = %v, want ErrNotStaged", err)
	}
}

func TestOpenReportsAnIdentifierThatNamesAFile(t *testing.T) {
	staging := newStaging(t)
	id := uuid.New()
	if err := os.WriteFile(staging.Path(id), []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := staging.Open(id); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("err = %v, want ErrNotStaged", err)
	}
}

func TestOpenPassesOverFoldersInsideAnUpload(t *testing.T) {
	staging := newStaging(t)
	id := uuid.New()
	if err := os.MkdirAll(filepath.Join(staging.Path(id), "disc 2"), 0o755); err != nil {
		t.Fatal(err)
	}

	upload, err := staging.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(upload.Files) != 0 {
		t.Fatalf("files = %#v, want the folder passed over", upload.Files)
	}
}

func TestOpenReportsAFolderItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	staging := newStaging(t)
	id := uuid.New()
	if err := os.Mkdir(staging.Path(id), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(staging.Path(id), 0o755) })

	if _, err := staging.Open(id); err == nil {
		t.Fatal("an unreadable folder was read")
	}
}

func TestListReportsEveryStagedUpload(t *testing.T) {
	staging := newStaging(t)
	first, _, err := staging.Accept(context.Background(),
		firstOf(multipartBody(t, map[string]string{"01 Mysterons.flac": "audio"})))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := staging.Accept(context.Background(),
		firstOf(multipartBody(t, map[string]string{"02 Sour Times.flac": "audio"})))
	if err != nil {
		t.Fatal(err)
	}

	uploads, err := staging.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 2 {
		t.Fatalf("uploads = %#v", uploads)
	}
	found := map[uuid.UUID]bool{uploads[0].ID: true, uploads[1].ID: true}
	if !found[first.ID] || !found[second.ID] {
		t.Fatalf("uploads = %#v, want both", uploads)
	}
}

// Anything in the folder that Schall did not put there is passed over rather
// than reported as an upload nobody can account for.
func TestListPassesOverWhatSchallDidNotStage(t *testing.T) {
	staging := newStaging(t)
	if err := os.Mkdir(filepath.Join(staging.Root(), "not-a-uuid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging.Root(), "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	uploads, err := staging.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 0 {
		t.Fatalf("uploads = %#v, want nothing listed", uploads)
	}
}

func TestListReportsAStagingFolderItCannotRead(t *testing.T) {
	staging := newStaging(t)
	if err := os.RemoveAll(staging.Root()); err != nil {
		t.Fatal(err)
	}

	if _, err := staging.List(); err == nil {
		t.Fatal("a staging folder that is gone was listed")
	}
}

func TestListReportsAnUploadItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	staging := newStaging(t)
	id := uuid.New()
	if err := os.Mkdir(staging.Path(id), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(staging.Path(id), 0o755) })

	if _, err := staging.List(); err == nil {
		t.Fatal("an unreadable upload was listed")
	}
}

// An upload removed between the listing and the reading is not an error: it is
// one fewer upload.
func TestListPassesOverAnUploadThatWentAwayUnderIt(t *testing.T) {
	staging := newStaging(t)
	id := uuid.New()
	if err := os.Mkdir(staging.Path(id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(staging.Path(id)); err != nil {
		t.Fatal(err)
	}

	uploads, err := staging.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 0 {
		t.Fatalf("uploads = %#v", uploads)
	}
}

func TestDiscardRemovesAStagedUpload(t *testing.T) {
	staging := newStaging(t)
	upload, _, err := staging.Accept(context.Background(),
		firstOf(multipartBody(t, map[string]string{"01 Mysterons.flac": "audio"})))
	if err != nil {
		t.Fatal(err)
	}

	if err := staging.Discard(upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging.Path(upload.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat = %v, want the folder gone", err)
	}
}

func TestDiscardAcceptsAnUploadThatIsAlreadyGone(t *testing.T) {
	staging := newStaging(t)

	if err := staging.Discard(uuid.New()); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardReportsAnUploadItCouldNotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	staging := newStaging(t)
	id := uuid.New()
	if err := os.MkdirAll(filepath.Join(staging.Path(id), "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(staging.Path(id), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(staging.Path(id), 0o755) })

	if err := staging.Discard(id); err == nil {
		t.Fatal("an upload that could not be removed reported success")
	}
}

// The worker asks this to decide whether a second attempt could go differently.
func TestRefusedNamesWhatTheUploadItselfAnswered(t *testing.T) {
	for _, err := range []error{
		ErrNoFiles, ErrUnsafeName, ErrUnsupported, ErrDuplicateName,
		ErrNoHeadroom, ErrNotStaged, ErrUnreadable,
	} {
		if !Refused(err) {
			t.Errorf("Refused(%v) = false, want the upload's own answer", err)
		}
	}
}

func TestRefusedLeavesAFilesystemFailureRetryable(t *testing.T) {
	if Refused(errors.New("input/output error")) {
		t.Fatal("a filesystem failure was treated as the upload's own answer")
	}
}

func firstOf(parts *multipart.Reader, _ string) *multipart.Reader { return parts }
