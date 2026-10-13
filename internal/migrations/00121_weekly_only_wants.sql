-- +goose Up
-- A weekly playlist lease is a licence to delete the file a want delivered
-- (ADR 0021). It may only be granted when the weekly playlist is the one thing
-- that asked for the recording. Origin cannot say that: a person accepting a
-- suggestion writes 'recommendation' too, and a later ask for the same
-- recording returns the existing want without writing anything.
--
-- weekly_only is true on a want the weekly playlist created, and any other ask
-- for the same recording clears it. Existing wants start false: nothing
-- recorded whether somebody else asked for them, so they are kept.
ALTER TABLE acquisition_targets
    ADD COLUMN weekly_only BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE acquisition_targets
    DROP COLUMN IF EXISTS weekly_only;
