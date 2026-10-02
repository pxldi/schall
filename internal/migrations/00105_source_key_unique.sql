-- +goose Up
-- One live want per address (ADR 0040 §1). Two entries keyed to one address
-- are one want, as two entries that resolve to one recording are (00024). The
-- index has the shape of acquisition_targets_recording_idx: a superseded row
-- keeps its key and points at the survivor, so it is exempt.
--
-- Wants already keyed twice are merged first. The survivor is the want that
-- holds the track, then a want still looking, then the oldest. The others are
-- superseded by it and keep their key, their copies and their history.
WITH ranked AS (
    SELECT id,
           first_value(id) OVER key_group AS survivor_id,
           row_number() OVER key_group AS rank
    FROM acquisition_targets
    WHERE source IS NOT NULL AND status <> 'superseded'
    WINDOW key_group AS (
        PARTITION BY source, external_id
        ORDER BY CASE status
                     WHEN 'acquired' THEN 0
                     WHEN 'not_wanted' THEN 2
                     ELSE 1
                 END,
                 created_at, id
    )
)
UPDATE acquisition_targets targets
SET status = 'superseded',
    superseded_by_id = ranked.survivor_id,
    review_reason = NULL,
    not_wanted_at = NULL,
    next_attempt_at = NULL,
    next_search_at = NULL,
    summary = 'Another wishlist entry is keyed to the same address. That entry is looked for instead.',
    updated_at = now()
FROM ranked
WHERE targets.id = ranked.id
  AND ranked.rank > 1;

CREATE UNIQUE INDEX acquisition_targets_source_key_idx
    ON acquisition_targets (source, external_id)
    WHERE source IS NOT NULL AND status <> 'superseded';

-- +goose Down
-- Wants merged on the way up stay superseded: nothing records which of them
-- were merged here rather than by keying.
DROP INDEX IF EXISTS acquisition_targets_source_key_idx;
