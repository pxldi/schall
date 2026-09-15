package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const insertLabelRefreshJob = `
INSERT INTO jobs (kind, payload)
SELECT 'refresh_label_metadata', jsonb_build_object('labelId', id)
FROM labels
WHERE id = $1
RETURNING id, status, created_at
`

const activeLabelRefreshJob = `
SELECT id, status, created_at
FROM jobs
WHERE kind = 'refresh_label_metadata'
  AND status IN ('queued', 'running')
  AND payload->>'labelId' = $1::text
ORDER BY created_at DESC
LIMIT 1
`

// QueueLabelRefresh asks for the walk over a label's releases. Asking twice is
// one walk: a refresh already queued or running for this label is the answer.
//
// This is handwritten rather than sqlc-generated because the earlier
// read-then-insert could not see another transaction's uncommitted row: a
// sweep and a "Refresh now" press racing each other could both find nothing
// active and both insert, each holding half the walk's progress. The unique
// index label_refresh_jobs_active_idx (00070_labels.sql) makes the database
// the arbiter instead — this always attempts the insert, and a 23505 means
// somebody else's insert won the race, so what is returned is theirs.
func (q *Queries) QueueLabelRefresh(ctx context.Context, labelID uuid.UUID) (QueueLabelRefreshRow, error) {
	var i QueueLabelRefreshRow
	err := q.db.QueryRow(ctx, insertLabelRefreshJob, labelID).Scan(&i.ID, &i.Status, &i.CreatedAt)
	if err == nil {
		return i, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return QueueLabelRefreshRow{}, err
	}
	var conflict *pgconn.PgError
	if !errors.As(err, &conflict) || conflict.Code != "23505" {
		return QueueLabelRefreshRow{}, err
	}
	if scanErr := q.db.QueryRow(ctx, activeLabelRefreshJob, labelID).
		Scan(&i.ID, &i.Status, &i.CreatedAt); scanErr != nil {
		return QueueLabelRefreshRow{}, scanErr
	}
	return i, nil
}

// QueueLabelRefreshRow is the queued or already-active pass a label's refresh
// is answered with.
type QueueLabelRefreshRow struct {
	ID        uuid.UUID          `json:"id"`
	Status    string             `json:"status"`
	CreatedAt pgtype.Timestamptz `json:"created_at"`
}
