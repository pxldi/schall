-- +goose Up
-- A want keeps one held copy of each piece of audio it holds and sets the
-- others aside as duplicates. The owner asked for this on 2026-10-12: 32 wants
-- held 4,490 of 6,349 fetched copies, most of them the same upload again. A
-- duplicate says nothing about which recording the audio is, and the inbox
-- passes remove its file as they remove a refused copy's.
ALTER TABLE acquisition_target_files
    DROP CONSTRAINT acquisition_target_files_verdict_valid;
ALTER TABLE acquisition_target_files
    ADD CONSTRAINT acquisition_target_files_verdict_valid CHECK (verdict IN (
        'fetching', 'undelivered', 'accepted', 'discarded_audio', 'discarded_tags',
        'held',
        -- Sounds the same as a better held copy of the same want.
        'duplicate'
    ));

-- One pass sets aside the duplicates already held. Each want's hunt does the
-- same for its own copies from then on.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'set_aside_duplicate_copies', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'set_aside_duplicate_copies' AND status IN ('queued', 'running')
);

-- +goose Down
DELETE FROM jobs WHERE kind = 'set_aside_duplicate_copies' AND status = 'queued';
-- A duplicate was a held copy, and goes back to being asked about.
UPDATE acquisition_target_files SET verdict = 'held' WHERE verdict = 'duplicate';
ALTER TABLE acquisition_target_files
    DROP CONSTRAINT acquisition_target_files_verdict_valid;
ALTER TABLE acquisition_target_files
    ADD CONSTRAINT acquisition_target_files_verdict_valid CHECK (verdict IN (
        'fetching', 'undelivered', 'accepted', 'discarded_audio', 'discarded_tags',
        'held'
    ));
