-- +goose Up
-- Two MusicBrainz rows that name the same artists, hold the same title and run
-- the same length are one registered track, and the audio naming the other of
-- them no longer refuses a copy (docs/decisions/0031). 1244 copies on 83 wants
-- were discarded under the old rule, with the audio as the only thing that
-- disagreed.
--
-- One pass over the copies already decided about. It is queued here rather than
-- left for somebody to press, because nobody has to be asked whether a rule that
-- changed should be applied to what it changed under. The pass reaches the wants
-- that hold an anchor, are still looking, and have no accepted copy
-- (CopiesToJudgeAgain); every other copy stays exactly as it is until a want is
-- anchored, which queues the same pass for that want on its own.
--
-- Nothing here decides anything. The copies go back through the same validation
-- any copy gets, which is the only place a verdict is ever reached.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_copies_again', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
);

-- +goose Down
-- The copies keep whatever validation came to. A verdict reached by the rules in
-- force when it was reached is not this migration's to undo.
DELETE FROM jobs WHERE kind = 'judge_copies_again' AND status = 'queued';
