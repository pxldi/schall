package coverart

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
)

// A track Schall holds by its SoundCloud address belongs to no release, so the
// release pass cannot reach it. Its picture goes into the folder the file is
// in, so that a player reading folders shows it.
func TestAPictureIsWrittenBesideAFileThatBelongsToNoRelease(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "Uploads")
	store := &fakeDiskStore{
		files:     []db.SourceFileToPictureRow{{ID: uuid.New(), Folder: music}},
		fileCover: db.FileCoverArtRow{Image: aPicture(3), ContentType: "image/jpeg", Source: "soundcloud"},
		roots:     []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(music, "cover.jpg"))
	if err != nil {
		t.Fatalf("no picture was written beside the file: %v", err)
	}
	if !bytes.Equal(written, aPicture(3)) {
		t.Fatalf("picture = %q, want the one held for the file", written)
	}
}

// A folder that already holds a cover keeps it. Two uploads can land in one
// folder, and the first one's picture is the one that stays.
func TestAFolderThatAlreadyHasACoverIsLeftAlone(t *testing.T) {
	root := t.TempDir()
	music := folder(t, root, "Uploads")
	if err := os.WriteFile(filepath.Join(music, "cover.jpg"), aPicture(1), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeDiskStore{
		files:     []db.SourceFileToPictureRow{{ID: uuid.New(), Folder: music}},
		fileCover: db.FileCoverArtRow{Image: aPicture(3), ContentType: "image/jpeg", Source: "soundcloud"},
		roots:     []db.LibraryRootRow{{Path: root}},
	}

	if _, err := NewDiskWriter(store, &fakeArchive{}, zerolog.Nop()).Write(context.Background()); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(music, "cover.jpg"))
	if err != nil || !bytes.Equal(written, aPicture(1)) {
		t.Fatalf("cover.jpg = %q, %v; want the picture that was already there", written, err)
	}
}
