-- +goose Up
-- The anchor sweep now follows the pages MusicBrainz links a recording to (a
-- Deezer track, an Apple song, a Bandcamp track or album) before it searches
-- uploads by name. Wants with no anchor that hold a copy waiting in Review are
-- asked once more under the new reading. Other wants meet it on their next
-- scheduled attempt.
UPDATE acquisition_targets
SET anchor_next_attempt_at = now(),
    updated_at = now()
WHERE acquired_at IS NULL
  AND not_wanted_at IS NULL
  AND superseded_by_id IS NULL
  AND source IS NULL
  AND musicbrainz_recording_id IS NOT NULL
  AND anchor_fingerprint IS NULL
  AND EXISTS (
      SELECT 1 FROM acquisition_target_files held
      WHERE held.acquisition_target_id = acquisition_targets.id
        AND held.verdict = 'held'
  );

-- +goose Down
