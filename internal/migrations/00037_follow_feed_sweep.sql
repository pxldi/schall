-- +goose Up
-- Following an artist has meant one refresh, at the moment of the press, and
-- then nothing. A release published a week later reached the catalogue only if
-- somebody pressed refresh again, which is the manual step the follow was
-- supposed to replace — and it reached the wants never.
--
-- The feed is a sweep of its own rather than a rescheduled artist refresh. The
-- artists list reads each artist's refresh state from the newest
-- refresh_artist_metadata job, so a refresh queued a day ahead would leave every
-- followed artist reading as "refresh queued" forever. Keeping the clock in its
-- own kind leaves that display alone and keeps what the feed does separable
-- from what a person asked for.
--
-- One sweeper, enforced by the table rather than hoped for, exactly as the
-- acquisition sweep and the picture sweep are: a self-rescheduling job that
-- forked would double its own rate every pass.
CREATE UNIQUE INDEX jobs_follow_feed_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_follow_feed' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_follow_feed_sweep_idx;
