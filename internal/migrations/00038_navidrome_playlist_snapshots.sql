-- +goose Up
-- What Navidrome was last told about one playlist (ADR 0004). Sync is a
-- three-way merge and this is its third side: without a record of what was
-- actually pushed, every difference between the want list and the player's
-- listing is ambiguous for exactly the tracks the user is still waiting on —
-- a track nobody ever sent cannot be a track somebody removed.
CREATE TABLE navidrome_playlist_snapshots (
    playlist_id UUID PRIMARY KEY
        REFERENCES playlists(id) ON DELETE CASCADE,
    navidrome_playlist_id TEXT NOT NULL,
    -- Adopted lists were created in Navidrome and joined afterwards; the rest
    -- Schall created. Records which side the object came from; decides nothing.
    adopted BOOLEAN NOT NULL DEFAULT false,
    pushed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT navidrome_playlist_snapshots_id_present
        CHECK (btrim(navidrome_playlist_id) <> '')
);

-- One remote list belongs to one Schall list. Two lists pushing into the same
-- Navidrome playlist would each read the other's tracks as additions made by
-- the user, and this is also how an adopted list is recognised as one Schall
-- has already joined rather than one to create again.
CREATE UNIQUE INDEX navidrome_playlist_snapshots_remote_idx
    ON navidrome_playlist_snapshots (navidrome_playlist_id);

-- One row per track actually pushed. Never a row for an unacquired want:
-- 0004's whole point is that what was never pushed cannot have been removed.
CREATE TABLE navidrome_playlist_snapshot_tracks (
    playlist_id UUID NOT NULL REFERENCES
        navidrome_playlist_snapshots(playlist_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    navidrome_song_id TEXT NOT NULL,
    -- 0010: an entry Navidrome stops listing is a removal only when getSong on
    -- this id still reports this path. Verbatim, not joined — the file may
    -- since have moved, and the question is what was agreed at push time.
    library_path TEXT NOT NULL,
    -- Both SET NULL, and both only ever an aid: the pair above is the whole
    -- evidence a sync reads. A file deleted from the library must not take the
    -- record of what was pushed with it, or the entry it was pushed for would
    -- read as a removal the user never made.
    library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    playlist_entry_id UUID REFERENCES playlist_entries(id) ON DELETE SET NULL,
    pushed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (playlist_id, position),
    CONSTRAINT navidrome_playlist_snapshot_tracks_position_positive
        CHECK (position >= 1),
    CONSTRAINT navidrome_playlist_snapshot_tracks_path_present
        CHECK (btrim(library_path) <> '')
);

-- Every sync asks the same question of every id it pushed — is this one still
-- listed, and if not, does it still mean the file it meant — so the lookup by
-- id within a list is the one this table exists to serve.
CREATE INDEX navidrome_playlist_snapshot_tracks_song_idx
    ON navidrome_playlist_snapshot_tracks (playlist_id, navidrome_song_id);

-- A playlist created in Navidrome and adopted afterwards is a want list like
-- any other (0008), and the remote object it came from is its source_id — so
-- 'navidrome' joins the sources a list may be born from. Re-added rather than
-- edited in place: 00026 has been applied everywhere, and an applied migration
-- is never changed, exactly as 00028 re-added the import statuses.
--
-- The other two source constraints already say the right thing about such a
-- list and are left alone: it is not manual and it names the remote playlist,
-- so playlists_source_id_matches_source holds; it has no revision to carry —
-- Navidrome offers no snapshot id and 0004 reads the pushed state instead —
-- so playlists_revision_only_remote holds vacuously.
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome'));

-- One live sync per playlist, in the shape 00026 gave the import: a second
-- push while one is in flight would diff against a snapshot the first is
-- still in the middle of replacing.
CREATE UNIQUE INDEX jobs_active_navidrome_playlist_sync_idx
    ON jobs (kind, (payload ->> 'playlistId'))
    WHERE kind = 'sync_navidrome_playlists' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_navidrome_playlist_sync_idx;
DROP TABLE IF EXISTS navidrome_playlist_snapshot_tracks;
DROP TABLE IF EXISTS navidrome_playlist_snapshots;

ALTER TABLE playlists DROP CONSTRAINT IF EXISTS playlists_source_valid;
-- An adopted list has no meaning without the source it was adopted from, and
-- no other source describes it truthfully: 'manual' would have to give up the
-- remote id, which is the one thing that says which list it is.
DELETE FROM playlists WHERE source = 'navidrome';
ALTER TABLE playlists
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual'));
