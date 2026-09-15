-- +goose Up
-- One picture-fetcher, enforced by the table rather than hoped for, exactly as
-- the transfer poller and the acquisition sweeper are.
--
-- It exists so that fetching artwork happens in one place, at a pace this
-- installation chose, instead of a page of a hundred releases each asking an
-- archive on the user's behalf as it renders. Two sweeps running would be two
-- installations' worth of requests arriving at somebody else's server at once,
-- from a job whose whole point is that the rate is deliberate.
CREATE UNIQUE INDEX jobs_cover_art_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_cover_art' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_cover_art_sweep_idx;
