-- +goose Up
-- What a rejected resolution was, so that taking the rejection back can put it
-- back whole.
--
-- A want (acquisition_targets) is one piece of music Schall has been asked to
-- find. Resolving it means deciding which MusicBrainz recording the entry names,
-- and a resolved want carries three facts about that answer: the recording, the
-- release group the answer ran through (musicbrainz_release_group_id, which is
-- the first rule of the release-choice order at import time), and how the answer
-- was reached (resolution_method — 'isrc', 'manual', and so on).
--
-- "Wrong target" is the review answer meaning the entry was resolved to the
-- wrong recording. Recording it erases all three facts from the want, because
-- the want goes back to 'unresolved' and an unresolved want may hold no answer.
-- The recording survives in acquisition_target_rejected_recordings, so the next
-- lookup cannot return it again. The other two survived nowhere.
--
-- ADR 0019 lets a person take that answer back while the entry has not been
-- resolved again. Until now the taking back could restore only the recording:
-- the want came back with no release group and with 'manual' as its method,
-- which said the recording stood on the say-so of the person who put it back
-- because that was the only thing still known to be true. Both facts are now
-- remembered on the rejection at the moment it is written, and both go back onto
-- the want when it is taken back.
--
-- Both columns are nullable, and a rejection written before this migration has
-- neither. Taking one of those back behaves exactly as it did before: the want
-- comes back with no release group, and its method reads 'manual'. Nothing has
-- to be back-filled and nothing fails, because the values were never recorded
-- anywhere and a guess at them would be a fact Schall invented.
ALTER TABLE acquisition_target_rejected_recordings
    ADD COLUMN musicbrainz_release_group_id UUID,
    ADD COLUMN resolution_method TEXT;

-- +goose Down
ALTER TABLE acquisition_target_rejected_recordings
    DROP COLUMN IF EXISTS resolution_method,
    DROP COLUMN IF EXISTS musicbrainz_release_group_id;
