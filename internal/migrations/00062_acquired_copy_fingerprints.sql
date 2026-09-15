-- +goose Up
-- The fingerprint of a copy a peer sent, kept beside the verdict about it.
--
-- A want is one piece of music somebody asked for. A copy is one file a peer on
-- the Soulseek network sent for that want, and acquisition_target_files is what
-- became of each copy: accepted, refused, or held as a question for a person.
-- One want commonly holds many held copies — on 2026-08-16 there were 129 of
-- them behind 44 questions — and a person answering has no way to see that five
-- of the eleven files under one want are the same audio under different names.
--
-- A fingerprint is what would say so. It is about eight numbers a second
-- computed from the decoded audio, the same form AcoustID uses and the same form
-- acquisition_targets.anchor_fingerprint is already stored in. Two copies of one
-- master produce nearly the same numbers even after re-encoding, so two copies
-- can be compared against each other without asking any service and without
-- MusicBrainz holding the music at all (internal/chromaprint).
--
-- Until now the number was computed and thrown away: validation fingerprints a
-- copy to measure it against the want's published sample, reads one distance off
-- it, and keeps nothing. The columns below keep it.
--
-- This records a measurement and nothing else. No copy is admitted, refused or
-- reordered because of what is stored here — grouping the review queue by it is
-- presentation (ROADMAP 10c), and the rules that decide a copy are untouched.
ALTER TABLE acquisition_target_files
    -- The compressed form fpcalc emits, which is the form every other
    -- fingerprint in Schall is kept in. NULL means no fingerprint was computed:
    -- the file was gone, fpcalc is not installed, or the audio would not decode.
    -- That is silence, and no rule may read it as agreement or as disagreement.
    ADD COLUMN audio_fingerprint TEXT,
    -- How many seconds of the file were read. It is kept because a fingerprint
    -- means nothing without it: a copy read to two minutes and one read whole
    -- are different measurements of the same file, and comparing them without
    -- knowing which is which would read a short read as disagreement.
    ADD COLUMN audio_fingerprint_seconds INTEGER,
    ADD CONSTRAINT acquisition_target_files_fingerprint_seconds_positive
        CHECK (audio_fingerprint_seconds IS NULL OR audio_fingerprint_seconds > 0);

-- The copies the backfill pass still has to read. Every copy already
-- fingerprinted is left out of the index rather than walked past, because once
-- the collection settles they are nearly all of them.
CREATE INDEX acquisition_target_files_unfingerprinted_idx
    ON acquisition_target_files (decided_at)
    WHERE verdict = 'held' AND audio_fingerprint IS NULL;

-- One backfill pass at a time, enforced by the table rather than hoped for, in
-- the shape the picture, lyrics and preview sweeps already use. The pass decodes
-- audio, which is seconds of processor time per file, and a second pass would
-- read the same files at the same moment.
CREATE UNIQUE INDEX jobs_copy_fingerprint_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_copy_fingerprints' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_copy_fingerprint_sweep_idx;
DROP INDEX IF EXISTS acquisition_target_files_unfingerprinted_idx;

ALTER TABLE acquisition_target_files
    DROP CONSTRAINT IF EXISTS acquisition_target_files_fingerprint_seconds_positive,
    DROP COLUMN IF EXISTS audio_fingerprint,
    DROP COLUMN IF EXISTS audio_fingerprint_seconds;
