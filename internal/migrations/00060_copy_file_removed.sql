-- +goose Up
-- A copy is one file Schall fetched for a want. When the copy is proven and
-- imported, the scan makes a library file of it and the copy records which file
-- it became.
--
-- That link is set to nothing when somebody deletes the file, which is right —
-- it points at a row that no longer exists — but it leaves two different things
-- looking identical: a copy that has been imported and is waiting for the scan
-- to make its file, and a copy whose file was made and then deleted. The first
-- means "the music is arriving, do not go looking"; the second means "the music
-- is gone, go looking". Told apart by nothing, a want whose surplus copy
-- somebody deleted spent two hours saying a copy was being taken into the
-- library, and then stopped for good without ever looking again.
--
-- So the deletion is written down. It is a fact about the file, not a judgement
-- about the copy: the verdict that admitted the copy stays exactly as it was,
-- and what its audio proved is still on the record.
ALTER TABLE acquisition_target_files
    ADD COLUMN file_removed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE acquisition_target_files
    DROP COLUMN IF EXISTS file_removed_at;
