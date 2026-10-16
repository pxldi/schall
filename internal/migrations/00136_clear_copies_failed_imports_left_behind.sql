-- +goose Up
-- Before 00135, an import that re-encoded a copy placed it in the library and
-- then failed on downloads_local_path_idx, so the file stayed with no copy row.
-- 00135 then retried three refused requests of one want at once. Five songs
-- were left holding up to seven files each. The person chose on 2026-10-16 to
-- keep one and remove the rest. The job lists the files itself
-- (db.LeftBehindCopies), removes them under a licence (ADR 0021) and matches the
-- file it keeps.
INSERT INTO jobs (kind, payload, max_attempts)
VALUES ('clear_left_behind_copies', '{}'::jsonb, 3);

-- +goose Down
DELETE FROM jobs WHERE kind = 'clear_left_behind_copies' AND status = 'queued';
