package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pxldi/schall/internal/dbtest"
)

// countingDB counts the statements sent through it.
type countingDB struct {
	DBTX
	statements int
}

func (c *countingDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.statements++
	return c.DBTX.Exec(ctx, sql, args...)
}

func (c *countingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.statements++
	return c.DBTX.Query(ctx, sql, args...)
}

func (c *countingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.statements++
	return c.DBTX.QueryRow(ctx, sql, args...)
}

// The Overview read each row of its lists on its own: 380 statements for one
// page load on a real collection. The lists are now looked up a whole list at
// a time, so a page of full lists costs the same as a page of short ones.
func TestOverviewReadsItsListsAWholeListAtATime(t *testing.T) {
	pool := dbtest.Setup(t)
	ctx := context.Background()
	now := time.Now()

	// Thirty songs by thirty artists from thirty albums, each its own
	// sitting, so every list on the page is full.
	listens := make([]ListenInsert, 0, 30)
	for i := range 30 {
		listens = append(listens, ListenInsert{
			ListenedAt:    now.Add(-time.Duration(i+1) * 31 * time.Minute),
			ArtistName:    fmt.Sprintf("Artist %d • Guest %d", i, i),
			TrackName:     fmt.Sprintf("Song %d", i),
			ReleaseName:   fmt.Sprintf("Album %d", i),
			RecordingMBID: uuid.NewString(),
			ReleaseMBID:   uuid.NewString(),
		})
	}
	if _, err := New(pool).InsertListens(ctx, listens); err != nil {
		t.Fatalf("InsertListens() error = %v", err)
	}

	counting := &countingDB{DBTX: pool}
	got, err := New(counting).Overview(ctx, "UTC", now)
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if len(got.Sessions) != 25 {
		t.Fatalf("sessions = %d, want a full list of 25", len(got.Sessions))
	}
	if counting.statements > 25 {
		t.Errorf("one Overview sent %d statements, want at most 25", counting.statements)
	}
}

// A player can credit a song to two artists while the file is tagged with
// the lead alone. The file still pictures the song.
func TestOverviewPicturesASharedCreditFromAFileTaggedWithTheLead(t *testing.T) {
	pool := dbtest.Setup(t)
	ctx := context.Background()
	now := time.Now()

	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (path, size_bytes, modified_at, title_tag, artist_tag)
		VALUES ('/music/souly.flac', 1000, now(), 'Sweet', 'Souly')
		RETURNING id`).Scan(&fileID); err != nil {
		t.Fatalf("insert a file: %v", err)
	}
	if _, err := New(pool).InsertListens(ctx, []ListenInsert{
		{ListenedAt: now.Add(-time.Minute), ArtistName: "Souly • Waterboutus", TrackName: "Sweet"},
	}); err != nil {
		t.Fatalf("InsertListens() error = %v", err)
	}

	got, err := New(pool).Overview(ctx, "UTC", now)
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if len(got.MostPlayed) != 1 || got.MostPlayed[0].FileID.Bytes != fileID {
		t.Errorf("most played = %+v, want Sweet pictured by %s", got.MostPlayed, fileID)
	}
}
