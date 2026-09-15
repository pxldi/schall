-- +goose Up
-- The picture of an artist, cached exactly as a release's cover is and for the
-- same reasons: served by Schall so a page makes no requests on the user's
-- behalf, and with an absence recorded so a nameless face is not asked about
-- again on every view.
--
-- It is display and never evidence. MusicBrainz says which images are of the
-- artist whose ID the catalogue already holds, so there is no question of whose
-- face this is — and no answer here reaches matching, identity, or an import
-- grader either way.
CREATE TABLE artist_images (
    artist_id UUID PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,

    image BYTEA,
    content_type TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT artist_images_image_typed
        CHECK ((image IS NULL) = (content_type = ''))
);

-- +goose Down
DROP TABLE artist_images;
