-- +goose Up
-- Music bought from a store, ripped from a disc, or dropped into a watched
-- folder is owned music. Until now Schall could only describe such a file
-- through the catalogue of a followed artist, so a file nobody followed was
-- indistinguishable from a file whose identity had failed: both were merely
-- 'unmatched'. Resolution is the missing half. It asks the metadata providers
-- what one file is, without requiring anyone to follow anything first.
--
-- The states are the ones the requirement names, and they are genuinely
-- different questions:
--
--   pending      nothing has been asked yet
--   resolved     one external identity is proven for this file
--   needs_review plausible identities exist, none of them conclusive
--   conflict     the file's own evidence disagrees with what was found
--   local_only   the providers know nothing; the file is owned all the same
--   failed       the provider could not be reached; this is about Schall,
--                not about the music
--
-- local_only is a statement about the provider, never about the library. An
-- unresolved file is still owned music and still counts against a download.
-- resolution_summary says in one sentence why the file is in the state it is
-- in, for every state. A status nobody can explain is a status nobody trusts.
-- resolution_error is kept separate and is only ever about Schall failing to
-- ask, never about the answer.
ALTER TABLE library_files
    ADD COLUMN resolution_status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN resolution_summary TEXT,
    ADD COLUMN resolution_attempted_at TIMESTAMPTZ,
    ADD COLUMN resolution_error TEXT,
    ADD CONSTRAINT library_files_resolution_status_valid
        CHECK (resolution_status IN (
            'pending', 'resolved', 'needs_review', 'conflict', 'local_only', 'failed'
        ));

-- The files still to be asked about. A provider that could not be reached is
-- among them: that failure is about Schall, and an outage must not become a
-- permanent state for the music it interrupted.
CREATE INDEX library_files_resolution_unasked_idx
    ON library_files (created_at)
    WHERE missing_at IS NULL AND resolution_status IN ('pending', 'failed');

-- The identity of a file is what the providers proved about it, and it is kept
-- apart from track_mappings on purpose. A mapping ties a file to a catalogue
-- track the local library happens to hold; an identity says what the recording
-- is, whether or not any followed artist ever brings it into the catalogue.
-- Learning the identity first is what lets a file resolved today be matched
-- automatically the day its release is ingested.
--
-- kind is 'external' for a proven provider identity and 'local_only' for a
-- decision that no external identity exists. Both are decisions worth keeping:
-- the second one is how the user stops being asked.
CREATE TABLE library_file_identities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    library_file_id UUID NOT NULL UNIQUE REFERENCES library_files(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'musicbrainz',
    musicbrainz_recording_id UUID,
    musicbrainz_release_group_id UUID,
    artist_name TEXT,
    release_title TEXT,
    track_title TEXT,
    duration_ms INTEGER,
    isrc TEXT,
    method TEXT NOT NULL,
    confidence REAL,
    is_manual BOOLEAN NOT NULL DEFAULT false,
    -- summary reads as a sentence and evidence lists what agreed, so a stored
    -- identity can always be read back as the reason it was accepted.
    summary TEXT NOT NULL DEFAULT '',
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT library_file_identities_kind_valid
        CHECK (kind IN ('external', 'local_only')),
    -- An external identity without a recording identifies nothing.
    CONSTRAINT library_file_identities_external_identified
        CHECK (kind <> 'external' OR musicbrainz_recording_id IS NOT NULL),
    -- A local-only decision that carries a recording contradicts itself.
    CONSTRAINT library_file_identities_local_only_bare
        CHECK (kind <> 'local_only' OR musicbrainz_recording_id IS NULL),
    CONSTRAINT library_file_identities_confidence_range
        CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1)
);

CREATE INDEX library_file_identities_recording_idx
    ON library_file_identities (musicbrainz_recording_id)
    WHERE musicbrainz_recording_id IS NOT NULL;

-- The candidates behind a file that was not resolved. They are rewritten by
-- every attempt, because they describe what the providers say now, and they
-- exist so that a question can be answered rather than merely reported.
CREATE TABLE library_file_candidates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    library_file_id UUID NOT NULL REFERENCES library_files(id) ON DELETE CASCADE,
    provider TEXT NOT NULL DEFAULT 'musicbrainz',
    musicbrainz_recording_id UUID NOT NULL,
    musicbrainz_release_group_id UUID,
    artist_name TEXT NOT NULL DEFAULT '',
    release_title TEXT,
    track_title TEXT NOT NULL,
    duration_ms INTEGER,
    isrc TEXT,
    rank INTEGER NOT NULL,
    agrees TEXT[] NOT NULL DEFAULT '{}',
    differs TEXT[] NOT NULL DEFAULT '{}',
    summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (library_file_id, musicbrainz_recording_id),
    CONSTRAINT library_file_candidates_rank_positive CHECK (rank > 0)
);

CREATE INDEX library_file_candidates_file_idx
    ON library_file_candidates (library_file_id, rank);

-- One resolution per file at a time. Resolution costs provider requests, and a
-- scan that rediscovers the same file must not queue the same question twice.
CREATE UNIQUE INDEX jobs_active_file_resolution_idx
    ON jobs (kind, (payload->>'fileId'))
    WHERE kind = 'resolve_library_file' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_file_resolution_idx;
DROP TABLE IF EXISTS library_file_candidates;
DROP TABLE IF EXISTS library_file_identities;
DROP INDEX IF EXISTS library_files_resolution_unasked_idx;
ALTER TABLE library_files
    DROP CONSTRAINT IF EXISTS library_files_resolution_status_valid,
    DROP COLUMN IF EXISTS resolution_error,
    DROP COLUMN IF EXISTS resolution_attempted_at,
    DROP COLUMN IF EXISTS resolution_summary,
    DROP COLUMN IF EXISTS resolution_status;
