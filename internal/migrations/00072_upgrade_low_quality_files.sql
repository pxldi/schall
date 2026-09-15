-- +goose Up
-- A file can be imported before a bit-rate floor is set, or before it is
-- raised. The floor already refuses a source candidate under it
-- (internal/sources/preferences.go, #428); nothing until now looked back at
-- what the library already holds.
--
-- Whether that look runs at all, on a switch shipped off: a sweep that leads
-- to music being deleted is not one an installation gets by upgrading. See
-- weekly_playlist_settings (00052) for the same reasoning.
CREATE TABLE upgrade_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    enabled BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT upgrade_settings_single_row CHECK (singleton)
);

-- A want raised by the upgrade sweep, and the file it exists to replace. It is
-- provenance and a pointer together: the origin says the sweep asked for this
-- rather than a person, and the file is what the ordinary acquisition loop's
-- proof — AcoustID, the anchor, or a settled library witness — will be judged
-- against once a copy of the recording turns up. Nothing about how the want is
-- pursued changes: the floor that refused this file for being under it also
-- refuses any candidate under it, so nothing here can fetch a copy no better
-- than what is already on disc.
ALTER TABLE acquisition_targets
    ADD COLUMN upgrade_of_library_file_id UUID
        REFERENCES library_files(id) ON DELETE SET NULL;

ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_origin_valid,
    ADD CONSTRAINT acquisition_targets_origin_valid
        CHECK (origin IN ('manual', 'playlist', 'follow_feed', 'recommendation', 'label_feed', 'upgrade')),
    ADD CONSTRAINT acquisition_targets_upgrade_names_a_file
        CHECK (upgrade_of_library_file_id IS NULL OR origin = 'upgrade');

-- One open upgrade want per file. The sweep walks the library in batches and a
-- second pass must never raise a second want for a file the first pass already
-- asked about, whatever else changed underneath it.
CREATE UNIQUE INDEX acquisition_targets_upgrade_of_file_idx
    ON acquisition_targets (upgrade_of_library_file_id)
    WHERE upgrade_of_library_file_id IS NOT NULL
      AND status NOT IN ('not_wanted', 'superseded');

-- One sweep at a time, in the shape the spectrum and loudness sweeps already
-- use (00060, 00067).
CREATE UNIQUE INDEX jobs_upgrade_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_upgrade_candidates' AND status IN ('queued', 'running');

-- When a fetched copy was compared against this file and turned out no
-- better: a lower bit rate, or the same format at the same bit rate. That
-- copy is discarded rather than kept as a second file (it was never wanted as
-- a copy; the want wanted an upgrade), and this file stands back from being
-- offered as a candidate again for a while. Without a stand-back, a
-- recording whose only copies on offer are no better than what is already
-- held would be searched again on every single pass — and Soulseek bans a
-- client that searches the same thing repeatedly (issue #193's shape).
--
-- It is cleared by nothing automatic: a fresh scan of the file, or a
-- genuinely better copy landing, both leave it exactly where it was, because
-- both cases mean the file is no longer a candidate at all (its bit rate
-- changed, or it went). It only ever matters while the file is still under
-- the floor, which is what UpgradeCandidates reads it alongside.
ALTER TABLE library_files ADD COLUMN upgrade_refused_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE library_files DROP COLUMN IF EXISTS upgrade_refused_at;
DROP INDEX IF EXISTS jobs_upgrade_sweep_idx;
DROP INDEX IF EXISTS acquisition_targets_upgrade_of_file_idx;
ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_upgrade_names_a_file,
    DROP CONSTRAINT IF EXISTS acquisition_targets_origin_valid,
    ADD CONSTRAINT acquisition_targets_origin_valid
        CHECK (origin IN ('manual', 'playlist', 'follow_feed', 'recommendation', 'label_feed'));
ALTER TABLE acquisition_targets DROP COLUMN IF EXISTS upgrade_of_library_file_id;
DROP TABLE IF EXISTS upgrade_settings;
