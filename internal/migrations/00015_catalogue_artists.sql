-- +goose Up
-- A file resolved to a recording still had nowhere to go. Its identity was
-- proven, but the recording could only be mapped once some followed artist
-- happened to bring the release into the catalogue, so music bought outside
-- Schall waited on a decision about an artist the user had never been asked
-- about.
--
-- Ingesting the release behind a proven identity closes that loop, and it needs
-- somewhere to put the artist. Following an artist is a statement of intent:
-- keep their releases complete. Holding an artist in the catalogue is a
-- statement of fact: the library contains their music. They are different
-- claims, and conflating them would answer "what am I missing?" with releases
-- nobody asked to complete.
--
-- followed_at IS NULL is therefore the catalogue-only artist. The column was
-- NOT NULL because until now every artist was followed by construction.
ALTER TABLE artists
    ALTER COLUMN followed_at DROP NOT NULL;

-- Why an artist nobody followed is in the catalogue, in one sentence. An
-- unexplained row in a list of artists the user never chose reads as a bug.
ALTER TABLE artists
    ADD COLUMN catalogue_summary TEXT,
    ADD CONSTRAINT artists_catalogue_only_explained
        CHECK (followed_at IS NOT NULL OR btrim(coalesce(catalogue_summary, '')) <> '');

CREATE INDEX artists_followed_idx ON artists (sort_name) WHERE followed_at IS NOT NULL;

-- One ingest per release group at a time. Two files from the same album resolve
-- independently and would otherwise ask MusicBrainz the same question twice.
CREATE UNIQUE INDEX jobs_active_release_group_ingest_idx
    ON jobs (kind, (payload->>'releaseGroupId'))
    WHERE kind = 'ingest_release_group' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_active_release_group_ingest_idx;
DROP INDEX IF EXISTS artists_followed_idx;
ALTER TABLE artists
    DROP CONSTRAINT IF EXISTS artists_catalogue_only_explained,
    DROP COLUMN IF EXISTS catalogue_summary;
UPDATE artists SET followed_at = now() WHERE followed_at IS NULL;
ALTER TABLE artists
    ALTER COLUMN followed_at SET NOT NULL;
