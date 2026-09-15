-- +goose Up
ALTER TABLE albums
    ADD COLUMN preferred_musicbrainz_release_id UUID;

ALTER TABLE library_files
    ADD COLUMN match_status TEXT NOT NULL DEFAULT 'unmatched',
    ADD COLUMN match_candidate_count INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT library_files_match_status_valid
        CHECK (match_status IN ('matched', 'unmatched', 'ambiguous')),
    ADD CONSTRAINT library_files_match_candidates_nonnegative
        CHECK (match_candidate_count >= 0);

UPDATE library_files
SET match_status = 'matched'
WHERE EXISTS (
    SELECT 1 FROM track_mappings
    WHERE track_mappings.library_file_id = library_files.id
);

CREATE INDEX library_files_recording_match_idx
    ON library_files (musicbrainz_recording_id)
    WHERE missing_at IS NULL AND musicbrainz_recording_id IS NOT NULL;

CREATE INDEX library_files_isrc_match_idx
    ON library_files (upper(btrim(isrc)))
    WHERE missing_at IS NULL AND isrc IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS library_files_isrc_match_idx;
DROP INDEX IF EXISTS library_files_recording_match_idx;
ALTER TABLE library_files
    DROP CONSTRAINT IF EXISTS library_files_match_candidates_nonnegative,
    DROP CONSTRAINT IF EXISTS library_files_match_status_valid,
    DROP COLUMN IF EXISTS match_candidate_count,
    DROP COLUMN IF EXISTS match_status;
ALTER TABLE albums DROP COLUMN IF EXISTS preferred_musicbrainz_release_id;
