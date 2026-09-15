-- +goose Up
-- The recordings a person has said an entry is not.
--
-- Wrong target is the one review outcome with nowhere to be written. Telling
-- Schall it resolved an entry to the wrong recording means sending the target
-- back to 'unresolved' to be looked up again — and acquisition_targets_resolution_absent
-- requires musicbrainz_recording_id to be NULL in that state. The rejected answer
-- is erased by the very act of asking the question again, so the next lookup is
-- free to return it, and the one after that, forever.
--
-- Hence a second table rather than a column: the answer field cannot hold a
-- rejection, by design, because a rejection is not an answer.
--
-- decided_by exists to be constrained rather than to vary. An exclusion narrows
-- what the resolver may conclude from, and a narrowing can turn two candidates
-- that could not be told apart into one that concludes alone. That is sound when
-- the rejection came from a person — it is evidence they supplied, and manual
-- decisions are final here — and it would be circular if Schall could write its
-- own: it would be discarding its candidates and then concluding from what it had
-- left itself. So only a person may write one, and the check says so rather than
-- the convention.
CREATE TABLE acquisition_target_rejected_recordings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE CASCADE,
    musicbrainz_recording_id UUID NOT NULL,

    -- One sentence saying what was rejected, kept so the queue can show what has
    -- already been ruled out instead of silently returning fewer candidates.
    summary TEXT NOT NULL DEFAULT '',
    decided_by TEXT NOT NULL DEFAULT 'user',
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT acquisition_target_rejected_decided_by_valid
        CHECK (decided_by = 'user')
);

-- Saying the same thing twice about one entry is the same fact, not a second one.
CREATE UNIQUE INDEX acquisition_target_rejected_recordings_idx
    ON acquisition_target_rejected_recordings
       (acquisition_target_id, musicbrainz_recording_id);

-- +goose Down
DROP TABLE IF EXISTS acquisition_target_rejected_recordings;
