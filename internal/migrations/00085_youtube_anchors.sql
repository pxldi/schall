-- +goose Up
-- An anchor is the audio a want is proven against (migration 00058). Until now
-- it was only ever a distributor's preview, fetched by the want's own ISRC.
--
-- Most of the music the review queue holds has no ISRC. On 2026-09-05 the queue
-- held 216 copies on 75 wants, 71 of them with no ISRC on the entry and none on
-- MusicBrainz: leaks, sessions and unreleased tracks. Deezer has nothing for
-- them and AcoustID has never heard them, so every copy waits for a person.
--
-- For those wants the reference is a YouTube upload, chosen by the want's name
-- and length and by nothing else, and the most-viewed upload that matches both
-- is the one. It cannot be fetched by a key, so it is searched for, and the two
-- columns below are what makes that choice checkable afterwards: which upload
-- was picked, and how popular it was when it was picked. See
-- docs/decisions/0029-a-popular-upload-found-by-name-is-an-audio-anchor.md.
ALTER TABLE acquisition_targets
    -- The upload's own title and the channel that published it, which is what a
    -- person reads instead of a video id.
    ADD COLUMN anchor_label TEXT,
    -- How many views the upload had when it was chosen. Provenance and nothing
    -- else: no rule reads it after the choice is made.
    ADD COLUMN anchor_views BIGINT;

-- The wants that were written off before there was a second place to look.
-- Each of these three sentences is the preview path saying it had nothing, and
-- that is now the start of the upload path rather than the end of the search, so
-- they go back on the schedule. Only wants still looking for music are reopened,
-- and only wants that never got an anchor from anywhere.
UPDATE acquisition_targets
SET anchor_unavailable = NULL,
    anchor_next_attempt_at = now(),
    updated_at = now()
WHERE anchor_fingerprint IS NULL
  AND anchor_unavailable IN (
      'Deezer publishes no preview for this recording''s ISRC.',
      'This entry carries no ISRC, and a preview is only ever fetched by ISRC.',
      'This entry''s ISRC is not a registration code a preview can be fetched by.'
  )
  AND status IN ('unresolved', 'pending', 'searching', 'awaiting_review');

-- The wants that were never put in line at all, because a want created without
-- an ISRC was not scheduled (ScheduleAnchor asked for one until now). They join
-- the schedule on the same terms.
UPDATE acquisition_targets
SET anchor_next_attempt_at = now(),
    updated_at = now()
WHERE anchor_fingerprint IS NULL
  AND anchor_unavailable IS NULL
  AND anchor_next_attempt_at IS NULL
  AND status IN ('unresolved', 'pending', 'searching', 'awaiting_review');

-- +goose Down
ALTER TABLE acquisition_targets
    DROP COLUMN IF EXISTS anchor_label,
    DROP COLUMN IF EXISTS anchor_views;
