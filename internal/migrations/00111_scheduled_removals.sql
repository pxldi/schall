-- +goose Up
-- Removing a song takes away the want at once and deletes the file Schall
-- downloaded for it 30 days later. Pressing want again inside those 30 days
-- keeps the file.
--
-- A row here is the licence ADR 0021 asks for: the person's decision, written
-- down when they pressed remove and before anything is unlinked. One row per
-- file. A file somebody put in the library by hand never gets a row, because
-- the row is only written for a file a download or an accepted copy brought in.
CREATE TABLE scheduled_removals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The want that was taken away. A want is never deleted while something
    -- says why a file went, so this restricts.
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE RESTRICT,
    -- The music that was removed. Empty for a want keyed to a source track,
    -- which names no recording.
    musicbrainz_recording_id UUID,
    -- The file, and its path as a plain value: the library row is gone by the
    -- time a removed row is read again.
    library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    library_path TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    removes_at TIMESTAMPTZ NOT NULL,
    -- scheduled  waiting for removes_at
    -- cancelled  the music was wanted again first
    -- removed    Schall deleted the file
    -- spared     the day came and something else still needed the file
    -- gone       the file left the library by another hand first
    state TEXT NOT NULL DEFAULT 'scheduled',
    settled_at TIMESTAMPTZ,
    settled_reason TEXT NOT NULL DEFAULT '',
    CONSTRAINT scheduled_removals_state_valid
        CHECK (state IN ('scheduled', 'cancelled', 'removed', 'spared', 'gone')),
    CONSTRAINT scheduled_removals_path_present
        CHECK (btrim(library_path) <> ''),
    CONSTRAINT scheduled_removals_after_request
        CHECK (removes_at > requested_at),
    CONSTRAINT scheduled_removals_settled_stamped
        CHECK ((state = 'scheduled') = (settled_at IS NULL)),
    -- A file is only ever deleted on a day the person was shown.
    CONSTRAINT scheduled_removals_removed_when_due
        CHECK (state <> 'removed' OR settled_at >= removes_at)
);

-- One live schedule per file. Removing the same song twice keeps the first
-- date.
CREATE UNIQUE INDEX scheduled_removals_one_live_idx
    ON scheduled_removals (library_file_id)
    WHERE state = 'scheduled';

-- The sweep's question: what is due.
CREATE INDEX scheduled_removals_due_idx
    ON scheduled_removals (removes_at)
    WHERE state = 'scheduled';

-- The pages' question: is this recording's file leaving, and when.
CREATE INDEX scheduled_removals_recording_idx
    ON scheduled_removals (musicbrainz_recording_id)
    WHERE state = 'scheduled';

-- +goose Down
DROP TABLE IF EXISTS scheduled_removals;
