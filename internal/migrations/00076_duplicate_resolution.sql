-- +goose Up
-- Keeping one copy of a recording, without asking.
--
-- The library ends up holding one recording twice for ordinary reasons: a
-- release reissued, a compilation carrying a track an album already had, an
-- upgrade that arrived beside the file it was meant to replace. Every one of
-- those became a question on the duplicates screen, and a person who has said
-- "they are all fine, keep any of them" was being asked it once per recording
-- for the rest of the library's life.
--
-- So Schall can settle them itself. It settles only what it can prove: a
-- recording whose copies are the same audio — same container, same bit rate,
-- sample rate and channels, lengths within the five seconds matching allows —
-- or one where a copy is provably better than every other. Anything else is
-- still a question, because a group Schall cannot show to be the same audio
-- twice is not a group of copies, and no instruction about copies reaches it.
--
-- Off unless somebody turns it on. This is the only recurring thing in Schall
-- that deletes music without a person present for each deletion, and a default
-- that did would be Schall deciding on behalf of an installation that never
-- said anything. Every deletion is still licensed exactly as a press on the
-- duplicates screen is (00065), and the licence says which of the two it was.
CREATE TABLE duplicate_resolution_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    enabled BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT duplicate_resolution_settings_single_row CHECK (singleton)
);

-- One sweep queued or running at a time, in the shape 00048, 00052 and 00071
-- gave the others. Two would each read the copies the other is deleting.
CREATE UNIQUE INDEX jobs_duplicate_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_duplicates' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_duplicate_sweep_idx;
DROP TABLE IF EXISTS duplicate_resolution_settings;
