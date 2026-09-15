-- +goose Up
-- One pass over the download inbox, and what it did to every file it looked at.
--
-- The inbox is the folder slskd writes completed downloads into. It was mounted
-- read-only until 2026-09-10, so every import copied its source and left it, and
-- the library now holds a second copy of everything that was ever imported. The
-- retention setting removes each import's source from now on; it does not touch
-- the backlog, the refused copies, or the folders Schall never recorded. This is
-- what does.
--
-- The rule is in docs/decisions/0036. Nothing here is decided by similarity: a
-- file is kept when a record says something still needs it, and deleted when a
-- record says nothing does.
CREATE TABLE inbox_cleanups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Who asked. A deletion of somebody's music is a person's decision and the
    -- record says whose, in the spirit of docs/decisions/0021.
    requested_by TEXT NOT NULL DEFAULT '',
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A dry run classifies every file and deletes none of them.
    dry_run BOOLEAN NOT NULL,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    -- One entry per class: {"imported": {"files": 12, "bytes": 3400}}. A class
    -- added later needs no migration, and the file rows below are the record
    -- these numbers are a summary of.
    counts JSONB NOT NULL DEFAULT '{}'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    CONSTRAINT inbox_cleanups_started_before_finished
        CHECK (finished_at IS NULL OR started_at IS NOT NULL),
    -- A run that failed says so and is over. A run still going has nothing to
    -- report yet.
    CONSTRAINT inbox_cleanups_failure_is_finished
        CHECK (btrim(error) = '' OR finished_at IS NOT NULL)
);

CREATE INDEX inbox_cleanups_recent_idx ON inbox_cleanups (requested_at DESC);

-- One pass at a time. Two passes would walk the same folders and race each
-- other's unlinks, and the check in the repository is a read followed by a
-- write, which two requests a millisecond apart both pass.
CREATE UNIQUE INDEX inbox_cleanups_live_idx
    ON inbox_cleanups ((finished_at IS NULL))
    WHERE finished_at IS NULL;

-- Every file the pass looked at, with the class it was put in and when it was
-- unlinked. The row is written before the unlink, never after: a crash between
-- the two leaves a row saying the file was doomed and is still there, which is
-- readable, and the opposite order would delete music the record cannot name.
--
-- deleted_at is NULL for a dry run and for every kept file.
CREATE TABLE inbox_cleanup_files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cleanup_id UUID NOT NULL REFERENCES inbox_cleanups(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    class TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    deleted_at TIMESTAMPTZ,
    CONSTRAINT inbox_cleanup_files_class_valid CHECK (class IN (
        -- The four classes that are deleted.
        --
        -- The source of an import that completed, whose managed copy was
        -- confirmed on disc first.
        'imported',
        -- A copy judged to be other audio, or contradicted by its own tags.
        'refused',
        -- A copy of a want that is finished with: acquired, not wanted, or
        -- superseded.
        'settled',
        -- No download request and no copy resolves to it. Folders fetched by
        -- hand in slskd land here, and the owner asked for them to go too.
        'unknown',

        -- And the five that are kept.
        --
        -- A held copy of a want still pending, searching, unresolved, or
        -- waiting for an answer. These are the review questions.
        'kept_question',
        -- A download request still transferring, waiting to import, in review,
        -- or failed where a person can retry it.
        'kept_open',
        -- Changed in the last 24 hours. A transfer slskd has not reported yet
        -- must not be deleted under a record that does not mention it.
        'kept_recent',
        -- An import that completed whose managed copy could not be confirmed.
        -- The library cannot answer for this recording, so the inbox keeps it.
        'kept_unconfirmed',
        -- A record resolves to it and no rule above covers it: a cancelled
        -- request, or a copy still being fetched. Kept because the rule says
        -- nothing about it.
        'kept_undecided',
        -- Chosen for deletion and refused at the last moment, because the file
        -- or the folder holding it was no longer an ordinary path inside the
        -- inbox. The walk and the unlink are minutes apart on a folder of
        -- 36,000 files, and a symlink put there in between would take the
        -- deletion outside the inbox.
        'kept_unsafe_path'
    )),
    CONSTRAINT inbox_cleanup_files_path_present CHECK (btrim(path) <> ''),
    CONSTRAINT inbox_cleanup_files_size_nonnegative CHECK (size_bytes >= 0),
    -- Only a delete class is ever unlinked.
    CONSTRAINT inbox_cleanup_files_deleted_class
        CHECK (deleted_at IS NULL
               OR class IN ('imported', 'refused', 'settled', 'unknown'))
);

-- One row per file per pass. A pass a crash interrupted is safe to run again,
-- and it must not write a second row for a file the first attempt already
-- recorded and deleted.
CREATE UNIQUE INDEX inbox_cleanup_files_cleanup_idx
    ON inbox_cleanup_files (cleanup_id, path);

CREATE INDEX inbox_cleanup_files_class_idx
    ON inbox_cleanup_files (cleanup_id, class);

-- +goose Down
DROP INDEX IF EXISTS inbox_cleanup_files_class_idx;
DROP INDEX IF EXISTS inbox_cleanup_files_cleanup_idx;
DROP TABLE IF EXISTS inbox_cleanup_files;
DROP INDEX IF EXISTS inbox_cleanups_live_idx;
DROP INDEX IF EXISTS inbox_cleanups_recent_idx;
DROP TABLE IF EXISTS inbox_cleanups;
