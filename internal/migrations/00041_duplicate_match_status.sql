-- +goose Up

-- A file whose every candidate track already belongs to another file is not
-- ambiguous. Nothing is undecided about it: the track is held, manual decisions
-- win and are permanent, and this file cannot have it. Calling that ambiguous
-- put it in the review queue and asked which track it sits in, over a list of
-- one track it is not allowed to take — a question whose only answers were to
-- overturn a decision somebody already made, or to leave it and be asked again.
--
-- It is a duplicate: a second file for music the library already has in place.
-- That is a state rather than a question, so it is recorded as one and the queue
-- stops carrying it. What to do about having the same music twice is a different
-- question, asked where duplicates are resolved rather than where matching is.
ALTER TABLE library_files
    DROP CONSTRAINT IF EXISTS library_files_match_status_valid,
    ADD CONSTRAINT library_files_match_status_valid
        CHECK (match_status IN ('matched', 'unmatched', 'ambiguous', 'duplicate'));

-- +goose Down
UPDATE library_files SET match_status = 'ambiguous' WHERE match_status = 'duplicate';

ALTER TABLE library_files
    DROP CONSTRAINT IF EXISTS library_files_match_status_valid,
    ADD CONSTRAINT library_files_match_status_valid
        CHECK (match_status IN ('matched', 'unmatched', 'ambiguous'));
