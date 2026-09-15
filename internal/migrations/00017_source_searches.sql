-- +goose Up
-- Searching for a source takes as long as the provider's timeout allows, and
-- doing it once per release is why acquiring twelve of them is a job nobody
-- starts. The search therefore moves off the request that asks for it: one row
-- per release, filled in by the worker, read back by the interface.
--
-- What this table is not is a decision. Every row holds the ranked candidates
-- exactly as the search returned them, and nothing here records that one was
-- chosen. Choosing still goes through download_requests, one release at a time,
-- with the duplicate question asked and answered as it always was. Ranking is a
-- heuristic over what peers advertise, and a folder that scores well can still
-- be the wrong edition; searching in bulk is a saving in waiting, never in
-- judgement.
CREATE TABLE source_searches (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- run_id groups the releases one request asked about. It deliberately has
    -- no row of its own: a run's progress is an aggregate over its members, so
    -- there is no parent status that can disagree with them.
    run_id UUID NOT NULL,
    album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'queued',
    -- The phrase actually searched, normalized, so the interface can never show
    -- a query the provider could not have matched.
    query TEXT NOT NULL DEFAULT '',
    -- The ranked candidates as the search returned them, stored whole. A later
    -- search of the same release will find different peers online, so a result
    -- read back tomorrow has to be the one that was actually offered.
    candidates JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- error is why the search could not be run, kept apart from finding
    -- nothing: "no peer had it" and "slskd was unreachable" lead to different
    -- next steps and must never render the same.
    error TEXT,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    searched_at TIMESTAMPTZ,
    CONSTRAINT source_searches_status_valid
        CHECK (status IN ('queued', 'searching', 'found', 'none', 'failed'))
);

-- One result per release per run. A job that is retried after the worker was
-- interrupted updates its row rather than adding a second answer for the same
-- question.
CREATE UNIQUE INDEX source_searches_run_album_idx ON source_searches (run_id, album_id);

CREATE INDEX source_searches_run_idx ON source_searches (run_id, requested_at);

-- +goose Down
DROP TABLE IF EXISTS source_searches;
