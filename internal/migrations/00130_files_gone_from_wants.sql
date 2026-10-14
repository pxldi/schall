-- +goose Up
-- A library file deleted as a surplus copy of a recording left two kinds of row
-- pointing at nothing. Deleting now carries both (db.CarryToKeptFile,
-- db.RecordFileRemoved); this repairs what earlier deletions left. Its SELECT
-- equivalent took 1.7 s read-only on live data (2026-10-14): 26 copies, 66 wants
-- re-pointed, 13 looking again.
--
-- First, accepted copies whose file went without being written down. The want
-- had settled on another copy, so this one never recorded its file, and the
-- deletion could not find it. Without the stamp a pending want reads such a
-- copy as still on its way into the library. A copy counts as gone when its
-- transfer no longer leads to a library file, by link or by path, a day after
-- it was accepted: a scan takes minutes, and a want gives up waiting after two
-- hours.
WITH gone AS (
    UPDATE acquisition_target_files copies
    SET file_removed_at = now(), updated_at = now()
    WHERE copies.verdict = 'accepted'
      AND copies.file_removed_at IS NULL
      AND copies.library_file_id IS NULL
      AND copies.download_request_id IS NOT NULL
      AND copies.decided_at < now() - interval '1 day'
      AND NOT EXISTS (
          SELECT 1 FROM downloads
          LEFT JOIN library_files files ON files.path = downloads.local_path
          WHERE downloads.download_request_id = copies.download_request_id
            AND downloads.remote_path = copies.remote_path
            AND (downloads.library_file_id IS NOT NULL OR files.id IS NOT NULL)
      )
    RETURNING copies.acquisition_target_id
)
-- A pending want that stopped on such a copy gets its next look back, as
-- db.RearmWantsHoldingFile gives one when a file is deleted today.
UPDATE acquisition_targets
SET next_attempt_at = now(), summary = 'Your copy was deleted. Looking for it again.',
    recheck_reason = NULL, recheck_after = NULL, recheck_attempts = 0,
    updated_at = now()
WHERE status = 'pending'
  AND next_attempt_at IS NULL
  AND id IN (SELECT acquisition_target_id FROM gone);

-- Second, acquired wants whose file is gone. Where the library still holds the
-- recording, the want points at that file: the same present file, read by the
-- same rule, that db.OwnedFileForRecording settles a want against.
WITH owned AS (
    SELECT targets.id AS target_id, (
        SELECT library_files.id
        FROM library_files
        LEFT JOIN library_file_identities
            ON library_file_identities.library_file_id = library_files.id
        LEFT JOIN track_mappings ON track_mappings.library_file_id = library_files.id
        LEFT JOIN tracks ON tracks.id = track_mappings.track_id
        WHERE library_files.missing_at IS NULL
          AND (library_file_identities.musicbrainz_recording_id = targets.musicbrainz_recording_id
               OR tracks.musicbrainz_recording_id = targets.musicbrainz_recording_id
               OR EXISTS (
                   SELECT 1 FROM library_file_second_rows second_row
                   WHERE second_row.identity_id = library_file_identities.id
                     AND second_row.musicbrainz_recording_id = targets.musicbrainz_recording_id))
        ORDER BY library_files.created_at, library_files.id
        LIMIT 1
    ) AS file_id
    FROM acquisition_targets targets
    WHERE targets.status = 'acquired'
      AND targets.acquired_library_file_id IS NULL
      AND targets.musicbrainz_recording_id IS NOT NULL
)
UPDATE acquisition_targets
SET acquired_library_file_id = owned.file_id, updated_at = now()
FROM owned
WHERE acquisition_targets.id = owned.target_id
  AND owned.file_id IS NOT NULL;

-- Where nothing in the library is proven to be the recording, the music is
-- gone and the want looks for it again, which is what "Find sources" on the
-- release page asked the person to do by hand. A song on weekly trial is left
-- alone: the weekly run settles its own wants.
UPDATE acquisition_targets
SET status = 'pending', acquired_at = NULL,
    next_attempt_at = now(), summary = 'Your copy was deleted. Looking for it again.',
    recheck_reason = NULL, recheck_after = NULL, recheck_attempts = 0,
    updated_at = now()
WHERE status = 'acquired'
  AND acquired_library_file_id IS NULL
  AND musicbrainz_recording_id IS NOT NULL
  AND source IS NULL
  AND NOT weekly_only;

-- +goose Down
-- Nothing to undo: every row changed here pointed at a file that no longer
-- exists.
SELECT 1;
