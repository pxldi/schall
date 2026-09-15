-- +goose Up

-- What the audio in a file actually is, as opposed to what its name suggests.
--
-- Until now library_files held a size, a duration and a fingerprint, and the
-- only thing standing in for quality was the extension on the path. That is a
-- label somebody typed: a 320 kbps MP3 and a 192 kbps one are the same word.
-- So the library could see that it held one recording twice and could not say
-- which of the two copies was the lesser, and the answer to a duplicate could
-- only ever be a person opening both files somewhere else.
--
-- These four are read off the stream by the same tag library that already
-- writes tags, so nothing new is decoded and nothing new is trusted. All of
-- them are nullable and stay null for a file whose audio would not be read:
-- unknown is unknown, and a comparison missing a term refuses rather than
-- guesses.
ALTER TABLE library_files
    ADD COLUMN bit_rate_kbps INTEGER,
    ADD COLUMN sample_rate_hz INTEGER,
    ADD COLUMN channels INTEGER;

-- How long the stream is, measured off the stream.
--
-- duration_ms beside it is deliberately not this. That one is what the scanner
-- can measure without decoding -- a FLAC's STREAMINFO -- falling back to what a
-- tag claims, and for an MP3 it is usually nothing at all. Matching reads it,
-- and a duration is one of the things that can void a pair, so what it holds is
-- a settled question that is not reopened here.
--
-- This column is read by nothing that decides what a file is. It exists so two
-- copies of one recording can be compared as audio: a copy that is thirty
-- seconds long is not the lesser copy of anything, it is a different file, and
-- without a length for both there is no way to notice.
ALTER TABLE library_files ADD COLUMN measured_duration_ms INTEGER;

-- When the audio was last read, which is not the same as whether it said
-- anything. A file whose stream could not be parsed leaves the four columns
-- null and this set, so a scan looks once rather than every time, and the
-- comparison keeps refusing for want of the numbers rather than for want of a
-- record that somebody tried.
ALTER TABLE library_files ADD COLUMN properties_read_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE library_files
    DROP COLUMN bit_rate_kbps,
    DROP COLUMN sample_rate_hz,
    DROP COLUMN channels,
    DROP COLUMN measured_duration_ms,
    DROP COLUMN properties_read_at;
