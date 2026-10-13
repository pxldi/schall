-- +goose Up
-- Re-match the releases that files with contradicting witnesses touch.
--
-- A reconcile now voids a tag link when the file's proven identity names
-- another recording, and counts each other file by one recording when it asks
-- whether a track has a single claimant (internal/matching, automaticCandidates).
-- The scan reconciles only new or changed files, so the links and duplicate
-- states written under the old rule stay until something reconciles them.
--
-- There is no job that reconciles one file. refresh_album_metadata is the job
-- that re-matches a release (QueueAlbumRefresh), and its ReconcileAlbum reaches
-- every file mapped to one of the release's tracks, tagged with one of their
-- recordings, or resolved to one of them. So this queues that job once for each
-- release such a file touches:
--
--   * present files whose tag and proven identity name different recordings,
--   * present files left as duplicates, because a file that only contradicted
--     its tag may have been the contender that made them one.
--
-- Staggered two seconds apart and skipping albums with no release group, as in
-- 00044. Each job costs one MusicBrainz request.
WITH affected AS (
    SELECT library_files.id,
           library_files.musicbrainz_recording_id AS tag_recording_id,
           identity.musicbrainz_recording_id AS identity_recording_id
    FROM library_files
    LEFT JOIN library_file_identities identity
           ON identity.library_file_id = library_files.id
    WHERE library_files.missing_at IS NULL
      AND (
          library_files.match_status = 'duplicate'
          OR (library_files.musicbrainz_recording_id IS NOT NULL
              AND identity.musicbrainz_recording_id IS NOT NULL
              AND identity.musicbrainz_recording_id NOT IN (
                  SELECT recording_id_aliases(library_files.musicbrainz_recording_id)))
      )
), touched AS (
    SELECT tracks.album_id
    FROM affected
    JOIN tracks ON tracks.musicbrainz_recording_id IN (
            SELECT recording_id_aliases(affected.tag_recording_id))
        OR tracks.musicbrainz_recording_id IN (
            SELECT recording_id_aliases(affected.identity_recording_id))
    UNION
    SELECT tracks.album_id
    FROM affected
    JOIN track_mappings ON track_mappings.library_file_id = affected.id
    JOIN tracks ON tracks.id = track_mappings.track_id
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
-- The queued refreshes are left alone, for the reason 00044 gives: nothing in
-- a job row says this migration added it, and running one re-matches a release
-- the way it always did.
SELECT 1;
