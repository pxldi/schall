-- +goose Up
-- Spotify says of every track whether it is the explicit recording. Schall read
-- the field and threw it away, and it is the one thing the source knows that
-- separates the two rows MusicBrainz holds for a censored track: the explicit
-- master and the clean edition, one title, one credit, one length, two ISRCs.
--
-- It is stored on the entry and on the want, next to the rest of the evidence
-- the source wrote, because it is evidence about the request rather than an
-- answer. What it is used for is exclusion alone: an entry flagged explicit is
-- never resolved to a row MusicBrainz marks as the clean edition. It admits
-- nothing on its own. See
-- docs/decisions/0032-an-explicit-edition-is-preferred-over-a-clean-one.md.
--
-- NULL is silence. A source that does not say, an entry made by hand, and every
-- row that existed before this migration all carry it, and silence excludes
-- nothing.
ALTER TABLE playlist_entries ADD COLUMN entry_explicit BOOLEAN;
ALTER TABLE acquisition_targets ADD COLUMN entry_explicit BOOLEAN;

-- +goose Down
ALTER TABLE acquisition_targets DROP COLUMN entry_explicit;
ALTER TABLE playlist_entries DROP COLUMN entry_explicit;
