-- +goose Up
CREATE TABLE library_roots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    path TEXT NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT true,
    last_scanned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT library_roots_path_absolute CHECK (left(path, 1) = '/')
);

ALTER TABLE library_files
    ADD COLUMN library_root_id UUID REFERENCES library_roots(id) ON DELETE RESTRICT,
    ADD COLUMN missing_at TIMESTAMPTZ,
    ADD COLUMN scan_error TEXT;

CREATE INDEX library_files_root_idx ON library_files (library_root_id);
CREATE INDEX library_files_present_idx ON library_files (library_root_id)
    WHERE missing_at IS NULL;

CREATE UNIQUE INDEX jobs_active_library_scan_idx
    ON jobs (kind)
    WHERE kind = 'scan_library'
      AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_library_scan_idx;
DROP INDEX IF EXISTS library_files_present_idx;
DROP INDEX IF EXISTS library_files_root_idx;
ALTER TABLE library_files
    DROP COLUMN IF EXISTS scan_error,
    DROP COLUMN IF EXISTS missing_at,
    DROP COLUMN IF EXISTS library_root_id;
DROP TABLE IF EXISTS library_roots;
