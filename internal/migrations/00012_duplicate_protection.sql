-- +goose Up
-- A download is an acquisition, and Schall must not acquire music the library
-- may already hold. Owning a release is easy to prove; the harder case is a
-- file that is plainly this release but whose track identity was never
-- resolved. Such a file is not evidence of absence, so it pauses the request
-- for confirmation rather than being ignored.
--
-- duplicate_evidence is what the user was actually shown when they confirmed,
-- kept verbatim beside the timestamp of the answer. A later scan will find a
-- different library, and that must not rewrite the decision that was made.
ALTER TABLE download_requests
    ADD COLUMN duplicate_acknowledged_at TIMESTAMPTZ,
    ADD COLUMN duplicate_evidence JSONB,
    -- An acknowledgement without its evidence would record that someone said
    -- yes without recording what to.
    ADD CONSTRAINT download_requests_duplicate_ack_consistent
        CHECK ((duplicate_acknowledged_at IS NULL) = (duplicate_evidence IS NULL));

-- +goose Down
ALTER TABLE download_requests
    DROP CONSTRAINT IF EXISTS download_requests_duplicate_ack_consistent,
    DROP COLUMN IF EXISTS duplicate_evidence,
    DROP COLUMN IF EXISTS duplicate_acknowledged_at;
