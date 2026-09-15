-- +goose Up
-- A library file is one audio file on disc that Schall has indexed. The
-- duplicates screen lists the recordings the library holds more than one file
-- for, and a person reading it can now press "Keep this copy": Schall deletes
-- the other present copies of that recording, from disc and from the database.
--
-- Deleting music is the one thing Schall does that cannot be undone, and
-- docs/decisions/0021 says removal must be licensed — nothing is deleted
-- without a row that says why. The weekly playlist's lease is that licence for
-- a song obtained on trial, and it cannot serve here: a lease names the weekly
-- run and the want that granted it, both NOT NULL, and a person clearing a
-- duplicate has neither.
--
-- This table is the licence for that press. One row per deleted file, and the
-- row is committed with the delete of the library_files row and BEFORE the
-- audio is unlinked. That order is the whole point of the table: the record
-- exists before the irreversible step, so no unlink can happen that nothing
-- accounts for. A crash between the two leaves the row here saying what was
-- being done, with unlinked_at still empty, and the audio still on disc.
--
-- One file is one transaction. Deleting three copies is three commits, not one:
-- the second file's unlink failing must not take back the first file's record
-- of an unlink that already happened.
CREATE TABLE library_file_removals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- What was deleted, as plain values. The library_files row is gone by the
    -- time this row is committed, so a foreign key here would hold nothing;
    -- library_file_leases copies its path for the same reason.
    library_file_id UUID NOT NULL,
    library_path TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    -- The music this was a copy of, and the copy that stays. The survivor is a
    -- file that still exists, so it is referenced; if it is deleted later the
    -- reference falls away and the path still says which copy it was.
    musicbrainz_recording_id UUID NOT NULL,
    kept_library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    kept_path TEXT NOT NULL,
    -- Who asked and why, in words rather than a code. Schall is single-user, so
    -- "the person at the screen" is the only actor there is; what is worth
    -- recording is which screen and which press, because a second path to
    -- deletion must not be readable as this one.
    removed_by TEXT NOT NULL,
    reason TEXT NOT NULL,
    removed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- When the audio actually left the disc, which is a separate fact from the
    -- library row going. Empty means the unlink has not succeeded: either it
    -- has not been tried yet, or it failed and unlink_error says how. Those
    -- rows are what the retry reads, and what the duplicates screen names as
    -- still on disc.
    unlinked_at TIMESTAMPTZ,
    unlink_error TEXT NOT NULL DEFAULT '',
    CONSTRAINT library_file_removals_path_present
        CHECK (btrim(library_path) <> ''),
    CONSTRAINT library_file_removals_kept_path_present
        CHECK (btrim(kept_path) <> ''),
    CONSTRAINT library_file_removals_actor_present
        CHECK (btrim(removed_by) <> '' AND btrim(reason) <> ''),
    -- The copy that stays is never the copy that goes.
    CONSTRAINT library_file_removals_kept_is_not_removed
        CHECK (kept_library_file_id IS NULL
               OR kept_library_file_id <> library_file_id),
    -- A file that is gone has nothing left to complain about.
    CONSTRAINT library_file_removals_error_only_while_present
        CHECK (unlinked_at IS NULL OR btrim(unlink_error) = '')
);

-- The retry's whole question: which audio the library has let go of and not yet
-- managed to delete. Almost every row is settled, so the settled ones are left
-- out of the index rather than walked past.
CREATE INDEX library_file_removals_awaiting_unlink_idx
    ON library_file_removals (removed_at)
    WHERE unlinked_at IS NULL;

-- The question asked of this table is "what happened to the copies of this
-- recording", newest first.
CREATE INDEX library_file_removals_recording_idx
    ON library_file_removals (musicbrainz_recording_id, removed_at DESC);

-- +goose Down
DROP TABLE IF EXISTS library_file_removals;
