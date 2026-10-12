-- +goose Up
-- The picture of a release somebody listened to, keyed by its MusicBrainz
-- release ID, for the Overview rows that name a release the library does not
-- hold. Those rows used to point the browser at the Cover Art Archive, which
-- took from 0.75 to 7 seconds a picture on every visit. Now Schall asks the
-- archive once and serves the bytes itself, the bargain release_cover_art
-- makes for releases the catalogue holds.
--
-- Display only, like every cover: nothing here is read by matching or
-- identity. A row with no image is "asked, and there is none"; it is asked
-- again after thirty days, because the archive gains pictures.
CREATE TABLE listened_release_covers (
    musicbrainz_release_id UUID PRIMARY KEY,
    image BYTEA,
    content_type TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT listened_release_covers_image_typed
        CHECK ((image IS NULL) = (content_type = ''))
);

-- +goose Down
DROP TABLE listened_release_covers;
