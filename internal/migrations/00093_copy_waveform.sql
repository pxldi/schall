-- +goose Up
-- The shape of a copy's audio: two hundred bytes, one per slice of the track,
-- each the loudest sample in that slice scaled so the loudest slice reads 255.
-- The review queue draws it on the card so a person can see where the music is
-- before pressing play.
--
-- It is a picture and nothing else. No verdict reads it, it says nothing about
-- identity and nothing about quality, and a copy without one is judged exactly
-- as it was before. NULL means nobody has computed it, which is where every row
-- that existed before this migration starts.
ALTER TABLE acquisition_target_files ADD COLUMN waveform BYTEA;

-- +goose Down
ALTER TABLE acquisition_target_files DROP COLUMN waveform;
