-- +goose Up
-- `release_editions` is the one MusicBrainz release Schall chose to represent
-- an album. Its `album_id` column was declared UNIQUE when the table was
-- created in migration 00002, and PostgreSQL builds an index of its own to
-- enforce a UNIQUE column — `release_editions_album_id_key`. The same
-- migration then created `release_editions_album_id_idx` on that one column as
-- well, so the table has carried two identical btree indexes ever since.
--
-- The second one answers no query the first cannot. It only costs: every
-- insert, update and delete writes it, and it holds disk and cache. Nothing in
-- the code or in `queries/` names it, so dropping it changes no plan.
DROP INDEX IF EXISTS release_editions_album_id_idx;

-- +goose Down
CREATE INDEX release_editions_album_id_idx ON release_editions (album_id);
