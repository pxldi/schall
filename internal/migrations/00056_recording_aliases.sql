-- +goose Up
-- The recording identifiers MusicBrainz has merged away, and what each one
-- answers to now.
--
-- MusicBrainz is the metadata service Schall reads its catalogue from. A
-- recording there is one performance of one piece of music, and it has an
-- identifier. MusicBrainz merges recordings: two rows that turn out to describe
-- one performance become one, the surviving identifier answers for both, and the
-- retired one keeps resolving at the API through a redirect. A file tagged
-- before a merge carries the retired identifier for ever; a catalogue refreshed
-- after it holds the surviving one.
--
-- Schall compares recording identifiers in three places. Identity resolution
-- already copes, because it is Go code holding one file and can ask the provider
-- which recording an identifier names now. The other two cannot ask anybody: the
-- library join is one SQL statement over the whole library, and import
-- validation compares two strings. Whatever they compare against has to already
-- be in the database, which is what this table is.
--
-- A row is written at one moment and one moment only: when Schall asked
-- MusicBrainz which recording an identifier names and MusicBrainz answered with
-- a different one. That is the provider stating the equivalence in its own
-- words, which is exactly the evidence the identity path already treats as
-- conclusive. Nothing else may write a row — not a similarity, not a heuristic,
-- not a user action. This adds no new kind of proof; it repairs one that is
-- already accepted.
--
-- Two things it deliberately does not do. It never makes two different
-- recordings agree: an identifier with no row here is compared exactly as it is
-- today, so a genuinely different recording still contradicts and still voids
-- the pair. And it never removes evidence: every pair that concludes today
-- concludes afterwards, by the same rule.
CREATE TABLE recording_aliases (
    -- The identifier a file may still carry. It is the primary key because one
    -- retired identifier resolves to one recording — that is what a merge is.
    retired_id UUID PRIMARY KEY,

    -- What MusicBrainz answers with today.
    surviving_id UUID NOT NULL,

    -- When the provider said so. Kept because this is a copy of somebody else's
    -- answer: if MusicBrainz ever splits a recording apart again, the age of the
    -- row is the only thing that says how long ago it was true.
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A recording that is its own alias says nothing and would make the
    -- canonical lookup a loop.
    CONSTRAINT recording_aliases_not_itself CHECK (retired_id <> surviving_id)
);

-- Read in the other direction by anything asking "which identifiers answer for
-- this recording", which is how a track finds the files tagged before a merge.
CREATE INDEX recording_aliases_surviving_idx ON recording_aliases (surviving_id);

-- +goose StatementBegin
-- Which recording an identifier names today. An identifier nothing was merged
-- into is its own answer, so every caller gets a value back and none of them has
-- to write the coalesce out again.
CREATE FUNCTION canonical_recording_id(id UUID) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT coalesce(
        (SELECT surviving_id FROM recording_aliases WHERE retired_id = id),
        id)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Every identifier that answers for one recording: the surviving one, and each
-- one merged into it.
--
-- This is the shape the comparisons use rather than canonical_recording_id on
-- both sides, and the reason is the index. `tracks.musicbrainz_recording_id` is
-- indexed; wrapping that column in a function throws the index away and turns
-- one lookup into a scan of every track in the catalogue. Comparing the indexed
-- column against a small set of identifiers keeps the lookup, and the set is
-- small by construction — it is one recording and its merges.
--
-- It is symmetric on purpose. A file tagged before a merge holds the retired
-- identifier while the catalogue holds the survivor, and a catalogue refreshed
-- before a merge holds the retired one while a freshly tagged file holds the
-- survivor. Both are the same recording and both have to meet.
CREATE FUNCTION recording_id_aliases(id UUID) RETURNS SETOF UUID
LANGUAGE sql STABLE AS $$
    WITH canonical AS (SELECT canonical_recording_id(id) AS id)
    SELECT canonical.id FROM canonical WHERE canonical.id IS NOT NULL
    UNION
    SELECT recording_aliases.retired_id
    FROM recording_aliases, canonical
    WHERE recording_aliases.surviving_id = canonical.id
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS recording_id_aliases(UUID);
DROP FUNCTION IF EXISTS canonical_recording_id(UUID);
DROP TABLE IF EXISTS recording_aliases;
