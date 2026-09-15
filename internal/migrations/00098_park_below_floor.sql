-- +goose Up
-- A want that has only seen copies below the user's bitrate floor spends a
-- source search every time it climbs the ordinary ladder. Keep the history of
-- that answer and the person's one-time decision to accept the best copy.
ALTER TABLE acquisition_targets
    ADD COLUMN below_floor_streak SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN below_floor_since TIMESTAMPTZ,
    ADD COLUMN floor_waived_at TIMESTAMPTZ;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_below_floor_streak_nonnegative
        CHECK (below_floor_streak >= 0);

-- +goose Down
ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_below_floor_streak_nonnegative;

ALTER TABLE acquisition_targets
    DROP COLUMN IF EXISTS below_floor_streak,
    DROP COLUMN IF EXISTS below_floor_since,
    DROP COLUMN IF EXISTS floor_waived_at;
