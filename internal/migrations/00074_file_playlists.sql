-- +goose Up
-- A playlist can also arrive as a file: a CSV another tool exported, or an M3U
-- an old player wrote. Everything after the door already exists (ADR 0008) —
-- entries, wants, the join — so the only thing missing is a source that says
-- where the list came from. This is the third source added this way, after
-- 'navidrome' (00038) and 'weekly' (00052).
--
-- A file list keeps a source_id, and 00052's constraint already requires one
-- of every source except 'manual' and 'weekly'. What it holds is the name of
-- the file that was imported. That is the list's identity: importing the same
-- file again has to find the list it made last time rather than build a second
-- one beside it, and the unique index on (source, source_id) is what makes
-- that impossible to get wrong. The playlist's own name is separate and the
-- user may change it; the file name is not touched by that.
--
-- There is no revision to record. A file does not change behind Schall's back,
-- and re-reading a path is a different feature nobody has asked for.
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome', 'weekly', 'file'));

-- +goose Down
-- An imported list without its source is not describable as anything else:
-- 'manual' would have to give up the file name, which is the one thing that
-- says which file made it.
DELETE FROM playlists WHERE source = 'file';
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome', 'weekly'));
