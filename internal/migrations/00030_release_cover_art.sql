-- +goose Up
-- The picture of a release, cached where Schall can serve it itself.
--
-- Artwork is display and never evidence. Nothing here is read by matching, by
-- identity, or by any import grader, and nothing in it may be written into
-- library_file_identities or track_mappings: a cover is a picture beside a
-- release, and a picture cannot say what a file is. It is keyed by album, and the
-- album's own MusicBrainz release — an identifier Schall already resolved and
-- already trusts — is what it was fetched by, so there is no second guess about
-- which release the picture belongs to.
--
-- Cached rather than referenced by URL, because a page rendering a hundred remote
-- images makes a hundred third-party requests on the user's behalf. Holding the
-- bytes means the browser asks Schall and Schall asked the Cover Art Archive
-- once, ever, for each release. It lives in Postgres rather than beside the
-- managed library so that it travels with a backup and needs no volume of its
-- own; a cover is tens of kilobytes and there is one per release.
--
-- A row with no image is the answer "asked, and there is none". It is the reason
-- this is a table rather than a nullable column: without somewhere to record the
-- absence, a release with no artwork would be asked about again on every page
-- view forever.
CREATE TABLE release_cover_art (
    album_id UUID PRIMARY KEY REFERENCES albums(id) ON DELETE CASCADE,

    image BYTEA,
    content_type TEXT NOT NULL DEFAULT '',

    -- Which archive answered, so a cache filled by one source can be told apart
    -- later from one filled by another.
    source TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- An image and the type it is are one fact. Bytes nobody can name the format
    -- of are unservable, and a type with no bytes describes nothing.
    CONSTRAINT release_cover_art_image_typed
        CHECK ((image IS NULL) = (content_type = ''))
);

-- +goose Down
DROP TABLE release_cover_art;
