package tagging

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.senan.xyz/taglib"
)

// A file Schall holds by its SoundCloud address says so in its comment, and the
// address survives a revert: a mapping being withdrawn does not make the
// address untrue. A file Schall has nothing to say about keeps the comment it
// arrived with.
func TestTheCommentIsWrittenOnlyWhenThereIsOneToWrite(t *testing.T) {
	path := fixture(t, "silence.flac")
	tags := proved()
	tags.Comment = "https://soundcloud.com/abo/illegal"
	if err := Write(path, tags); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := read(t, path)["COMMENT"]; !slices.Equal(got, []string{"https://soundcloud.com/abo/illegal"}) {
		t.Fatalf("COMMENT = %q", got)
	}

	if _, err := Revert(path); err != nil {
		t.Fatalf("Revert() error = %v", err)
	}
	if got := read(t, path)["COMMENT"]; !slices.Equal(got, []string{"https://soundcloud.com/abo/illegal"}) {
		t.Fatalf("COMMENT after a revert = %q, want the address still there", got)
	}

	bare := fixture(t, "silence.flac")
	if err := Write(bare, proved()); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := read(t, bare)["COMMENT"]; len(got) != 0 {
		t.Fatalf("COMMENT = %q, want the file's own comment left alone", got)
	}
}

// The picture goes into the file so that a copy of the file carries it. Nothing
// else about the file changes.
func TestArtworkIsWrittenIntoTheFile(t *testing.T) {
	path := fixture(t, "silence.flac")
	before := read(t, path)

	picture, err := os.ReadFile(filepath.Join("testdata", "silence.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteArtwork(path, picture); err != nil {
		t.Fatalf("WriteArtwork() error = %v", err)
	}
	written, err := taglib.ReadImage(path)
	if err != nil || !bytes.Equal(written, picture) {
		t.Fatalf("the picture read back is %d bytes, %v", len(written), err)
	}
	if got := read(t, path)["TITLE"]; !slices.Equal(got, before["TITLE"]) {
		t.Fatalf("TITLE = %q, want %q — a picture write changes no tag", got, before["TITLE"])
	}
	if err := WriteArtwork(path, nil); err == nil {
		t.Fatal("a write with no picture in it was accepted")
	}
}
