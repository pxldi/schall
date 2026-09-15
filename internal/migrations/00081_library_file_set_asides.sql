-- +goose Up
-- A set-aside is a person's answer to the question on the upload pass. It
-- suppresses that question and says nothing about what the file is, so it is
-- kept apart from every matching and identity table.
CREATE TABLE library_file_set_asides (
    library_file_id UUID PRIMARY KEY REFERENCES library_files(id) ON DELETE CASCADE,
    decided_by TEXT NOT NULL,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS library_file_set_asides;
