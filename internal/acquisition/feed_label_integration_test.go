package acquisition

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// TestTheFeedWantsARecordingPublishedByAFollowedLabel is the label half of the
// promise TestTheFeedWantsARecordingReleasedAfterTheFollow makes for an artist:
// a release from a label somebody follows becomes a want, naming the label as
// its origin rather than the artist — the user may not follow that artist at
// all.
func TestTheFeedWantsARecordingPublishedByAFollowedLabel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	followed := time.Now().AddDate(0, -1, 0)
	published := time.Now().AddDate(0, 0, -1)
	// The artist is held, not followed: the label is what brings this release
	// in, and nobody asked to keep this artist's own catalogue complete.
	recordingID := seedFollowedRelease(ctx, t, pool, release{releaseDate: &published})
	seedLabelForAlbum(ctx, t, pool, recordingID, followed)

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	target := wantFor(ctx, t, pool, recordingID)
	if target.Origin != "label_feed" {
		t.Fatalf("origin = %s, want label_feed", target.Origin)
	}
}

// A recording a followed artist and a followed label both cover is wanted
// once. The two queries the feed reads cannot see each other's exclusions
// within one pass, so the pass itself has to remember what it already wanted.
func TestTheFeedWantsOneRecordingWhenBothAFollowedArtistAndAFollowedLabelCoverIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	feed := newTestFeed(pool)

	followed := time.Now().AddDate(0, -1, 0)
	published := time.Now().AddDate(0, 0, -1)
	recordingID := seedFollowedRelease(ctx, t, pool, release{
		followedAt: followed, releaseDate: &published,
	})
	seedLabelForAlbum(ctx, t, pool, recordingID, followed)

	if _, err := feed.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	var wants int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM acquisition_targets WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&wants); err != nil {
		t.Fatalf("count wants: %v", err)
	}
	if wants != 1 {
		t.Fatalf("wants = %d, want exactly one", wants)
	}
}

// seedLabelForAlbum follows a label and links it to the release the given
// recording sits on, found through the track that already carries it.
func seedLabelForAlbum(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID, followedAt time.Time,
) uuid.UUID {
	t.Helper()

	var albumID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT album_id FROM tracks WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&albumID); err != nil {
		t.Fatalf("read album for recording %s: %v", recordingID, err)
	}

	var labelID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO labels (musicbrainz_id, name, followed_at, monitor_level)
		VALUES ($1, 'Bloodfire Descent', $2, 'everything')
		RETURNING id
	`, uuid.New(), followedAt).Scan(&labelID); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO label_releases (label_id, album_id) VALUES ($1, $2)
	`, labelID, albumID); err != nil {
		t.Fatalf("link label release: %v", err)
	}
	return labelID
}
