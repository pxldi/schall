-- +goose Up
-- Cancelling a source-search run reads back which of its releases will never be
-- searched, and that answer lives in the job rather than in `source_searches`:
-- a stopped release still says 'queued', because nothing ever looked for it,
-- and what makes the queue permanent is the cancelled job. Reaching a job by
-- run ID meant a scan of `jobs` by kind and payload, which no index covered —
-- ~11ms against 200k jobs, linear in a table that grows with every scan,
-- refresh and search and is never purged.
--
-- The read is gated so a run searched to the end skips it, but a run with
-- anything still waiting pays it per page-open, and a cancelled run pays it for
-- as long as it is looked at, since its stopped releases keep saying 'queued'
-- by design. Indexing the run ID the query actually filters on removes the
-- scan.
--
-- Partial, because `search_album_sources` is one job kind among several and
-- this lookup never asks about the others.
--
-- Indexed as uuid rather than as the text the payload stores, because that is
-- how both queries spell it. An index on the bare `payload->>'runId'` is a
-- different expression from `(payload->>'runId')::uuid`, and the planner will
-- not use one for the other: measured against 200k jobs it kept the parallel
-- sequential scan at ~32ms, where this index answers in ~0.05ms.
CREATE INDEX jobs_source_search_run_idx
    ON jobs (((payload->>'runId')::uuid))
    WHERE kind = 'search_album_sources';

-- +goose Down
DROP INDEX IF EXISTS jobs_source_search_run_idx;
