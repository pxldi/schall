package downloads

import (
	"os"
	"path/filepath"
	"testing"
)

// An inbox configured through a symlink, as a mounted volume often is. Before,
// the folder inside it resolved to the real path while the inbox did not, so
// every folder read as escaping the inbox and no import could run.
func TestAFolderInASymlinkedInboxIsInsideIt(t *testing.T) {
	real := t.TempDir()
	if err := os.Mkdir(filepath.Join(real, "Album"), 0o755); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(t.TempDir(), "inbox")
	if err := os.Symlink(real, inbox); err != nil {
		t.Fatal(err)
	}

	folder, err := inboxFolder(inbox, `Music\Album`)
	if err != nil {
		t.Fatalf("err = %v, want the folder found inside the inbox", err)
	}
	if filepath.Base(folder) != "Album" {
		t.Fatalf("folder = %q", folder)
	}
}

// A symlink inside the inbox that points out of it still escapes.
func TestAFolderLinkedFromOutsideTheInboxIsRefused(t *testing.T) {
	inbox, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(inbox, "Album")); err != nil {
		t.Fatal(err)
	}

	if _, err := inboxFolder(inbox, `Music\Album`); err == nil {
		t.Fatal("err = nil, want a folder outside the inbox refused")
	}
}
