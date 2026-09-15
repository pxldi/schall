-- +goose Up
-- A judging pass that failed on a copy left its request at 'validating', with
-- nothing to retry it. Such a request reads as a copy still being checked: the
-- review queue hides the copy and OpenTargetRequest keeps the want from
-- fetching another. Put each back where its recorded verdict says it belongs.
UPDATE download_requests requests
SET import_status = CASE WHEN copies.verdict = 'held' THEN 'needs_review' ELSE 'discarded' END,
    updated_at = now()
FROM acquisition_target_files copies
WHERE copies.download_request_id = requests.id
  AND copies.verdict IN ('held', 'discarded_audio', 'discarded_tags', 'undelivered')
  AND requests.status = 'completed'
  AND requests.import_status = 'validating';

-- The pass that measures a want's held copies against an anchor that has just
-- landed was not being queued: its query failed on a parameter Postgres could
-- not type. Queue it once for every open want that holds an anchor and a held
-- copy never measured against it.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_want_copies',
       jsonb_build_object('acquisition_target_id', wants.id),
       3,
       now()
FROM (SELECT DISTINCT targets.id
      FROM acquisition_targets targets
      JOIN acquisition_target_files copies ON copies.acquisition_target_id = targets.id
      WHERE targets.status = 'pending'
        AND targets.anchor_fingerprint IS NOT NULL
        AND copies.verdict = 'held'
        AND copies.decided_by = 'schall'
        AND coalesce(copies.evidence->'anchor'->>'measured', '') <> 'true') wants
WHERE NOT EXISTS (
      SELECT 1 FROM jobs
      WHERE kind = 'judge_want_copies'
        AND status IN ('queued', 'running')
        AND payload->>'acquisition_target_id' = wants.id::text
  );

-- +goose Down
DELETE FROM jobs
WHERE kind = 'judge_want_copies'
  AND status = 'queued'
  AND created_at >= now() - interval '1 minute';
