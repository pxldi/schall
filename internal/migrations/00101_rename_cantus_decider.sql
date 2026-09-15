-- +goose Up

-- The project is called Schall now. Two stored values still said "cantus": the
-- decider on an acquired copy, and the keep signal a person presses. A database
-- created after the rename already holds the new spelling, so every statement
-- here is written to be a no-op in that case.

ALTER TABLE acquisition_target_files
    DROP CONSTRAINT IF EXISTS acquisition_target_files_decided_by_valid;

UPDATE acquisition_target_files SET decided_by = 'schall' WHERE decided_by = 'cantus';

ALTER TABLE acquisition_target_files
    ALTER COLUMN decided_by SET DEFAULT 'schall',
    ADD CONSTRAINT acquisition_target_files_decided_by_valid
        CHECK (decided_by IN ('schall', 'user'));

ALTER TABLE library_file_keep_reads
    DROP CONSTRAINT IF EXISTS library_file_keep_reads_signal_valid,
    DROP CONSTRAINT IF EXISTS library_file_keep_reads_pressed_keep_is_not_a_read,
    DROP CONSTRAINT IF EXISTS library_file_keep_reads_read_names_its_run;

UPDATE library_file_keep_reads SET signal = 'schall_keep' WHERE signal = 'cantus_keep';

ALTER TABLE library_file_keep_reads
    ADD CONSTRAINT library_file_keep_reads_signal_valid
        CHECK (signal IN ('navidrome_star', 'schall_keep')),
    ADD CONSTRAINT library_file_keep_reads_pressed_keep_is_not_a_read
        CHECK ((signal = 'schall_keep')
               = (run_id IS NULL AND outcome = 'kept')),
    ADD CONSTRAINT library_file_keep_reads_read_names_its_run
        CHECK (signal = 'schall_keep' OR run_id IS NOT NULL);

-- +goose Down

ALTER TABLE acquisition_target_files
    DROP CONSTRAINT IF EXISTS acquisition_target_files_decided_by_valid;

UPDATE acquisition_target_files SET decided_by = 'cantus' WHERE decided_by = 'schall';

ALTER TABLE acquisition_target_files
    ALTER COLUMN decided_by SET DEFAULT 'cantus',
    ADD CONSTRAINT acquisition_target_files_decided_by_valid
        CHECK (decided_by IN ('cantus', 'user'));

ALTER TABLE library_file_keep_reads
    DROP CONSTRAINT IF EXISTS library_file_keep_reads_signal_valid,
    DROP CONSTRAINT IF EXISTS library_file_keep_reads_pressed_keep_is_not_a_read,
    DROP CONSTRAINT IF EXISTS library_file_keep_reads_read_names_its_run;

UPDATE library_file_keep_reads SET signal = 'cantus_keep' WHERE signal = 'schall_keep';

ALTER TABLE library_file_keep_reads
    ADD CONSTRAINT library_file_keep_reads_signal_valid
        CHECK (signal IN ('navidrome_star', 'cantus_keep')),
    ADD CONSTRAINT library_file_keep_reads_pressed_keep_is_not_a_read
        CHECK ((signal = 'cantus_keep')
               = (run_id IS NULL AND outcome = 'kept')),
    ADD CONSTRAINT library_file_keep_reads_read_names_its_run
        CHECK (signal = 'cantus_keep' OR run_id IS NOT NULL);
