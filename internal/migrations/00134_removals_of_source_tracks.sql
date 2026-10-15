-- +goose Up
-- A keyed want replaces the copy it took from its link with a better one
-- (owner's decision, 2026-10-15). Such a file is a track on a service and no
-- MusicBrainz recording, so its licence names the track instead: source_key is
-- "soundcloud:293", the same key the file's source identity carries. Every
-- licence still names the music it was written for (ADR 0021).
--
-- DROP NOT NULL changes the catalogue only, and the CHECK reads a table that
-- holds one row per deleted file, so this stays well inside startup.
ALTER TABLE library_file_removals
    ALTER COLUMN musicbrainz_recording_id DROP NOT NULL,
    ADD COLUMN source_key TEXT,
    ADD CONSTRAINT library_file_removals_names_music
        CHECK (musicbrainz_recording_id IS NOT NULL OR btrim(coalesce(source_key, '')) <> '');

-- +goose Down
-- A licence is kept whatever it named. One for a source track keeps the nil
-- identifier, which names no recording, so the column can be required again.
UPDATE library_file_removals
SET musicbrainz_recording_id = '00000000-0000-0000-0000-000000000000'
WHERE musicbrainz_recording_id IS NULL;
ALTER TABLE library_file_removals
    DROP CONSTRAINT IF EXISTS library_file_removals_names_music,
    DROP COLUMN IF EXISTS source_key,
    ALTER COLUMN musicbrainz_recording_id SET NOT NULL;
