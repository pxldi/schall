package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrLibraryFileNotFound    = errors.New("library file not found")
	ErrLibraryFileNotSetAside = errors.New("library file is not set aside")
)

// SetLibraryFileAside records that a person wants the upload question for one
// file kept out of the way. It does not alter the file or any evidence about it.
func (q *Queries) SetLibraryFileAside(ctx context.Context, fileID uuid.UUID) error {
	var exists bool
	if err := q.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM library_files WHERE id = $1)
	`, fileID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrLibraryFileNotFound
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO library_file_set_asides (library_file_id, decided_by)
		VALUES ($1, 'user')
		ON CONFLICT (library_file_id) DO NOTHING
	`, fileID)
	return err
}

// ClearLibraryFileSetAside removes the person's suppression of the upload
// question. It leaves the file's matching and identity state unchanged.
func (q *Queries) ClearLibraryFileSetAside(ctx context.Context, fileID uuid.UUID) error {
	tag, err := q.db.Exec(ctx, `
		DELETE FROM library_file_set_asides WHERE library_file_id = $1
	`, fileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := q.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM library_files WHERE id = $1)
	`, fileID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrLibraryFileNotFound
	}
	return ErrLibraryFileNotSetAside
}

// LibraryFileSetAside reports whether a present file has a set-aside row.
// pgx.ErrNoRows is returned for a file that does not exist.
func (q *Queries) LibraryFileSetAside(ctx context.Context, fileID uuid.UUID) (bool, error) {
	var setAside, fileExists bool
	err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_file_set_asides
			WHERE library_file_id = $1
		), EXISTS (
			SELECT 1 FROM library_files WHERE id = $1
		)
	`, fileID).Scan(&setAside, &fileExists)
	if err != nil {
		return false, err
	}
	if !fileExists {
		return false, pgx.ErrNoRows
	}
	return setAside, nil
}
