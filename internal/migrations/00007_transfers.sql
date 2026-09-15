-- +goose Up
-- A request now has a life beyond being recorded: it can be started, and then
-- it either finishes or fails. "requested" and "started" are both open states,
-- so neither may be requested a second time for the same folder.
ALTER TABLE download_requests
    DROP CONSTRAINT download_requests_status_valid,
    ADD CONSTRAINT download_requests_status_valid
        CHECK (status IN ('requested', 'started', 'completed', 'failed', 'cancelled')),
    ADD COLUMN started_at TIMESTAMPTZ,
    ADD COLUMN error_message TEXT;

DROP INDEX download_requests_open_source_idx;
CREATE UNIQUE INDEX download_requests_open_source_idx
    ON download_requests (album_id, provider, source_username, source_directory)
    WHERE status IN ('requested', 'started');

-- Each row is one file transfer, belonging to the request that asked for it, so
-- a file acquired through Schall can be traced back to the release it was meant
-- to satisfy. Nothing is imported from here yet: these rows record what the
-- provider is doing, not what the library owns.
ALTER TABLE downloads
    ADD COLUMN download_request_id UUID REFERENCES download_requests(id) ON DELETE CASCADE,
    ADD COLUMN remote_path TEXT,
    ADD COLUMN size_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN transferred_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN provider_transfer_id TEXT,
    ADD COLUMN provider_state TEXT,
    ADD CONSTRAINT downloads_size_nonnegative CHECK (size_bytes >= 0),
    ADD CONSTRAINT downloads_transferred_nonnegative CHECK (transferred_bytes >= 0),
    -- A row belonging to a request describes one remote file, so it must say
    -- which one.
    ADD CONSTRAINT downloads_request_path_present
        CHECK (download_request_id IS NULL OR btrim(coalesce(remote_path, '')) <> '');

-- Starting the same request twice must not enqueue a file twice.
CREATE UNIQUE INDEX downloads_request_path_idx
    ON downloads (download_request_id, remote_path)
    WHERE download_request_id IS NOT NULL;

-- Transfer progress is followed by a single job that reschedules itself while
-- anything is still moving, so there is never more than one poller and no
-- polling at all once every transfer has settled.
CREATE UNIQUE INDEX jobs_active_download_poll_idx
    ON jobs (kind)
    WHERE kind = 'poll_downloads'
      AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_download_poll_idx;
DROP INDEX IF EXISTS downloads_request_path_idx;

ALTER TABLE downloads
    DROP CONSTRAINT IF EXISTS downloads_request_path_present,
    DROP CONSTRAINT IF EXISTS downloads_transferred_nonnegative,
    DROP CONSTRAINT IF EXISTS downloads_size_nonnegative,
    DROP COLUMN IF EXISTS provider_state,
    DROP COLUMN IF EXISTS provider_transfer_id,
    DROP COLUMN IF EXISTS transferred_bytes,
    DROP COLUMN IF EXISTS size_bytes,
    DROP COLUMN IF EXISTS remote_path,
    DROP COLUMN IF EXISTS download_request_id;

DROP INDEX IF EXISTS download_requests_open_source_idx;
CREATE UNIQUE INDEX download_requests_open_source_idx
    ON download_requests (album_id, provider, source_username, source_directory)
    WHERE status = 'requested';

ALTER TABLE download_requests
    DROP COLUMN IF EXISTS error_message,
    DROP COLUMN IF EXISTS started_at,
    DROP CONSTRAINT download_requests_status_valid,
    ADD CONSTRAINT download_requests_status_valid
        CHECK (status IN ('requested', 'cancelled'));
