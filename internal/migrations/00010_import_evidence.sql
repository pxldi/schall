-- +goose Up
-- A paused import used to keep only the first reason validation stopped at.
-- Evidence is the whole account of one validation attempt: what every
-- downloaded file claimed, what the catalogue edition expects, and every
-- disagreement between them. It lives on the append-only review history rather
-- than on the request, so a later attempt cannot rewrite what an earlier one
-- observed.
ALTER TABLE download_import_reviews ADD COLUMN evidence JSONB;

-- +goose Down
ALTER TABLE download_import_reviews DROP COLUMN IF EXISTS evidence;
