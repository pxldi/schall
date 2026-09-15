package downloads

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
)

// The inbox was mounted read-only until 2026-09-10, so it holds a copy of
// everything ever imported and of everything ever refused. This is the pass that
// empties it, and the whole of it is one question: does the rule keep the files
// something still needs and delete the rest.
//
// The fixture is one folder per class, so the two runs can be read as a table.
// A dry run must delete nothing at all; a real one must delete exactly the four
// delete classes and leave the five keeps where they are.

// inboxFixture is one folder of one file in the inbox, and what the pass should
// make of it.
type inboxFixture struct {
	folder string
	name   string
	class  string
	fresh  bool
}

var inboxFixtures = []inboxFixture{
	{folder: "Imported", name: "01.flac", class: db.InboxImported},
	{folder: "Refused", name: "02.flac", class: db.InboxRefused},
	{folder: "Settled", name: "03.flac", class: db.InboxSettled},
	{folder: "!Soulseek", name: "04.mp3", class: db.InboxUnknown},
	{folder: "Question", name: "05.flac", class: db.InboxKeptQuestion},
	{folder: "Open", name: "06.flac", class: db.InboxKeptOpen},
	{folder: "Fresh", name: "07.flac", class: db.InboxKeptRecent, fresh: true},
	{folder: "Unconfirmed", name: "08.flac", class: db.InboxKeptUnconfirmed},
}

func TestCleaningTheInboxCountsEverythingAndDeletesTheFourClasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	inbox := inboxOnDisc(t)
	libraryPath, _ := recordedInbox(ctx, t, pool, inbox)
	cleaner := NewInboxCleaner(queries, inbox, []string{t.TempDir()}, zerolog.Nop())

	dry, err := queries.QueueInboxCleanup(ctx, "leo", true, time.Now())
	if err != nil {
		t.Fatalf("ask for a dry run: %v", err)
	}
	if err := cleaner.Clean(ctx, dry.ID); err != nil {
		t.Fatalf("count what can go: %v", err)
	}

	counted, err := queries.InboxCleanup(ctx, dry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if counted.FinishedAt == nil || counted.Error != "" {
		t.Fatalf("the dry run ended as %#v", counted)
	}
	for _, fixture := range inboxFixtures {
		if count := counted.Counts[fixture.class]; count.Files != 1 {
			t.Errorf("the dry run counted %d files as %s, want 1", count.Files, fixture.class)
		}
		if _, err := os.Stat(filepath.Join(inbox, fixture.folder, fixture.name)); err != nil {
			t.Errorf("the dry run removed %s/%s: %v", fixture.folder, fixture.name, err)
		}
	}
	for _, fixture := range inboxFixtures {
		class, deletedAt := recordedFile(ctx, t, pool, dry.ID,
			filepath.Join(inbox, fixture.folder, fixture.name))
		if class != fixture.class {
			t.Errorf("the dry run recorded %s/%s as %q, want %q",
				fixture.folder, fixture.name, class, fixture.class)
		}
		if deletedAt != nil {
			t.Errorf("the dry run stamped %s/%s as deleted", fixture.folder, fixture.name)
		}
	}

	real, err := queries.QueueInboxCleanup(ctx, "leo", false, time.Now())
	if err != nil {
		t.Fatalf("ask for the deletion: %v", err)
	}
	if err := cleaner.Clean(ctx, real.ID); err != nil {
		t.Fatalf("clean the inbox: %v", err)
	}

	for _, fixture := range inboxFixtures {
		path := filepath.Join(inbox, fixture.folder, fixture.name)
		_, err := os.Stat(path)
		deletes := fixture.class == db.InboxImported || fixture.class == db.InboxRefused ||
			fixture.class == db.InboxSettled || fixture.class == db.InboxUnknown
		if deletes && err == nil {
			t.Errorf("%s/%s was classified %s and is still there", fixture.folder, fixture.name, fixture.class)
		}
		if !deletes && err != nil {
			t.Errorf("%s/%s was classified %s and was deleted anyway: %v",
				fixture.folder, fixture.name, fixture.class, err)
		}
		class, deletedAt := recordedFile(ctx, t, pool, real.ID, path)
		if class != fixture.class {
			t.Errorf("the run recorded %s as %q, want %q", path, class, fixture.class)
		}
		if deletes != (deletedAt != nil) {
			t.Errorf("%s is class %s and its row says deleted_at %v", path, class, deletedAt)
		}
	}

	// The folders the deletions emptied go with them, and the ones still holding
	// a file stay. os.Remove is the only thing that takes a folder away, so a
	// folder with anything at all in it is refused.
	for _, fixture := range inboxFixtures {
		folder := filepath.Join(inbox, fixture.folder)
		_, err := os.Stat(folder)
		emptied := fixture.class == db.InboxImported || fixture.class == db.InboxRefused ||
			fixture.class == db.InboxSettled || fixture.class == db.InboxUnknown
		if emptied && err == nil {
			t.Errorf("%s was emptied and is still there", fixture.folder)
		}
		if !emptied && err != nil {
			t.Errorf("%s still holds a file and was removed: %v", fixture.folder, err)
		}
	}

	// The library copy of the imported file is not the pass's to touch.
	if _, err := os.Stat(libraryPath); err != nil {
		t.Errorf("the managed copy went with its source: %v", err)
	}
}

// The walk over 36,082 files takes minutes, and the records can move inside
// them. A want pursued again after its copy was classified for deletion is a
// question again, and the copy is the file that answers it.
func TestAWantPursuedAgainWhileThePassRunsKeepsItsCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	inbox := inboxOnDisc(t)
	_, settled := recordedInbox(ctx, t, pool, inbox)
	settledFile := filepath.Join(inbox, "Settled", "03.flac")

	// The want is pursued again between the two reads of the rule, which is the
	// moment the pass has to notice.
	store := &wantPursuedAgain{Queries: queries, pool: pool, targetID: settled}
	cleaner := NewInboxCleaner(store, inbox, []string{t.TempDir()}, zerolog.Nop())

	run, err := queries.QueueInboxCleanup(ctx, "leo", false, time.Now())
	if err != nil {
		t.Fatalf("ask for the deletion: %v", err)
	}
	if err := cleaner.Clean(ctx, run.ID); err != nil {
		t.Fatalf("clean the inbox: %v", err)
	}
	if store.reads < 2 {
		t.Fatalf("the pass read the rule %d times, want it read again before deleting", store.reads)
	}

	if _, err := os.Stat(settledFile); err != nil {
		t.Errorf("the copy of a want being looked for again was deleted: %v", err)
	}
	class, deletedAt := recordedFile(ctx, t, pool, run.ID, settledFile)
	if class != db.InboxKeptQuestion || deletedAt != nil {
		t.Errorf("its row says %q, deleted_at %v; want %q and nothing",
			class, deletedAt, db.InboxKeptQuestion)
	}
	counted, err := queries.InboxCleanup(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if counted.Counts[db.InboxSettled].Files != 0 {
		t.Errorf("settled counts %d files, want none left in it",
			counted.Counts[db.InboxSettled].Files)
	}
	if counted.Counts[db.InboxKeptQuestion].Files != 2 {
		t.Errorf("questions count %d files, want the one already there and the one spared",
			counted.Counts[db.InboxKeptQuestion].Files)
	}
	// The other three delete classes went as usual.
	if _, err := os.Stat(filepath.Join(inbox, "Refused", "02.flac")); err == nil {
		t.Error("the refused copy survived a pass that only spared one file")
	}
}

// wantPursuedAgain is the store with one thing added: the second time the pass
// reads the copies, the settled want has been pursued again. That second read is
// the one immediately before anything is unlinked.
type wantPursuedAgain struct {
	*db.Queries
	pool     *pgxpool.Pool
	targetID uuid.UUID
	reads    int
}

func (store *wantPursuedAgain) InboxCleanupCopies(
	ctx context.Context,
) ([]db.InboxCopyRow, error) {
	store.reads++
	if store.reads == 2 {
		// acquired_at goes with the status: a want that is being looked for
		// again has not arrived, and the constraints say so.
		if _, err := store.pool.Exec(ctx, `
			UPDATE acquisition_targets
			SET status = 'pending', acquired_at = NULL
			WHERE id = $1
		`, store.targetID); err != nil {
			return nil, err
		}
	}
	return store.Queries.InboxCleanupCopies(ctx)
}

// TestAPassOverTheInboxRefusesToRunTwiceAtOnce holds the schema to the rule the
// handler reports as a conflict.
func TestAPassOverTheInboxRefusesToRunTwiceAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := db.New(dbtest.Setup(t))

	first, err := queries.QueueInboxCleanup(ctx, "leo", true, time.Now())
	if err != nil {
		t.Fatalf("ask for a pass: %v", err)
	}
	if _, err := queries.QueueInboxCleanup(ctx, "leo", true, time.Now()); err == nil {
		t.Fatal("a second pass was queued while the first was still going")
	} else if !isRunning(err) {
		t.Fatalf("the second pass was refused with %v", err)
	}

	if err := queries.FinishInboxCleanup(ctx, first.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.QueueInboxCleanup(ctx, "leo", false, time.Now()); err != nil {
		t.Fatalf("a pass was refused after the first finished: %v", err)
	}
}

func isRunning(err error) bool {
	return err != nil && err.Error() == db.ErrInboxCleanupRunning.Error()
}

// inboxOnDisc writes one folder per class, and dates every file three days back
// except the one the grace period is meant to keep.
func inboxOnDisc(t *testing.T) string {
	t.Helper()
	inbox := t.TempDir()
	old := time.Now().Add(-72 * time.Hour)
	for _, fixture := range inboxFixtures {
		folder := filepath.Join(inbox, fixture.folder)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, fixture.name)
		if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		if fixture.fresh {
			continue
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return inbox
}

// recordedInbox writes the records that make each folder what it is, and returns
// the managed copy of the imported one.
func recordedInbox(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, inbox string,
) (string, uuid.UUID) {
	t.Helper()
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Anetha', 'Anetha')
	`, artistID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3, 'Mothership')
	`, albumID, artistID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// The imported one: a completed import whose managed copy is on disc.
	library := filepath.Join(t.TempDir(), "01.flac")
	if err := os.WriteFile(library, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	importedRequest(ctx, t, pool, albumID, "Imported", "01.flac", library)
	// And the one whose managed copy is not, which is the case the rule keeps.
	importedRequest(ctx, t, pool, albumID, "Unconfirmed", "08.flac",
		filepath.Join(t.TempDir(), "gone.flac"))

	// The open one: arrived, and waiting for somebody to answer its review.
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			album_id, provider, source_username, source_directory, status, import_status,
			file_count, expected_track_count, total_size_bytes, format, score, files
		)
		VALUES ($1, 'slskd', 'peer', 'peer\Open', 'completed', 'needs_review',
		        1, 1, 5, 'flac', 1,
		        '[{"path": "peer\\Open\\06.flac", "name": "06.flac", "extension": "flac", "sizeBytes": 5}]')
	`, albumID); err != nil {
		t.Fatal(err)
	}

	// One copy per remaining class, each on a want in the state that decides it.
	copyOfWant(ctx, t, pool, "discarded_audio", "pending", `peer\Refused\02.flac`, "02.flac", "")
	settled := copyOfWant(ctx, t, pool, "held", "acquired", `peer\Settled\03.flac`, "03.flac", "")
	copyOfWant(ctx, t, pool, "held", "pending", `peer\Question\05.flac`, "05.flac", "")
	return library, settled
}

// importedRequest is one download request whose import completed, with the
// transfer row that says where the file ended up.
func importedRequest(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	albumID uuid.UUID, folder, fileName, localPath string,
) {
	t.Helper()
	sourceDirectory := `peer\` + folder
	remotePath := sourceDirectory + `\` + fileName
	files := []db.DownloadRequestFile{
		{Path: remotePath, Name: fileName, Extension: "flac", SizeBytes: 5},
	}
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	var requestID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO download_requests (
			album_id, provider, source_username, source_directory, status, import_status,
			imported_at, import_path, file_count, expected_track_count, total_size_bytes,
			format, score, files
		)
		VALUES ($1, 'slskd', 'peer', $2, 'completed', 'imported', now(), '/music', 1, 1, 5,
		        'flac', 1, $3::jsonb)
		RETURNING id
	`, albumID, sourceDirectory, encoded).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (album_id, download_request_id, remote_path, local_path, status)
		VALUES ($1, $2, $3, $4, 'completed')
	`, albumID, requestID, remotePath, localPath); err != nil {
		t.Fatal(err)
	}
}

// copyOfWant is one file a peer sent for one want, with the verdict it was given
// and the state its want ended in.
func copyOfWant(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	verdict, status, remotePath, fileName, libraryPath string,
) uuid.UUID {
	t.Helper()
	targetID := uuid.New()
	// A want that arrived carries the stamp saying so, and the constraints
	// refuse the state without it.
	var acquiredAt, notWantedAt *time.Time
	switch status {
	case "acquired":
		now := time.Now()
		acquiredAt = &now
	case "not_wanted":
		now := time.Now()
		notWantedAt = &now
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, status, entry_title, entry_artist, musicbrainz_recording_id,
			acquired_at, not_wanted_at
		)
		VALUES ($1, 'manual', $2, 'Mothership', 'Anetha', $3, $4, $5)
	`, targetID, status, uuid.New(), acquiredAt, notWantedAt); err != nil {
		t.Fatal(err)
	}
	var libraryFileID *uuid.UUID
	if libraryPath != "" {
		id := uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO library_files (id, path, size_bytes, modified_at)
			VALUES ($1, $2, 5, now())
		`, id, libraryPath); err != nil {
			t.Fatal(err)
		}
		libraryFileID = &id
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path, file_name,
			verdict, library_file_id
		)
		VALUES ($1, 'slskd', 'peer', $2, $3, $4, $5)
	`, targetID, remotePath, fileName, verdict, libraryFileID); err != nil {
		t.Fatal(err)
	}
	return targetID
}

func recordedFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, cleanupID uuid.UUID, path string,
) (string, *time.Time) {
	t.Helper()
	var class string
	var deletedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT class, deleted_at FROM inbox_cleanup_files
		WHERE cleanup_id = $1 AND path = $2
	`, cleanupID, path).Scan(&class, &deletedAt); err != nil {
		t.Fatalf("no row for %s: %v", path, err)
	}
	return class, deletedAt
}
