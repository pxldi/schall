-- +goose Up
-- A copy whose title names another song is now refused when no audio check
-- spoke for it. One pass puts the copies already held through the same
-- validation, so those questions leave Review and their wants search on.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_copies_again', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
);

-- +goose Down
DELETE FROM jobs WHERE kind = 'judge_copies_again' AND status = 'queued';
