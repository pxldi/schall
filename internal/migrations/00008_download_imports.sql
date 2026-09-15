-- +goose Up
ALTER TABLE download_requests
    ADD COLUMN import_status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN import_error TEXT,
    ADD COLUMN import_path TEXT,
    ADD COLUMN imported_at TIMESTAMPTZ,
    ADD CONSTRAINT download_requests_import_status_valid
        CHECK (import_status IN ('pending', 'validating', 'imported', 'needs_review')),
    ADD CONSTRAINT download_requests_import_consistent
        CHECK (
            (import_status = 'imported') =
            (imported_at IS NOT NULL AND import_path IS NOT NULL)
        );

ALTER TABLE downloads
    ADD COLUMN local_path TEXT,
    ADD COLUMN library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX downloads_local_path_idx
    ON downloads (local_path)
    WHERE local_path IS NOT NULL;

CREATE UNIQUE INDEX jobs_active_download_import_idx
    ON jobs ((payload->>'requestId'))
    WHERE kind = 'import_download' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_download_import_idx;
DROP INDEX IF EXISTS downloads_local_path_idx;

ALTER TABLE downloads
    DROP COLUMN IF EXISTS library_file_id,
    DROP COLUMN IF EXISTS local_path;

ALTER TABLE download_requests
    DROP CONSTRAINT IF EXISTS download_requests_import_consistent,
    DROP CONSTRAINT IF EXISTS download_requests_import_status_valid,
    DROP COLUMN IF EXISTS imported_at,
    DROP COLUMN IF EXISTS import_path,
    DROP COLUMN IF EXISTS import_error,
    DROP COLUMN IF EXISTS import_status;
