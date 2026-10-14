package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

// A file filed under the single's row is the library's copy of the album's row
// too, once that row is recorded as a second row of the same song.
func TestASecondRowIsOwnedByTheFileThatAnswersIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	singleRow, albumRow := uuid.New(), uuid.New()
	fileID := seedLibraryFileWithIdentity(ctx, t, pool, "/music/single.mp3", singleRow)
	if err := AddSecondRow(ctx, pool, fileID, albumRow); err != nil {
		t.Fatal(err)
	}

	owned, err := New(pool).OwnedFileForRecording(ctx, albumRow)
	if err != nil {
		t.Fatal(err)
	}
	if !owned.Valid || owned.UUID != fileID {
		t.Fatalf("owned = %v, want %s", owned, fileID)
	}
}

// A new answer about what the file is drops the rows the old one answered.
func TestASecondRowGoesWithTheIdentityItHangsOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	fileID := seedLibraryFileWithIdentity(ctx, t, pool, "/music/single.mp3", uuid.New())
	if err := AddSecondRow(ctx, pool, fileID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM library_file_identities WHERE library_file_id = $1`, fileID); err != nil {
		t.Fatal(err)
	}

	rows, err := SecondRows(ctx, pool, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("second rows = %v, want none", rows)
	}
}

// The row the identity is filed under is not a second row of itself.
func TestTheFiledRowIsNotAddedAsASecondRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	row := uuid.New()
	fileID := seedLibraryFileWithIdentity(ctx, t, pool, "/music/single.mp3", row)
	if err := AddSecondRow(ctx, pool, fileID, row); err != nil {
		t.Fatal(err)
	}

	rows, err := SecondRows(ctx, pool, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("second rows = %v, want none", rows)
	}
}
