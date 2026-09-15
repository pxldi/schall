package coverart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
)

// A release the archives have no picture of is often one somebody already owns a
// copy of, and that copy is carrying the cover.
func TestAReleaseIsPicturedByThePictureInItsOwnFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, jpegOfSize(t, 900, 900))

	artwork, err := Embedded([]string{path})
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	if len(artwork.Image) == 0 || artwork.Source != NameEmbedded {
		t.Fatalf("artwork = %+v, want the picture out of the file, labelled as such", artwork)
	}
}

// A cover packed into a file is whatever size the person who packed it felt
// like, and this one is a thumbnail beside a name.
func TestAPictureOutOfAFileIsStoredAtAReadableSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, jpegOfSize(t, 1600, 1600))

	artwork, err := Embedded([]string{path})
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(artwork.Image))
	if err != nil {
		t.Fatalf("decode the stored cover: %v", err)
	}
	if decoded.Bounds().Dx() != coverWidth {
		t.Fatalf("stored width = %d, want it scaled to %d", decoded.Bounds().Dx(), coverWidth)
	}
}

// A picture smaller than the size everything is stored at is left at its own
// size rather than blown up into a blurred one.
func TestASmallPictureIsNotEnlarged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, jpegOfSize(t, 200, 200))

	artwork, err := Embedded([]string{path})
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(artwork.Image))
	if err != nil {
		t.Fatalf("decode the stored cover: %v", err)
	}
	if decoded.Bounds().Dx() != 200 {
		t.Fatalf("stored width = %d, want the picture left at its own size", decoded.Bounds().Dx())
	}
}

// A file carrying no picture is an absence, and the next file is tried.
func TestAFileCarryingNoPictureIsPassedOver(t *testing.T) {
	folder := t.TempDir()
	bare := filepath.Join(folder, "01.mp3")
	writeTaggedMP3(t, bare, nil)
	pictured := filepath.Join(folder, "02.mp3")
	writeTaggedMP3(t, pictured, jpegOfSize(t, 600, 600))

	artwork, err := Embedded([]string{bare, pictured})
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	if len(artwork.Image) == 0 {
		t.Fatal("the picture in the second file was not found")
	}
}

// A release whose files carry nothing has no picture here, which the caller
// writes down and stops asking about.
func TestAReleaseWhoseFilesCarryNoPictureIsAnAbsence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, nil)

	if _, err := Embedded([]string{path}); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// Something that will not decode is not a picture, whatever the file says it is.
func TestSomethingThatIsNotAPictureIsNotACover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, []byte("not a picture at all"))

	if _, err := Embedded([]string{path}); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the bytes refused", err)
	}
}

// A box set is not worth reading end to end for a thumbnail. Almost every
// release answers on the first file, so only the first few are opened.
func TestOnlyTheFirstFewOfAReleasesFilesAreOpened(t *testing.T) {
	folder := t.TempDir()
	paths := make([]string, 0, maxEmbeddedFiles+1)
	for index := range maxEmbeddedFiles + 1 {
		path := filepath.Join(folder, fmt.Sprintf("%02d.mp3", index))
		picture := []byte(nil)
		if index == maxEmbeddedFiles {
			picture = jpegOfSize(t, 200, 200)
		}
		writeTaggedMP3(t, path, picture)
		paths = append(paths, path)
	}

	if _, err := Embedded(paths); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the release read no further than the cap", err)
	}
}

// A file the library knows about and the disk does not is this release's
// problem, not the sweep's: the next file is tried.
func TestAFileTheDiskNoLongerHasIsPassedOver(t *testing.T) {
	folder := t.TempDir()
	pictured := filepath.Join(folder, "02.mp3")
	writeTaggedMP3(t, pictured, jpegOfSize(t, 200, 200))

	artwork, err := Embedded([]string{filepath.Join(folder, "gone.mp3"), pictured})
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	if len(artwork.Image) == 0 {
		t.Fatal("the picture in the file that is still there was not found")
	}
}

// A file nothing can read a tag out of carries no picture anybody can read
// either, and it is passed over rather than failing the release.
func TestAFileWithNoReadableTagIsPassedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	if err := os.WriteFile(path, []byte("not audio at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Embedded([]string{path}); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the unreadable file passed over", err)
	}
}

// A picture larger than anything a thumbnail could be is not decoded at all: a
// file can carry an arbitrarily large payload, and this is a name on a page.
func TestAPictureTooLargeToBeAThumbnailIsNotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, make([]byte, maxImageBytes+64))

	if _, err := Embedded([]string{path}); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the oversized payload refused", err)
	}
}

// A banner far wider than it is tall would scale to no height at all, and a
// picture of zero pixels is not a picture.
func TestAPictureFarWiderThanItIsTallKeepsAtLeastOneRowOfPixels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, jpegOfSize(t, 2000, 1))

	artwork, err := Embedded([]string{path})
	if err != nil {
		t.Fatalf("Embedded() error = %v", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(artwork.Image))
	if err != nil {
		t.Fatalf("decode the stored cover: %v", err)
	}
	if decoded.Bounds().Dy() < 1 {
		t.Fatalf("stored height = %d, want at least one row of pixels", decoded.Bounds().Dy())
	}
}

// A release whose files could not be listed is not one whose files carry no
// picture, and saying so would have the sweep record an absence permanently.
func TestAReleaseWhoseFilesCouldNotBeListedIsNotAnAbsence(t *testing.T) {
	library := NewLibrary(failingFiles{})

	_, err := library.Cover(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

type failingFiles struct{}

func (failingFiles) ReleaseMappedFilePaths(
	context.Context, db.ReleaseMappedFilePathsParams,
) ([]string, error) {
	return nil, errors.New("the database is not answering")
}

// Only files a track mapping tied to this release are read. A mapping is a match
// this project proved, which is what makes the picture inside one of them a
// picture of this release rather than of whatever was in a folder.
func TestOnlyTheReleasesOwnFilesAreRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, jpegOfSize(t, 600, 600))
	asked := uuid.New()
	files := &fakeFiles{paths: map[uuid.UUID][]string{asked: {path}}}

	library := NewLibrary(files)
	if _, err := library.Cover(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a release with no mapped files reported as an absence", err)
	}
	artwork, err := library.Cover(context.Background(), asked)
	if err != nil {
		t.Fatalf("Cover() error = %v", err)
	}
	if len(artwork.Image) == 0 {
		t.Fatal("the picture in the release's own file was not found")
	}
}

// The release's own copy is a proven attachment and a name search is a guess, so
// the copy is asked first and the guess is never reached when it answers.
func TestTheReleasesOwnCopyIsPreferredToANameSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "01.mp3")
	writeTaggedMP3(t, path, jpegOfSize(t, 600, 600))
	albumID := uuid.New()
	guess := &countingByName{}

	sources := Sources{
		FromLibrary: NewLibrary(&fakeFiles{paths: map[uuid.UUID][]string{albumID: {path}}}),
		ByName:      guess,
	}
	artwork, err := sources.Front(context.Background(), Release{
		AlbumID: albumID, Artist: "an artist", Title: "a title",
	})
	if err != nil {
		t.Fatalf("Front() error = %v", err)
	}
	if artwork.Source != NameEmbedded {
		t.Fatalf("source = %q, want the copy that is already here", artwork.Source)
	}
	if guess.asked {
		t.Fatal("a name was searched although the release's own copy answered")
	}
}

type countingByName struct{ asked bool }

func (guess *countingByName) Cover(context.Context, string, string) (Artwork, error) {
	guess.asked = true
	return Artwork{Image: []byte("a guess"), ContentType: "image/jpeg", Source: NameITunes}, nil
}

type fakeFiles struct{ paths map[uuid.UUID][]string }

func (files *fakeFiles) ReleaseMappedFilePaths(
	_ context.Context, params db.ReleaseMappedFilePathsParams,
) ([]string, error) {
	return files.paths[params.AlbumID], nil
}

// jpegOfSize is a picture of a given size, which is all a cover needs to be here.
func jpegOfSize(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			picture.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 90, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, nil); err != nil {
		t.Fatalf("encode a test picture: %v", err)
	}
	return encoded.Bytes()
}

// writeTaggedMP3 writes a file carrying an ID3v2 tag, with an attached picture
// when one is given. The audio itself is irrelevant: nothing here listens to it.
func writeTaggedMP3(t *testing.T, path string, picture []byte) {
	t.Helper()
	var frames bytes.Buffer
	if picture != nil {
		var body bytes.Buffer
		body.WriteByte(0) // ISO-8859-1 text
		body.WriteString("image/jpeg")
		body.WriteByte(0)
		body.WriteByte(3) // front cover
		body.WriteByte(0) // empty description
		body.Write(picture)

		frames.WriteString("APIC")
		frames.Write(syncSafe(body.Len()))
		frames.Write([]byte{0, 0})
		frames.Write(body.Bytes())
	}
	title := append([]byte{0}, []byte("a title")...)
	frames.WriteString("TIT2")
	frames.Write(syncSafe(len(title)))
	frames.Write([]byte{0, 0})
	frames.Write(title)

	var file bytes.Buffer
	file.WriteString("ID3")
	file.Write([]byte{4, 0, 0})
	file.Write(syncSafe(frames.Len()))
	file.Write(frames.Bytes())

	if err := os.WriteFile(path, file.Bytes(), 0o644); err != nil {
		t.Fatalf("write a test file: %v", err)
	}
}

// syncSafe is how ID3v2 writes a length: seven bits per byte.
func syncSafe(size int) []byte {
	return []byte{
		byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f),
		byte(size >> 7 & 0x7f), byte(size & 0x7f),
	}
}
