-- +goose Up
-- A lowest bitrate per lossy format, and a change to what an unstated bitrate
-- means.
--
-- Schall searches peer networks for copies of music. Every folder a peer offers
-- is a candidate, and `source_preferences` (00055) is the one row where the user
-- says which formats to prefer, which never to fetch, and the lowest bitrate a
-- lossy copy may have. That bitrate floor was one number for every lossy format
-- at once. One number cannot say what people actually want: 320 kbps is the top
-- of MP3 and out of reach for Opus, so a person asking for good MP3 had either
-- to refuse Opus outright or to accept bad MP3.
--
-- format_minimum_bitrates is that floor written per format: {"mp3": 320,
-- "opus": 160}. A format named here is held to its own number and the single
-- `minimum_bitrate` no longer applies to it — "MP3 at least 320" means exactly
-- that, whatever the general floor says. A lossy format not named here keeps the
-- general floor. A lossless format has no bitrate to compare and passes both.
--
-- The second change is not a column. Until now a copy whose bitrate the peer
-- never stated passed every floor, on the reading that silence is not evidence.
-- That reading belongs to matching, where a guess invents a false match. Here it
-- inverted the setting: somebody who asked for 320 kbps MP3 was sent MP3s of no
-- stated bitrate, which is the low quality copy the setting exists to refuse.
-- From now on an unstated bitrate does not meet a floor. The copy leaves the
-- results with its reason recorded and counted on screen, so the person can
-- lower the floor or clear it. A copy whose *format* nobody stated is still
-- kept: a floor names formats, and this candidate has named none.
--
-- Nothing here admits anything. These preferences decide only what Schall will
-- try to fetch. A copy that passes every floor is still identified by its audio,
-- is still held rather than imported when nobody can identify it, and no
-- preference has ever admitted a file or deleted one.
ALTER TABLE source_preferences
    ADD COLUMN format_minimum_bitrates JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- An object and nothing else. The names and the ranges are checked where the
    -- setting is saved, because a refusal there can name the format and say what
    -- the range is; a database error cannot.
    ADD CONSTRAINT source_preferences_format_floors_object
        CHECK (jsonb_typeof(format_minimum_bitrates) = 'object');

-- +goose Down
ALTER TABLE source_preferences
    DROP CONSTRAINT IF EXISTS source_preferences_format_floors_object,
    DROP COLUMN IF EXISTS format_minimum_bitrates;
