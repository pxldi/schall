package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// LostWant is a settled want whose file the library no longer calls its
// recording.
type LostWant struct {
	Target AcquisitionTargetRow
	// HoldsCopy says the want holds an accepted copy that became a library
	// file. Such a want is judged again by the filing rules. One without was
	// settled on the library's word alone.
	HoldsCopy bool
}

// LostWants lists the settled wants whose recording no present file holds while
// the file they settled on is still there, oldest first.
//
// A want settles against a file because the library called that file the want's
// recording at the time. Resolution can answer again later, AcoustID most
// often, and file the file under another MusicBrainz row. Nothing looked at the
// want after that, so it stayed acquired over a file that answers another row,
// and its track read "Fetched once, file gone" over a file that is there.
//
// Held is read the way OwnedFileForRecording reads it, through merges too: an
// identity, a mapping onto a track, or a second row. A file gone from disk or
// deleted is the removals' business and not listed here.
//
// changedOnly keeps the wants holding a copy whose file the library answered
// again after they settled, or has no answer for at all. A want can settle on
// purpose against a file filed under another row (a clean edition held as the
// explicit one, ADR 0032), and without this every sweep would open it again. A
// want with no copy costs MusicBrainz lookups to judge, so it is left to the
// full list.
func (q *Queries) LostWants(ctx context.Context, limit int32, changedOnly bool) ([]LostWant, error) {
	rows, err := q.db.Query(ctx, `
		SELECT`+acquisitionTargetColumns+`,
		       EXISTS (
		           SELECT 1 FROM acquisition_target_files files
		           JOIN library_files copy_file ON copy_file.id = files.library_file_id
		           WHERE files.acquisition_target_id = acquisition_targets.id
		             AND files.verdict = 'accepted'
		             AND copy_file.missing_at IS NULL
		       )
		FROM acquisition_targets
		JOIN library_files settled_on
		    ON settled_on.id = acquisition_targets.acquired_library_file_id
		   AND settled_on.missing_at IS NULL
		LEFT JOIN library_file_identities settled_identity
		    ON settled_identity.library_file_id = settled_on.id
		WHERE acquisition_targets.status = 'acquired'
		  AND acquisition_targets.musicbrainz_recording_id IS NOT NULL
		  AND acquisition_targets.source IS NULL
		  AND (NOT $2::boolean
		       OR (EXISTS (
		               SELECT 1 FROM acquisition_target_files files
		               WHERE files.acquisition_target_id = acquisition_targets.id
		                 AND files.verdict = 'accepted'
		                 AND files.library_file_id = settled_on.id)
		           AND (settled_identity.id IS NULL
		                OR settled_identity.created_at > acquisition_targets.acquired_at)))
		  AND NOT EXISTS (
		      SELECT 1
		      FROM recording_id_aliases(acquisition_targets.musicbrainz_recording_id) wanted (id)
		      CROSS JOIN LATERAL (
		          SELECT identity.library_file_id
		          FROM library_file_identities identity
		          WHERE identity.musicbrainz_recording_id = wanted.id
		          UNION ALL
		          SELECT track_mappings.library_file_id
		          FROM tracks
		          JOIN track_mappings ON track_mappings.track_id = tracks.id
		          WHERE tracks.musicbrainz_recording_id = wanted.id
		          UNION ALL
		          SELECT identity.library_file_id
		          FROM library_file_second_rows second_row
		          JOIN library_file_identities identity ON identity.id = second_row.identity_id
		          WHERE second_row.musicbrainz_recording_id = wanted.id
		      ) holder
		      JOIN library_files held ON held.id = holder.library_file_id
		      WHERE held.missing_at IS NULL
		  )
		ORDER BY acquisition_targets.acquired_at, acquisition_targets.id
		LIMIT $1
	`, limit, changedOnly)
	if err != nil {
		return nil, fmt.Errorf("list the settled wants whose file is filed as another song: %w", err)
	}
	defer rows.Close()

	lost := make([]LostWant, 0)
	for rows.Next() {
		var want LostWant
		if err := scanAcquisitionTargetInto(rows, &want.Target, &want.HoldsCopy); err != nil {
			return nil, err
		}
		lost = append(lost, want)
	}
	return lost, rows.Err()
}

// ReopenLostWant puts a settled want back in the queue, due now, so the sweep
// judges it the way it judges a want arriving today. It reports false when the
// want is no longer acquired, which means somebody decided about it meanwhile.
func (q *Queries) ReopenLostWant(ctx context.Context, id uuid.UUID, summary string) (bool, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'pending',
		    acquired_at = NULL,
		    acquired_library_file_id = NULL,
		    next_attempt_at = now(),
		    summary = $2,
		    updated_at = now()
		WHERE id = $1 AND status = 'acquired'
	`, id, summary)
	if err != nil {
		return false, fmt.Errorf("reopen want %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// MoveSettledFile points a settled want at the file that holds its recording
// now. The want stays acquired; only the file it names changes.
func (q *Queries) MoveSettledFile(ctx context.Context, id, fileID uuid.UUID, summary string) error {
	if _, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET acquired_library_file_id = $2, summary = $3, updated_at = now()
		WHERE id = $1 AND status = 'acquired'
	`, id, fileID, summary); err != nil {
		return fmt.Errorf("point want %s at file %s: %w", id, fileID, err)
	}
	return nil
}
