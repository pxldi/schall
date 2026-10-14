package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// AddSecondRow records that a library file also answers another MusicBrainz
// row of the same song, beside the row its identity is filed under (migration
// 00126). The caller has proven the two rows are one registered track; this
// writes the answer down. The row hangs off the file's current identity, so a
// new answer about the file drops it.
//
// A recording the identity already answers, directly or through a merge, adds
// nothing. A file with no identity has nothing to hang the row off and adds
// nothing either. The file is not reconciled here; the caller does that once
// its transaction commits.
func AddSecondRow(ctx context.Context, db DBTX, fileID, recordingID uuid.UUID) error {
	if recordingID == uuid.Nil {
		return nil
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO library_file_second_rows (identity_id, musicbrainz_recording_id)
		SELECT identity.id, $2
		FROM library_file_identities identity
		WHERE identity.library_file_id = $1
		  AND identity.musicbrainz_recording_id IS NOT NULL
		  AND $2::uuid NOT IN (SELECT recording_id_aliases(identity.musicbrainz_recording_id))
		ON CONFLICT DO NOTHING
	`, fileID, recordingID); err != nil {
		return fmt.Errorf("record file %s as also recording %s: %w", fileID, recordingID, err)
	}
	return nil
}

// SecondRows lists the other rows a library file answers besides its identity.
func SecondRows(ctx context.Context, db DBTX, fileID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, `
		SELECT second_row.musicbrainz_recording_id
		FROM library_file_second_rows second_row
		JOIN library_file_identities identity ON identity.id = second_row.identity_id
		WHERE identity.library_file_id = $1
		ORDER BY second_row.musicbrainz_recording_id
	`, fileID)
	if err != nil {
		return nil, fmt.Errorf("read the second rows of file %s: %w", fileID, err)
	}
	defer rows.Close()
	var found []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found = append(found, id)
	}
	return found, rows.Err()
}

// RecordSecondRow is AddSecondRow on the queries' own connection.
func (q *Queries) RecordSecondRow(ctx context.Context, fileID, recordingID uuid.UUID) error {
	return AddSecondRow(ctx, q.db, fileID, recordingID)
}
