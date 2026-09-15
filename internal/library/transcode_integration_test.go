package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

// The import writes the file and asks for a scan; the scan is what creates the
// library row, some seconds later. This is the moment the note the import left
// against a path becomes a fact about a row.
func TestAScanReadsWhatAnImportSaidTheFileUsedToBe(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)

	rootPath := t.TempDir()
	audioPath := filepath.Join(rootPath, "Portishead - Roads.mp3")
	if err := os.WriteFile(audioPath, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, uuid.New(), rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO import_transcodes (local_path, source_format, source_size_bytes, target_format)
		VALUES ($1, 'flac', 40000000, 'mp3')
	`, audioPath); err != nil {
		t.Fatal(err)
	}

	if _, err := NewScanner(pool).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	var format string
	var original int64
	if err := pool.QueryRow(ctx, `
		SELECT transcoded_from_format, original_size_bytes FROM library_files WHERE path = $1
	`, audioPath).Scan(&format, &original); err != nil {
		t.Fatal(err)
	}
	if format != "flac" || original != 40_000_000 {
		t.Fatalf("the row says it came from %q at %d bytes", format, original)
	}
}

// The tagging pass rewrites tags into a file the moment it is imported, which
// changes its size and brings the next scan back to the same row. What the file
// used to be must survive that: it is the only record there is.
func TestAReScanKeepsWhatTheFileUsedToBe(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)

	rootPath := t.TempDir()
	audioPath := filepath.Join(rootPath, "Portishead - Roads.mp3")
	if err := os.WriteFile(audioPath, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, uuid.New(), rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO import_transcodes (local_path, source_format, source_size_bytes, target_format)
		VALUES ($1, 'flac', 40000000, 'mp3')
	`, audioPath); err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	// The note is gone and the file has changed size, which is exactly the
	// state a tagged import leaves behind.
	if _, err := pool.Exec(ctx, `DELETE FROM import_transcodes`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audioPath, []byte("broken fixture, retagged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	var format string
	if err := pool.QueryRow(ctx, `
		SELECT transcoded_from_format FROM library_files WHERE path = $1
	`, audioPath).Scan(&format); err != nil {
		t.Fatal(err)
	}
	if format != "flac" {
		t.Fatalf("the second scan forgot the file was a %q", format)
	}
}
