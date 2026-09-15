package downloads

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The pass reads the inbox as a folder of downloads: a file no download request
// and no copy resolves to is a file nothing needs. A library file resolves to
// nothing here, so an inbox that overlaps the library would classify the
// collection as unknown and delete it. Both directions are refused before
// anything is walked.

// haltedCleanups is enough store for a pass that never gets as far as reading
// anything, and it keeps what the pass recorded about itself.
type haltedCleanups struct {
	row     db.InboxCleanupRow
	failure string
	counts  map[string]db.InboxCleanupCount
	started bool
	read    bool
}

func (store *haltedCleanups) InboxCleanup(
	_ context.Context, _ uuid.UUID,
) (db.InboxCleanupRow, error) {
	return store.row, nil
}

func (store *haltedCleanups) StartInboxCleanup(_ context.Context, _ uuid.UUID) error {
	store.started = true
	return nil
}

func (store *haltedCleanups) FinishInboxCleanup(
	_ context.Context, _ uuid.UUID, counts map[string]db.InboxCleanupCount, failure string,
) error {
	store.counts, store.failure = counts, failure
	return nil
}

func (store *haltedCleanups) RecordInboxCleanupFiles(
	_ context.Context, _ uuid.UUID, _ []db.InboxCleanupFile,
) error {
	return nil
}

func (store *haltedCleanups) MarkInboxCleanupFilesDeleted(
	_ context.Context, _ uuid.UUID, _ []string, _ time.Time,
) error {
	return nil
}

func (store *haltedCleanups) ReclassifyInboxCleanupFiles(
	_ context.Context, _ uuid.UUID, _ []string, _ string,
) error {
	return nil
}

func (store *haltedCleanups) InboxCleanupCopies(_ context.Context) ([]db.InboxCopyRow, error) {
	store.read = true
	return nil, nil
}

func (store *haltedCleanups) InboxCleanupRequests(_ context.Context) ([]db.InboxRequestRow, error) {
	store.read = true
	return nil, nil
}

func TestAnInboxInsideTheLibraryIsNeverCleaned(t *testing.T) {
	library := t.TempDir()
	inbox := filepath.Join(library, "incoming")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	music := filepath.Join(library, "Anetha", "01.flac")
	if err := os.MkdirAll(filepath.Dir(music), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(music, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &haltedCleanups{row: db.InboxCleanupRow{ID: uuid.New()}}
	cleaner := NewInboxCleaner(store, inbox, []string{library}, zerolog.Nop())

	err := cleaner.Clean(context.Background(), store.row.ID)

	if err == nil {
		t.Fatal("a pass over an inbox inside the library was allowed to run")
	}
	if !strings.Contains(store.failure, "inside the music folder") {
		t.Errorf("the row says %q, want the reason nothing was deleted", store.failure)
	}
	if store.started || store.read {
		t.Error("the pass started work before refusing")
	}
	if _, err := os.Stat(music); err != nil {
		t.Errorf("the library file went: %v", err)
	}
}

func TestAnInboxHoldingTheLibraryIsNeverCleaned(t *testing.T) {
	inbox := t.TempDir()
	library := filepath.Join(inbox, "music")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	store := &haltedCleanups{row: db.InboxCleanupRow{ID: uuid.New()}}
	cleaner := NewInboxCleaner(store, inbox, []string{"", library}, zerolog.Nop())

	err := cleaner.Clean(context.Background(), store.row.ID)

	if err == nil {
		t.Fatal("a pass over an inbox holding the library was allowed to run")
	}
	if !strings.Contains(store.failure, "holds the music folder") {
		t.Errorf("the row says %q, want the reason nothing was deleted", store.failure)
	}
}

func TestAnInboxBesideTheLibraryIsCleaned(t *testing.T) {
	store := &haltedCleanups{row: db.InboxCleanupRow{ID: uuid.New(), DryRun: true}}
	cleaner := NewInboxCleaner(store, t.TempDir(), []string{t.TempDir()}, zerolog.Nop())

	if err := cleaner.Clean(context.Background(), store.row.ID); err != nil {
		t.Fatalf("a pass over an inbox outside the library was refused: %v", err)
	}
	if store.failure != "" {
		t.Errorf("the row says %q, want nothing", store.failure)
	}
}

// A folder replaced by a symlink between the walk and the unlink would take
// os.Remove out of the inbox entirely. Every path is asked about again at the
// moment it would go.
func TestAFileReachedThroughASymlinkIsNotDeleted(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	music := filepath.Join(outside, "01.flac")
	if err := os.WriteFile(music, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	// What the walk recorded is a path under the inbox; what is there now
	// reaches somebody else's folder.
	if err := os.Symlink(outside, filepath.Join(root, "Album")); err != nil {
		t.Fatal(err)
	}
	cleaner := NewInboxCleaner(&haltedCleanups{}, root, nil, zerolog.Nop())
	file := db.InboxCleanupFile{
		Path: filepath.Join(root, "Album", "01.flac"), Class: db.InboxUnknown,
	}

	class, deletable := cleaner.stillDeletable(root, file, emptyRule())

	if deletable {
		t.Fatal("a file reached through a symlink out of the inbox would have been deleted")
	}
	if class != db.InboxKeptUnsafePath {
		t.Errorf("class = %q, want %q", class, db.InboxKeptUnsafePath)
	}
}

func TestASymlinkInTheInboxIsNotDeleted(t *testing.T) {
	root := t.TempDir()
	music := filepath.Join(t.TempDir(), "01.flac")
	if err := os.WriteFile(music, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "01.flac")
	if err := os.Symlink(music, link); err != nil {
		t.Fatal(err)
	}
	cleaner := NewInboxCleaner(&haltedCleanups{}, root, nil, zerolog.Nop())

	class, deletable := cleaner.stillDeletable(
		root, db.InboxCleanupFile{Path: link, Class: db.InboxUnknown}, emptyRule())

	if deletable || class != db.InboxKeptUnsafePath {
		t.Errorf("a symlink was %q and deletable = %v", class, deletable)
	}
}

// A file written to while the pass was walking is inside the grace period now,
// whatever it was when the walk read it.
func TestAFileWrittenToWhileThePassRunsIsNotDeleted(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "01.flac")
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleaner := NewInboxCleaner(&haltedCleanups{}, root, nil, zerolog.Nop())

	class, deletable := cleaner.stillDeletable(
		root, db.InboxCleanupFile{Path: path, Class: db.InboxUnknown}, emptyRule())

	if deletable || class != db.InboxKeptRecent {
		t.Errorf("a file just written was %q and deletable = %v", class, deletable)
	}
}

func emptyRule() inboxRule {
	return inboxRule{
		question:    map[string]bool{},
		open:        map[string]bool{},
		imported:    map[string]bool{},
		unconfirmed: map[string]bool{},
		refused:     map[string]bool{},
		settled:     map[string]bool{},
		known:       map[string]bool{},
	}
}
