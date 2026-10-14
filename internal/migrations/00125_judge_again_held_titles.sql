-- +goose Up
-- 00124 queued a judging pass for copies whose title names another song, but
-- the pass only offered copies of wants with a sample, so most were never
-- reached. The pass now offers them too. One more pass judges them.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_copies_again', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
);

-- A library file proven to be a recording the catalogue does not hold is now
-- unmatched rather than ambiguous. 00123 reached ambiguous files through their
-- identity's releases, which such a file has none of, so this reaches them
-- through the releases their tag names. ReconcileAlbum reaches every file
-- tagged with one of the release's recordings. The set is the ambiguous files
-- only, about 65 live.
WITH ambiguous AS (
    SELECT library_files.musicbrainz_recording_id AS recording_id
    FROM library_files
    WHERE library_files.missing_at IS NULL
      AND library_files.match_status = 'ambiguous'
      AND library_files.musicbrainz_recording_id IS NOT NULL
), recordings AS (
    SELECT DISTINCT aliased.recording_id
    FROM ambiguous
    CROSS JOIN LATERAL (
        SELECT recording_id_aliases(ambiguous.recording_id)
    ) AS aliased(recording_id)
    WHERE aliased.recording_id IS NOT NULL
), touched AS (
    SELECT DISTINCT tracks.album_id
    FROM recordings
    JOIN tracks ON tracks.musicbrainz_recording_id = recordings.recording_id
)
INSERT INTO jobs (kind, payload, run_after)
SELECT 'refresh_album_metadata',
       jsonb_build_object('albumId', albums.id::text),
       now() + (row_number() OVER (ORDER BY albums.created_at) * interval '2 seconds')
FROM albums
WHERE albums.id IN (SELECT album_id FROM touched)
  AND albums.musicbrainz_release_group_id IS NOT NULL
ON CONFLICT (kind, (payload->>'albumId'))
    WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
DO NOTHING;

-- +goose Down
DELETE FROM jobs WHERE kind = 'judge_copies_again' AND status = 'queued';
