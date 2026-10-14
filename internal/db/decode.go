package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// FileToDecodeCheckRow is one library file the decode sweep still owes a check.
type FileToDecodeCheckRow struct {
	ID   uuid.UUID
	Path string
}

// FilesToDecodeCheck lists present files nobody has decoded since their bytes
// last changed, oldest scan first. The scanner and the transcoder clear
// decode_checked_at when they see new bytes, which is what puts a file back
// here.
func (q *Queries) FilesToDecodeCheck(ctx context.Context, limit int32) ([]FileToDecodeCheckRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, path
		FROM library_files
		WHERE decode_checked_at IS NULL
		  AND missing_at IS NULL
		ORDER BY scanned_at, id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	files := []FileToDecodeCheckRow{}
	for rows.Next() {
		var file FileToDecodeCheckRow
		if err := rows.Scan(&file.ID, &file.Path); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// RecordDecodeCheck stores what one decode found: the first audio error, or
// nothing for a clean decode. updated_at is left alone for the reason
// RecordFileSpectrum gives.
func (q *Queries) RecordDecodeCheck(ctx context.Context, fileID uuid.UUID, decodeError string) error {
	_, err := q.db.Exec(ctx, `
		UPDATE library_files
		SET decode_error = nullif($2::text, ''),
		    decode_checked_at = now()
		WHERE id = $1
	`, fileID, decodeError)
	return err
}

// QueueDecodeSweep schedules the next decode pass, in the shape
// QueueSpectrumSweep uses. Migration 00122's partial unique index keeps it to
// one pass at a time.
func (q *Queries) QueueDecodeSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_decode_check', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_decode_check' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}

// EnsureDecodeSweepQueued adds a pass only when none is queued and none is
// running. Startup is the only caller.
func (q *Queries) EnsureDecodeSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_decode_check', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_decode_check' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}
