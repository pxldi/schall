package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ListenInsert is one listen as the sync job hands it over. Blank identifiers
// are stored as NULL; the names are stored as they came.
type ListenInsert struct {
	ListenedAt     time.Time
	ArtistName     string
	TrackName      string
	ReleaseName    string
	RecordingMBID  string
	ReleaseMBID    string
	CAAReleaseMBID string
	ArtistMBIDs    []string
}

// InsertListens stores a page of listens and says how many were new. A listen
// already held (same moment, artist and track) is skipped, so a page pulled
// twice costs nothing.
func (q *Queries) InsertListens(ctx context.Context, listens []ListenInsert) (int64, error) {
	if len(listens) == 0 {
		return 0, nil
	}
	var inserted int64
	for _, listen := range listens {
		artistIDs := make([]uuid.UUID, 0, len(listen.ArtistMBIDs))
		for _, raw := range listen.ArtistMBIDs {
			if id, err := uuid.Parse(raw); err == nil {
				artistIDs = append(artistIDs, id)
			}
		}
		var artistArray any
		if len(artistIDs) > 0 {
			artistArray = artistIDs
		}
		tag, err := q.db.Exec(ctx, `
			INSERT INTO listens (
				listened_at, artist_name, track_name, release_name,
				recording_mbid, release_mbid, caa_release_mbid, artist_mbids
			)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8)
			ON CONFLICT (listened_at, artist_name, track_name) DO NOTHING`,
			listen.ListenedAt, listen.ArtistName, listen.TrackName, listen.ReleaseName,
			nullUUID(listen.RecordingMBID), nullUUID(listen.ReleaseMBID),
			nullUUID(listen.CAAReleaseMBID), artistArray,
		)
		if err != nil {
			return inserted, fmt.Errorf("insert listen: %w", err)
		}
		inserted += tag.RowsAffected()
	}
	return inserted, nil
}

// nullUUID reads an identifier the service may have left blank or malformed.
func nullUUID(raw string) pgtype.UUID {
	id, err := uuid.Parse(raw)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// ListenBounds is the oldest and newest listen held, and how many there are.
// Both times are invalid when there are none.
type ListenBounds struct {
	Oldest pgtype.Timestamptz
	Newest pgtype.Timestamptz
	Count  int64
}

// ListenBounds says how far the stored history reaches, which is what the
// sync job needs to decide whether to pull forward or fill in backwards.
func (q *Queries) ListenBounds(ctx context.Context) (ListenBounds, error) {
	var bounds ListenBounds
	err := q.db.QueryRow(ctx, `
		SELECT min(listened_at), max(listened_at), count(*) FROM listens`,
	).Scan(&bounds.Oldest, &bounds.Newest, &bounds.Count)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ListenBounds{}, fmt.Errorf("read listen bounds: %w", err)
	}
	return bounds, nil
}

// QueueListensSync asks for one sync at the given time. There is at most one
// queued or running at a time (migration 00094); asking while one exists
// changes nothing.
func (q *Queries) QueueListensSync(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sync_listens', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sync_listens' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// EnsureListensSyncQueued is what startup calls: one sync if none is on the
// books, and the one already waiting keeps its time.
func (q *Queries) EnsureListensSyncQueued(ctx context.Context, runAfter time.Time) error {
	return q.QueueListensSync(ctx, runAfter)
}
