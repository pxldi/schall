package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// Nothing is stored until the default is departed from, and departing from it
// is one row that keeps being the same row.
func TestTheLayoutIsAbsentUntilItIsChanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	if _, err := queries.LibraryLayoutSettings(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows before anything is stored", err)
	}

	stored, err := queries.SaveLibraryLayoutSettings(ctx, "{artist}/{album}")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Template != "{artist}/{album}" {
		t.Fatalf("template = %q", stored.Template)
	}

	again, err := queries.SaveLibraryLayoutSettings(ctx, "{album} [{id}]")
	if err != nil {
		t.Fatal(err)
	}
	if again.Template != "{album} [{id}]" {
		t.Fatalf("template = %q, want the second save to replace the first", again.Template)
	}

	read, err := queries.LibraryLayoutSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.Template != "{album} [{id}]" {
		t.Fatalf("template = %q, want one row rather than two", read.Template)
	}
}

// The parser is what refuses a template, and it names the word that was wrong.
// These are the backstop under it: a template that reached the table another
// way still cannot be one that writes outside the library.
func TestTheTableRefusesATemplateThatWouldWriteOutsideTheLibrary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := New(dbtest.Setup(t))

	for _, template := range []string{"/srv/music/{album}", "../{artist}/{album}", "   "} {
		if _, err := queries.SaveLibraryLayoutSettings(ctx, template); err == nil {
			t.Fatalf("SaveLibraryLayoutSettings(%q) = nil, want the constraint to refuse it", template)
		}
	}
}

// The journal is what makes a layout migration resumable, so the one thing it
// must not allow is two runs planning a rename of the same file at once.
func TestOnlyOneMoveIsOpenPerFileAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := insertLayoutTestFile(ctx, t, pool, "/music/Artist/Album [aaaa1111]/01.flac")
	plan := func(runID uuid.UUID, to string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO library_file_moves (run_id, library_file_id, from_path, to_path)
			VALUES ($1, $2, $3, $4)
		`, runID, fileID, "/music/Artist/Album [aaaa1111]/01.flac", to)
		return err
	}

	if err := plan(uuid.New(), "/music/Artist/Album/01.flac"); err != nil {
		t.Fatal(err)
	}
	if err := plan(uuid.New(), "/music/Album/01.flac"); err == nil {
		t.Fatal("a second run planned a move of a file another run was already renaming")
	}

	if _, err := pool.Exec(ctx, `
		UPDATE library_file_moves SET status = 'moved', moved_at = now()
		WHERE library_file_id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	// A settled move is not an open one, so the next run may plan its own.
	if err := plan(uuid.New(), "/music/Album/01.flac"); err != nil {
		t.Fatalf("a settled move still blocked the next run: %v", err)
	}
}

// A move that never happened has no time it happened at, and a reason recorded
// against a move that did not fail is a contradiction.
func TestAMoveCannotClaimToHaveHappenedWithoutHavingHappened(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := insertLayoutTestFile(ctx, t, pool, "/music/Artist/Album [bbbb2222]/01.flac")
	insert := func(status, moved, errText string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO library_file_moves (
				run_id, library_file_id, from_path, to_path, status, moved_at, error
			)
			VALUES ($1, $2, $3, $4, $5, nullif($6, '')::timestamptz, nullif($7, ''))
		`, uuid.New(), fileID, "/from.flac", "/to.flac", status, moved, errText)
		return err
	}

	if err := insert("planned", "2026-08-02T12:00:00Z", ""); err == nil {
		t.Fatal("a planned move carried a time it was renamed at")
	}
	if err := insert("moved", "", ""); err == nil {
		t.Fatal("a move that happened carried no time it happened at")
	}
	if err := insert("moved", "2026-08-02T12:00:00Z", "disc full"); err == nil {
		t.Fatal("a move that succeeded carried a reason it failed")
	}
	if err := insert("failed", "", "disc full"); err != nil {
		t.Fatalf("a failed move with a reason was refused: %v", err)
	}
}

// A rename onto the same path is not a move. Planning one would make a no-op
// run look like work and a rename onto itself look like a failure.
func TestAMoveToWhereTheFileAlreadyIsIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := insertLayoutTestFile(ctx, t, pool, "/music/Artist/Album [cccc3333]/01.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_moves (run_id, library_file_id, from_path, to_path)
		VALUES ($1, $2, $3, $3)
	`, uuid.New(), fileID, "/music/same.flac"); err == nil {
		t.Fatal("a move from a path to itself was planned")
	}
}

func insertLayoutTestFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, path string,
) uuid.UUID {
	t.Helper()
	var rootID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_roots (path) VALUES ($1)
		ON CONFLICT (path) DO UPDATE SET path = excluded.path
		RETURNING id
	`, "/music").Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (library_root_id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1, now())
		RETURNING id
	`, rootID, path).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}
