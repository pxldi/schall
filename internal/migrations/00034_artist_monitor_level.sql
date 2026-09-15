-- +goose Up
-- Following an artist used to mean the whole discography counts: every release
-- group MusicBrainz knows became part of "n of m owned", and wanting everything
-- wanted all of it — live dumps, bootleg singles, remix EPs. The monitor level
-- says up front what counts and what gets wanted, so those decisions stop being
-- made one per-track dismissal at a time. It filters counting and auto-wanting,
-- never data: refresh still ingests the full discography, an unmonitored
-- release stays browsable and can still be wanted by hand, and per-track
-- not-wanted decisions stay permanent underneath whatever the level says.
ALTER TABLE artists
    ADD COLUMN monitor_level TEXT NOT NULL DEFAULT 'everything',
    ADD CONSTRAINT artists_monitor_level_valid
        CHECK (monitor_level IN ('everything', 'main', 'albums_eps', 'owned'));

-- "Main releases" is decided by MusicBrainz's own types, and the primary type
-- alone cannot say that an album is a live recording or a compilation — that is
-- what secondary types carry. They were fetched all along and dropped at this
-- table; kept lowercased, matching album_type. Existing rows read as "none
-- known" until the next refresh fills them in, which errs toward counting a
-- release rather than silently hiding it.
ALTER TABLE albums
    ADD COLUMN secondary_types TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE albums
    DROP COLUMN IF EXISTS secondary_types;
ALTER TABLE artists
    DROP CONSTRAINT IF EXISTS artists_monitor_level_valid,
    DROP COLUMN IF EXISTS monitor_level;
