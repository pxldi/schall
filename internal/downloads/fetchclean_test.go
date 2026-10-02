package downloads

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

type fakeFetchCleanupStore struct {
	copies []db.InboxCopyRow
}

func (store *fakeFetchCleanupStore) SourceFetchCopies(context.Context) ([]db.InboxCopyRow, error) {
	return store.copies, nil
}

// Nothing cleaned the fetch folder before: refused, unreadable and settled
// fetched copies stayed on disc for good (ADR 0040 §3). Each file here is one
// thing the rule says about a fetched file.
func TestTheFetchCleanerRemovesOnlyWhatNothingNeeds(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(t.TempDir(), "in the library.mp3")
	if err := os.WriteFile(library, []byte("mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)

	type fixture struct {
		name    string
		copied  *db.InboxCopyRow
		recent  bool
		folder  string
		removed bool
	}
	fixtures := []fixture{
		{name: "refused", copied: &db.InboxCopyRow{Verdict: "discarded_audio", WantStatus: "unresolved"}, removed: true},
		{name: "unreadable", copied: &db.InboxCopyRow{Verdict: "undelivered", WantStatus: "unresolved"}, removed: true},
		{name: "question", copied: &db.InboxCopyRow{Verdict: "held", WantStatus: "unresolved"}},
		{name: "held, want acquired", copied: &db.InboxCopyRow{Verdict: "held", WantStatus: "acquired"}, removed: true},
		{
			name:    "accepted, library holds it",
			copied:  &db.InboxCopyRow{Verdict: "accepted", WantStatus: "acquired", LibraryPath: library},
			removed: true,
		},
		{
			name:   "accepted, library copy missing",
			copied: &db.InboxCopyRow{Verdict: "accepted", WantStatus: "acquired", LibraryPath: "/gone.mp3"},
		},
		{name: "on its way", copied: &db.InboxCopyRow{Verdict: "fetching", WantStatus: "unresolved"}},
		{name: "refused but recent", copied: &db.InboxCopyRow{Verdict: "discarded_tags", WantStatus: "unresolved"}, recent: true},
		{name: "unrecorded", removed: true},
		{name: "unrecorded but recent", recent: true},
		{name: "not a want's folder", folder: "notes"},
	}

	store := &fakeFetchCleanupStore{}
	paths := map[string]string{}
	for _, fixture := range fixtures {
		folder := fixture.folder
		if folder == "" {
			folder = uuid.NewString()
		}
		name := fixture.name + ".mp3"
		path := filepath.Join(root, folder, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("mp3"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !fixture.recent {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
		if fixture.copied != nil {
			copied := *fixture.copied
			copied.SourceDirectory, copied.FileName = folder, name
			copied.RemotePath = folder + "/" + name
			store.copies = append(store.copies, copied)
		}
		paths[fixture.name] = path
	}
	// A fresh fetch into the folder an unreadable copy left names the same path.
	// The keep wins.
	refetched := filepath.Join(root, uuid.NewString(), "293.mp3")
	if err := os.MkdirAll(filepath.Dir(refetched), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refetched, []byte("mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(refetched, old, old); err != nil {
		t.Fatal(err)
	}
	for _, verdict := range []string{"undelivered", "held"} {
		store.copies = append(store.copies, db.InboxCopyRow{
			SourceDirectory: filepath.Base(filepath.Dir(refetched)), FileName: "293.mp3",
			Verdict: verdict, WantStatus: "unresolved",
		})
	}

	removed, err := NewFetchCleaner(store, root, zerolog.Nop()).Clean(context.Background())
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}

	want := 0
	for _, fixture := range fixtures {
		_, err := os.Stat(paths[fixture.name])
		gone := os.IsNotExist(err)
		if gone != fixture.removed {
			t.Errorf("%s: removed = %v, want %v", fixture.name, gone, fixture.removed)
		}
		if fixture.removed {
			want++
		}
	}
	if _, err := os.Stat(refetched); err != nil {
		t.Errorf("a fetch that is a question again was removed: %v", err)
	}
	if removed != want {
		t.Errorf("Clean() = %d, want %d", removed, want)
	}
	if _, err := os.Stat(filepath.Dir(paths["refused"])); !os.IsNotExist(err) {
		t.Errorf("the emptied want folder is still there: %v", err)
	}
	if _, err := os.Stat(library); err != nil {
		t.Errorf("the library copy was touched: %v", err)
	}
}
