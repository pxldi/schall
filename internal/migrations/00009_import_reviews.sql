-- +goose Up
-- An import that stopped for review keeps only its latest reason on the
-- request. This table is the append-only history behind that reason, so a
-- retried import can be read as a sequence of decisions rather than a single
-- overwritten message.
CREATE TABLE download_import_reviews (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    download_request_id UUID NOT NULL REFERENCES download_requests(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    detail TEXT,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT download_import_reviews_kind_valid
        CHECK (kind IN ('paused', 'revalidated', 'imported'))
);

CREATE INDEX download_import_reviews_request_idx
    ON download_import_reviews (download_request_id, recorded_at);

-- A request that is already waiting for review keeps its reason as the first
-- audited entry, so history does not begin empty for work done before this.
INSERT INTO download_import_reviews (download_request_id, kind, detail, recorded_at)
SELECT id, 'paused', import_error, updated_at
FROM download_requests
WHERE import_status = 'needs_review';

-- +goose Down
DROP TABLE IF EXISTS download_import_reviews;
