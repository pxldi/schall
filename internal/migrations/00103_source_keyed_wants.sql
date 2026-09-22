-- +goose Up
-- A person can key a want to the address of a track on SoundCloud, YouTube or
-- Bandcamp (ADR 0038). The key has the shape a source identity got in 00066,
-- and a file admitted for the want is written a source identity with the same
-- three values. A want keyed this way is never resolved against MusicBrainz:
-- the person said what it is.
ALTER TABLE acquisition_targets
    ADD COLUMN source TEXT,
    ADD COLUMN external_id TEXT,
    ADD COLUMN external_url TEXT,
    -- What yt-dlp said about the address when the person confirmed it: title,
    -- artist, uploader and account, length and artwork. ADR 0038 §5 names the
    -- admitted file with these, and they are kept because the file arrives
    -- weeks later and a second lookup could answer differently.
    ADD COLUMN source_lookup JSONB,
    -- The lowest bit rate, in kbit/s, a copy proven by its audio may have
    -- before it is held instead of admitted (ADR 0038 §7). Keying writes 320.
    ADD COLUMN minimum_bitrate INTEGER;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_source_key_whole
        CHECK ((source IS NULL) = (external_id IS NULL)
               AND (source IS NULL) = (external_url IS NULL)),
    ADD CONSTRAINT acquisition_targets_source_key_valid
        CHECK (source IS NULL
               OR (source IN ('soundcloud', 'youtube', 'bandcamp')
                   AND btrim(external_id) <> ''
                   AND btrim(external_url) <> '')),
    ADD CONSTRAINT acquisition_targets_source_or_recording
        CHECK (source IS NULL OR musicbrainz_recording_id IS NULL),
    -- Resolution never asks MusicBrainz about a keyed want again, so a keyed
    -- want has no resolution schedule. A statement that gave it one fails here
    -- instead of waking the sweeper for a want nothing will select.
    ADD CONSTRAINT acquisition_targets_source_unscheduled
        CHECK (source IS NULL OR next_attempt_at IS NULL),
    ADD CONSTRAINT acquisition_targets_source_lookup_keyed
        CHECK (source_lookup IS NULL OR source IS NOT NULL),
    ADD CONSTRAINT acquisition_targets_minimum_bitrate_keyed
        CHECK ((source IS NULL) = (minimum_bitrate IS NULL)),
    ADD CONSTRAINT acquisition_targets_minimum_bitrate_positive
        CHECK (minimum_bitrate IS NULL OR minimum_bitrate > 0);

-- The excerpt fetched from the address admits a copy, so a keyed want may be
-- acquired with no recording, as a Deezer or Topic anchored one may.
ALTER TABLE acquisition_targets
    DROP CONSTRAINT acquisition_targets_acquired_identified;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_acquired_identified
        CHECK (status <> 'acquired'
               OR musicbrainz_recording_id IS NOT NULL
               OR anchor_source IN ('deezer', 'youtube-topic', 'source'));

-- +goose Down
-- Keyed wants go back to waiting to be resolved, with a resolution schedule,
-- and lose the excerpt anchor, which the code before this migration cannot
-- read. They are put in line for an anchor of the older kind. The files'
-- source identities stay: each is a valid 00066 identity with is_manual set.
-- The key's own constraints go first, because giving a keyed want a
-- resolution schedule is exactly what one of them refuses.
ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_minimum_bitrate_positive,
    DROP CONSTRAINT IF EXISTS acquisition_targets_minimum_bitrate_keyed,
    DROP CONSTRAINT IF EXISTS acquisition_targets_source_lookup_keyed,
    DROP CONSTRAINT IF EXISTS acquisition_targets_source_unscheduled,
    DROP CONSTRAINT IF EXISTS acquisition_targets_source_or_recording,
    DROP CONSTRAINT IF EXISTS acquisition_targets_source_key_valid,
    DROP CONSTRAINT IF EXISTS acquisition_targets_source_key_whole;

UPDATE acquisition_targets
SET status = 'unresolved',
    review_reason = NULL,
    acquired_at = NULL,
    acquired_library_file_id = NULL,
    next_attempt_at = now(),
    next_search_at = NULL,
    search_attempts = 0,
    anchor_fingerprint = NULL,
    anchor_source = NULL,
    anchor_reference = NULL,
    anchor_seconds = NULL,
    anchor_label = NULL,
    anchor_views = NULL,
    anchor_fetched_at = NULL,
    anchor_unavailable = NULL,
    anchor_next_attempt_at = now(),
    updated_at = now()
WHERE source IS NOT NULL
  AND status IN ('unresolved', 'acquired');

UPDATE acquisition_targets
SET anchor_fingerprint = NULL,
    anchor_source = NULL,
    anchor_reference = NULL,
    anchor_seconds = NULL,
    anchor_label = NULL,
    anchor_views = NULL,
    anchor_fetched_at = NULL,
    anchor_unavailable = NULL,
    anchor_next_attempt_at = now(),
    updated_at = now()
WHERE anchor_source = 'source';

ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_acquired_identified;

ALTER TABLE acquisition_targets
    ADD CONSTRAINT acquisition_targets_acquired_identified
        CHECK (status <> 'acquired'
               OR musicbrainz_recording_id IS NOT NULL
               OR anchor_source IN ('deezer', 'youtube-topic'));

ALTER TABLE acquisition_targets
    DROP COLUMN IF EXISTS minimum_bitrate,
    DROP COLUMN IF EXISTS source_lookup,
    DROP COLUMN IF EXISTS external_url,
    DROP COLUMN IF EXISTS external_id,
    DROP COLUMN IF EXISTS source;
