-- +goose Up

-- Whether a file's audio decodes from start to end. A read-only check of the
-- live library on 2026-10-14 found 8 FLAC and 31 MP3 files that do not. The
-- upgrade sweep asks for a replacement of each one.
--
-- Both columns start empty. The decode sweep fills them a few files at a time,
-- so this migration touches no rows.

-- The first audio error ffmpeg reported, or NULL for a clean decode or a file
-- nobody has checked yet.
ALTER TABLE library_files ADD COLUMN decode_error TEXT;

-- When the file was decoded. The scanner clears it when a file's size or
-- modification time changes, which puts the file back in front of the sweep.
ALTER TABLE library_files ADD COLUMN decode_checked_at TIMESTAMPTZ;

-- One decode sweep at a time, as for the spectrum sweep (00067).
CREATE UNIQUE INDEX jobs_decode_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_decode_check' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_decode_sweep_idx;
ALTER TABLE library_files
    DROP COLUMN decode_error,
    DROP COLUMN decode_checked_at;
