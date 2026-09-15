-- +goose Up

-- Transcoding on import: what Schall does with a copy after it has been proven
-- and before it is filed.
--
-- A lossless copy of one song is five to ten times the size of a good MP3 of
-- the same song, and a collection kept on a small disc runs out of room long
-- before it runs out of music. So the operator may ask for higher formats to be
-- written into the library as MP3 instead of as they arrived. It is off by
-- default, it is lossy, and it cannot be undone: the original bytes are the
-- provider's copy and Schall does not keep them.
--
-- It changes nothing about what a file is. Every identification -- AcoustID,
-- the published sample, the tags, the duration -- is made on the bytes that
-- arrived, and the verdict is recorded against those bytes. This runs after the
-- verdict and only after it.
ALTER TABLE import_settings
    -- Off unless somebody asks for it. Re-encoding music the user did not ask
    -- to have re-encoded is not a default anybody should discover afterwards.
    ADD COLUMN transcode_enabled BOOLEAN NOT NULL DEFAULT false,
    -- What the copy is written as. Only MP3 is implemented; the column is an
    -- enumeration rather than a flag so adding Opus or AAC later is a value
    -- rather than a second column.
    ADD COLUMN transcode_target TEXT NOT NULL DEFAULT 'mp3',
    -- How good that copy is. Stored as text because the choice is one of a
    -- fixed set and one of them is not a number: 'V0' is LAME's variable-rate
    -- setting, which averages around 245 kbit/s and spends its bits where the
    -- music needs them.
    ADD COLUMN transcode_bitrate TEXT NOT NULL DEFAULT '320',
    -- Which arrivals are re-encoded. 'lossless' is the cautious answer: only a
    -- format that lost nothing in the first place, so nothing already lossy is
    -- ever put through a second encoder. 'above_target' also takes a lossy file
    -- whose bit rate is higher than the target's.
    ADD COLUMN transcode_when TEXT NOT NULL DEFAULT 'lossless',
    ADD CONSTRAINT import_settings_transcode_target_valid
        CHECK (transcode_target IN ('mp3')),
    ADD CONSTRAINT import_settings_transcode_bitrate_valid
        CHECK (transcode_bitrate IN
            ('V0', '128', '160', '192', '224', '256', '288', '320')),
    ADD CONSTRAINT import_settings_transcode_when_valid
        CHECK (transcode_when IN ('lossless', 'above_target'));

-- What a file in the library was before it was re-encoded.
--
-- library_files rows are written by the scan that walks the disc, and a scan
-- can only see what is there now: an MP3 filed by an import that shrank a FLAC
-- looks exactly like an MP3 somebody always had. These two columns are the only
-- record that the file used to be something else, which is what lets the file
-- row say where it came from and how much room the change bought.
--
-- Null means the file was never transcoded by Schall, which is true of every
-- file that existed before this migration and of every file imported with the
-- setting off.
--
-- transcode_checked_at is set on a file the existing-library sweep looked at
-- and found nothing to do for -- already the target format, below the
-- threshold, or unreadable -- so the sweep does not open it again on every
-- pass. A file the sweep re-encodes never needs it: transcoded_from_format
-- being set is itself the reason the query below stops matching it. A rescan
-- that finds the file changed clears both, the same way a changed file clears
-- its loudness measurement.
ALTER TABLE library_files
    ADD COLUMN transcoded_from_format TEXT,
    ADD COLUMN original_size_bytes BIGINT,
    ADD COLUMN transcode_checked_at TIMESTAMPTZ;

-- The note an import leaves for the scan that has not happened yet.
--
-- The import writes the file and asks for a scan; the scan is what creates the
-- library_files row, some seconds later. Nothing carries a fact from the first
-- to the second, so the import writes it here against the path it wrote, and
-- the scan copies it onto the row it creates for that path.
--
-- Rows are kept after the scan has read them. A library that is rebuilt from
-- the disc -- a root removed and added again -- then still knows which of its
-- files Schall shrank.
CREATE TABLE import_transcodes (
    local_path TEXT PRIMARY KEY,
    -- The format the copy arrived in, as the extension on the provider's file
    -- named it: FLAC, WAV, APE. It is a label rather than a decode, which is
    -- all the file row needs to say "From FLAC".
    source_format TEXT NOT NULL,
    source_size_bytes BIGINT NOT NULL,
    target_format TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- An import's history is where a person reads what became of their music, and
-- re-encoding is the one step in an import that changes the audio. 'transcoded'
-- says a copy was written smaller; 'transcode_failed' says it was meant to be
-- and was not, so the file in the library is the original and nobody has to
-- wonder why the setting appears to do nothing.
ALTER TABLE download_import_reviews
    DROP CONSTRAINT download_import_reviews_kind_valid;
ALTER TABLE download_import_reviews
    ADD CONSTRAINT download_import_reviews_kind_valid
        CHECK (kind IN (
            'paused', 'revalidated', 'imported', 'resolved', 'withdrawn',
            'source_removed', 'source_retained', 'discarded',
            'transcoded', 'transcode_failed'
        ));

-- The button in Settings that walks the library already on disc asks for one
-- of these; the worker takes it in small batches and requeues itself while
-- more of the library is left, the same shape the loudness pass and the tags
-- pass already use. The partial unique index is what keeps two presses, or a
-- press racing the worker's own requeue, from ever running two passes at once.
CREATE UNIQUE INDEX jobs_transcode_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_transcode' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_transcode_sweep_idx;
ALTER TABLE download_import_reviews
    DROP CONSTRAINT download_import_reviews_kind_valid;
DELETE FROM download_import_reviews
    WHERE kind IN ('transcoded', 'transcode_failed');
ALTER TABLE download_import_reviews
    ADD CONSTRAINT download_import_reviews_kind_valid
        CHECK (kind IN (
            'paused', 'revalidated', 'imported', 'resolved', 'withdrawn',
            'source_removed', 'source_retained', 'discarded'
        ));

DROP TABLE IF EXISTS import_transcodes;

ALTER TABLE library_files
    DROP COLUMN transcoded_from_format,
    DROP COLUMN original_size_bytes,
    DROP COLUMN transcode_checked_at;

ALTER TABLE import_settings
    DROP CONSTRAINT import_settings_transcode_target_valid,
    DROP CONSTRAINT import_settings_transcode_bitrate_valid,
    DROP CONSTRAINT import_settings_transcode_when_valid,
    DROP COLUMN transcode_enabled,
    DROP COLUMN transcode_target,
    DROP COLUMN transcode_bitrate,
    DROP COLUMN transcode_when;
