-- +goose Up
-- What has already been asked about one remote file for one want.
--
-- A want is pursued one candidate at a time, and the loop reads this before
-- choosing what to fetch: an offer it has already judged is never fetched twice,
-- and a want with nothing left to try waits for its next attempt instead of
-- downloading the same wrong file every hour.
--
-- The verdicts are of two kinds, and the difference is the whole reason this
-- table is not a list of rejections. A judgement about the music is permanent:
-- the audio was identified as something else, or the file's own identifiers
-- contradict the recording, and that will be as true tomorrow. A transport fact
-- is not: bytes that never arrived, or arrived truncated, say nothing about
-- whether this peer holds the right music, and this may be the only copy anybody
-- is sharing. Only the first kind takes an offer out of the running for good.
--
-- decided_by is what makes this the same store the review queue will write to.
-- Its "none of these" is the same sentence as a discard — this file is not that
-- recording — and two stores would let the loop offer a file somebody had just
-- rejected.
CREATE TABLE acquisition_target_files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE CASCADE,

    -- The offer, as the peer advertised it. remote_path is the only name a peer
    -- will serve, so it is stored verbatim, separators and all.
    provider TEXT NOT NULL,
    source_username TEXT NOT NULL,
    remote_path TEXT NOT NULL,
    file_name TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT,

    verdict TEXT NOT NULL,
    decided_by TEXT NOT NULL DEFAULT 'schall',
    -- One sentence saying what this file came to, because a file nobody can
    -- explain is a file nobody trusts. evidence is everything the verdict was
    -- reached from, kept whole: the tags as they were read, the recording it was
    -- checked against, what the audio was identified as, and what the bytes
    -- turned out to be. It is what the review queue will show, and it has to
    -- still be readable once the peer's copy is gone.
    summary TEXT NOT NULL DEFAULT '',
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- The request that fetched it, and the library file it became. Both are
    -- SET NULL rather than CASCADE: the judgement outlives the transfer record
    -- and the file, and a verdict that vanished with them would be asked again.
    download_request_id UUID REFERENCES download_requests(id) ON DELETE SET NULL,
    library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,

    decided_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT acquisition_target_files_verdict_valid CHECK (verdict IN (
        -- Chosen, and a transfer is in flight. Not a judgement about anything.
        'fetching',
        -- The bytes never arrived, or were not what was offered. A fact about
        -- the transfer, so this offer may be tried again another day.
        'undelivered',
        -- Proven to be the wanted recording, and imported.
        'accepted',
        -- The audio was identified and it is not this recording.
        'discarded_audio',
        -- The file's own identifiers or its length contradict the recording.
        'discarded_tags',
        -- Nothing could decide. The copy is kept as a question rather than
        -- accepted or thrown away.
        'held'
    )),
    CONSTRAINT acquisition_target_files_decided_by_valid
        CHECK (decided_by IN ('schall', 'user')),
    CONSTRAINT acquisition_target_files_provider_present CHECK (btrim(provider) <> ''),
    CONSTRAINT acquisition_target_files_username_present CHECK (btrim(source_username) <> ''),
    CONSTRAINT acquisition_target_files_path_present CHECK (btrim(remote_path) <> ''),
    CONSTRAINT acquisition_target_files_size_nonnegative
        CHECK (size_bytes IS NULL OR size_bytes >= 0),
    -- Only an accepted file may name a library file. An accepted one does not
    -- name it yet: the library row does not exist until the scan that follows the
    -- import has created it, and the offer is completed then.
    CONSTRAINT acquisition_target_files_file_accepted
        CHECK (library_file_id IS NULL OR verdict = 'accepted')
);

-- One row per offer per want. The loop reconciles into it rather than appending,
-- because "have we already tried this file for this want" has one answer, and a
-- history of it would be a diary rather than a record. What every attempt at the
-- want came to is kept in acquisition_target_attempts, which is that history.
CREATE UNIQUE INDEX acquisition_target_files_offer_idx
    ON acquisition_target_files
       (acquisition_target_id, provider, source_username, remote_path);

CREATE INDEX acquisition_target_files_target_idx
    ON acquisition_target_files (acquisition_target_id, decided_at DESC);

-- The questions waiting for somebody, across every want. This is what the review
-- queue will read, and what says how often the boundary could not decide.
CREATE INDEX acquisition_target_files_held_idx
    ON acquisition_target_files (decided_at DESC)
    WHERE verdict = 'held';

-- A candidate that arrived and was not the wanted recording is not waiting to be
-- imported, and it is not a question for a person either. Without a state of its
-- own it would sit at 'pending', read as an import that had not happened yet, and
-- be queued again by the pending sweep at every boot.
ALTER TABLE download_requests
    DROP CONSTRAINT download_requests_import_status_valid,
    ADD CONSTRAINT download_requests_import_status_valid
        CHECK (import_status IN (
            'pending', 'validating', 'imported', 'needs_review', 'discarded'
        ));

-- A refused candidate belongs in the request's history for the same reason every
-- pause does: it is the only durable account of what became of the bytes.
ALTER TABLE download_import_reviews
    DROP CONSTRAINT download_import_reviews_kind_valid;
ALTER TABLE download_import_reviews
    ADD CONSTRAINT download_import_reviews_kind_valid
        CHECK (kind IN (
            'paused', 'revalidated', 'imported', 'resolved', 'withdrawn',
            'source_removed', 'source_retained', 'discarded'
        ));

-- +goose Down
ALTER TABLE download_import_reviews
    DROP CONSTRAINT download_import_reviews_kind_valid;
DELETE FROM download_import_reviews WHERE kind = 'discarded';
ALTER TABLE download_import_reviews
    ADD CONSTRAINT download_import_reviews_kind_valid
        CHECK (kind IN (
            'paused', 'revalidated', 'imported', 'resolved', 'withdrawn',
            'source_removed', 'source_retained'
        ));

ALTER TABLE download_requests
    DROP CONSTRAINT IF EXISTS download_requests_import_status_valid;
UPDATE download_requests SET import_status = 'needs_review' WHERE import_status = 'discarded';
ALTER TABLE download_requests
    ADD CONSTRAINT download_requests_import_status_valid
        CHECK (import_status IN ('pending', 'validating', 'imported', 'needs_review'));

DROP TABLE IF EXISTS acquisition_target_files;
