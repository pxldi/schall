-- +goose Up
-- A source identity: what a file is, when MusicBrainz has never heard of it.
--
-- Schall names music by asking MusicBrainz, the open music catalogue, which
-- recording a file holds. A recording there is one row for one performance, and
-- its identifier is the key every other decision in Schall hangs from. That
-- works for music that was published through a label and registered. It does
-- not work for an edit somebody uploaded to SoundCloud: MusicBrainz holds no
-- row for it, so the file resolves to `local_only` — owned music the providers
-- know nothing about — and stays a path with a filename for a name.
--
-- This adds a second key beside the first, and it is deliberately sparse. A
-- source identity says: this file is the track published at this address, on
-- this service, under this identifier there. It proves nothing about any
-- recording, and it is never turned into one.
--
-- Three columns carry it:
--
--   source       the service the track lives on, 'soundcloud' today
--   external_id  that service's own identifier, written 'soundcloud:293'
--   external_url the page the track is published at
--
-- A source identity is a manual decision. Somebody looked at a file, pasted the
-- address of the track it holds, and confirmed it. So it is written with
-- is_manual = true, which is what already stops identity resolution from asking
-- the providers about the file again (internal/identity/service.go, the
-- `decided` branch of Resolve).
ALTER TABLE library_file_identities
    ADD COLUMN source TEXT,
    ADD COLUMN external_id TEXT,
    ADD COLUMN external_url TEXT;

ALTER TABLE library_file_identities
    DROP CONSTRAINT library_file_identities_kind_valid;

ALTER TABLE library_file_identities
    ADD CONSTRAINT library_file_identities_kind_valid
        CHECK (kind IN ('external', 'local_only', 'source')),
    -- A source identity without the service's own identifier identifies
    -- nothing, and one carrying a recording is claiming to be the other kind of
    -- key. Neither is a source identity, so neither is storable.
    ADD CONSTRAINT library_file_identities_source_identified
        CHECK (kind <> 'source' OR (
            source IS NOT NULL AND btrim(source) <> ''
            AND external_id IS NOT NULL AND btrim(external_id) <> ''
            AND musicbrainz_recording_id IS NULL
        )),
    -- The other two kinds have nothing to say about a service. A row that
    -- filled these in would be a source identity filed under the wrong kind.
    ADD CONSTRAINT library_file_identities_external_id_is_a_source
        CHECK (kind = 'source' OR (source IS NULL AND external_id IS NULL));

-- Reading back which file holds a given SoundCloud track. Not unique: the same
-- track uploaded twice is two files, and that is the duplicate question, not a
-- storage error.
CREATE INDEX library_file_identities_source_idx
    ON library_file_identities (source, external_id)
    WHERE kind = 'source';

-- 'source' joins the states a file's resolution can be in. It is not a state
-- the resolver ever writes: resolution asks MusicBrainz, and this is the answer
-- that no question to MusicBrainz produced. It is here because the library list
-- filters on this column, and a person looking for the tracks Schall holds by
-- address rather than by recording has to be able to ask for them.
ALTER TABLE library_files
    DROP CONSTRAINT library_files_resolution_status_valid;

ALTER TABLE library_files
    ADD CONSTRAINT library_files_resolution_status_valid
        CHECK (resolution_status IN (
            'pending', 'resolved', 'needs_review', 'conflict', 'local_only',
            'failed', 'source'
        ));

-- The uploader of a SoundCloud track is an artist, and the library shows their
-- name beside the music like any other. They are not a MusicBrainz artist, and
-- they must never be merged with one: "Abo" on SoundCloud and "Abo" in
-- MusicBrainz are two names that happen to match, and joining them by name is
-- the guess Schall does not make.
--
-- artists.musicbrainz_id was already nullable, for catalogue artists nobody
-- follows. These two columns are the second key, in the same shape the identity
-- table now carries: the service, and its own identifier there. A separate
-- artist_sources table would have meant a join in every query that lists an
-- artist; two nullable columns leave all of them untouched.
ALTER TABLE artists
    ADD COLUMN source TEXT,
    ADD COLUMN external_id TEXT;

-- One artist row per uploader. A second SoundCloud track by the same person
-- finds this row rather than making another.
CREATE UNIQUE INDEX artists_source_idx
    ON artists (source, external_id)
    WHERE source IS NOT NULL AND external_id IS NOT NULL;

-- The picture a source-identified file is shown with, and the one written
-- beside it on disc for the player.
--
-- Cover art is already cached per release in release_cover_art, keyed by
-- album_id, which is that table's primary key. A SoundCloud track belongs to no
-- release: there is no album row to key it by, and making the key nullable
-- would take the primary key off a table whose whole shape is one picture per
-- release. So the picture for a file is its own table with its own key, holding
-- the same four columns and the same bargain — a row with no image records
-- "asked, nobody had one", so it is never asked again.
CREATE TABLE file_cover_art (
    library_file_id UUID PRIMARY KEY REFERENCES library_files(id) ON DELETE CASCADE,
    image BYTEA,
    content_type TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT file_cover_art_image_typed
        CHECK ((image IS NULL) = (content_type = ''))
);

-- +goose Down
-- Going back takes the second key away, so the rows that were nothing but that
-- key have to go with it. The columns holding what a source identity said are
-- dropped below; a row left behind would be a `source` identity that names
-- nothing, under a CHECK that no longer allows the word.
--
-- The files themselves are untouched. What they lose is Schall's record of what
-- they are, and 'pending' is the honest thing to put back: the feature that
-- answered the question is gone, so the file is one the resolver has not
-- answered yet. It will be asked about again, get 'local_only' back, and be
-- exactly where it was before this migration existed.
--
-- The uploader's artist row stays. It loses the two columns that said which
-- account it was, and becomes an ordinary artist the library merely holds —
-- which is what `catalogue_summary` on it already says.
DELETE FROM library_file_identities WHERE kind = 'source';
UPDATE library_files
SET resolution_status = 'pending',
    resolution_summary = NULL,
    resolution_attempted_at = NULL
WHERE resolution_status = 'source';

DROP TABLE IF EXISTS file_cover_art;
DROP INDEX IF EXISTS artists_source_idx;
ALTER TABLE artists
    DROP COLUMN IF EXISTS external_id,
    DROP COLUMN IF EXISTS source;
ALTER TABLE library_files
    DROP CONSTRAINT IF EXISTS library_files_resolution_status_valid;
ALTER TABLE library_files
    ADD CONSTRAINT library_files_resolution_status_valid
        CHECK (resolution_status IN (
            'pending', 'resolved', 'needs_review', 'conflict', 'local_only', 'failed'
        ));
DROP INDEX IF EXISTS library_file_identities_source_idx;
ALTER TABLE library_file_identities
    DROP CONSTRAINT IF EXISTS library_file_identities_external_id_is_a_source,
    DROP CONSTRAINT IF EXISTS library_file_identities_source_identified,
    DROP CONSTRAINT IF EXISTS library_file_identities_kind_valid,
    DROP COLUMN IF EXISTS external_url,
    DROP COLUMN IF EXISTS external_id,
    DROP COLUMN IF EXISTS source;
ALTER TABLE library_file_identities
    ADD CONSTRAINT library_file_identities_kind_valid
        CHECK (kind IN ('external', 'local_only'));
