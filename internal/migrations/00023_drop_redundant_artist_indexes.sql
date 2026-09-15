-- +goose Up
-- Two indexes on `artists` that nothing reaches any more.
--
-- artists_sort_name_idx (00001) indexed sort_name alone, and no query orders on
-- sort_name alone: the only ORDER BY in the codebase that mentions it is the
-- artists list, which sorts on (followed_at IS NULL), sort_name, id — an
-- expression whose own index arrived in 00020. artists_followed_idx (00015) was
-- that list's followed half before it, partial on followed_at IS NOT NULL over
-- the same sort_name; the leading column of artists_list_order_idx now
-- separates followed from held, so walking one half of it is the same walk.
--
-- Checked rather than assumed. Against 20k artists, the list on every scope and
-- at depth, and the list totals, chose artists_list_order_idx with these two
-- present and produced the same plans and the same times without them:
--
--   followed, first page   0.25ms -> 0.21ms
--   held, page 100         4.67ms -> 4.34ms
--   everyone, first page   0.04ms -> 0.04ms
--   list totals            1.67ms -> 1.68ms
--
-- An index nothing reads is not free: it is written on every insert and every
-- rename, and it reads as load-bearing to whoever comes next.
DROP INDEX IF EXISTS artists_sort_name_idx;
DROP INDEX IF EXISTS artists_followed_idx;

-- +goose Down
CREATE INDEX artists_sort_name_idx ON artists (sort_name);
CREATE INDEX artists_followed_idx ON artists (sort_name) WHERE followed_at IS NOT NULL;
