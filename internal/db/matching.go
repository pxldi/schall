package db

import (
	"context"

	"github.com/google/uuid"
)

func (q *Queries) SelectAlbumEdition(ctx context.Context, albumID, releaseID uuid.UUID) (LibraryScanJobRow, error) {
	var row LibraryScanJobRow
	err := q.db.QueryRow(ctx, `
		WITH selected AS (
		    UPDATE albums
		    SET preferred_musicbrainz_release_id = $2, updated_at = now()
		    WHERE id = $1 AND musicbrainz_release_group_id IS NOT NULL
		    RETURNING id
		), cancelled AS (
		    UPDATE jobs
		    SET status = 'cancelled', completed_at = now(), updated_at = now()
		    WHERE kind = 'refresh_album_metadata'
		      AND payload->>'albumId' = $1::text
		      AND status = 'queued'
		      AND EXISTS (SELECT 1 FROM selected)
		), inserted AS (
		    INSERT INTO jobs (kind, payload)
		    SELECT 'refresh_album_metadata', jsonb_build_object('albumId', id::text)
		    FROM selected
		    ON CONFLICT (kind, (payload->>'albumId'))
		        WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
		    DO NOTHING
		    RETURNING id, status, created_at
		)
		SELECT id, status, created_at FROM inserted
		UNION ALL
		SELECT id, status, created_at
		FROM jobs
		WHERE kind = 'refresh_album_metadata'
		  AND payload->>'albumId' = $1::text
		  AND status IN ('queued', 'running')
		ORDER BY created_at
		LIMIT 1
	`, albumID, releaseID).Scan(&row.ID, &row.Status, &row.CreatedAt)
	return row, err
}

// QueueAlbumRefresh asks for one release to be looked at again: its track list
// fetched from MusicBrainz once more, and the library re-examined against it.
//
// It is what "re-match" means for a release. Nothing about the rules changes —
// this queues the same pass a release gets when it is first ingested, and that
// pass decides what it has always decided.
//
// One pass per release, enforced by jobs_active_album_refresh_idx. A release
// already queued or already running keeps the row it has, and the answer says
// so: `queued` is false, which is what lets a caller report "re-matched 4, 2
// already running" rather than claiming six.
//
// Askable is the second answer, and it is a different fact from the first. A
// release the catalogue no longer holds, and one with no MusicBrainz release
// group to ask about, are both unaskable — and neither is the same as a release
// whose pass is already running. Without this the two would report identically,
// and pressing on a release that had been deleted would read as "already
// queued".
func (q *Queries) QueueAlbumRefresh(
	ctx context.Context, albumID uuid.UUID,
) (queued, askable bool, err error) {
	err = q.db.QueryRow(ctx, `
		WITH asking AS (
		    SELECT id FROM albums
		    WHERE id = $1 AND musicbrainz_release_group_id IS NOT NULL
		), inserted AS (
		    INSERT INTO jobs (kind, payload)
		    SELECT 'refresh_album_metadata', jsonb_build_object('albumId', id::text)
		    FROM asking
		    ON CONFLICT (kind, (payload->>'albumId'))
		        WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
		    DO NOTHING
		    RETURNING id
		)
		SELECT EXISTS (SELECT 1 FROM inserted), EXISTS (SELECT 1 FROM asking)
	`, albumID).Scan(&queued, &askable)
	return queued, askable, err
}
