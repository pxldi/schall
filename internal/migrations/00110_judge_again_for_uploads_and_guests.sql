-- +goose Up
-- Two rules changed under copies already decided about. An upload found under
-- the want's name, with the file's title, artist and length, now picks the want
-- out of an AcoustID cluster that names it beside another row, and admits a copy
-- whose cluster named only another row of the same registered track. One pass
-- applies them, through the same validation any copy gets. Stopped wants whose
-- rows differ only by a guest are looked at again at start, as every stopped
-- want is.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_copies_again', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
);

-- +goose Down
DELETE FROM jobs WHERE kind = 'judge_copies_again' AND status = 'queued';
