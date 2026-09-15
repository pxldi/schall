-- +goose Up
-- Who a SoundCloud track is by, when the uploader is not that person.
--
-- SoundCloud titles carry the artist, the track and a download tag in one
-- string: `PinkPantheress - Illegal (Abo Edit) [FREE DL]`, uploaded by `Abo`.
-- 00066 recorded the uploader as the artist, because the uploader is the only
-- name SoundCloud states as a field. That reads in the library as a track
-- called "PinkPantheress - Illegal (Abo Edit) [FREE DL]" by Abo, which is not
-- what anybody would say out loud about it.
--
-- artist_name now holds the person the track is by and remixer_name holds the
-- one who edited it. Both come from a person: Schall suggests the split, shows
-- it, and writes what they confirmed. Neither is evidence about any MusicBrainz
-- recording, and neither is ever matched to one by name.
--
-- Nullable, because most tracks have no remixer and because every row written
-- before this has none. A NULL means the uploader is the artist, which is what
-- those rows already say.
ALTER TABLE library_file_identities ADD COLUMN remixer_name TEXT;

-- The other two kinds of identity name a MusicBrainz recording and have nothing
-- to say about who edited it. A row that filled this in would be a source
-- identity filed under the wrong kind.
ALTER TABLE library_file_identities
    ADD CONSTRAINT library_file_identities_remixer_is_a_source
        CHECK (kind = 'source' OR remixer_name IS NULL);

-- +goose Down
ALTER TABLE library_file_identities
    DROP CONSTRAINT IF EXISTS library_file_identities_remixer_is_a_source;
ALTER TABLE library_file_identities DROP COLUMN IF EXISTS remixer_name;
