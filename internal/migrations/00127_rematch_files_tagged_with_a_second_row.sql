-- +goose Up
-- Match again every file that answers a second row of its song.
--
-- After 00126, a file whose tag named its second row read as a contradiction
-- between tag and identity, went ambiguous and lost every track (4 files live
-- on 2026-10-14: YUNGXANHOE, The Joy, Looking for Trouble, Power). The matcher
-- now reads that tag as agreeing with the identity. ReconcileAlbum reaches every
-- file resolved to one of the release's tracks, so this queues it for the
-- releases those files are filed under, staggered as in 00123. The set is the
-- files with a second row, 8 live.
WITH touched AS (
    SELECT DISTINCT tracks.album_id
    FROM library_file_second_rows second_row
    JOIN library_file_identities identity ON identity.id = second_row.identity_id
    JOIN tracks ON tracks.musicbrainz_recording_id = identity.musicbrainz_recording_id
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
