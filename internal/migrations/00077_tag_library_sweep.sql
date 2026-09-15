-- +goose Up
-- One pass over the library's tags queued or running at a time, in the shape
-- 00053 and 00067 give the lyrics and spectrum sweeps.
--
-- The button's own guard already refused a second pass by checking first and
-- inserting second, which is two statements a race between two requests can
-- both pass. This index makes the database refuse the second insert instead,
-- which is what the new weekly schedule needs: a press and a scheduled run can
-- now land at the same moment without one of them erroring out.
CREATE UNIQUE INDEX jobs_tag_library_sweep_idx
    ON jobs (kind)
    WHERE kind = 'tag_library' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_tag_library_sweep_idx;
