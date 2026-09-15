-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE artists (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    musicbrainz_id UUID UNIQUE,
    name TEXT NOT NULL,
    sort_name TEXT NOT NULL,
    followed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_refreshed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT artists_name_not_blank CHECK (btrim(name) <> '')
);

CREATE INDEX artists_sort_name_idx ON artists (sort_name);

CREATE TABLE albums (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artist_id UUID NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    musicbrainz_release_group_id UUID UNIQUE,
    title TEXT NOT NULL,
    release_date DATE,
    album_type TEXT NOT NULL DEFAULT 'album',
    cover_url TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT albums_title_not_blank CHECK (btrim(title) <> '')
);

CREATE INDEX albums_artist_id_idx ON albums (artist_id);

CREATE TABLE tracks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    musicbrainz_recording_id UUID,
    title TEXT NOT NULL,
    disc_number INTEGER NOT NULL DEFAULT 1,
    track_number INTEGER,
    duration_ms INTEGER,
    isrc TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tracks_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT tracks_disc_positive CHECK (disc_number > 0),
    CONSTRAINT tracks_number_positive CHECK (track_number IS NULL OR track_number > 0),
    CONSTRAINT tracks_duration_nonnegative CHECK (duration_ms IS NULL OR duration_ms >= 0)
);

CREATE INDEX tracks_album_id_idx ON tracks (album_id);
CREATE INDEX tracks_musicbrainz_recording_id_idx ON tracks (musicbrainz_recording_id)
    WHERE musicbrainz_recording_id IS NOT NULL;
CREATE INDEX tracks_isrc_idx ON tracks (isrc) WHERE isrc IS NOT NULL;

CREATE TABLE library_files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    path TEXT NOT NULL UNIQUE,
    size_bytes BIGINT NOT NULL,
    modified_at TIMESTAMPTZ NOT NULL,
    fingerprint TEXT,
    artist_tag TEXT,
    album_tag TEXT,
    title_tag TEXT,
    disc_number INTEGER,
    track_number INTEGER,
    duration_ms INTEGER,
    musicbrainz_recording_id UUID,
    isrc TEXT,
    scanned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT library_files_size_nonnegative CHECK (size_bytes >= 0)
);

CREATE TABLE track_mappings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    track_id UUID NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    library_file_id UUID NOT NULL REFERENCES library_files(id) ON DELETE CASCADE,
    method TEXT NOT NULL,
    confidence REAL,
    is_manual BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (track_id),
    UNIQUE (library_file_id),
    CONSTRAINT track_mappings_confidence_range
        CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
    CONSTRAINT track_mappings_manual_method
        CHECK (NOT is_manual OR method = 'manual')
);

CREATE TABLE downloads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    album_id UUID REFERENCES albums(id) ON DELETE SET NULL,
    track_id UUID REFERENCES tracks(id) ON DELETE SET NULL,
    provider TEXT NOT NULL DEFAULT 'slskd',
    provider_download_id TEXT,
    status TEXT NOT NULL DEFAULT 'queued',
    source_username TEXT,
    source_path TEXT,
    error_message TEXT,
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT downloads_target_present CHECK (album_id IS NOT NULL OR track_id IS NOT NULL),
    CONSTRAINT downloads_status_valid
        CHECK (status IN ('queued', 'searching', 'downloading', 'importing', 'completed', 'failed', 'cancelled'))
);

CREATE INDEX downloads_status_idx ON downloads (status);

CREATE TABLE jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT jobs_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT jobs_max_attempts_positive CHECK (max_attempts > 0),
    CONSTRAINT jobs_status_valid
        CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled'))
);

CREATE INDEX jobs_runnable_idx ON jobs (run_after, created_at) WHERE status = 'queued';

CREATE TABLE metadata_sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type TEXT NOT NULL,
    entity_id UUID NOT NULL,
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (entity_type, entity_id, provider)
);

-- +goose Down
DROP TABLE IF EXISTS metadata_sources;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS downloads;
DROP TABLE IF EXISTS track_mappings;
DROP TABLE IF EXISTS library_files;
DROP TABLE IF EXISTS tracks;
DROP TABLE IF EXISTS albums;
DROP TABLE IF EXISTS artists;
