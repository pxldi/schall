package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/dbtest"
)

func TestSelectAlbumEditionPersistsPreferenceAndQueuesRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, releaseGroupID, releaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		)
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		VALUES ($2, $1, $3, 'Dummy')
	`, artistID, albumID, releaseGroupID); err != nil {
		t.Fatal(err)
	}

	job, err := New(pool).SelectAlbumEdition(ctx, albumID, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "queued" {
		t.Fatalf("job = %#v", job)
	}
	var savedReleaseID uuid.UUID
	var payloadAlbumID string
	if err := pool.QueryRow(ctx, `
		SELECT albums.preferred_musicbrainz_release_id, jobs.payload->>'albumId'
		FROM albums
		JOIN jobs ON jobs.id = $2
		WHERE albums.id = $1
	`, albumID, job.ID).Scan(&savedReleaseID, &payloadAlbumID); err != nil {
		t.Fatal(err)
	}
	if savedReleaseID != releaseID || payloadAlbumID != albumID.String() {
		t.Fatalf("selection = release %s, payload album %q", savedReleaseID, payloadAlbumID)
	}
}
