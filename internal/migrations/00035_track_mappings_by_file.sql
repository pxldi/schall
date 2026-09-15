-- +goose Up
-- 00033 dropped track_mappings_library_file_id_key so that one file could
-- answer the same recording on every release that lists it. The index behind
-- that constraint went with it, and it was the only one on library_file_id, so
-- every question asked of a file's mappings has read the whole table since.
-- The column is still one of the two this table is looked up by.
--
-- Measured against 20k files holding a mapping each, before -> after:
--
--   library list, first page      28.9ms -> 12.2ms
--   reconcile: is this manual?     1.55ms ->  0.62ms
--   OwnedFileForRecording         13.6ms -> 14.3ms
--   ReconcileAlbum                10.7ms -> 11.0ms
--
-- The last two join every mapping to every file whatever indexes exist; they
-- are listed to say the index does not help them rather than to claim it does.
-- The two it does help are the ones asked once per file: the list runs its
-- lookup once a row, and reconcile runs one on every file it scans.
--
-- Not unique, and not a way back to one mapping per file: two mappings for the
-- same file is what 00033 exists to allow.
CREATE INDEX track_mappings_library_file_id_idx ON track_mappings (library_file_id);

-- +goose Down
DROP INDEX IF EXISTS track_mappings_library_file_id_idx;
