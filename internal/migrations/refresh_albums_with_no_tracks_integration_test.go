package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

const refreshAlbumsWithNoTracksSchema = "schall_refresh_albums_with_no_tracks_check"

// 00129 queues one refresh for each album with no tracks, and none for an
// album that has its tracks or has no MusicBrainz release group to ask about.
func TestTheMigrationRefreshesAlbumsWithNoTracks(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")
	schema := refreshAlbumsWithNoTracksSchema

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + schema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 128); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}

	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO `+schema+`.artists (musicbrainz_id, name, sort_name)
		VALUES (gen_random_uuid(), 'Yeat', 'Yeat')
		RETURNING id
	`).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	album := func(title string, releaseGroup bool) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO `+schema+`.albums (artist_id, musicbrainz_release_group_id, title)
			VALUES ($1, CASE WHEN $3 THEN gen_random_uuid() END, $2)
			RETURNING id
		`, artistID, title, releaseGroup).Scan(&id); err != nil {
			t.Fatalf("insert album %q: %v", title, err)
		}
		return id
	}
	empty := album("ADL", true)
	full := album("2093", true)
	ripped := album("Own rip", false)
	if _, err := pool.Exec(ctx, `
		INSERT INTO `+schema+`.tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, gen_random_uuid(), 'Psycho CEO', 1, 1)
	`, full); err != nil {
		t.Fatal(err)
	}

	if err := migrations.UpTo(ctx, scopedURL, 129); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	queued := func(albumID uuid.UUID) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM `+schema+`.jobs
			WHERE kind = 'refresh_album_metadata' AND status = 'queued'
			  AND payload->>'albumId' = $1::text
		`, albumID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := queued(empty); got != 1 {
		t.Errorf("refreshes for the album with no tracks = %d, want 1", got)
	}
	if got := queued(full); got != 0 {
		t.Errorf("refreshes for the album with tracks = %d, want 0", got)
	}
	if got := queued(ripped); got != 0 {
		t.Errorf("refreshes for the album with no release group = %d, want 0", got)
	}
}
