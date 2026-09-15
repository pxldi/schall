-- +goose Up
-- A retry reuses the transfer row a request already has, because the unique
-- index says a remote path belongs to one transfer, and that is what leaves the
-- files which arrived untouched. It also means each requeue overwrites what the
-- previous attempt reported. Once a file eventually arrives, the record says it
-- arrived and nothing else: not that it took four tries, and not what the peer
-- said the first three times.
--
-- That is the wrong thing to forget. "This peer failed this file twice with
-- 'Peer is offline'" is the difference between retrying again and searching for
-- a different source, and it is exactly the evidence a user has to reconstruct
-- from memory today.
--
-- The attempts are therefore kept beside the transfer rather than on it. The
-- transfer row stays the current state of one remote path, which is what the
-- rest of the system reads; the history is append-only and is never consulted
-- to decide anything.
ALTER TABLE downloads
    ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1,
    ADD CONSTRAINT downloads_attempt_positive CHECK (attempt >= 1);

CREATE TABLE download_transfer_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    download_id UUID NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
    -- Which attempt this was, matching downloads.attempt at the time it
    -- settled. It is stored rather than derived from ordering so a row can be
    -- read on its own.
    attempt INTEGER NOT NULL,
    -- outcome is only ever a settled state. An attempt that is still moving has
    -- not come to anything yet, and a history of in-flight states would just be
    -- the poller's diary.
    outcome TEXT NOT NULL,
    -- detail is the provider's own words. slskd says things like
    -- 'Completed, TimedOut', and paraphrasing that loses the only description
    -- of the failure anyone has.
    detail TEXT,
    transferred_bytes BIGINT NOT NULL DEFAULT 0,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT download_transfer_attempts_outcome_valid
        CHECK (outcome IN ('completed', 'failed', 'cancelled')),
    CONSTRAINT download_transfer_attempts_attempt_positive
        CHECK (attempt >= 1),
    CONSTRAINT download_transfer_attempts_bytes_nonnegative
        CHECK (transferred_bytes >= 0)
);

-- One outcome per attempt, enforced rather than hoped for. The poller reports
-- the same settled state on every pass until slskd forgets the transfer, so
-- appending has to be idempotent instead of merely infrequent.
CREATE UNIQUE INDEX download_transfer_attempts_attempt_idx
    ON download_transfer_attempts (download_id, attempt);

-- +goose Down
DROP TABLE IF EXISTS download_transfer_attempts;

ALTER TABLE downloads
    DROP CONSTRAINT IF EXISTS downloads_attempt_positive,
    DROP COLUMN IF EXISTS attempt;
