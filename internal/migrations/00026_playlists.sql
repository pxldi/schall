-- +goose Up
-- A playlist is a want list with a source (ADR 0008). Three lifetimes live in
-- three tables: the list, its entries, and the join to the wants — because
-- removing a track from a Spotify playlist is not the statement "stop trying
-- to obtain this recording", and one recording can sit on five playlists
-- while being one want.
CREATE TABLE playlists (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source TEXT NOT NULL,
    source_id TEXT,
    -- Spotify's snapshot_id. Deliberately not named "snapshot": 0004 uses that
    -- word for the last state pushed to Navidrome, and the two must never be
    -- read as the same thing.
    source_revision TEXT,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    owner_name TEXT NOT NULL DEFAULT '',
    track_count INTEGER NOT NULL DEFAULT 0,
    imported_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT playlists_source_valid CHECK (source IN ('spotify', 'manual')),
    CONSTRAINT playlists_name_present CHECK (btrim(name) <> ''),
    CONSTRAINT playlists_track_count_nonnegative CHECK (track_count >= 0),
    -- A remote list must say which remote object it is; a manual one has none
    -- to name. The revision belongs to the remote object, never to a manual
    -- list.
    CONSTRAINT playlists_source_id_matches_source
        CHECK ((source = 'manual') = (source_id IS NULL)),
    CONSTRAINT playlists_revision_only_remote
        CHECK (source_revision IS NULL OR source_id IS NOT NULL)
);

CREATE UNIQUE INDEX playlists_source_identity_idx
    ON playlists (source, source_id)
    WHERE source_id IS NOT NULL;

-- One row per track, in order, carrying the same verbatim evidence a target
-- holds (00024): the entry is the input to a decision, and it must survive
-- the playlist being renamed remotely, so re-import reconciles by
-- source_track_id and never by these words.
CREATE TABLE playlist_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    playlist_id UUID NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    source_track_id TEXT,
    entry_artist TEXT NOT NULL DEFAULT '',
    entry_title TEXT NOT NULL DEFAULT '',
    entry_album TEXT NOT NULL DEFAULT '',
    entry_duration_ms INTEGER,
    entry_isrc TEXT,
    -- Where "already owned, creates nothing" is recorded. SET NULL rather than
    -- anything cleverer: a deleted file un-answers the question, and the next
    -- import asks it again.
    owned_library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT playlist_entries_position_positive CHECK (position >= 1),
    CONSTRAINT playlist_entries_duration_nonnegative
        CHECK (entry_duration_ms IS NULL OR entry_duration_ms >= 0),
    -- An entry with nothing to resolve and nothing to search for is not an
    -- entry, exactly as 00024 refuses such a target.
    CONSTRAINT playlist_entries_identifiable
        CHECK (btrim(entry_title) <> '' OR entry_isrc IS NOT NULL)
);

-- Reconciliation identity. A track that appears twice on the remote list
-- collapses to one entry at its first position: the want is one want, and two
-- rows with one source_track_id would make idempotency impossible.
CREATE UNIQUE INDEX playlist_entries_source_track_idx
    ON playlist_entries (playlist_id, source_track_id)
    WHERE source_track_id IS NOT NULL;

CREATE INDEX playlist_entries_order_idx
    ON playlist_entries (playlist_id, position);

-- The join 00024 anticipated, many-to-many in both directions: an entry
-- outlives a superseded target, and one target serves the same recording on
-- several playlists. Join rows point at the target that existed at import
-- time; a later supersession is followed through superseded_by_id, the trail
-- 0006 built.
CREATE TABLE playlist_entry_targets (
    playlist_entry_id UUID NOT NULL
        REFERENCES playlist_entries(id) ON DELETE CASCADE,
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (playlist_entry_id, acquisition_target_id)
);

CREATE INDEX playlist_entry_targets_target_idx
    ON playlist_entry_targets (acquisition_target_id);

-- Credentials for the Spotify Web API, in the shape slskd_settings set
-- (00005). The refresh token arrives later than the client pair — it exists
-- only after the OAuth round-trip — and access tokens live in memory only,
-- never here.
CREATE TABLE spotify_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    client_id TEXT NOT NULL,
    client_secret TEXT NOT NULL,
    refresh_token TEXT,
    -- Whose account the refresh token speaks for, for the settings page to
    -- show; display only, decides nothing.
    account_name TEXT,
    connection_status TEXT NOT NULL DEFAULT 'unknown',
    connection_error TEXT,
    last_checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT spotify_settings_single_row CHECK (singleton),
    CONSTRAINT spotify_settings_client_id_present CHECK (btrim(client_id) <> ''),
    CONSTRAINT spotify_settings_client_secret_present
        CHECK (btrim(client_secret) <> ''),
    CONSTRAINT spotify_settings_status_valid
        CHECK (connection_status IN ('unknown', 'ok', 'failed'))
);

-- One live import per playlist, enforced by the table exactly as the sweep's
-- index is (00024).
CREATE UNIQUE INDEX jobs_active_playlist_import_idx
    ON jobs (kind, (payload ->> 'playlistId'))
    WHERE kind = 'import_playlist' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_playlist_import_idx;
DROP TABLE IF EXISTS spotify_settings;
DROP TABLE IF EXISTS playlist_entry_targets;
DROP TABLE IF EXISTS playlist_entries;
DROP TABLE IF EXISTS playlists;
