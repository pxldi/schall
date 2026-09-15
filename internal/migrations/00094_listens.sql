-- +goose Up
-- What the account listened to, one row per listen, copied from ListenBrainz
-- by the sync job so the Overview can count days, hours and weekdays in the
-- reader's own time zone without asking the service for each figure.
--
-- The key is the moment plus the names, which is what ListenBrainz itself
-- treats as one listen: pulling a page twice inserts nothing twice.
CREATE TABLE listens (
    listened_at TIMESTAMPTZ NOT NULL,
    artist_name TEXT NOT NULL,
    track_name TEXT NOT NULL,
    release_name TEXT,
    recording_mbid UUID,
    release_mbid UUID,
    caa_release_mbid UUID,
    artist_mbids UUID[],
    inserted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (listened_at, artist_name, track_name)
);

CREATE INDEX listens_listened_at_idx ON listens (listened_at DESC);

-- One sync at a time, the same way the other sweeps are kept single.
CREATE UNIQUE INDEX jobs_listens_sync_idx
    ON jobs (kind)
    WHERE kind = 'sync_listens' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_listens_sync_idx;
DROP TABLE listens;
