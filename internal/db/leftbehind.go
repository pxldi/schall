package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LeftBehindGroup is one recording the library holds extra copies of because
// an import placed them and then failed, or because an upgrade copy stayed
// beside a damaged file. Keeper stays; Extras go.
type LeftBehindGroup struct {
	RecordingID uuid.UUID
	KeeperID    uuid.UUID
	Extras      []uuid.UUID
}

// LeftBehindCopies lists the copies two failures left in the library between
// 2026-10-14 and 2026-10-16 (thread "Import scheitert an doppeltem Pfad").
//
// An import re-encoded a lossless copy, placed it, and then failed on
// downloads_local_path_idx, so no copy row links the file. Migration 00135 then
// retried three refused requests of one want at once. Each such file is found
// by the import's own 'transcoded' note naming it, written in the six hours
// before the scan made its row, on a request made for a want. Uploads and album
// imports write no such note for a want, so they are never listed.
//
// Only those files and upgrade copies are extras. A file that came any other
// way is never listed, damaged files are never kept or removed, and a group
// with nothing to remove is left out. The keeper is the file mapped to a track,
// then one a replacement kept, then a file that was there before, then the
// higher bit rate, then the newest.
func (q *Queries) LeftBehindCopies(ctx context.Context) ([]LeftBehindGroup, error) {
	rows, err := q.db.Query(ctx, `
		WITH upgrade_copies AS (
		    SELECT DISTINCT copies.library_file_id AS file_id,
		           targets.musicbrainz_recording_id AS recording_id
		    FROM acquisition_target_files copies
		    JOIN acquisition_targets targets ON targets.id = copies.acquisition_target_id
		    WHERE targets.origin = 'upgrade' AND copies.verdict = 'accepted'
		      AND copies.library_file_id IS NOT NULL
		      AND targets.musicbrainz_recording_id IS NOT NULL
		), placed AS (
		    SELECT DISTINCT ON (files.id)
		           files.id AS file_id, wants.musicbrainz_recording_id AS want_recording
		    FROM library_files files
		    JOIN import_transcodes notes ON notes.local_path = files.path
		    JOIN download_import_reviews reviews
		        ON reviews.kind = 'transcoded'
		       AND strpos(reviews.detail,
		                  ' was written as ' || regexp_replace(files.path, '^.*/', '') || ':') > 0
		       AND reviews.recorded_at BETWEEN files.created_at - interval '6 hours'
		                                   AND files.created_at
		    JOIN download_requests requests ON requests.id = reviews.download_request_id
		    JOIN acquisition_targets wants ON wants.id = requests.acquisition_target_id
		    WHERE files.missing_at IS NULL
		      AND files.created_at >= '2026-10-14' AND files.created_at < '2026-10-17'
		      AND NOT EXISTS (
		          SELECT 1 FROM acquisition_target_files linked
		          WHERE linked.library_file_id = files.id)
		    ORDER BY files.id, reviews.recorded_at DESC
		), extras AS (
		    SELECT file_id, recording_id FROM upgrade_copies
		    UNION
		    SELECT placed.file_id,
		           coalesce((SELECT i.musicbrainz_recording_id FROM library_file_identities i
		                     WHERE i.library_file_id = placed.file_id AND i.kind = 'external'),
		                    placed.want_recording)
		    FROM placed
		), recordings AS (
		    SELECT DISTINCT recording_id FROM extras WHERE recording_id IS NOT NULL
		), song_files AS (
		    SELECT DISTINCT ON (recordings.recording_id, files.id)
		           recordings.recording_id, files.id AS file_id, files.bit_rate_kbps,
		           files.created_at,
		           EXISTS (SELECT 1 FROM track_mappings m
		                   WHERE m.library_file_id = files.id) AS mapped,
		           EXISTS (SELECT 1 FROM library_file_removals r
		                   WHERE r.kept_library_file_id = files.id
		                     AND r.removed_by = 'the automatic quality upgrade') AS kept_before,
		           extras.file_id IS NOT NULL AS extra
		    FROM recordings
		    JOIN library_files files ON files.missing_at IS NULL AND files.decode_error IS NULL
		    LEFT JOIN extras ON extras.file_id = files.id
		                    AND extras.recording_id = recordings.recording_id
		    WHERE extras.file_id IS NOT NULL
		       OR EXISTS (SELECT 1 FROM library_file_identities i
		                  WHERE i.library_file_id = files.id AND i.kind = 'external'
		                    AND i.musicbrainz_recording_id = recordings.recording_id)
		), ranked AS (
		    SELECT song_files.*, row_number() OVER (
		        PARTITION BY recording_id
		        ORDER BY mapped DESC, kept_before DESC, extra ASC,
		                 coalesce(bit_rate_kbps, 0) DESC, created_at DESC, file_id
		    ) AS rank
		    FROM song_files
		)
		SELECT keeper.recording_id, keeper.file_id,
		       array_agg(extra.file_id ORDER BY extra.file_id)
		FROM ranked keeper
		JOIN ranked extra ON extra.recording_id = keeper.recording_id
		                 AND extra.rank > 1 AND extra.extra
		WHERE keeper.rank = 1
		GROUP BY keeper.recording_id, keeper.file_id
		ORDER BY keeper.recording_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list the copies failed imports left behind: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (LeftBehindGroup, error) {
		var group LeftBehindGroup
		err := row.Scan(&group.RecordingID, &group.KeeperID, &group.Extras)
		return group, err
	})
}
