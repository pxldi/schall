-- +goose Up
-- A want MusicBrainz has no recording for may be searched when its anchor can
-- admit a copy: a Deezer preview fetched by the entry's ISRC, or the artist's
-- Topic upload (ADR 0037). The copy it finds is judged against that anchor
-- alone, and an admitted copy is written a source identity keyed by it.
--
-- The search keeps a schedule of its own. next_attempt_at on an unresolved want
-- is when MusicBrainz is asked again, and it keeps that meaning; next_search_at
-- is when Soulseek is. search_attempts is the rung the search is on, kept apart
-- from attempts so that a search coming back empty does not slow resolution,
-- which is the stronger way out of this state.
ALTER TABLE acquisition_targets
    ADD COLUMN next_search_at TIMESTAMPTZ,
    ADD COLUMN search_attempts INTEGER NOT NULL DEFAULT 0;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_search_only_unresolved
        CHECK (next_search_at IS NULL OR status = 'unresolved'),
    ADD CONSTRAINT acquisition_targets_search_attempts_nonnegative
        CHECK (search_attempts >= 0);

-- 'acquired' no longer needs a recording when the anchor that admitted the copy
-- can admit one. 'pending' and 'searching' still do.
ALTER TABLE acquisition_targets
    DROP CONSTRAINT acquisition_targets_identified_when_actionable;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_identified_when_actionable
        CHECK (status NOT IN ('pending', 'searching')
               OR musicbrainz_recording_id IS NOT NULL),
    ADD CONSTRAINT acquisition_targets_acquired_identified
        CHECK (status <> 'acquired'
               OR musicbrainz_recording_id IS NOT NULL
               OR anchor_source IN ('deezer', 'youtube-topic'));

CREATE INDEX acquisition_targets_search_due_idx
    ON acquisition_targets (next_search_at, created_at)
    WHERE status = 'unresolved' AND next_search_at IS NOT NULL;

-- The wants that already hold an admitting anchor were anchored before there
-- was a search to schedule, so nothing would ever make them due.
UPDATE acquisition_targets
   SET next_search_at = now(), updated_at = now()
 WHERE status = 'unresolved'
   AND anchor_fingerprint IS NOT NULL
   AND anchor_source IN ('deezer', 'youtube-topic');

-- +goose Down
-- An automatic source identity did not exist before this migration: 00066 made
-- a source identity a person's decision. Its files go back to 'pending', which
-- is what 00066's own Down does, and a want acquired without a recording goes
-- back to waiting to be resolved. The files stay in the library.
DELETE FROM library_file_identities WHERE kind = 'source' AND NOT is_manual;
UPDATE library_files
SET resolution_status = 'pending',
    resolution_summary = NULL,
    resolution_attempted_at = NULL
WHERE resolution_status = 'source'
  AND NOT EXISTS (
      SELECT 1 FROM library_file_identities
      WHERE library_file_identities.library_file_id = library_files.id
  );
UPDATE acquisition_targets
SET status = 'unresolved',
    acquired_at = NULL,
    acquired_library_file_id = NULL,
    next_attempt_at = now(),
    updated_at = now()
WHERE status = 'acquired' AND musicbrainz_recording_id IS NULL;

DROP INDEX IF EXISTS acquisition_targets_search_due_idx;

ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_acquired_identified,
    DROP CONSTRAINT IF EXISTS acquisition_targets_identified_when_actionable,
    DROP CONSTRAINT IF EXISTS acquisition_targets_search_attempts_nonnegative,
    DROP CONSTRAINT IF EXISTS acquisition_targets_search_only_unresolved;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_identified_when_actionable
        CHECK (status NOT IN ('pending', 'searching', 'acquired')
               OR musicbrainz_recording_id IS NOT NULL);

ALTER TABLE acquisition_targets
    DROP COLUMN IF EXISTS search_attempts,
    DROP COLUMN IF EXISTS next_search_at;
