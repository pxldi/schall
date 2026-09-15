-- +goose Up
-- One lyrics-fetcher, enforced by the table rather than hoped for, exactly as
-- the picture sweep is.
--
-- Lyrics come from LRCLIB, a free public service with no key and no bill, run by
-- somebody who did not ask to be asked. The sweep walks the library one page at
-- a time and pauses between requests, which is only a pace if there is one sweep
-- doing the walking: two live passes would be two installations' worth of
-- requests arriving there at once.
--
-- Nothing is recorded about lyrics anywhere in this database. The .lrc file
-- beside the music is the whole record of what was fetched, and its absence is
-- the whole record of what was not — so this index is the only thing the feature
-- adds to the schema.
CREATE UNIQUE INDEX jobs_lyrics_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_lyrics' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_lyrics_sweep_idx;
