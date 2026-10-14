-- +goose Up
-- The other MusicBrainz rows of the same song that a library file answers.
--
-- MusicBrainz often holds one song twice or more, on a single and on the album
-- it was lifted from, and never merges the rows. Schall reads such rows as one
-- registered track (ADR 0025, 0031) and keeps one file. That file is filed under
-- one row, so the tracks carrying the other rows lost it. On 2026-09-05 the
-- duplicate sweep deleted the traence copy of "Nichts hittet mehr" because the
-- single's copy was then filed as the same recording; twenty minutes later the
-- single's want re-filed that copy as the single's row, and traence track 7 was
-- left with nothing.
--
-- A row here says the file's identity also answers this recording. It hangs off
-- the identity, so a new answer about what the file is drops it with the old
-- one. The matcher maps the file to these rows' tracks beside its own.
CREATE TABLE library_file_second_rows (
    identity_id UUID NOT NULL REFERENCES library_file_identities (id) ON DELETE CASCADE,
    musicbrainz_recording_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (identity_id, musicbrainz_recording_id)
);

CREATE INDEX library_file_second_rows_recording_idx
    ON library_file_second_rows (musicbrainz_recording_id);

-- The files already re-filed this way. Re-filing did not record the row it
-- left, but a duplicate removal that kept the file does: it names the recording
-- the kept file was a copy of when the sweep ran. That is the row the later
-- re-filing moved the file off. 8 files live on 2026-10-14; the removals table
-- and the same-registration identities are both small.
INSERT INTO library_file_second_rows (identity_id, musicbrainz_recording_id)
SELECT DISTINCT ON (identity.id) identity.id, removal.musicbrainz_recording_id
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
ORDER BY identity.id, removal.removed_at DESC;

-- Match those files again. ReconcileAlbum reaches every file resolved to one of
-- the release's tracks, so this queues it for the releases of the rows the
-- files are filed under, staggered as in 00123.
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
DROP TABLE IF EXISTS library_file_second_rows;
