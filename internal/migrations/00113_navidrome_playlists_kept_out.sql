-- +goose Up
-- Navidrome playlist ids the sweep must never adopt. A snapshot was the only
-- record that a remote list belonged to Schall, and a snapshot goes with its
-- playlist: a list somebody stopped following, or a pushed list whose snapshot
-- was lost, came back at the next sweep as a new list. "Cantus Weekly" and a
-- second "New releases" did this twice. This table outlives both.
--
--   pushed      Schall created the list in Navidrome
--   unfollowed  the Schall list that held it was removed
CREATE TABLE navidrome_playlists_kept_out (
    navidrome_playlist_id TEXT PRIMARY KEY,
    reason TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT navidrome_playlists_kept_out_reason_valid
        CHECK (reason IN ('pushed', 'unfollowed')),
    CONSTRAINT navidrome_playlists_kept_out_id_present
        CHECK (btrim(navidrome_playlist_id) <> '')
);

-- Kept by trigger, because a playlist is removed by cascade and by hand as well
-- as by the API, and every one of those has to leave the id behind.
-- +goose StatementBegin
CREATE FUNCTION navidrome_playlists_keep_out() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        INSERT INTO navidrome_playlists_kept_out (navidrome_playlist_id, reason)
        VALUES (OLD.navidrome_playlist_id, 'unfollowed')
        ON CONFLICT (navidrome_playlist_id) DO NOTHING;
        RETURN OLD;
    END IF;
    IF NOT NEW.adopted THEN
        INSERT INTO navidrome_playlists_kept_out (navidrome_playlist_id, reason)
        VALUES (NEW.navidrome_playlist_id, 'pushed')
        ON CONFLICT (navidrome_playlist_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER navidrome_playlists_keep_out_written
    AFTER INSERT OR UPDATE OF navidrome_playlist_id, adopted
    ON navidrome_playlist_snapshots
    FOR EACH ROW EXECUTE FUNCTION navidrome_playlists_keep_out();

-- A sync also drops the snapshot when the list was deleted in Navidrome. That
-- id no longer exists there, so keeping it out costs nothing.
CREATE TRIGGER navidrome_playlists_keep_out_removed
    AFTER DELETE ON navidrome_playlist_snapshots
    FOR EACH ROW EXECUTE FUNCTION navidrome_playlists_keep_out();

INSERT INTO navidrome_playlists_kept_out (navidrome_playlist_id, reason)
SELECT navidrome_playlist_id, 'pushed'
FROM navidrome_playlist_snapshots
WHERE NOT adopted
ON CONFLICT (navidrome_playlist_id) DO NOTHING;

-- +goose Down
DROP TRIGGER IF EXISTS navidrome_playlists_keep_out_removed ON navidrome_playlist_snapshots;
DROP TRIGGER IF EXISTS navidrome_playlists_keep_out_written ON navidrome_playlist_snapshots;
DROP FUNCTION IF EXISTS navidrome_playlists_keep_out();
DROP TABLE IF EXISTS navidrome_playlists_kept_out;
