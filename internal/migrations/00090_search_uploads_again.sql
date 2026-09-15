-- +goose Up
-- The chooser now reads featuring credits and artist-title dashes that older
-- searches rejected. Give open wants written off for that exact reason one
-- search under the new reading.
UPDATE acquisition_targets
SET anchor_unavailable = NULL,
    anchor_next_attempt_at = now(),
    updated_at = now()
WHERE acquired_at IS NULL
  AND not_wanted_at IS NULL
  AND superseded_by_id IS NULL
  AND anchor_fingerprint IS NULL
  AND anchor_unavailable LIKE '%No upload matched this entry''s name and length.'
  AND status IN ('unresolved', 'pending', 'searching', 'awaiting_review');

-- +goose Down
