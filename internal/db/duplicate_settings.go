package db

import (
	"context"
	"fmt"
	"time"
)

// DuplicateResolutionSettingsRow says whether Schall settles the recordings the
// library holds more than once by itself.
type DuplicateResolutionSettingsRow struct {
	Enabled bool `json:"enabled"`
}

// DuplicateResolutionSettings reads the setting. An installation nobody has
// answered for is off, which is what the column default says too.
func (q *Queries) DuplicateResolutionSettings(ctx context.Context) (DuplicateResolutionSettingsRow, error) {
	var row DuplicateResolutionSettingsRow
	err := q.db.QueryRow(ctx, `
		SELECT enabled FROM duplicate_resolution_settings WHERE singleton
	`).Scan(&row.Enabled)
	if err != nil {
		return DuplicateResolutionSettingsRow{}, err
	}
	return row, nil
}

// SaveDuplicateResolutionSettings writes it.
func (q *Queries) SaveDuplicateResolutionSettings(
	ctx context.Context, enabled bool,
) (DuplicateResolutionSettingsRow, error) {
	var row DuplicateResolutionSettingsRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO duplicate_resolution_settings (singleton, enabled)
		VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()
		RETURNING enabled
	`, enabled).Scan(&row.Enabled)
	if err != nil {
		return DuplicateResolutionSettingsRow{}, fmt.Errorf("save the duplicate resolution setting: %w", err)
	}
	return row, nil
}

// QueueDuplicateSweep asks for one pass over the recordings held more than
// once. The partial unique index from 00076 allows one queued or running sweep,
// so asking while one is waiting is not an error and adds nothing.
func (q *Queries) QueueDuplicateSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, run_after)
		VALUES ('sweep_duplicates', '{}'::jsonb, $1)
		ON CONFLICT DO NOTHING
	`, runAfter)
	if err != nil {
		return fmt.Errorf("queue the pass over the recordings held more than once: %w", err)
	}
	return nil
}
