-- +goose Up
-- The new-releases playlist: the listening surface of the follow feed (#267).
--
-- Schall lets a person follow an artist or a label. A sweep called
-- `sweep_follow_feed` then asks MusicBrainz what they have published since,
-- and wants every recording on it the library does not hold. The acquisition
-- loop obtains those recordings and the files appear in the library. All of
-- that happens without anybody pressing anything, and nowhere in it does the
-- *player* ever say "here is what your artists just released".
--
-- This is that surface. One playlist, kept filled with the tracks the library
-- owns from releases the feed surfaced inside a window of days, newest release
-- first, pushed to the player by the ordinary Navidrome playlist sync. It
-- rotates membership and nothing else: a release leaving the window leaves a
-- playlist, and the files, the wants and every decision stay exactly as they
-- were. Nothing here can delete music — unlike the weekly playlist (00052),
-- which is why none of that machinery is reused.

-- A playlist is a want list with a source (ADR 0008). This one is a local list
-- like the weekly one: it names no remote object, so it joins the source_id
-- exception. Re-added rather than edited in place, the shape 00038 and 00052
-- used, because an applied migration is never changed.
--
-- 'file' is carried forward from 00074 rather than dropped: re-adding a check
-- writes it whole, so every source that came before has to be named again. It
-- was left out while this branch was open and every CSV and M3U import failed
-- against the constraint.
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome', 'weekly', 'file', 'new_releases')),
    DROP CONSTRAINT playlists_source_id_matches_source,
    ADD CONSTRAINT playlists_source_id_matches_source
        CHECK ((source IN ('manual', 'weekly', 'new_releases')) = (source_id IS NULL));

-- One list for the installation, rewritten as the window moves rather than
-- replaced. A second one would push into a second Navidrome playlist and the
-- person would have two lists with the same name.
CREATE UNIQUE INDEX playlists_new_releases_singleton_idx
    ON playlists ((source = 'new_releases'))
    WHERE source = 'new_releases';

-- Whether the playlist is kept at all, and how far back it reaches.
--
-- It ships switched on, unlike the weekly playlist: this one adds and removes
-- playlist rows and can never delete a file, so there is nothing here that a
-- person has to consent to before it happens. Turning it off empties the list,
-- and the next sync empties it in the player too.
CREATE TABLE new_releases_playlist_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    enabled BOOLEAN NOT NULL DEFAULT true,
    -- How many days back a release may have been published and still be on the
    -- list. Sixty is about two months of releases: long enough that a person
    -- who listens once a fortnight still finds what arrived, short enough that
    -- the list stays a list of new music.
    window_days INTEGER NOT NULL DEFAULT 60,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT new_releases_playlist_settings_single_row CHECK (singleton),
    -- A bound at both ends. A window of nothing is the feature switched off
    -- under another name, and a window of years is the library with a playlist
    -- drawn around it.
    CONSTRAINT new_releases_playlist_settings_window_sane
        CHECK (window_days BETWEEN 1 AND 365)
);

-- One row per file the refresh put on the list, and the record that outlives
-- the entry it made.
--
-- It exists for one question the entry cannot answer: a person who takes a
-- track out of this playlist in their player has made a decision, and by ADR
-- 0013 the sync answers that by deleting the `playlist_entries` row. Without a
-- row that survives that deletion, the next refresh sees a file it owns, on a
-- release inside the window, with no entry — which is exactly what a file it
-- has never added looks like — and puts it straight back. The person would
-- remove it every week and it would return every week.
--
-- So the entry is held here with ON DELETE SET NULL, and an empty
-- playlist_entry_id is read as the removal it is. The refresh deletes its own
-- member row before it deletes an entry, so a track leaving because its release
-- aged out is never read as somebody's decision.
CREATE TABLE new_release_playlist_members (
    -- One member per file: the same file cannot be on the list twice, and the
    -- file is what a refresh has in its hand when it asks whether this is
    -- already there.
    library_file_id UUID PRIMARY KEY REFERENCES library_files(id) ON DELETE CASCADE,
    playlist_entry_id UUID REFERENCES playlist_entries(id) ON DELETE SET NULL,
    -- The release it came from and the date that release was published. The
    -- date is copied rather than joined: it is what the list is ordered by and
    -- aged by, and a catalogue correction months later must not silently move
    -- a track somebody is looking at.
    album_id UUID REFERENCES albums(id) ON DELETE SET NULL,
    released_on DATE NOT NULL,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when the person took the track out in their player. Permanent, which
    -- is what "manual decisions always win" means here: the refresh passes over
    -- a withdrawn row for good, and never deletes one — a withdrawn row whose
    -- release has aged out is kept precisely so that widening the window later
    -- does not re-add music somebody removed.
    withdrawn_at TIMESTAMPTZ,
    CONSTRAINT new_release_playlist_members_withdrawn_has_no_entry
        CHECK (withdrawn_at IS NULL OR playlist_entry_id IS NULL)
);

-- The order the list is written in, and the only rows a refresh reads for it.
CREATE INDEX new_release_playlist_members_order_idx
    ON new_release_playlist_members (released_on DESC, added_at DESC)
    WHERE withdrawn_at IS NULL;

-- A file the library loses takes its entry with it.
--
-- The member row references library_files ON DELETE CASCADE and the entry
-- references it ON DELETE SET NULL (00026), so deleting a file removed the one
-- thing that could reach the entry and left the entry behind. It then looked
-- exactly like a track somebody added to the list in their player: never
-- rotated out, never cleaned up, and pushed to the player as a line with no
-- music under it. Every deletion made another one — the duplicate resolution
-- and the re-encoding sweep delete files as a matter of course.
--
-- Done here rather than in the code that deletes, because there is more than
-- one such place and a new one must not have to remember this.

-- +goose StatementBegin
CREATE FUNCTION drop_new_release_entry_with_its_file() RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM playlist_entries
    WHERE id IN (
        SELECT playlist_entry_id
        FROM new_release_playlist_members
        WHERE library_file_id = OLD.id AND playlist_entry_id IS NOT NULL
    );
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER library_files_drop_new_release_entry
    BEFORE DELETE ON library_files
    FOR EACH ROW
    EXECUTE FUNCTION drop_new_release_entry_with_its_file();

-- One refresh queued or running at a time, in the shape 00048 and 00052 gave
-- the other sweeps. Two of them would each read the members the other is in
-- the middle of writing.
CREATE UNIQUE INDEX jobs_new_releases_playlist_refresh_idx
    ON jobs (kind)
    WHERE kind = 'refresh_new_releases_playlist' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_new_releases_playlist_refresh_idx;
DROP TRIGGER IF EXISTS library_files_drop_new_release_entry ON library_files;
DROP FUNCTION IF EXISTS drop_new_release_entry_with_its_file();
DROP TABLE IF EXISTS new_release_playlist_members;
DROP TABLE IF EXISTS new_releases_playlist_settings;
DROP INDEX IF EXISTS playlists_new_releases_singleton_idx;
DELETE FROM playlists WHERE source = 'new_releases';
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_id_matches_source,
    ADD CONSTRAINT playlists_source_id_matches_source
        CHECK ((source IN ('manual', 'weekly')) = (source_id IS NULL)),
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome', 'weekly', 'file'));
