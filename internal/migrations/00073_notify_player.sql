-- +goose Up
-- The player is told the library changed from a dozen places: a download
-- import, an upload, a layout migration, a tagging pass, a duplicate deleted,
-- a transcode replacing a copy. Each used to tell it directly, which meant a
-- burst of overnight imports asked the player to rescan once per file. This
-- job is the one path all of them queue instead, and the unique index below
-- is what turns a burst into one telling: a second import queueing while the
-- first is still waiting finds a row already there and changes nothing.
--
-- The index does leave one narrow gap: a change that lands while the job is
-- already running (not merely queued) is swallowed the same way, and is only
-- announced by whichever change queues the next one. That is acceptable for a
-- courtesy — the player finds the library on its own schedule regardless.
CREATE UNIQUE INDEX jobs_active_notify_player_idx
    ON jobs (kind)
    WHERE kind = 'notify_player'
      AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_notify_player_idx;
