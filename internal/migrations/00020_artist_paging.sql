-- +goose Up
-- Paging the artists list bounds what the interface asks for, but on its own it
-- does not bound what the database does. The list sorts on an expression —
-- followed artists first, then everyone the library holds — and nothing indexed
-- that expression, so Postgres read every artist, ran the per-artist refresh
-- lookup for every one of them, sorted the lot, and threw away all but the
-- page. On 20k artists that was ~900ms per page, and the page polls itself
-- while a refresh is running.
--
-- Indexing the sort order exactly as the list states it lets the plan walk the
-- artists in order and stop at the page boundary, which is what makes LIMIT
-- mean something here: the same page measured ~1.6ms.
CREATE INDEX artists_list_order_idx
    ON artists ((followed_at IS NULL), sort_name, id);

-- The other half of the same page. Each listed artist carries the status of
-- their newest metadata refresh, looked up by artist ID inside the job payload,
-- and no index reached it — so every artist on the page cost a full scan of
-- `jobs`, a table that grows with every scan, refresh and source search rather
-- than with the catalogue. With 100k jobs behind it a 50-artist page spent
-- ~240ms on those lookups alone; with this index, ~0.3ms.
--
-- Not unique, unlike the other job indexes here: refreshes accumulate per
-- artist on purpose, and the lookup wants the newest of them.
CREATE INDEX jobs_artist_refresh_idx
    ON jobs ((payload->>'artistId'), created_at DESC)
    WHERE kind = 'refresh_artist_metadata';

-- +goose Down
DROP INDEX IF EXISTS jobs_artist_refresh_idx;
DROP INDEX IF EXISTS artists_list_order_idx;
