-- +goose Up
-- Where Schall files the music it imports, in the shape slskd_settings (00005)
-- and navidrome_settings (00036) set.
--
-- One row, because the layout describes how this installation files everything
-- it keeps, not how one root is arranged. A second root is another place music
-- lives, not another way of arranging it.
--
-- The template names folders and only folders. A file keeps the name it
-- arrived with — the peer's, or the disc's — copied verbatim by both importers
-- today and not rewritten here, so nothing in this table can rename a file.
--
-- And the layout decides nothing about the music. No part of matching or
-- identity reads a path: naming a file after a song has never made it one, and
-- moving it somewhere else does not stop it being one.
CREATE TABLE library_layout_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    -- The folder template. There is deliberately no column default: what
    -- Schall files music as when nobody has said otherwise is
    -- library.DefaultTemplate, and a default written here as well would be a
    -- second answer to the same question, free to drift from the first. An
    -- absent row means the default, which is exactly what both importers build
    -- today — so an installation that never opens this setting keeps filing
    -- music where it always has, and a first migration run against an
    -- untouched template moves nothing.
    template TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT library_layout_settings_single_row CHECK (singleton),
    CONSTRAINT library_layout_settings_template_present
        CHECK (btrim(template) <> ''),
    -- A backstop under the validation the API does, not a replacement for it.
    -- A template is turned into a path, and a template that begins at the root
    -- or climbs out of it writes music outside the library. The real check
    -- names the offending part; this one only makes the state unreachable.
    CONSTRAINT library_layout_settings_template_relative
        CHECK (template NOT LIKE '/%' AND template NOT LIKE '%..%')
);

-- The journal a layout migration writes.
--
-- Moving a library is tens of thousands of renames and is not one transaction.
-- A row is written before its rename and settled after it, so a run that is
-- interrupted leaves a library that can be described — every file is either
-- where it was or where it was going, and which is which is written down —
-- rather than one silently in two layouts.
--
-- It records; it decides nothing, and nothing reads it to work out what a file
-- is. from_path is also the whole undo instruction, which is why there is no
-- separate table of runs: reversing a run is renaming every moved row back,
-- and that needs no memory of the template it was migrating away from.
CREATE TABLE library_file_moves (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The run this move belongs to, so one migration is resumable, reportable
    -- and reversible as a unit.
    run_id UUID NOT NULL,
    library_file_id UUID NOT NULL REFERENCES library_files(id) ON DELETE CASCADE,
    from_path TEXT NOT NULL,
    to_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'planned',
    error TEXT,
    planned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    moved_at TIMESTAMPTZ,
    CONSTRAINT library_file_moves_status_valid
        CHECK (status IN ('planned', 'moved', 'failed', 'reverted')),
    -- A move to where the file already is is not a move. Planning one would
    -- make a no-op run look like work and a rename onto itself look like a
    -- failure.
    CONSTRAINT library_file_moves_paths_differ CHECK (from_path <> to_path),
    -- A file that was never renamed has no time it was renamed at. A reverted
    -- move was renamed twice and keeps the first, because that is the one the
    -- journal is a record of.
    CONSTRAINT library_file_moves_settled
        CHECK ((status IN ('moved', 'reverted')) = (moved_at IS NOT NULL)),
    -- A reason recorded against a move that did not fail is a contradiction.
    CONSTRAINT library_file_moves_error_explains_failure
        CHECK (status = 'failed' OR error IS NULL)
);

-- Resuming a run is reading back its unfinished rows.
CREATE INDEX library_file_moves_run_idx ON library_file_moves (run_id, status);

-- One open move per file, in the shape download_requests_open_target_idx set:
-- a second run started while one is in flight has to find the open move rather
-- than plan a rename that would race the first one to the same file.
CREATE UNIQUE INDEX library_file_moves_open_file_idx
    ON library_file_moves (library_file_id)
    WHERE status = 'planned';

-- +goose Down
DROP TABLE IF EXISTS library_file_moves;
DROP TABLE IF EXISTS library_layout_settings;
