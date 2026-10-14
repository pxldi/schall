-- +goose Up

-- What each invariant check counted, one row per rule per run (db.Invariants).
-- The check used to log its counts only, and a pod swap lost the first run's
-- log on 2026-10-14. Zeros are stored too, so a run reads whole. The check
-- keeps 90 days. A new, empty table: this migration touches no rows.
CREATE TABLE invariant_check_results (
    checked_at TIMESTAMPTZ NOT NULL,
    invariant TEXT NOT NULL,
    violations BIGINT NOT NULL,
    PRIMARY KEY (checked_at, invariant),
    CONSTRAINT invariant_check_results_violations_nonnegative CHECK (violations >= 0)
);

-- +goose Down
DROP TABLE IF EXISTS invariant_check_results;
