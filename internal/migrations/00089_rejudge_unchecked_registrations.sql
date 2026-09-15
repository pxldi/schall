-- +goose Up
-- A failed MusicBrainz lookup left these copies as questions. Ask again after
-- the outage has had time to clear, and let the same import rules answer them.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_want_copies',
       jsonb_build_object('acquisition_target_id', files.acquisition_target_id),
       3,
       now()
FROM (SELECT DISTINCT acquisition_target_id
      FROM acquisition_target_files
      WHERE verdict = 'held'
        AND summary LIKE 'The audio was identified as another recording, and MusicBrainz could not be asked%') files
WHERE NOT EXISTS (
      SELECT 1 FROM jobs
      WHERE kind = 'judge_want_copies'
        AND status IN ('queued', 'running')
        AND payload->>'acquisition_target_id' = files.acquisition_target_id::text
  );

-- +goose Down
DELETE FROM jobs
WHERE kind = 'judge_want_copies'
  AND status = 'queued'
  AND payload->>'acquisition_target_id' IN (
      SELECT files.acquisition_target_id::text
      FROM acquisition_target_files files
      WHERE files.verdict = 'held'
        AND files.summary LIKE 'The audio was identified as another recording, and MusicBrainz could not be asked%'
  );
