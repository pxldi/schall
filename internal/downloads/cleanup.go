package downloads

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// inboxGrace is how long a file is left alone after it was last written.
//
// slskd reports a transfer some time after the bytes land, and a file nothing
// has recorded yet is indistinguishable from a file nothing needs. Twenty-four
// hours is longer than any transfer this installation has taken and longer than
// the gap between a transfer finishing and the poller reading it.
const inboxGrace = 24 * time.Hour

// incompleteFolder is skipped wherever it sits directly under the inbox. No
// slskd configuration in this repository puts partial transfers there, and the
// cost of skipping a folder of that name is a folder nobody cleans.
const incompleteFolder = "incomplete"

// inboxRecordBatch is how many file rows are written at once. The live inbox
// holds 36,082 files, and one statement carrying all of them is a statement
// nothing can read in a log.
const inboxRecordBatch = 500

// The want states rule 1 keeps a held copy for, and the ones that settle it.
// Between them they are every state a want can be in.
var (
	liveWants    = map[string]bool{"pending": true, "awaiting_review": true, "searching": true, "unresolved": true}
	settledWants = map[string]bool{"acquired": true, "not_wanted": true, "superseded": true}
)

// InboxCleanupStore is what a pass over the download inbox reads and writes.
type InboxCleanupStore interface {
	InboxCleanup(context.Context, uuid.UUID) (db.InboxCleanupRow, error)
	StartInboxCleanup(context.Context, uuid.UUID) error
	FinishInboxCleanup(context.Context, uuid.UUID, map[string]db.InboxCleanupCount, string) error
	RecordInboxCleanupFiles(context.Context, uuid.UUID, []db.InboxCleanupFile) error
	MarkInboxCleanupFilesDeleted(context.Context, uuid.UUID, []string, time.Time) error
	ReclassifyInboxCleanupFiles(context.Context, uuid.UUID, []string, string) error
	InboxCleanupCopies(context.Context) ([]db.InboxCopyRow, error)
	InboxCleanupRequests(context.Context) ([]db.InboxRequestRow, error)
}

// InboxCleaner deletes the files in the download inbox that nothing needs any
// more, by the rule in docs/decisions/0036.
//
// Nothing here is decided by similarity, by a name or by a score. A file is kept
// when a record says something still needs it, deleted when a record says
// nothing does, and every file it looked at is written down with the class it
// was put in before anything is unlinked.
type InboxCleaner struct {
	store     InboxCleanupStore
	inboxPath string
	// libraryPaths are the managed library and every root a music folder may be
	// added under. An inbox that sits inside one of them, or holds one, would
	// have the library's own files read as files nothing recorded, so the pass
	// refuses to run at all rather than delete them.
	libraryPaths []string
	logger       zerolog.Logger
	now          func() time.Time
}

func NewInboxCleaner(
	store InboxCleanupStore, inboxPath string, libraryPaths []string, logger zerolog.Logger,
) *InboxCleaner {
	return &InboxCleaner{
		store:        store,
		inboxPath:    filepath.Clean(inboxPath),
		libraryPaths: libraryPaths,
		logger:       logger,
		now:          time.Now,
	}
}

// WithClock sets the clock the grace period is measured against.
func (cleaner *InboxCleaner) WithClock(now func() time.Time) *InboxCleaner {
	cleaner.now = now
	return cleaner
}

// Clean carries out one pass and closes its row, whether it finished or failed.
// The row is the only thing holding the next pass back, so a failure is recorded
// on it rather than left open.
func (cleaner *InboxCleaner) Clean(ctx context.Context, cleanupID uuid.UUID) error {
	counts, failure, err := cleaner.clean(ctx, cleanupID)
	if err != nil {
		failure = err.Error()
	}
	if closing := cleaner.store.FinishInboxCleanup(ctx, cleanupID, counts, failure); closing != nil {
		if err == nil {
			return closing
		}
		cleaner.logger.Error().Err(closing).Str("cleanup_id", cleanupID.String()).
			Msg("could not close a pass over the download inbox")
	}
	return err
}

func (cleaner *InboxCleaner) clean(
	ctx context.Context, cleanupID uuid.UUID,
) (map[string]db.InboxCleanupCount, string, error) {
	cleanup, err := cleaner.store.InboxCleanup(ctx, cleanupID)
	if err != nil {
		return nil, "", err
	}
	if cleanup.FinishedAt != nil {
		// Already carried out. Running the pass again would be safe; writing a
		// second account of it over the first would not.
		return cleanup.Counts, cleanup.Error, nil
	}
	if err := cleaner.outsideTheLibrary(); err != nil {
		return nil, "", err
	}
	if err := cleaner.store.StartInboxCleanup(ctx, cleanupID); err != nil {
		return nil, "", err
	}

	// Resolved once, because inboxFolder resolves every candidate it is given
	// and the walk has to produce the same paths those come back as.
	root, err := filepath.EvalSymlinks(cleaner.inboxPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve the download inbox %s: %w", cleaner.inboxPath, err)
	}

	rule, err := cleaner.read(ctx, root)
	if err != nil {
		return nil, "", err
	}
	files, folders, err := cleaner.walk(root)
	if err != nil {
		return nil, "", err
	}

	now := cleaner.now()
	counts := map[string]db.InboxCleanupCount{}
	classified := map[string][]db.InboxCleanupFile{}
	for _, file := range files {
		class := rule.classify(file, now)
		row := db.InboxCleanupFile{Path: file.path, Class: class, SizeBytes: file.size}
		classified[class] = append(classified[class], row)
		tally := counts[class]
		tally.Files++
		tally.Bytes += file.size
		counts[class] = tally
	}

	// Every file is on record before any of them is unlinked. A crash between
	// the two leaves rows saying a file was doomed and is still there, which is
	// readable; the other order would delete music the record cannot name.
	for _, class := range inboxClasses() {
		if err := cleaner.record(ctx, cleanupID, classified[class]); err != nil {
			return counts, "", err
		}
	}
	if cleanup.DryRun {
		cleaner.report(cleanup, counts, 0)
		return counts, "", nil
	}

	// Read again, right before anything is unlinked. The walk over 36,082 files
	// takes minutes, and in those minutes a want can be pursued again and a file
	// can be written to. A pass that deleted from the picture it took at the
	// start would delete a copy that is a question again.
	fresh, err := cleaner.read(ctx, root)
	if err != nil {
		return counts, "", err
	}
	failures := 0
	for _, class := range db.InboxDeleteClasses {
		removed, failed, moved, err := cleaner.delete(ctx, cleanupID, root, classified[class], fresh)
		if err != nil {
			return counts, "", err
		}
		failures += failed
		tally := counts[class]
		tally.Failed = failed
		counts[class] = tally
		for keptClass, files := range moved {
			if err := cleaner.spare(ctx, cleanupID, keptClass, files); err != nil {
				return counts, "", err
			}
			move(counts, class, keptClass, files)
		}
		_ = removed
	}
	cleaner.report(cleanup, counts, cleaner.removeEmptyFolders(folders))
	if failures > 0 {
		// Recorded on the row and never swallowed: a pass that says it deleted
		// what it could not is the one account nobody can act on. The rows of
		// the files that would not go keep their class and no deleted_at.
		return counts, fmt.Sprintf(
			"%d file(s) could not be removed from the download inbox; they are still there",
			failures), nil
	}
	return counts, "", nil
}

// spare takes files out of a delete class and records the class the pass ended
// up putting them in.
func (cleaner *InboxCleaner) spare(
	ctx context.Context, cleanupID uuid.UUID, class string, files []db.InboxCleanupFile,
) error {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	for start := 0; start < len(paths); start += inboxRecordBatch {
		end := min(start+inboxRecordBatch, len(paths))
		if err := cleaner.store.ReclassifyInboxCleanupFiles(
			ctx, cleanupID, paths[start:end], class,
		); err != nil {
			return err
		}
	}
	return nil
}

// move carries a set of files from one class's tally to another's.
func move(counts map[string]db.InboxCleanupCount, from, to string, files []db.InboxCleanupFile) {
	out, in := counts[from], counts[to]
	for _, file := range files {
		out.Files--
		out.Bytes -= file.SizeBytes
		in.Files++
		in.Bytes += file.SizeBytes
	}
	counts[from], counts[to] = out, in
}

// outsideTheLibrary refuses a pass over an inbox that overlaps the managed
// library or any root a music folder may be added under.
//
// The rule reads the inbox as a folder of downloads: a file no download request
// and no copy resolves to is a file nothing needs. Library files resolve to
// nothing here, so an inbox inside a music folder would classify the collection
// itself as unknown and delete it. Both directions are refused, because a music
// folder added inside the inbox later is the same accident arriving the other
// way round.
func (cleaner *InboxCleaner) outsideTheLibrary() error {
	inbox := cleaner.inboxPath
	for _, configured := range cleaner.libraryPaths {
		configured = strings.TrimSpace(configured)
		if configured == "" {
			continue
		}
		for _, candidate := range bothForms(configured) {
			for _, mine := range bothForms(inbox) {
				if within(candidate, mine) {
					return fmt.Errorf(
						"the download inbox %s is inside the music folder %s, so nothing was deleted",
						inbox, configured)
				}
				if within(mine, candidate) {
					return fmt.Errorf(
						"the download inbox %s holds the music folder %s, so nothing was deleted",
						inbox, configured)
				}
			}
		}
	}
	return nil
}

// bothForms is a path as it was written and as the filesystem resolves it. A
// containment check that read only one of the two would miss an inbox that
// reaches a music folder through a link.
func bothForms(path string) []string {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil || resolved == cleaned {
		return []string{cleaned}
	}
	return []string{cleaned, resolved}
}

// within reports a candidate at or under parent.
func within(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// inboxClasses is every class, the four that delete first.
func inboxClasses() []string {
	return append(append([]string{}, db.InboxDeleteClasses...), db.InboxKeepClasses...)
}

// record writes down what the pass decided about a batch of files.
func (cleaner *InboxCleaner) record(
	ctx context.Context, cleanupID uuid.UUID, files []db.InboxCleanupFile,
) error {
	for start := 0; start < len(files); start += inboxRecordBatch {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+inboxRecordBatch, len(files))
		if err := cleaner.store.RecordInboxCleanupFiles(ctx, cleanupID, files[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// delete unlinks one class and stamps what it removed. It reports what it could
// not remove, and the files it took out of the class instead.
//
// A file that will not go is left where it is with its row unstamped and its
// class unchanged: the pass is an account of what happened, and one refused
// unlink is not a reason to stop deleting the rest.
func (cleaner *InboxCleaner) delete(
	ctx context.Context, cleanupID uuid.UUID, root string,
	files []db.InboxCleanupFile, fresh inboxRule,
) (int, int, map[string][]db.InboxCleanupFile, error) {
	deleted := make([]string, 0, len(files))
	moved := map[string][]db.InboxCleanupFile{}
	failed := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return 0, 0, nil, err
		}
		if class, deletable := cleaner.stillDeletable(root, file, fresh); !deletable {
			moved[class] = append(moved[class], file)
			continue
		}
		if err := os.Remove(file.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleaner.logger.Warn().Err(err).
				Str("path", file.Path).Str("class", file.Class).
				Msg("an inbox file could not be removed")
			failed++
			continue
		}
		deleted = append(deleted, file.Path)
	}
	for start := 0; start < len(deleted); start += inboxRecordBatch {
		end := min(start+inboxRecordBatch, len(deleted))
		if err := cleaner.store.MarkInboxCleanupFilesDeleted(
			ctx, cleanupID, deleted[start:end], cleaner.now(),
		); err != nil {
			return 0, 0, nil, err
		}
	}
	return len(deleted), failed, moved, nil
}

// stillDeletable asks the rule about one file again, at the moment it would be
// unlinked. It reports the class the file has moved to, or that it may go.
//
// Three things can have changed since the walk classified it, and each of them
// is a file that must not be deleted:
//
//   - the records moved. A want pursued again, an import undone, a request
//     retried: the reloaded rule answers for all of them.
//   - the file was written to. It is inside the grace period now, whatever it
//     was when the walk read it.
//   - the path stopped being an ordinary file inside the inbox. A folder
//     replaced by a symlink between the walk and the unlink would take
//     os.Remove somewhere else entirely, so the file and the folder holding it
//     are both checked again here.
func (cleaner *InboxCleaner) stillDeletable(
	root string, file db.InboxCleanupFile, fresh inboxRule,
) (string, bool) {
	info, err := os.Lstat(file.Path)
	if errors.Is(err, os.ErrNotExist) {
		// Gone since the walk. There is nothing to refuse and nothing to
		// remove, and os.Remove says so a moment later.
		return "", true
	}
	if err != nil || !info.Mode().IsRegular() {
		return db.InboxKeptUnsafePath, false
	}
	if !within(root, resolvedFolder(file.Path)) {
		return db.InboxKeptUnsafePath, false
	}
	class := fresh.classify(
		inboxEntry{path: file.Path, size: info.Size(), modifiedAt: info.ModTime()},
		cleaner.now(),
	)
	if inboxKeepClass(class) {
		return class, false
	}
	return "", true
}

// resolvedFolder is the folder holding a file with its symlinks followed, or an
// empty string when it cannot be resolved. An empty string is inside nothing.
func resolvedFolder(path string) string {
	resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return ""
	}
	return resolved
}

// inboxKeepClass reports one of the classes the pass leaves alone.
func inboxKeepClass(class string) bool {
	return slices.Contains(db.InboxKeepClasses, class)
}

// removeEmptyFolders takes away the folders the deletions emptied, deepest
// first, and reports how many went.
//
// os.Remove is the whole guard: it refuses a folder with anything at all in it,
// so a folder holding artwork, a log, or a file that would not go is left where
// it is. Deepest first is what lets a folder of empty folders go in one pass.
func (cleaner *InboxCleaner) removeEmptyFolders(folders []string) int {
	sort.Slice(folders, func(one, other int) bool {
		return strings.Count(folders[one], string(filepath.Separator)) >
			strings.Count(folders[other], string(filepath.Separator))
	})
	removed := 0
	for _, folder := range folders {
		if err := os.Remove(folder); err == nil {
			removed++
		}
	}
	return removed
}

func (cleaner *InboxCleaner) report(
	cleanup db.InboxCleanupRow, counts map[string]db.InboxCleanupCount, folders int,
) {
	for _, class := range inboxClasses() {
		tally := counts[class]
		cleaner.logger.Info().
			Str("cleanup_id", cleanup.ID.String()).
			Bool("dry_run", cleanup.DryRun).
			Str("class", class).
			Int("files", tally.Files).
			Int64("bytes", tally.Bytes).
			Int("failed", tally.Failed).
			Int("empty_folders_removed", folders).
			Msg("inbox cleanup class")
	}
}

// inboxEntry is one file in the inbox as the walk found it.
type inboxEntry struct {
	path       string
	size       int64
	modifiedAt time.Time
}

// walk reads the inbox once and returns its ordinary files and its folders.
//
// A symlink is neither: it is not a regular file, so it is never classified and
// never unlinked. A folder that cannot be read is skipped with its contents,
// which means nothing under it is deleted and the folder itself will not go.
func (cleaner *InboxCleaner) walk(root string) ([]inboxEntry, []string, error) {
	files := make([]inboxEntry, 0, 1024)
	folders := make([]string, 0, 512)
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			cleaner.logger.Warn().Err(err).Str("path", name).
				Msg("a folder in the download inbox could not be read")
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if name == root {
				return nil
			}
			if entry.Name() == incompleteFolder && filepath.Dir(name) == root {
				return fs.SkipDir
			}
			folders = append(folders, name)
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, inboxEntry{path: name, size: info.Size(), modifiedAt: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk the download inbox %s: %w", root, err)
	}
	return files, folders, nil
}

// inboxRule is what the records say about the files in the inbox: the paths
// something still needs, and the paths nothing does.
type inboxRule struct {
	question    map[string]bool
	open        map[string]bool
	imported    map[string]bool
	unconfirmed map[string]bool
	refused     map[string]bool
	settled     map[string]bool
	// known is every path some record resolved to at all. A file in none of the
	// sets above and not in this one is a file nothing recorded.
	known map[string]bool
}

// classify says what becomes of one file. The order is the rule: the grace
// period first, then the two keeps, then the four delete classes in the order a
// run works through them, and a file no record mentions last.
func (rule inboxRule) classify(file inboxEntry, now time.Time) string {
	switch {
	case now.Sub(file.modifiedAt) < inboxGrace:
		return db.InboxKeptRecent
	case rule.question[file.path]:
		return db.InboxKeptQuestion
	case rule.open[file.path]:
		return db.InboxKeptOpen
	case rule.unconfirmed[file.path]:
		return db.InboxKeptUnconfirmed
	case rule.imported[file.path]:
		return db.InboxImported
	case rule.refused[file.path]:
		return db.InboxRefused
	case rule.settled[file.path]:
		return db.InboxSettled
	case !rule.known[file.path]:
		return db.InboxUnknown
	default:
		// A record resolves to it and no rule covers it: a cancelled request, or
		// a copy still being fetched. The rule says nothing about these, so the
		// file stays and the count says how many there were.
		return db.InboxKeptUndecided
	}
}

// read turns every copy and every download request into the paths they resolve
// to in the inbox. A record that resolves to no file on disc is skipped.
func (cleaner *InboxCleaner) read(ctx context.Context, root string) (inboxRule, error) {
	rule := inboxRule{
		question:    map[string]bool{},
		open:        map[string]bool{},
		imported:    map[string]bool{},
		unconfirmed: map[string]bool{},
		refused:     map[string]bool{},
		settled:     map[string]bool{},
		known:       map[string]bool{},
	}
	paths := &inboxPaths{root: root, folders: map[string]string{}}

	copies, err := cleaner.store.InboxCleanupCopies(ctx)
	if err != nil {
		return inboxRule{}, err
	}
	for _, copied := range copies {
		source, present := paths.file(copyFolder(copied), copyName(copied))
		if !present {
			continue
		}
		rule.known[source] = true
		switch {
		case copied.Verdict == "held" && liveWants[copied.WantStatus]:
			rule.question[source] = true
		case copied.Verdict == "discarded_audio", copied.Verdict == "discarded_tags":
			rule.refused[source] = true
		case settledWants[copied.WantStatus] &&
			(copied.Verdict == "held" || copied.Verdict == "accepted"):
			// An accepted copy is the library's now, so the library has to be
			// able to answer for it before the peer's copy goes.
			if copied.Verdict == "accepted" && !managedCopyPresent(copied.LibraryPath) {
				rule.unconfirmed[source] = true
				continue
			}
			rule.settled[source] = true
		}
	}

	requests, err := cleaner.store.InboxCleanupRequests(ctx)
	if err != nil {
		return inboxRule{}, err
	}
	for _, request := range requests {
		for _, file := range request.Files {
			source, present := paths.file(request.SourceDirectory, file.Name)
			if !present {
				continue
			}
			rule.known[source] = true
			switch {
			case openRequest(request):
				rule.open[source] = true
			case request.Status == "completed" && request.ImportStatus == "imported":
				if !managedCopyPresent(request.Imported[file.Path]) {
					rule.unconfirmed[source] = true
					continue
				}
				rule.imported[source] = true
			}
		}
	}
	return rule, nil
}

// openRequest reports a download request something is still doing, or that a
// person can still act on.
//
// 'requested' and 'started' are transfers in flight. 'failed' carries the Retry
// the downloads view offers on the files that never arrived, and retrying asks
// the peer for exactly the folder these files are in. A completed request is
// open only while its import has not settled: pending and validating are the
// import itself, needs_review is a person's question. Cancelled requests, and
// completed ones whose import finished or was discarded, are not open.
func openRequest(request db.InboxRequestRow) bool {
	switch request.Status {
	case "requested", "started", "failed":
		return true
	case "completed":
		return request.ImportStatus == "pending" ||
			request.ImportStatus == "validating" ||
			request.ImportStatus == "needs_review"
	}
	return false
}

// managedCopyPresent asks what removeSource asks before it deletes an import's
// source: the managed copy this record names is on disc and holds ordinary
// bytes. Its size is not compared, because nothing here knows what was
// validated and a re-encoded copy is deliberately not the size of its source.
func managedCopyPresent(localPath string) bool {
	if strings.TrimSpace(localPath) == "" {
		return false
	}
	info, err := os.Lstat(localPath)
	return err == nil && info.Mode().IsRegular()
}

// copyFolder is the provider folder one copy arrived in: the request's source
// directory, or the copy's own remote path where the request row has gone. Both
// are the peer's own path in the peer's own separators, which is what
// inboxFolder takes.
func copyFolder(copied db.InboxCopyRow) string {
	if strings.TrimSpace(copied.SourceDirectory) != "" {
		return copied.SourceDirectory
	}
	return path.Dir(strings.ReplaceAll(copied.RemotePath, `\`, "/"))
}

func copyName(copied db.InboxCopyRow) string {
	if copied.FileName != "" {
		return copied.FileName
	}
	return path.Base(strings.ReplaceAll(copied.RemotePath, `\`, "/"))
}

// inboxPaths resolves records to files through the one derivation in inbox.go,
// remembering each provider folder it has already looked for. The live inbox
// holds 12,650 of them and tens of thousands of records naming them.
type inboxPaths struct {
	root    string
	folders map[string]string
}

func (paths *inboxPaths) file(sourceDirectory, fileName string) (string, bool) {
	folder, asked := paths.folders[sourceDirectory]
	if !asked {
		resolved, err := inboxFolder(paths.root, sourceDirectory)
		if err != nil {
			resolved = ""
		}
		paths.folders[sourceDirectory] = resolved
		folder = resolved
	}
	if folder == "" {
		return "", false
	}
	return regularFile(folder, fileName)
}
