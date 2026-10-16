-- +goose Up
-- After 00136 two files were left that the person chose by name on
-- 2026-10-16 (thread "Import scheitert an doppeltem Pfad"): the damaged Hum
-- FLAC, which kept its MP3 from taking the track, and a Pashanim MP3 a failed
-- import placed with no identity row. The job removes each under a licence
-- (ADR 0021) beside the file that stays, and matches that file again.
INSERT INTO jobs (kind, payload, max_attempts)
VALUES ('remove_chosen_copies', jsonb_build_object('copies', jsonb_build_array(
    jsonb_build_object(
        'remove', '/music/Hum/Electra 2000 [4e1612a2]/05 - Scraper.flac',
        'keep', '/music/Hum/Electra 2000 [4e1612a2]/5 - Scraper.mp3'),
    jsonb_build_object(
        'remove', '/music/Pashanim/grünewürfelflow [f5c05ff4]/07 who do you love.mp3',
        'keep', '/music/Pashanim/grünewürfelflow [f5c05ff4]/0107 - who do you love.mp3'))), 3);

-- +goose Down
DELETE FROM jobs WHERE kind = 'remove_chosen_copies' AND status = 'queued';
