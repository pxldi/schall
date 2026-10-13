-- +goose Up
-- A copy's measured length now picks the want out of an AcoustID cluster that
-- names it beside other rows, when every other row runs more than five seconds
-- longer or shorter. One pass applies it to copies already held, through the
-- same validation any copy gets.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_copies_again', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
);

-- +goose Down
DELETE FROM jobs WHERE kind = 'judge_copies_again' AND status = 'queued';
