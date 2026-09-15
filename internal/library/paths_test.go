package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathValidatorAcceptsNestedExistingDirectory(t *testing.T) {
	allowed := t.TempDir()
	nested := filepath.Join(allowed, "Artist")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	validator, err := NewPathValidator([]string{allowed})
	if err != nil {
		t.Fatal(err)
	}
	got, err := validator.Validate(nested)
	if err != nil {
		t.Fatal(err)
	}
	if got != nested {
		t.Fatalf("path = %q, want %q", got, nested)
	}
}

func TestPathValidatorRejectsTraversalOutsideAllowedRoot(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "music")
	outside := filepath.Join(base, "private")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	validator, err := NewPathValidator([]string{allowed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Validate(outside); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("error = %v, want outside-root error", err)
	}
}

func TestPathValidatorRejectsRelativeAndMissingPaths(t *testing.T) {
	validator, err := NewPathValidator([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"music", "/definitely/not/a/schall/folder"} {
		if _, err := validator.Validate(path); err == nil {
			t.Errorf("Validate(%q) succeeded", path)
		}
	}
}

func TestPathValidatorRejectsAnEmptyPath(t *testing.T) {
	validator, err := NewPathValidator([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Validate("   "); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("error = %v, want a path to be required", err)
	}
}

// A music folder is a folder. Pointing one at a file would have every scan walk
// nothing and report an empty library.
func TestPathValidatorRejectsAFileAsAMusicFolder(t *testing.T) {
	allowed := t.TempDir()
	file := filepath.Join(allowed, "Roads.mp3")
	if err := os.WriteFile(file, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	validator, err := NewPathValidator([]string{allowed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Validate(file); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("error = %v, want a file refused as a music folder", err)
	}
}

// Everything is inside the filesystem root, so allowing it would allow
// everything — including the paths the allow list exists to keep out.
func TestPathValidatorRejectsTheFilesystemRootAsAMusicFolder(t *testing.T) {
	validator, err := NewPathValidator([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Validate(string(filepath.Separator)); err == nil ||
		!strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("error = %v, want the filesystem root refused", err)
	}
}

func TestAllowedRootsRejectTheFilesystemRoot(t *testing.T) {
	if _, err := NewPathValidator([]string{string(filepath.Separator)}); err == nil ||
		!strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("error = %v, want the filesystem root refused as an allowed root", err)
	}
}

// An allowed root that is not absolute is a path that means something different
// depending on where the process was started, which is never what was intended.
func TestAllowedRootsRejectARelativePath(t *testing.T) {
	if _, err := NewPathValidator([]string{"music"}); err == nil ||
		!strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v, want a relative allowed root refused", err)
	}
}

// The setting is one string split on commas, so a trailing separator or a
// stray space is ordinary rather than a mistake worth refusing.
func TestAllowedRootsIgnoreBlankEntries(t *testing.T) {
	allowed := t.TempDir()
	validator, err := NewPathValidator([]string{"", "   ", allowed})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := validator.Validate(allowed); err != nil || got != allowed {
		t.Fatalf("Validate() = %q, %v; want the one real root honoured", got, err)
	}
}

// Nothing allowed means nothing may be added, and saying so at startup is far
// better than every music folder being refused with no reason anyone can see.
func TestAllowedRootsRefuseAListThatNamesNothing(t *testing.T) {
	if _, err := NewPathValidator([]string{"", "  "}); err == nil ||
		!strings.Contains(err.Error(), "at least one") {
		t.Fatalf("error = %v, want an empty allow list refused", err)
	}
}
