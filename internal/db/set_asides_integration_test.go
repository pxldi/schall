package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

func TestSetAsideIsIdempotentAndCanBeCleared(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, match_status)
		VALUES ($1, '/music/aside.flac', 1, now(), 'ambiguous')
	`, fileID); err != nil {
		t.Fatal(err)
	}

	if err := queries.SetLibraryFileAside(ctx, fileID); err != nil {
		t.Fatalf("SetLibraryFileAside(): %v", err)
	}
	if err := queries.SetLibraryFileAside(ctx, fileID); err != nil {
		t.Fatalf("SetLibraryFileAside() twice: %v", err)
	}

	var count int
	var decidedBy string
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int, min(decided_by)
		FROM library_file_set_asides WHERE library_file_id = $1
	`, fileID).Scan(&count, &decidedBy); err != nil {
		t.Fatal(err)
	}
	if count != 1 || decidedBy != "user" {
		t.Fatalf("set-asides = %d by %q, want one row by user", count, decidedBy)
	}

	listed, err := queries.ListLibraryFiles(ctx, ListLibraryFilesParams{Limit: 1, SetAside: boolPointer(true)})
	if err != nil {
		t.Fatalf("ListLibraryFiles() set aside: %v", err)
	}
	if len(listed.Items) != 1 || !listed.Items[0].SetAside {
		t.Fatalf("set-aside files = %#v", listed.Items)
	}

	if err := queries.ClearLibraryFileSetAside(ctx, fileID); err != nil {
		t.Fatalf("ClearLibraryFileSetAside(): %v", err)
	}
	listed, err = queries.ListLibraryFiles(ctx, ListLibraryFilesParams{Limit: 1, SetAside: boolPointer(false)})
	if err != nil {
		t.Fatalf("ListLibraryFiles() after clearing: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].SetAside {
		t.Fatalf("files after clearing = %#v", listed.Items)
	}
	if err := queries.ClearLibraryFileSetAside(ctx, fileID); !errors.Is(err, ErrLibraryFileNotSetAside) {
		t.Fatalf("clearing twice = %v, want ErrLibraryFileNotSetAside", err)
	}
}

func boolPointer(value bool) *bool { return &value }
