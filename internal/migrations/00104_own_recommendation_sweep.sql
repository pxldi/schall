-- +goose Up
-- One own-recommendation sweep at a time (ADR 0039 §6). The sweep writes the
-- published `schall` snapshot on every pass, so two chains would overwrite
-- each other's work and ask MusicBrainz twice.
CREATE UNIQUE INDEX jobs_own_recommendation_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_own_recommendations' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_own_recommendation_sweep_idx;
