-- +goose Up
-- A want in the review queue stops for two different reasons and the queue
-- shows them as one. Either the evidence is in and a person has to choose, or
-- nothing could be asked yet and something may still arrive: MusicBrainz was
-- unreachable, or the library has not said what the file it holds is. The
-- second kind is not a question, and a person working through the queue spends
-- their attention on it anyway. On 2026-09-08 that was 38 wants waiting on a
-- MusicBrainz timeout and 64 waiting on a library identity, out of 733.
--
-- These three columns say which kind a want is, and how much automatic patience
-- is left. Nothing here decides anything about the music: a want that is waiting
-- has its copies put back through the same grader over the same evidence, and
-- the answer it reaches is the answer a copy arriving today would reach.
ALTER TABLE acquisition_targets
    -- What the want is waiting for, and NULL when it is waiting for a person.
    -- 'musicbrainz' is a copy whose audio named another recording that nobody
    -- could ask about; 'library_identity' is a copy that is already a library
    -- file the library calls something else.
    ADD COLUMN recheck_reason TEXT,
    -- When the next automatic re-judge is due. It mirrors the run_after of the
    -- judge_want_copies job queued for the want, so a person reading the row can
    -- see when it will be looked at. NULL for a want whose recheck is caused by
    -- a fact arriving rather than by a clock, and for one waiting on a person.
    ADD COLUMN recheck_after TIMESTAMPTZ,
    -- How many automatic re-judges have been spent. The schedule runs out: a
    -- want that has asked MusicBrainz four times over two days and still cannot
    -- be told is a want only a person can answer.
    ADD COLUMN recheck_attempts INTEGER NOT NULL DEFAULT 0;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_recheck_reason_valid
        CHECK (recheck_reason IS NULL
               OR recheck_reason IN ('musicbrainz', 'library_identity')),
    -- A time to look again presupposes something to look again for.
    ADD CONSTRAINT acquisition_targets_recheck_scheduled_for_a_reason
        CHECK (recheck_after IS NULL OR recheck_reason IS NOT NULL),
    ADD CONSTRAINT acquisition_targets_recheck_attempts_nonnegative
        CHECK (recheck_attempts >= 0);

-- Read by the review queue's payload and by anybody asking what is waiting.
-- Partial, because almost every want is waiting for nothing.
CREATE INDEX acquisition_targets_recheck_reason_idx
    ON acquisition_targets (recheck_reason)
    WHERE recheck_reason IS NOT NULL;

-- Every settled library identity now asks whether a want stopped on that file,
-- and whether a want's copies can be judged against it. Both read the accepted
-- copies that became library files, which is a few in a table of thousands.
CREATE INDEX acquisition_target_files_filed_copy_idx
    ON acquisition_target_files (library_file_id)
    WHERE verdict = 'accepted' AND library_file_id IS NOT NULL;

-- The wants already in this state. Neither backfill changes a verdict; both say
-- what the row has been saying in its summary all along.
--
-- The MusicBrainz kind is due now: these have been re-judged every thirty
-- minutes since migration 00089, without a cap, and the schedule below is what
-- bounds them from here.
UPDATE acquisition_targets
SET recheck_reason = 'musicbrainz', recheck_after = now(), updated_at = now()
WHERE status = 'pending'
  AND EXISTS (
      SELECT 1 FROM acquisition_target_files files
      WHERE files.acquisition_target_id = acquisition_targets.id
        AND files.verdict = 'held'
        AND files.summary LIKE 'The audio was identified as another recording, and MusicBrainz could not be asked%'
  );

-- And the look itself. recheck_after is what the row says about the schedule;
-- the queued job is what makes the look happen, and nothing reads recheck_after
-- to start work. Migration 00089 queued a round of these, but a want whose job
-- has since been completed or failed away would sit at a due time nothing acts
-- on. Deduplicated against a job already waiting for that want, so a row still
-- carrying 00089's job is left with the one it has.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_want_copies',
       jsonb_build_object('acquisition_target_id', waiting.id),
       3,
       now()
FROM (SELECT id FROM acquisition_targets
      WHERE recheck_reason = 'musicbrainz') waiting
WHERE NOT EXISTS (
      SELECT 1 FROM jobs
      WHERE kind = 'judge_want_copies'
        AND status IN ('queued', 'running')
        AND payload->>'acquisition_target_id' = waiting.id::text
  );

-- The library kind waits for a fact rather than a clock: the identity of the
-- file it holds changing, which already gives the want its next look back.
UPDATE acquisition_targets
SET recheck_reason = 'library_identity', updated_at = now()
WHERE status = 'pending'
  AND next_attempt_at IS NULL
  AND recheck_reason IS NULL
  AND EXISTS (
      SELECT 1 FROM acquisition_target_files files
      WHERE files.acquisition_target_id = acquisition_targets.id
        AND files.verdict = 'accepted'
        AND files.library_file_id IS NOT NULL
  );

-- +goose Down
-- Before the column goes, because this reads it.
DELETE FROM jobs
WHERE kind = 'judge_want_copies'
  AND status = 'queued'
  AND payload->>'acquisition_target_id' IN (
      SELECT id::text FROM acquisition_targets WHERE recheck_reason = 'musicbrainz'
  );

DROP INDEX IF EXISTS acquisition_targets_recheck_reason_idx;
DROP INDEX IF EXISTS acquisition_target_files_filed_copy_idx;

ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_recheck_reason_valid,
    DROP CONSTRAINT IF EXISTS acquisition_targets_recheck_scheduled_for_a_reason,
    DROP CONSTRAINT IF EXISTS acquisition_targets_recheck_attempts_nonnegative;

ALTER TABLE acquisition_targets
    DROP COLUMN IF EXISTS recheck_reason,
    DROP COLUMN IF EXISTS recheck_after,
    DROP COLUMN IF EXISTS recheck_attempts;
