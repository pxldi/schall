-- +goose Up

-- A want is one recording somebody asked Schall to fetch, and every want
-- records an origin: one word saying where the asking came from. Origin is
-- provenance and never behaviour — nothing in the acquisition loop reads it,
-- and a want is pursued the same way whatever it says.
--
-- There are now four ways a want comes to exist rather than three. Beside
-- somebody typing one in, a playlist being imported, and a followed artist
-- releasing something, a reader can accept a suggestion on the Recommended
-- view. This widens what a want may say about itself and changes nothing about
-- what happens to it.
ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_origin_valid,
    ADD CONSTRAINT acquisition_targets_origin_valid
        CHECK (origin IN ('manual', 'playlist', 'follow_feed', 'recommendation'));

-- +goose Down
-- Wants recorded as coming from a suggestion still exist, and the older
-- constraint refuses them. They are read back as manual first, so a rollback is
-- never blocked by a row written while this was applied. What is lost is where
-- those wants came from, not the music they ask for.
UPDATE acquisition_targets SET origin = 'manual' WHERE origin = 'recommendation';

ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_origin_valid,
    ADD CONSTRAINT acquisition_targets_origin_valid
        CHECK (origin IN ('manual', 'playlist', 'follow_feed'));
