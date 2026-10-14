-- +goose Up
-- The MusicBrainz row a library file was filed under before a want's copy
-- re-filed it under a second row of the same registered track (ADR 0025, 0031).
--
-- Re-filing moved the file off the first row, so every catalogue track carrying
-- that row lost the file. On 2026-09-05 the duplicate sweep deleted the traence
-- copy of "Nichts hittet mehr" because the single's copy was then filed as the
-- same recording; twenty minutes later the single's want re-filed that copy as
-- the single's row, and traence track 7 was left with nothing. Schall had just
-- read the two rows as one registered track, so the file answers both. The
-- matcher reads this column as a second row the file answers.
ALTER TABLE library_file_identities ADD COLUMN second_row_recording_id UUID;

-- The files already re-filed this way. Re-filing does not record the row it
-- left, but a duplicate removal that kept the file does: it names the recording
-- the kept file was a copy of when the sweep ran. That is the row the later
-- re-filing moved the file off. 8 files live on 2026-10-14; the removals table
-- and the same-registration identities are both small.
WITH refiled AS (
    SELECT DISTINCT ON (identity.id)
           identity.id, removal.musicbrainz_recording_id AS second_row
    FROM library_file_identities identity
    JOIN library_files ON library_files.id = identity.library_file_id
    JOIN library_file_removals removal ON removal.kept_library_file_id = identity.library_file_id
    WHERE identity.method = 'same-registration'
      AND NOT identity.is_manual
      AND identity.musicbrainz_recording_id IS NOT NULL
      AND library_files.missing_at IS NULL
      AND removal.musicbrainz_recording_id IS NOT NULL
      AND removal.musicbrainz_recording_id NOT IN (
          SELECT recording_id_aliases(identity.musicbrainz_recording_id))
      AND removal.removed_at <= identity.created_at
    ORDER BY identity.id, removal.removed_at DESC
)
UPDATE library_file_identities
SET second_row_recording_id = refiled.second_row
FROM refiled
WHERE library_file_identities.id = refiled.id;

-- Match those files again. ReconcileAlbum reaches every file resolved to one of
-- the release's tracks, so this queues it for the releases of the rows the
-- files are filed under, staggered as in 00123.
WITH touched AS (
    SELECT DISTINCT tracks.album_id
    FROM library_file_identities identity
    JOIN tracks ON tracks.musicbrainz_recording_id = identity.musicbrainz_recording_id
    WHERE identity.second_row_recording_id IS NOT NULL
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
ALTER TABLE library_file_identities DROP COLUMN second_row_recording_id;
