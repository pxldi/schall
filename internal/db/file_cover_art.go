package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// FileCoverArtRow is the picture held for one file.
//
// Cover art is normally held per release, keyed by the album it belongs to. A
// file Schall holds by its address on another service belongs to no release, so
// its picture is held against the file itself. A row whose Image is empty
// records "asked, and there was none" — the same bargain release_cover_art
// makes, so that the question is not asked again for ever.
type FileCoverArtRow struct {
	Image       []byte
	ContentType string
	Source      string
	FetchedAt   pgtype.Timestamptz
}

// FileCoverArt reads the picture held for one file. A file nobody has fetched a
// picture for answers with an empty row and no error: no picture is not a
// failure.
func (q *Queries) FileCoverArt(ctx context.Context, fileID uuid.UUID) (FileCoverArtRow, error) {
	var row FileCoverArtRow
	err := q.db.QueryRow(ctx, `
		SELECT coalesce(image, ''::bytea), content_type, source, fetched_at
		FROM file_cover_art
		WHERE library_file_id = $1
	`, fileID).Scan(&row.Image, &row.ContentType, &row.Source, &row.FetchedAt)
	if err != nil {
		return FileCoverArtRow{}, err
	}
	return row, nil
}

// SourceFileToPictureRow is one file whose picture is not yet beside it on
// disc, as far as this pass can tell before it looks at the folder.
type SourceFileToPictureRow struct {
	ID     uuid.UUID
	Folder string
}

// SourceFilesToPicture lists the files Schall holds a picture for that are not
// part of any release.
//
// A player reads the picture out of the folder it is playing from. The release
// pass cannot reach these files: they have no album row, so there is no release
// folder for them to be listed under. The folder here is the one the file
// itself is in.
func (q *Queries) SourceFilesToPicture(ctx context.Context) ([]SourceFileToPictureRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT library_files.id,
		       regexp_replace(library_files.path, '/[^/]+$', '') AS folder
		FROM file_cover_art
		JOIN library_files ON library_files.id = file_cover_art.library_file_id
		WHERE file_cover_art.image IS NOT NULL
		  AND library_files.missing_at IS NULL
		ORDER BY library_files.path
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]SourceFileToPictureRow, 0)
	for rows.Next() {
		var row SourceFileToPictureRow
		if err := rows.Scan(&row.ID, &row.Folder); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
