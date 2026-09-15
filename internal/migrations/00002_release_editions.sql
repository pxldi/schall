-- +goose Up
CREATE TABLE release_editions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    album_id UUID NOT NULL UNIQUE REFERENCES albums(id) ON DELETE CASCADE,
    musicbrainz_release_id UUID NOT NULL UNIQUE,
    title TEXT NOT NULL,
    status TEXT,
    country TEXT,
    barcode TEXT,
    release_date TEXT,
    media_count INTEGER NOT NULL DEFAULT 0,
    track_count INTEGER NOT NULL DEFAULT 0,
    selected_automatically BOOLEAN NOT NULL DEFAULT true,
    selection_reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT release_editions_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT release_editions_media_nonnegative CHECK (media_count >= 0),
    CONSTRAINT release_editions_tracks_nonnegative CHECK (track_count >= 0),
    CONSTRAINT release_editions_reason_not_blank CHECK (btrim(selection_reason) <> '')
);

CREATE INDEX release_editions_album_id_idx ON release_editions (album_id);

ALTER TABLE tracks
    ADD CONSTRAINT tracks_album_position_unique
    UNIQUE (album_id, disc_number, track_number);

CREATE UNIQUE INDEX jobs_active_album_refresh_idx
    ON jobs (kind, (payload->>'albumId'))
    WHERE kind = 'refresh_album_metadata'
      AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_album_refresh_idx;
ALTER TABLE tracks DROP CONSTRAINT IF EXISTS tracks_album_position_unique;
DROP TABLE IF EXISTS release_editions;
