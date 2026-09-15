-- +goose Up
-- A standing "want what is missing" on a followed artist. While it is set,
-- every missing track of every monitored release is wanted, now and as
-- tracklists arrive: following an artist queues one release refresh per release
-- group, and those land one at a time behind MusicBrainz's rate limit, so a
-- single press wanted only the releases whose tracks had already arrived.
--
-- NULL means off. Clearing it stops future wanting and touches no want that
-- exists, because those were asked for.
ALTER TABLE artists ADD COLUMN want_missing_since TIMESTAMPTZ;

-- New follows watch main releases. 'everything' counted every live album,
-- compilation, remix and DJ mix MusicBrainz credits to the artist, which is not
-- what somebody following them is asking for. Existing rows keep the level they
-- have: the column default decides nothing that was already decided.
ALTER TABLE artists ALTER COLUMN monitor_level SET DEFAULT 'main';

-- +goose Down
ALTER TABLE artists ALTER COLUMN monitor_level SET DEFAULT 'everything';

ALTER TABLE artists DROP COLUMN IF EXISTS want_missing_since;
