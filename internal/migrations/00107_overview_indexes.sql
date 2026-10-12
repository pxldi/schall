-- +goose Up
-- The Overview pictures each listen with the library's copy of it, found by
-- recording ID or else by the file's title and artist tags compared without
-- case. Without an index the tag lookup read every present file, once per
-- listen, and the page made a few hundred of them.
CREATE INDEX library_files_tag_pair_idx
    ON library_files (lower(title_tag), lower(artist_tag))
    WHERE missing_at IS NULL;

-- Recently added on the same page is the newest present files.
CREATE INDEX library_files_present_created_idx
    ON library_files (created_at DESC)
    WHERE missing_at IS NULL;

-- +goose Down
DROP INDEX library_files_present_created_idx;
DROP INDEX library_files_tag_pair_idx;
