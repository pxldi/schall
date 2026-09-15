package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/dbtest"
)

// A root is where the scanner is told to look, so recording one has to make it
// visible to whatever asks what the library covers.
func TestARootIsListedOnceItIsAdded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	created, err := queries.CreateLibraryRoot(ctx, "/music")
	if err != nil {
		t.Fatalf("CreateLibraryRoot() error = %v", err)
	}
	if !created.Enabled {
		t.Fatal("a root nobody disabled arrives switched off")
	}

	roots, err := queries.ListLibraryRoots(ctx)
	if err != nil {
		t.Fatalf("ListLibraryRoots() error = %v", err)
	}
	if len(roots) != 1 || roots[0].Path != "/music" {
		t.Fatalf("roots = %+v, want the one just added", roots)
	}
}

// Roots are listed in path order rather than in the order somebody happened to
// add them, so the settings page reads the same way twice.
func TestRootsAreListedInPathOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	for _, path := range []string{"/music/singles", "/music/albums"} {
		if _, err := queries.CreateLibraryRoot(ctx, path); err != nil {
			t.Fatalf("CreateLibraryRoot() error = %v", err)
		}
	}

	roots, err := queries.ListLibraryRoots(ctx)
	if err != nil {
		t.Fatalf("ListLibraryRoots() error = %v", err)
	}
	if len(roots) != 2 || roots[0].Path != "/music/albums" {
		t.Fatalf("roots = %+v, want them in path order", roots)
	}
}

// One scan at a time. A second is not a second walk of the disc: it is the same
// walk, and asking for it is answered by the scan that is already going to run.
func TestAskingForAScanTwiceIsAnsweredByTheOneAlreadyQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	first, err := queries.QueueLibraryScan(ctx)
	if err != nil {
		t.Fatalf("QueueLibraryScan() error = %v", err)
	}
	second, err := queries.QueueLibraryScan(ctx)
	if err != nil {
		t.Fatalf("QueueLibraryScan() error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second scan = %s, want the one already queued (%s)", second.ID, first.ID)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'scan_library' AND status IN ('queued', 'running')
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued scans = %d, want exactly one", queued)
	}
}

// The summary is what the library page opens with, so a file the scan could not
// read is counted as an error rather than folded into the total and forgotten.
func TestTheSummaryCountsAFileTheScanCouldNotRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (path, size_bytes, modified_at, scan_error)
		VALUES ('/music/broken.flac', 1024, now(), 'the tags could not be read')
	`); err != nil {
		t.Fatal(err)
	}

	summary, err := New(pool).LibrarySummary(ctx)
	if err != nil {
		t.Fatalf("LibrarySummary() error = %v", err)
	}
	if summary.FileCount != 1 || summary.ErrorCount != 1 {
		t.Fatalf("summary = %d files of which %d unreadable, want 1 and 1",
			summary.FileCount, summary.ErrorCount)
	}
}

// A library nobody has scanned is idle rather than in some state it has never
// been in, so the page says "nothing has happened yet" instead of guessing.
func TestALibraryNobodyHasScannedIsIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	summary, err := New(pool).LibrarySummary(ctx)
	if err != nil {
		t.Fatalf("LibrarySummary() error = %v", err)
	}
	if summary.ScanStatus != "idle" {
		t.Fatalf("scan status = %q, want idle", summary.ScanStatus)
	}
}

// A root is one path, however many times it is offered. Two rows for one
// directory would have the scanner walk it twice and count everything in it
// twice over.
func TestARootIsOnePathHoweverOftenItIsOffered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := queries.CreateLibraryRoot(ctx, "/music"); err != nil {
		t.Fatalf("CreateLibraryRoot() error = %v", err)
	}

	if _, err := queries.CreateLibraryRoot(ctx, "/music"); err == nil {
		t.Fatal("the same directory was recorded as two roots")
	}
}

// Sync joins a song the player reports to a file by its path, so the lookup is
// exact and nothing else.
func TestALibraryFileIsFoundAtExactlyItsPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	fileID := seedLibraryFile(t, ctx, pool, "/music/artist/one.flac")

	file, err := queries.LibraryFileByPath(ctx, "/music/artist/one.flac")

	if err != nil {
		t.Fatalf("LibraryFileByPath() error = %v", err)
	}
	if file.ID != fileID || file.Path != "/music/artist/one.flac" {
		t.Fatalf("file = %#v, want the file at that path", file)
	}
}

func TestANeighbouringPathIsNotTheFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	seedLibraryFile(t, ctx, pool, "/music/artist/one.flac")

	_, err := queries.LibraryFileByPath(ctx, "/music/artist/One.flac")

	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("error = %v, want pgx.ErrNoRows for a path that is not the same string", err)
	}
}

// A file the last scan found missing is not a file the library holds: pointing
// a playlist entry at it would claim music that is not there.
func TestAMissingFileIsNotFoundByItsPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	fileID := seedLibraryFile(t, ctx, pool, "/music/artist/gone.flac")
	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET missing_at = now() WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}

	_, err := queries.LibraryFileByPath(ctx, "/music/artist/gone.flac")

	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("error = %v, want pgx.ErrNoRows", err)
	}
}
