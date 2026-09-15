-- +goose Up
-- A download request records that the user chose one remote folder for one
-- catalogue release. It is provenance, not a transfer: nothing in this table
-- starts, queues, or imports anything. When transfers arrive, each transfer
-- will reference the request it came from, so a file acquired through Schall
-- can always be traced back to the release it was meant to satisfy.
CREATE TABLE download_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    source_username TEXT NOT NULL,
    source_directory TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'requested',
    -- The state of the evidence when the user decided, kept verbatim. A later
    -- search will rank differently, and that must not rewrite the decision.
    file_count INTEGER NOT NULL,
    expected_track_count INTEGER NOT NULL,
    total_size_bytes BIGINT NOT NULL,
    format TEXT NOT NULL,
    average_bit_rate INTEGER,
    score DOUBLE PRECISION NOT NULL,
    reasons TEXT[] NOT NULL DEFAULT '{}',
    files JSONB NOT NULL DEFAULT '[]'::jsonb,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    cancelled_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT download_requests_status_valid
        CHECK (status IN ('requested', 'cancelled')),
    CONSTRAINT download_requests_provider_present CHECK (btrim(provider) <> ''),
    CONSTRAINT download_requests_username_present CHECK (btrim(source_username) <> ''),
    CONSTRAINT download_requests_file_count_positive CHECK (file_count > 0),
    CONSTRAINT download_requests_expected_nonnegative CHECK (expected_track_count >= 0),
    CONSTRAINT download_requests_size_nonnegative CHECK (total_size_bytes >= 0),
    CONSTRAINT download_requests_bit_rate_positive
        CHECK (average_bit_rate IS NULL OR average_bit_rate > 0),
    CONSTRAINT download_requests_score_range CHECK (score BETWEEN 0 AND 1),
    -- A cancelled request keeps its history; the timestamp and the status can
    -- never disagree about whether it is still wanted.
    CONSTRAINT download_requests_cancelled_consistent
        CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
);

-- The same folder cannot be requested twice for the same release while an
-- earlier request is still open, so an impatient second click cannot become a
-- duplicate acquisition. Cancelled requests stay out of the way of a retry.
CREATE UNIQUE INDEX download_requests_open_source_idx
    ON download_requests (album_id, provider, source_username, source_directory)
    WHERE status = 'requested';

CREATE INDEX download_requests_status_idx ON download_requests (status, requested_at DESC);
CREATE INDEX download_requests_album_id_idx ON download_requests (album_id);

-- +goose Down
DROP TABLE IF EXISTS download_requests;
