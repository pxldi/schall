package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// LibraryLayoutRow is the stored folder layout.
//
// There is no row until somebody departs from the default, so pgx.ErrNoRows is
// an ordinary answer here rather than a missing configuration: what this table
// records is a departure, not the existence of a layout. What the default
// itself is belongs to internal/library, which owns templates; this package
// deliberately knows nothing about the rest of Schall.
type LibraryLayoutRow struct {
	Template  string
	UpdatedAt pgtype.Timestamptz
}

// LibraryLayoutSettings returns the stored layout, or pgx.ErrNoRows when the
// default has never been departed from.
func (q *Queries) LibraryLayoutSettings(ctx context.Context) (LibraryLayoutRow, error) {
	var result LibraryLayoutRow
	err := q.db.QueryRow(ctx, `
		SELECT template, updated_at
		FROM library_layout_settings
		WHERE singleton
	`).Scan(&result.Template, &result.UpdatedAt)
	return result, err
}

// SaveLibraryLayoutSettings stores the layout template.
//
// It stores what it is given and leans on the constraints for the rest: a
// template is refused where somebody types it, by the parser that can name the
// word that was wrong, rather than here where the only answer available is
// that the database said no.
func (q *Queries) SaveLibraryLayoutSettings(
	ctx context.Context, template string,
) (LibraryLayoutRow, error) {
	var result LibraryLayoutRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO library_layout_settings (template)
		VALUES ($1)
		ON CONFLICT (singleton) DO UPDATE SET
			template = excluded.template,
			updated_at = now()
		RETURNING template, updated_at
	`, template).Scan(&result.Template, &result.UpdatedAt)
	return result, err
}
