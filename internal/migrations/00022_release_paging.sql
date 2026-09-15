-- +goose Up
-- The releases browser is now scoped, searched, filtered, ordered and paged on
-- the server. Paging bounds what the interface asks for; these indexes are what
-- make the database's half of it bounded too.
--
-- Measured against a synthetic catalogue of 60k releases across 20k artists,
-- with 528k tracks, 191k mappings, 300k library files and 145k jobs. Before is
-- the same paged query with none of these indexes present; the browser's own
-- first page is the "followed or owned" scope in artist order.
--
--   first page, artist order       262ms -> 79ms   (plus a 126ms count)
--   first page, year order         311ms -> 28ms
--   first page, title order desc   316ms -> 28ms
--   first page, followed only      164ms -> 28ms   (plus a 3ms count)
--   first page, status "partial"   73.5s -> 83ms   (plus a 1.20s count)
--
-- For comparison, what the browser did until now — fetch every release in one
-- unpaged query and filter in the client — measured ~2790ms for the query
-- alone.

-- The orders the list can be asked for, one index each, spelled as the ORDER BY
-- spells them. Ascending is what these indexes hold: an unknown release date
-- sorts last, which is what ASC means by default. Descending is the same index
-- read backwards, which puts unknown first — worth saying out loud, because a
-- second index with NULLS LAST on the way down is the only way to change it and
-- it is not worth one.
--
-- albums.id and artists.id are the last key of each: without a tiebreak two
-- releases with the same title would swap places between pages and one of them
-- would never be seen.
CREATE INDEX albums_release_order_idx ON albums (release_date, title, id);
CREATE INDEX albums_title_order_idx ON albums (title, id);

-- Ordering by artist is the browser's default and crosses tables: the key is on
-- artists, the rows are albums. Walking artists in name order and taking each
-- one's releases as they come is what lets the page stop at its own end, and
-- this is the index that walk needs.
CREATE INDEX artists_name_order_idx ON artists (name, id);

-- Every listed release carries the state of its metadata refresh, looked up by
-- album ID inside the job payload, and no index reached it. jobs_active_album_-
-- refresh_idx (00002) does not: it is partial on status IN ('queued','running')
-- because it exists to stop a second refresh being queued, while this lookup
-- has to see the failed and completed ones too, which is the whole point of it.
--
-- So each release on the page cost a scan of `jobs`, a table that grows with
-- every scan, refresh and source search rather than with the catalogue. Filter
-- the list by status — which asks the same question of every release, not just
-- the page's — and it was 84 seconds to count and 73 to list. With this index,
-- 1.2s and 83ms.
--
-- Not unique, unlike its neighbour: refreshes accumulate per release on purpose.
-- Keyed on status rather than on a timestamp because status is what both
-- readers look at — the list folds them together with bool_or, and the filters
-- ask whether one of a given status exists. Neither wants the newest.
CREATE INDEX jobs_album_refresh_idx
    ON jobs ((payload->>'albumId'), status)
    WHERE kind = 'refresh_album_metadata';

-- +goose Down
DROP INDEX IF EXISTS jobs_album_refresh_idx;
DROP INDEX IF EXISTS artists_name_order_idx;
DROP INDEX IF EXISTS albums_title_order_idx;
DROP INDEX IF EXISTS albums_release_order_idx;
