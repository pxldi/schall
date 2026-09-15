-- +goose Up

-- Where the music in a file stops, and what the file says made it.
--
-- 00045 stored what a file's headers claim: a bit rate, a sample rate, a channel
-- count. Those are read off the stream without decoding it, and they describe
-- the container rather than the audio in it. A FLAC made by decoding a 128 kbps
-- MP3 and encoding the result is an honest lossless container holding audio that
-- was squeezed and cannot be unsqueezed: every header on it says "lossless", and
-- when the library holds it beside a real 320 kbps MP3 of the same recording,
-- the fake is the one that looks better.
--
-- The damage is visible in the audio itself. A lossy encoder throws the top of
-- the frequency range away, so the sound stops dead at a frequency no lossless
-- encoder would ever stop at. These columns are that measurement, taken by
-- decoding a few seconds from the middle of the file.
--
-- All of them are quality and none of them is identity. A cutoff says nothing
-- about which recording a file holds — a transcode of the right recording is
-- still the right recording — so nothing that admits or refuses a file reads
-- them, and nothing may be added later that does. They exist so a person
-- choosing between two copies of one recording can see which is the lesser.

-- The top of the highest quarter-kilohertz band that still carries music.
ALTER TABLE library_files ADD COLUMN spectral_cutoff_hz INTEGER;

-- What the file says made it: "LAME3.100" on an MP3, a libFLAC version on a
-- FLAC. It is what the file claims about itself, shown beside the measurement
-- and never read as proof — a transcode is free to carry any encoder string at
-- all, and plenty carry the string of the encoder that laundered them.
ALTER TABLE library_files ADD COLUMN audio_encoder TEXT;

-- Whether the measurement is evidence the audio was squeezed before it reached
-- the container it is in now.
--
-- Three states, and the third is the point. TRUE is a suspicion with its cutoff
-- shown beside it, FALSE is a file that was looked at and gave no such sign, and
-- NULL is a file nothing was concluded about — silence in the stretch that was
-- read, audio that would not decode, a machine with no decoder. An absent
-- measurement is never agreement that a file is fine.
ALTER TABLE library_files ADD COLUMN transcode_suspected BOOLEAN;

-- When the audio was decoded, which is not the same as whether it said anything.
-- A file that concluded nothing is recorded as looked at, so the sweep asks once
-- rather than every pass.
ALTER TABLE library_files ADD COLUMN spectrum_read_at TIMESTAMPTZ;

-- Which files the sweep still owes a look. Every file in the library qualifies
-- on the day this ships, so the sweep walks the whole collection once; after
-- that the index holds only what a scan has re-read since.
CREATE INDEX library_files_spectrum_pending_idx
    ON library_files (id)
    WHERE spectrum_read_at IS NULL AND sample_rate_hz IS NOT NULL;

-- One spectrum sweep, enforced by the table rather than hoped for, exactly as
-- the lyrics and picture sweeps are. Decoding audio is the most expensive thing
-- Schall does to a file it already holds, and two live passes would be two of
-- them running over the same library at once.
CREATE UNIQUE INDEX jobs_spectrum_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_audio_spectrum' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_spectrum_sweep_idx;
DROP INDEX IF EXISTS library_files_spectrum_pending_idx;
ALTER TABLE library_files
    DROP COLUMN spectral_cutoff_hz,
    DROP COLUMN audio_encoder,
    DROP COLUMN transcode_suspected,
    DROP COLUMN spectrum_read_at;
