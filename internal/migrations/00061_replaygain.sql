-- +goose Up

-- How loud the audio in a file is, so a player can even the collection out.
--
-- Records are mastered at whatever loudness their era favoured, and two files
-- of one collection can sit ten decibels apart. A library played on shuffle
-- therefore lurches, and the listener reaches for the volume control between
-- one song and the next. ReplayGain is the remedy every player already
-- understands: measure a track once, write the number into the file as a tag,
-- and the player turns that track down or up by exactly that much.
--
-- These two are that measurement, held in the database so it is made once. They
-- join the family migration 00045 opened -- what the audio in a file actually
-- is, as opposed to what its name suggests -- and they belong to it in the way
-- that matters: they are quality and never identity. Nothing here says what
-- music a file holds, and nothing here may ever be read as evidence that two
-- files hold the same music, because two unrelated recordings mastered by the
-- same engineer measure the same.
--
-- replaygain_track_gain is decibels: what a player adds to the volume to bring
-- this track to the -18 LUFS reference that ReplayGain 2.0 fixes. A loud master
-- measures above the reference and carries a negative gain.
--
-- replaygain_track_peak is the loudest moment as a fraction of full scale, where
-- 1.0 is full scale. It can legitimately exceed 1.0: this is the true peak of
-- the reconstructed waveform, which passes between samples and so can be higher
-- than any single sample is. A player reads it to decide whether applying the
-- gain would clip. Neither is stored as the string a tag carries -- the album's
-- gain is worked out from its tracks' arithmetically, and a formatted string is
-- not something to do arithmetic on.
ALTER TABLE library_files
    ADD COLUMN replaygain_track_gain DOUBLE PRECISION,
    ADD COLUMN replaygain_track_peak DOUBLE PRECISION;

-- When the loudness was last measured, which is not the same as whether it said
-- anything. Measuring means decoding the whole stream, which is the most
-- expensive thing done to a file in this system, so a file whose audio gave no
-- answer is recorded as asked and left empty rather than decoded again on every
-- pass.
--
-- A file the measurement could not even be attempted for -- ffmpeg is optional
-- and an installation without it measures nothing -- leaves this null, so an
-- installation that gains ffmpeg later picks those files up instead of having
-- written them off.
ALTER TABLE library_files ADD COLUMN loudness_measured_at TIMESTAMPTZ;

-- The backfill walks the library a small batch at a time, and the batches are
-- small because each one decodes whole files on the single worker that runs
-- every other kind of job in turn. That is only a pace if there is one walk
-- doing the walking, so the table enforces one, exactly as the lyrics sweep and
-- the picture sweep are enforced.
CREATE UNIQUE INDEX jobs_loudness_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_loudness' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_loudness_sweep_idx;
ALTER TABLE library_files
    DROP COLUMN replaygain_track_gain,
    DROP COLUMN replaygain_track_peak,
    DROP COLUMN loudness_measured_at;
