-- +goose Up
-- What a release search turned away, kept beside what it found.
--
-- A source search asks a peer network for folders that might hold a release, one
-- row per release in `source_searches` (00017), read back by the release page.
-- The row holds the candidates the search returned. It never held the ones the
-- user's own format preferences took out, so a release whose every offer was MP3
-- at 128 kbps, under a floor the user set, read as "no peer is sharing it". That
-- is untrue, and it points at the network when the one thing that can change the
-- answer is a setting.
--
-- refused_candidates holds them: the peer, the folder, the format, why it went,
-- and which rule sent it. The page shows the count and the reasons behind a
-- disclosure, so the search can be believed and the floor can be lowered.
ALTER TABLE source_searches
    ADD COLUMN refused_candidates JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE source_searches
    DROP COLUMN IF EXISTS refused_candidates;
