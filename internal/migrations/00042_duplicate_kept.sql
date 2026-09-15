-- +goose Up

-- Keeping the second copy is an answer, and an answer nobody writes down is a
-- question asked again tomorrow.
--
-- Most duplicates end with one of them deleted: the library is meant to hold
-- each recording once. But one recording honestly appears on two releases an
-- owner wants both of -- the EP and the compilation that gathers it -- and the
-- catalogue holds that as one track, so the second file is a duplicate by every
-- test there is and deleting it would be wrong. Without somewhere to record
-- "yes, on purpose", the only way to empty the queue would be to delete music,
-- which is not a choice anybody should be pushed into by a list.
--
-- It is read only while the file is a duplicate. A file that stops being one --
-- the other copy went away, the catalogue grew a second track -- is judged on
-- what it is now, and this says nothing about that.
ALTER TABLE library_files ADD COLUMN duplicate_kept_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE library_files DROP COLUMN duplicate_kept_at;
