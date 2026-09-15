-- +goose Up

-- The AcoustID fingerprint on library_files is intentionally still a
-- two-minute lookup fingerprint. Witness comparison needs a separate
-- whole-file fingerprint, because a prefix can name a recording but cannot
-- certify an unseen ending.
ALTER TABLE library_files
    ADD COLUMN witness_fingerprint TEXT,
    ADD COLUMN witness_fingerprint_seconds INTEGER,
    ADD CONSTRAINT library_files_witness_fingerprint_seconds_positive
        CHECK (witness_fingerprint_seconds IS NULL OR witness_fingerprint_seconds > 0);

-- Existing library fingerprints remain available as partial witnesses until the
-- per-file backfill writes the full fingerprint. A missing full fingerprint is
-- an explicit partial-coverage state in SettledWitnesses.
CREATE INDEX library_files_witness_fingerprint_backfill_idx
    ON library_files (id)
    WHERE missing_at IS NULL
      AND coalesce(fingerprint, '') <> ''
      AND witness_fingerprint IS NULL;

CREATE UNIQUE INDEX jobs_active_library_witness_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_library_witnesses' AND status IN ('queued', 'running');

CREATE UNIQUE INDEX jobs_active_library_witness_file_idx
    ON jobs (kind, (payload->>'fileId'))
    WHERE kind = 'fingerprint_library_witness' AND status IN ('queued', 'running');

-- +goose Down

DROP INDEX IF EXISTS jobs_active_library_witness_file_idx;
DROP INDEX IF EXISTS jobs_active_library_witness_sweep_idx;
DROP INDEX IF EXISTS library_files_witness_fingerprint_backfill_idx;

ALTER TABLE library_files
    DROP CONSTRAINT IF EXISTS library_files_witness_fingerprint_seconds_positive,
    DROP COLUMN IF EXISTS witness_fingerprint_seconds,
    DROP COLUMN IF EXISTS witness_fingerprint;
