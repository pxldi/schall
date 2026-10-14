-- +goose Up
-- Re-match the releases that ambiguous library files are proven to belong to.
--
-- A reconcile now lets an identity proven by audio or by a person link a file
-- whose tag MBID names another recording (internal/matching,
-- automaticCandidates). Files left ambiguous under the old rule stay so until
-- something reconciles them, and refresh_album_metadata is the job that does
-- (see 00117). ReconcileAlbum reaches every file resolved to one of the
-- release's tracks, so this queues it for each release an ambiguous file's
-- identity names. The reconcile decides per file; one whose identity rests on
-- tags stays ambiguous.
--
-- The set is the ambiguous files only, about 130 live, so the startup cost is
-- one filtered scan of library_files. Staggered two seconds apart as in 00044.
WITH ambiguous AS (
    SELECT identity.musicbrainz_recording_id AS recording_id
    FROM library_files
    JOIN library_file_identities identity ON identity.library_file_id = library_files.id
    WHERE library_files.missing_at IS NULL
      AND library_files.match_status = 'ambiguous'
      AND identity.musicbrainz_recording_id IS NOT NULL
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
-- The queued refreshes are left alone, for the reason 00044 gives.
SELECT 1;
