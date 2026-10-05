-- +goose Up
-- The two inks a page is printed in, read once from the release's cover (ADR on
-- Duoton). A page takes them from the release it shows, so they are stored
-- beside the picture rather than worked out in the browser on every view.
--
-- Display only, like the picture they come from. Nothing in matching, identity
-- or import reads them.
--
-- ink_dark and ink_light are already clamped for legibility: the dark one has a
-- relative luminance of at most 0.035 and the light one at least 0.42. palette
-- is the cover's colour clusters, most common first, unclamped.
--
-- A NULL palette on a row with a picture means nobody has read the picture yet,
-- and the cover sweep reads it on its next pass. An empty palette means it was
-- read and nothing in it decoded (an SVG, say), so the row is not read again
-- until its picture changes.
ALTER TABLE release_cover_art
    ADD COLUMN ink_dark TEXT,
    ADD COLUMN ink_light TEXT,
    ADD COLUMN palette TEXT[],
    ADD CONSTRAINT release_cover_art_inks_paired
        CHECK ((ink_dark IS NULL) = (ink_light IS NULL)),
    ADD CONSTRAINT release_cover_art_inks_hex
        CHECK (ink_dark ~ '^#[0-9a-f]{6}$' AND ink_light ~ '^#[0-9a-f]{6}$'),
    ADD CONSTRAINT release_cover_art_inks_need_image
        CHECK (image IS NOT NULL OR (ink_dark IS NULL AND palette IS NULL));

-- +goose Down
ALTER TABLE release_cover_art
    DROP CONSTRAINT release_cover_art_inks_need_image,
    DROP CONSTRAINT release_cover_art_inks_hex,
    DROP CONSTRAINT release_cover_art_inks_paired,
    DROP COLUMN palette,
    DROP COLUMN ink_light,
    DROP COLUMN ink_dark;
