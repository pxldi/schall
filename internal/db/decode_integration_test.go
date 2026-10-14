package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
)

// A present file nobody has decoded is offered once. A recorded check, clean or
// damaged, takes it off the list, and a missing file is never offered.
func TestFilesToDecodeCheckOffersEachPresentFileOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	store := New(pool)

	clean, damaged, missing := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, scanned_at, missing_at)
		VALUES
		    ($1, '/music/a.flac', 1, now(), now() - interval '2 minutes', NULL),
		    ($2, '/music/b.mp3', 1, now(), now() - interval '1 minute', NULL),
		    ($3, '/music/c.flac', 1, now(), now(), now())
	`, clean, damaged, missing); err != nil {
		t.Fatal(err)
	}

	due, err := store.FilesToDecodeCheck(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 || due[0].ID != clean || due[1].ID != damaged {
		t.Fatalf("offered %+v; want the two present files, oldest scan first", due)
	}

	if err := store.RecordDecodeCheck(ctx, clean, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDecodeCheck(ctx, damaged, "Header missing"); err != nil {
		t.Fatal(err)
	}

	due, err = store.FilesToDecodeCheck(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("offered %+v after both were checked; want none", due)
	}
	var cleanError, damagedError pgtype.Text
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT decode_error FROM library_files WHERE id = $1),
		       (SELECT decode_error FROM library_files WHERE id = $2)
	`, clean, damaged).Scan(&cleanError, &damagedError); err != nil {
		t.Fatal(err)
	}
	if cleanError.Valid || damagedError.String != "Header missing" {
		t.Fatalf("decode_error = %v and %v; want NULL and the decoder's error", cleanError, damagedError)
	}
}
