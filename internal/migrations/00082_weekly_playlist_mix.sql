-- +goose Up

-- The weekly list drew from one pool, the suppressed recommendation list.
-- library_share is the percentage of the week that comes instead from music the
-- collection already holds. A library song joins the playlist and nothing else:
-- it gets no lease, and the lease is the only licence to delete
-- (docs/decisions/0021), so the refresh that rotates it off can never touch the
-- file. The absence of a lease is the whole enforcement — no code has to know
-- the rule.
--
-- one_per_artist is separate from the share on purpose. A person who wants a
-- spread and no library share should be able to ask for one, and a rule that
-- switched itself on as a side effect of setting a percentage would be a
-- surprise. Both default to what the feature did before this migration.
ALTER TABLE weekly_playlist_settings
    ADD COLUMN library_share INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN one_per_artist BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE weekly_playlist_settings
    ADD CONSTRAINT weekly_playlist_settings_library_share_sane
        CHECK (library_share BETWEEN 0 AND 100);

-- Mode is copied onto a run because the report a person reads a month later has
-- to say what the rules were that week. The mix is now one of those rules, so it
-- is copied for the same reason. library_count and replaced_count are the two
-- new things a run does: songs taken from the library, and slots freed by a want
-- that had a week and could not be proven.
ALTER TABLE weekly_playlist_runs
    ADD COLUMN library_share INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN one_per_artist BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN library_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN replaced_count INTEGER NOT NULL DEFAULT 0;

-- A library entry is deleted from the playlist when the week rotates, so the
-- playlist cannot say which songs the library pool has already offered. Without
-- that record a collection smaller than a few hundred recordings would show the
-- same songs every week. Keyed by recording rather than by file: two copies of
-- one recording are one song to a listener.
CREATE TABLE weekly_library_picks (
    musicbrainz_recording_id UUID PRIMARY KEY,
    library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE SET NULL,
    picked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX weekly_library_picks_picked_at_idx
    ON weekly_library_picks (picked_at);

-- +goose Down

DROP TABLE IF EXISTS weekly_library_picks;

ALTER TABLE weekly_playlist_runs
    DROP COLUMN IF EXISTS replaced_count,
    DROP COLUMN IF EXISTS library_count,
    DROP COLUMN IF EXISTS one_per_artist,
    DROP COLUMN IF EXISTS library_share;

ALTER TABLE weekly_playlist_settings
    DROP CONSTRAINT IF EXISTS weekly_playlist_settings_library_share_sane;

ALTER TABLE weekly_playlist_settings
    DROP COLUMN IF EXISTS one_per_artist,
    DROP COLUMN IF EXISTS library_share;
