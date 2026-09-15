-- +goose Up
-- Reclassify the ambiguous files that are really the one recording twice.
--
-- The matcher used to call a file ambiguous whenever its only candidate track
-- was already claimed by another file, whatever that other file turned out to
-- be. When the two files both resolve to the one MusicBrainz recording — the
-- same audio, imported once under each of two releases — that is not a choice
-- between tracks. It is the duplicate question, and it now sets match_status
-- to 'duplicate' instead of 'ambiguous' going forward (internal/matching,
-- heldBySameRecording).
--
-- New files reach that through a reconcile, and this backfills the ones that
-- were already sitting in the queue under the old rule. A file counts here
-- only when another present file resolves, through its own
-- musicbrainz_recording_id or a resolved library_file_identities row, to a
-- recording some catalogued track actually carries — the same test a reconcile
-- runs, read once against what the database already holds rather than
-- re-derived through a person's action.
WITH known AS (
    SELECT library_files.id AS file_id,
           coalesce(
               library_files.musicbrainz_recording_id,
               (SELECT identity.musicbrainz_recording_id
                FROM library_file_identities identity
                WHERE identity.library_file_id = library_files.id)
           ) AS recording_id
    FROM library_files
    WHERE library_files.missing_at IS NULL
      AND library_files.match_status = 'ambiguous'
), contested AS (
    SELECT known.file_id,
           (SELECT count(*) FROM tracks
            WHERE tracks.musicbrainz_recording_id
                  IN (SELECT recording_id_aliases(known.recording_id))
           ) AS track_count,
           -- Every file, this one included, whose own identity resolves to the
           -- recording. More than one is another file naming it too.
           (SELECT count(*) FROM library_files other
            LEFT JOIN library_file_identities other_identity
                   ON other_identity.library_file_id = other.id
            WHERE other.missing_at IS NULL
              AND (other.musicbrainz_recording_id
                       IN (SELECT recording_id_aliases(known.recording_id))
                   OR other_identity.musicbrainz_recording_id
                       IN (SELECT recording_id_aliases(known.recording_id)))
           ) AS file_count
    FROM known
    WHERE known.recording_id IS NOT NULL
)
UPDATE library_files
SET match_status = 'duplicate',
    match_candidate_count = greatest(contested.track_count, contested.file_count),
    updated_at = now()
FROM contested
WHERE library_files.id = contested.file_id
  -- The recording names a real track, so this file did reach 'ambiguous'
  -- through an identifier rather than through tags.
  AND contested.track_count > 0
  -- Somebody else answers to the same recording too.
  AND contested.file_count > 1;

-- +goose Down
-- There is nothing to restore. A file this moved back to 'ambiguous' would
-- immediately be found by the next reconcile and moved again, and the person
-- who resolved a duplicate in between made a decision this cannot see, so a
-- blind revert would put a duplicate exactly where it does not belong.
SELECT 1;
