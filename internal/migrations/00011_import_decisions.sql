-- +goose Up
-- Seeing why an import paused is not the same as being able to answer it. A
-- decision records that one downloaded file is one catalogue track, judged by
-- a person against evidence Schall could not resolve on its own.
--
-- observed_fingerprint is the identity of what the reviewer actually looked at.
-- A decision applies only while the file still says the same thing, so a
-- remembered judgement can never carry over to different audio.
CREATE TABLE download_import_decisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    download_request_id UUID NOT NULL REFERENCES download_requests(id) ON DELETE CASCADE,
    file_name TEXT NOT NULL,
    track_id UUID NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    observed_fingerprint TEXT NOT NULL,
    note TEXT,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One file resolves to one track, and one track accepts one file: a
    -- decision that could be read two ways is not a decision.
    CONSTRAINT download_import_decisions_one_per_file UNIQUE (download_request_id, file_name),
    CONSTRAINT download_import_decisions_one_per_track UNIQUE (download_request_id, track_id)
);

CREATE INDEX download_import_decisions_request_idx
    ON download_import_decisions (download_request_id);

-- Recording and withdrawing a decision join the same audited history as the
-- pauses and retries they belong to.
ALTER TABLE download_import_reviews DROP CONSTRAINT download_import_reviews_kind_valid;
ALTER TABLE download_import_reviews ADD CONSTRAINT download_import_reviews_kind_valid
    CHECK (kind IN ('paused', 'revalidated', 'imported', 'resolved', 'withdrawn'));

-- +goose Down
ALTER TABLE download_import_reviews DROP CONSTRAINT download_import_reviews_kind_valid;
ALTER TABLE download_import_reviews ADD CONSTRAINT download_import_reviews_kind_valid
    CHECK (kind IN ('paused', 'revalidated', 'imported'));
DROP TABLE IF EXISTS download_import_decisions;
