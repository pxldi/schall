-- +goose Up
-- Finding the last finished job of one kind, without reading the whole queue.
--
-- `jobs` is the queue every background job in Schall runs from. A row is written
-- when work is asked for and stays there after the work is done: nothing deletes
-- finished rows, so the table only grows with the age of the installation. The
-- sweep that reads a ListenBrainz account alone adds about 52 rows a day.
--
-- The Recommended view says beside its list when that sweep last refreshed it,
-- and whether the last attempt succeeded. That fact has no table of its own. It
-- is read out of this queue by finding the last finished `sweep_recommendations`
-- row (`RecommendationSweepState`), which is what let the feature ship without a
-- migration. The read runs on every fetch of the list, and the view asks again
-- every 15 seconds while any work is active.
--
-- Nothing indexed that question, so the read walked the whole table. Measured on
-- a database filled with a real job history: at one year, 199,001 rows, 12.2 ms
-- and 3,438 buffers to return one row; at five years, 995,001 rows, 32.8 ms. The
-- cost follows the age of the installation rather than the size of the
-- collection, which is what makes it worth an index rather than a wait
-- (issue #341).
--
-- Status is in the predicate and not in the key on purpose. The read asks for
-- three statuses at once — a pass can end completed, failed or cancelled, and
-- all three are finished — and an index keyed (kind, status, completed_at)
-- cannot deliver rows in completed_at order across three values of the middle
-- column. PostgreSQL reads every matching entry and sorts: 35.0 ms at five
-- years, no better than the scan it replaces. Keyed this way the ordering
-- survives and the read is one descending walk that stops at the first row:
-- 0.145 ms, 4 buffers, and flat as the table grows.
--
-- The kind is the leading column so that this answers the same question about
-- any job kind, not only the sweep.
--
-- This does not make the queue forget. A retention rule for finished jobs is a
-- separate decision and a larger one; it would not replace this index, because a
-- queue trimmed to 90 days still holds thousands of finished sweeps to walk.
CREATE INDEX IF NOT EXISTS jobs_last_finished_idx
    ON jobs (kind, completed_at DESC)
    WHERE status IN ('completed', 'failed', 'cancelled');

-- +goose Down
DROP INDEX IF EXISTS jobs_last_finished_idx;
