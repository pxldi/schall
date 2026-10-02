package downloads

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// FetchCleanupStore reads the copies fetched from keyed wants' addresses.
type FetchCleanupStore interface {
	SourceFetchCopies(ctx context.Context) ([]db.InboxCopyRow, error)
}

// FetchCleaner deletes the files in the source fetch folder that nothing needs
// any more (ADR 0040 §3). It applies the inbox's rule (ADR 0036) to a folder
// only Schall writes: a refused or unreadable copy goes, a copy whose want is
// settled goes once the library holds its own copy, and a held copy stays
// while its want is a question.
//
// Every file can be fetched again from its address, so the pass runs on its
// own and logs each file it removes, where the inbox pass records each file in
// a table and waits for a person.
type FetchCleaner struct {
	store  FetchCleanupStore
	root   string
	logger zerolog.Logger
	now    func() time.Time
}

// NewFetchCleaner returns a cleaner for the fetch folder at root, which
// startup has already resolved and proven to be outside every music folder.
func NewFetchCleaner(store FetchCleanupStore, root string, logger zerolog.Logger) *FetchCleaner {
	return &FetchCleaner{store: store, root: filepath.Clean(root), logger: logger, now: time.Now}
}

// WithClock sets the clock the grace period is measured against.
func (cleaner *FetchCleaner) WithClock(now func() time.Time) *FetchCleaner {
	cleaner.now = now
	return cleaner
}

// fetchedFile is one file found under a want's folder in the fetch folder.
type fetchedFile struct {
	path     string
	folder   string
	modified time.Time
}

// Clean removes what the rule says is no longer needed, and reports how many
// files it removed. A file it cannot remove is logged and left; the next pass
// asks again.
func (cleaner *FetchCleaner) Clean(ctx context.Context) (int, error) {
	files, err := cleaner.walk()
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	// Read after the walk, so a copy recorded while the folder was being read is
	// seen and kept.
	copies, err := cleaner.store.SourceFetchCopies(ctx)
	if err != nil {
		return 0, err
	}
	classes := fetchClasses(cleaner.root, copies)

	removed := 0
	emptied := map[string]bool{}
	for _, file := range files {
		// The grace the inbox pass gives every class (ADR 0036): a file written
		// in the last day can be a fetch whose copy is not recorded yet.
		if cleaner.now().Sub(file.modified) < inboxGrace {
			continue
		}
		class, known := classes[file.path]
		if !known {
			class = fetchUnrecorded
		}
		if class == fetchKept {
			continue
		}
		if !stillTheFile(cleaner.root, file) {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			cleaner.logger.Warn().Err(err).Str("file", file.path).Str("class", class).
				Msg("could not remove a fetched file nothing needs; the next pass asks again")
			continue
		}
		removed++
		emptied[file.folder] = true
		cleaner.logger.Info().Str("file", file.path).Str("class", class).
			Msg("removed a fetched file nothing needs")
	}
	// Only os.Remove, which refuses a folder with anything left in it.
	for folder := range emptied {
		_ = os.Remove(folder)
	}
	return removed, nil
}

// The classes a fetched file can fall into. Every class but fetchKept is
// deleted.
const (
	fetchKept = "kept"
	// fetchRefused is a copy whose audio or tags refused it.
	fetchRefused = "refused"
	// fetchUnreadable is a copy whose bytes could not be read. The want fetches
	// again after 30 days into a fresh file (ADR 0040 §2).
	fetchUnreadable = "unreadable"
	// fetchSettled is a held or accepted copy whose want is acquired, not wanted
	// or superseded. An accepted copy goes only once its library copy is there.
	fetchSettled = "settled"
	// fetchUnrecorded is a file in a want's folder that no copy names: a fetch
	// that stopped before it was recorded, or yt-dlp's own leftovers.
	fetchUnrecorded = "unrecorded"
)

// fetchClasses says what each recorded copy's file is. A keep beats a delete
// when two copies name one path, which happens when a want fetches again into
// the folder an unreadable copy left.
func fetchClasses(root string, copies []db.InboxCopyRow) map[string]string {
	classes := map[string]string{}
	for _, copied := range copies {
		folder, name := copyFolder(copied), copyName(copied)
		if _, err := uuid.Parse(folder); err != nil || filepath.Base(name) != name || name == "" {
			continue
		}
		path := filepath.Join(root, folder, name)
		class := fetchKept
		switch {
		case copied.Verdict == "discarded_audio", copied.Verdict == "discarded_tags":
			class = fetchRefused
		case copied.Verdict == "undelivered":
			class = fetchUnreadable
		case settledWants[copied.WantStatus] && copied.Verdict == "held":
			class = fetchSettled
		case settledWants[copied.WantStatus] && copied.Verdict == "accepted" &&
			managedCopyPresent(copied.LibraryPath):
			class = fetchSettled
		}
		if previous, seen := classes[path]; seen && (previous == fetchKept || class == fetchKept) {
			class = fetchKept
		}
		classes[path] = class
	}
	return classes
}

// walk lists the files in the wants' folders. A fetch writes into a folder
// named by the want's id, so anything else in the fetch folder was not written
// by a fetch and is left alone.
func (cleaner *FetchCleaner) walk() ([]fetchedFile, error) {
	folders, err := os.ReadDir(cleaner.root)
	if err != nil {
		return nil, fmt.Errorf("read the fetch folder %s: %w", cleaner.root, err)
	}
	var files []fetchedFile
	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}
		if _, err := uuid.Parse(folder.Name()); err != nil {
			continue
		}
		path := filepath.Join(cleaner.root, folder.Name())
		entries, err := os.ReadDir(path)
		if err != nil {
			cleaner.logger.Warn().Err(err).Str("folder", path).Msg("could not read a want's fetch folder")
			continue
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			files = append(files, fetchedFile{
				path: filepath.Join(path, entry.Name()), folder: path, modified: info.ModTime(),
			})
		}
	}
	return files, nil
}

// stillTheFile asks again at the moment of removal: the folder is still a
// folder directly under the root, not a symlink swapped in since the walk, and
// the file is still the ordinary file the walk saw.
func stillTheFile(root string, file fetchedFile) bool {
	folder, err := os.Lstat(file.folder)
	if err != nil || !folder.IsDir() || filepath.Dir(file.folder) != root {
		return false
	}
	info, err := os.Lstat(file.path)
	return err == nil && info.Mode().IsRegular() && info.ModTime().Equal(file.modified)
}
