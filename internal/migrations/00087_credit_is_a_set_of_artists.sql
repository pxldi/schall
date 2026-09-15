-- +goose Up
-- An artist credit is compared as the set of artists it names, and a credit
-- that disagrees no longer voids a pair the audio already identified
-- (docs/decisions/0030). Both rules changed under copies already decided about
-- and wants already stopped, so this puts the ones they bear on back in motion.
--
-- Nothing here decides anything. The wants get a next look and the copies go
-- back through the same validation any copy gets, which is the only place a
-- verdict is ever reached.

-- The wants stopped because every copy of them disagreed on the artist credit
-- alone. The test is the one that stopped them: at least one copy refused that
-- way, and no copy refused any other way. A want stopped for something else --
-- a copy held for a person, an entry that fits two recordings -- is left where
-- it is, because nothing has changed for it.
UPDATE acquisition_targets
SET next_attempt_at = now(),
    summary = 'The copies already found are being checked again. An artist credit is now compared as the set of artists it names.',
    updated_at = now()
WHERE status = 'pending'
  AND next_attempt_at IS NULL
  AND EXISTS (
      SELECT 1 FROM acquisition_target_files files
      WHERE files.acquisition_target_id = acquisition_targets.id
        AND files.verdict = 'discarded_tags'
        AND files.evidence->'differs' = '["artist"]'::jsonb
  )
  AND NOT EXISTS (
      SELECT 1 FROM acquisition_target_files files
      WHERE files.acquisition_target_id = acquisition_targets.id
        AND NOT (
            files.verdict = 'discarded_tags'
            AND files.evidence->'differs' = '["artist"]'::jsonb
        )
  );

-- One pass over the copies those two rules refused. It is queued here rather
-- than left for somebody to press, because nobody has to be asked whether a
-- rule that changed should be applied to what it changed under. The pass runs
-- once and queues no replacement; a copy it cannot answer stays exactly as it
-- is.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_credit_refusals', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_credit_refusals' AND status IN ('queued', 'running')
);

-- +goose Down
-- The wants keep their next look and the copies keep whatever validation came
-- to. Taking a look away would stop a want for a reason that no longer exists,
-- and a verdict reached by the rules in force when it was reached is not this
-- migration's to undo.
DELETE FROM jobs WHERE kind = 'judge_credit_refusals' AND status = 'queued';
