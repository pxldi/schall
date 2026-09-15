-- +goose Up
-- The same recording appears on more than one release: an EP and the
-- compilation that gathers it, a single and the album it was lifted from. The
-- catalogue carries a track row for each of them, and one file on disk is the
-- honest answer to all of them. Requiring a file to answer exactly one track
-- left sixteen proven files reading as music the library did not have, while
-- the bytes sat on the disc the whole time.
--
-- One file per track is kept. A track still cannot have two files, so "which
-- copy is this track" remains a question nobody guesses at. Only the other
-- direction is relaxed, and matching relaxes it only between tracks that name
-- the same recording -- see automaticCandidates in internal/matching/service.go,
-- where an identifier-found group is one recording on several releases rather
-- than a question about which recording the file is.
ALTER TABLE track_mappings DROP CONSTRAINT track_mappings_library_file_id_key;

-- +goose Down
-- Going back needs one mapping per file again, so the extras are dropped. A
-- decision somebody made by hand is the one kept.
DELETE FROM track_mappings WHERE id NOT IN (
    SELECT DISTINCT ON (library_file_id) id
    FROM track_mappings
    ORDER BY library_file_id, is_manual DESC, created_at, id
);
ALTER TABLE track_mappings
    ADD CONSTRAINT track_mappings_library_file_id_key UNIQUE (library_file_id);
