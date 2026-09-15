package library

import (
	"os"
	"path/filepath"
	"testing"
)

// Two music folders on one disc are one disc. Counting its free space once per
// folder would tell somebody the library had twice the room it has.
func TestTwoFoldersOnOneDiscAreMeasuredOnce(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "one")
	second := filepath.Join(root, "two")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	volumes := Volumes([]string{first, second})

	if len(volumes) != 1 {
		t.Fatalf("%d volumes, want one disc", len(volumes))
	}
	if len(volumes[0].Roots) != 2 {
		t.Fatalf("roots = %v, want both folders on the one disc", volumes[0].Roots)
	}
	if !volumes[0].Measured || volumes[0].TotalBytes <= 0 {
		t.Fatalf("volume = %#v, want a measurement", volumes[0])
	}
}

// A folder that cannot be read says so and carries no figures. A number nothing
// measured is a number somebody plans around.
func TestAFolderThatCannotBeReadSaysSoRatherThanGuessing(t *testing.T) {
	volumes := Volumes([]string{filepath.Join(t.TempDir(), "not there")})

	if len(volumes) != 1 {
		t.Fatalf("%d volumes, want the unreadable folder reported", len(volumes))
	}
	if volumes[0].Measured {
		t.Fatal("an unreadable folder was reported as measured")
	}
	if volumes[0].Error == "" {
		t.Fatal("an unreadable folder gave no reason")
	}
	if volumes[0].TotalBytes != 0 || volumes[0].FreeBytes != 0 {
		t.Fatalf("volume = %#v, want no figures at all", volumes[0])
	}
}
