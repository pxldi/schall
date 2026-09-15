-- +goose Up

ALTER TABLE library_files
    ADD COLUMN arrived_by_upload_id UUID;

COMMENT ON COLUMN library_files.arrived_by_upload_id IS
    'The upload that put this file here. Cleared after the one replacement check so a later re-identification cannot delete a file because of an old upload.';

-- +goose Down

ALTER TABLE library_files
    DROP COLUMN IF EXISTS arrived_by_upload_id;
