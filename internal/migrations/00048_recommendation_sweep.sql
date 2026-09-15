-- +goose Up
-- A recommendation refresh schedules its own successor. Letting two copies
-- become live would make every pass double the requests sent to ListenBrainz
-- and MusicBrainz, so the queue is the authority that there is only one.
CREATE UNIQUE INDEX jobs_recommendation_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_recommendations' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_recommendation_sweep_idx;
