-- +goose Up
-- The dashboard has to say the download source is not logged in to Soulseek,
-- and since when (#495). slskd sat in "Disconnecting" for two hours once,
-- deferring every want, and nothing on any screen said so — the only way to
-- see it was pressing Test in Settings → Sources.
--
-- Acquisition already learns this the moment a search is refused for it, so
-- the moment is kept here rather than polled for: set the first time a
-- refusal is heard, cleared the moment a search goes through.
ALTER TABLE slskd_settings ADD COLUMN logged_out_since TIMESTAMPTZ;

-- +goose Down
ALTER TABLE slskd_settings DROP COLUMN logged_out_since;
