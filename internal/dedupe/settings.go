package dedupe

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
)

// Settings reads and writes whether Schall settles the recordings the library
// holds more than once by itself.
type Settings struct{ pool *pgxpool.Pool }

func NewSettings(pool *pgxpool.Pool) *Settings { return &Settings{pool: pool} }

// Settings answers what is stored. An installation nobody has answered for is
// off — the same reading the column default gives.
func (settings *Settings) Settings(ctx context.Context) (db.DuplicateResolutionSettingsRow, error) {
	row, err := db.New(settings.pool).DuplicateResolutionSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.DuplicateResolutionSettingsRow{Enabled: false}, nil
	}
	return row, err
}

// SaveSettings writes it, and asks for a pass now where it has just been turned
// on. Turning it on is the whole instruction: a press that saved a setting and
// then waited hours for the backstop is a press that did nothing a person could
// see.
func (settings *Settings) SaveSettings(
	ctx context.Context, enabled bool,
) (db.DuplicateResolutionSettingsRow, error) {
	saved, err := db.New(settings.pool).SaveDuplicateResolutionSettings(ctx, enabled)
	if err != nil {
		return db.DuplicateResolutionSettingsRow{}, err
	}
	if saved.Enabled {
		// A failure to queue is not a failure to save. The setting is written,
		// which is what was asked for, and the backstop asks anyway.
		_ = db.New(settings.pool).QueueDuplicateSweep(ctx, time.Now())
	}
	return saved, nil
}
