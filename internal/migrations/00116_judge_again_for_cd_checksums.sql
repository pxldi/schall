-- +goose Up
-- A held lossless copy is now looked up in AccurateRip under the CDs
-- MusicBrainz lists its recording on, and a copy whose checksum is on file for
-- that track is admitted. One pass looks up the copies already held, through
-- the same validation any copy gets.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_copies_again', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
);

-- +goose Down
DELETE FROM jobs WHERE kind = 'judge_copies_again' AND status = 'queued';
