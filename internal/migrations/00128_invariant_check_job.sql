-- +goose Up

-- One invariant check at a time, as for the decode sweep (00122). The check
-- counts rows that break a rule Schall's data keeps (db.Invariants) and logs
-- each count above zero. It touches no rows.
CREATE UNIQUE INDEX jobs_invariant_check_idx
    ON jobs (kind)
    WHERE kind = 'check_invariants' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_invariant_check_idx;
