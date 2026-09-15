package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// SourcePreferencesRow is what the user has said about the copies Schall should
// prefer and the copies it must never fetch. See migration 00055.
//
// An installation that has never touched this has one empty row, or no row at
// all, and both mean the same thing: rank as Schall has always ranked.
type SourcePreferencesRow struct {
	Preferred      []string `json:"preferred_formats"`
	Unacceptable   []string `json:"unacceptable_formats"`
	MinimumBitRate int32    `json:"minimum_bitrate"`
	// FormatMinimumBitRate is the floor for one named lossy format, keyed by the
	// bare extension. A format named here is held to its own number instead of
	// MinimumBitRate. See migration 00061.
	FormatMinimumBitRate map[string]int32 `json:"format_minimum_bitrates"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

// SourcePreferences reads the stored preferences. A table with no row in it is
// answered with the empty preferences rather than pgx.ErrNoRows: an
// installation that has said nothing is not an error, and every caller of this
// would otherwise have to say so itself.
func (q *Queries) SourcePreferences(ctx context.Context) (SourcePreferencesRow, error) {
	var row SourcePreferencesRow
	err := q.db.QueryRow(ctx, `
		SELECT preferred_formats, unacceptable_formats, minimum_bitrate,
		       format_minimum_bitrates, updated_at
		FROM source_preferences
		WHERE singleton
	`).Scan(&row.Preferred, &row.Unacceptable, &row.MinimumBitRate,
		&row.FormatMinimumBitRate, &row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourcePreferencesRow{
			Preferred: []string{}, Unacceptable: []string{},
			FormatMinimumBitRate: map[string]int32{},
		}, nil
	}
	return row, err
}

// bitRateFloorLowered reports whether any effective lossy-format floor became
// lower. A named format floor replaces the general floor for that format.
func bitRateFloorLowered(old, next SourcePreferencesRow) bool {
	if next.MinimumBitRate < old.MinimumBitRate {
		return true
	}
	formats := make(map[string]struct{}, len(old.FormatMinimumBitRate)+len(next.FormatMinimumBitRate))
	for format := range old.FormatMinimumBitRate {
		formats[format] = struct{}{}
	}
	for format := range next.FormatMinimumBitRate {
		formats[format] = struct{}{}
	}
	for format := range formats {
		oldFloor := old.MinimumBitRate
		if floor, ok := old.FormatMinimumBitRate[format]; ok {
			oldFloor = floor
		}
		nextFloor := next.MinimumBitRate
		if floor, ok := next.FormatMinimumBitRate[format]; ok {
			nextFloor = floor
		}
		if nextFloor < oldFloor {
			return true
		}
	}
	return false
}

// SaveSourcePreferences stores what the user said, replacing whatever was there.
// There is one row, so there is nothing to merge: the settings screen sends the
// whole answer every time.
func (q *Queries) SaveSourcePreferences(
	ctx context.Context, saving SourcePreferencesRow,
) (SourcePreferencesRow, error) {
	// An empty list and no list are the same answer here, and the columns cannot
	// hold the second one. A caller that says "nothing" says it as an empty
	// array rather than being refused for the shape of a Go nil.
	if saving.Preferred == nil {
		saving.Preferred = []string{}
	}
	if saving.Unacceptable == nil {
		saving.Unacceptable = []string{}
	}
	if saving.FormatMinimumBitRate == nil {
		saving.FormatMinimumBitRate = map[string]int32{}
	}
	var row SourcePreferencesRow
	old, err := q.SourcePreferences(ctx)
	if err != nil {
		return SourcePreferencesRow{}, err
	}
	err = q.db.QueryRow(ctx, `
		INSERT INTO source_preferences (
			singleton, preferred_formats, unacceptable_formats, minimum_bitrate,
			format_minimum_bitrates
		)
		VALUES (true, $1, $2, $3, $4)
		ON CONFLICT (singleton) DO UPDATE SET
			preferred_formats = excluded.preferred_formats,
			unacceptable_formats = excluded.unacceptable_formats,
			minimum_bitrate = excluded.minimum_bitrate,
			format_minimum_bitrates = excluded.format_minimum_bitrates,
			updated_at = now()
		RETURNING preferred_formats, unacceptable_formats, minimum_bitrate,
		          format_minimum_bitrates, updated_at
	`, saving.Preferred, saving.Unacceptable, saving.MinimumBitRate,
		saving.FormatMinimumBitRate).Scan(
		&row.Preferred, &row.Unacceptable, &row.MinimumBitRate,
		&row.FormatMinimumBitRate, &row.UpdatedAt)
	if err != nil {
		return row, err
	}
	if bitRateFloorLowered(old, saving) {
		_, err = q.db.Exec(ctx, `
			UPDATE acquisition_targets
			SET next_attempt_at = now(), below_floor_streak = 0,
			    below_floor_since = NULL, updated_at = now()
			WHERE status = 'pending' AND below_floor_since IS NOT NULL
		`)
		if err != nil {
			return row, err
		}
	}
	return row, nil
}
