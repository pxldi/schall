-- +goose Up
-- Resolving an entry to a recording is the same question as resolving a file to
-- one, asked before any file exists. It has the same three answers: exactly one
-- recording, several plausible ones, or none yet — and the middle answer is only
-- worth having if what stood for and against each candidate is kept, because
-- otherwise the review queue shows a list of titles and asks the user to guess.
--
-- This is library_file_candidates for entries. Deliberately the same shape:
-- the two must not drift apart in what they are able to say about a candidate,
-- and a review queue that renders both reads one structure rather than two.
CREATE TABLE acquisition_target_candidates (
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE CASCADE,
    musicbrainz_recording_id UUID NOT NULL,
    musicbrainz_release_group_id UUID,
    artist_name TEXT NOT NULL DEFAULT '',
    release_title TEXT,
    track_title TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER,
    isrc TEXT,
    -- Display order as the attempt ranked it. Ranking never admits or refuses a
    -- candidate — it only decides which plausible answer is read first.
    rank INTEGER NOT NULL,
    -- What agreed and what disagreed, in the words the summary is built from.
    -- Kept per candidate rather than derived later: the provider's answer will
    -- have changed by the time anybody reads this, and the question the user is
    -- being asked is the one the attempt actually asked.
    agrees TEXT[] NOT NULL DEFAULT '{}',
    differs TEXT[] NOT NULL DEFAULT '{}',
    summary TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (acquisition_target_id, musicbrainz_recording_id),
    CONSTRAINT acquisition_target_candidates_rank_positive CHECK (rank >= 1),
    CONSTRAINT acquisition_target_candidates_duration_nonnegative
        CHECK (duration_ms IS NULL OR duration_ms >= 0)
);

CREATE INDEX acquisition_target_candidates_target_idx
    ON acquisition_target_candidates (acquisition_target_id, rank);

-- +goose Down
DROP TABLE IF EXISTS acquisition_target_candidates;
