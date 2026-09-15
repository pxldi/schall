-- +goose Up
-- A run may now be asked to record the download itself, for the releases whose
-- candidate is corroborated beyond doubt: every catalogue track matched by a
-- file carrying its name and running to its length.
--
-- The permission belongs to the run rather than to a global setting. It is
-- consent given when the search is started, for the releases it named, by
-- somebody who is about to look at the results — not a switch left on months
-- ago and forgotten. It is stored per row because a run has no row of its own,
-- and it is stored rather than inferred because "why did this appear in my
-- downloads" has to be answerable later.
--
-- What it does not do is answer the duplicate question. Whether to acquire
-- music the library already holds is a decision about the user's own files, and
-- a run that finds duplicate evidence leaves that release for review however
-- well corroborated its candidate is.
ALTER TABLE source_searches
    ADD COLUMN auto_request BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN auto_requested_at TIMESTAMPTZ,
    -- A row can only have acted if it was allowed to.
    ADD CONSTRAINT source_searches_auto_requested_permitted
        CHECK (auto_requested_at IS NULL OR auto_request);

-- +goose Down
ALTER TABLE source_searches
    DROP CONSTRAINT IF EXISTS source_searches_auto_requested_permitted,
    DROP COLUMN IF EXISTS auto_requested_at,
    DROP COLUMN IF EXISTS auto_request;
