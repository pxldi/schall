-- +goose Up

-- An identity can now be proven by a file the library already holds. A copy a
-- peer sent is compared against the fingerprint of an owned file whose identity
-- was settled by a proof, and audio that agrees with that file is the recording
-- that file was proven to be.
--
-- Such an identity is not proven on its own: it rests on the older file's proof.
-- These two columns are that debt written down. admitted_by_file_id names the
-- owned file, and admitted_by_proof keeps the method that settled it — 'manual',
-- 'fingerprint', 'isrc' and the rest — so a person reading why this file was
-- identified is told what proved the file behind it, and so the debt survives
-- the older file being removed from the library.
--
-- Withdrawing a decision withdraws everything admitted on the strength of it,
-- which is a walk down this column and is done in Go rather than by a cascade:
-- the rows are not deleted with the ancestor's row, they are withdrawn and the
-- files behind them are put back into the review queue with a sentence saying
-- why.
--
-- The reference is ON DELETE SET NULL rather than a cascade. A file leaving the
-- library does not unprove the audio that was compared against it, so what is
-- lost is the link and never the identity; the proof stays readable in
-- admitted_by_proof.
ALTER TABLE library_file_identities
    ADD COLUMN admitted_by_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    ADD COLUMN admitted_by_proof TEXT;

-- Read in one direction only: given a file whose decision is being taken back,
-- which identities rested on it.
CREATE INDEX library_file_identities_admitted_by_idx
    ON library_file_identities (admitted_by_file_id)
    WHERE admitted_by_file_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS library_file_identities_admitted_by_idx;

ALTER TABLE library_file_identities
    DROP COLUMN IF EXISTS admitted_by_file_id,
    DROP COLUMN IF EXISTS admitted_by_proof;
