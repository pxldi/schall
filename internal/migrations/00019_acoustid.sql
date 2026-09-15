-- +goose Up
-- Every identity signal Schall has until now comes from something a file or a
-- peer says about itself: a tag, a filename, a folder, a duration a stranger
-- typed. An acoustic fingerprint is computed from the decoded audio, so it is
-- the one signal that cannot be mislabelled — and mislabelled files are exactly
-- the ones worth catching.
--
-- The key belongs to imports because that is the only place it is used. A peer
-- discloses a filename and a length, never audio, so nothing can be fingerprinted
-- until the bytes have arrived.
--
-- AcoustID issues a key per application and only the operator can obtain one,
-- so this is empty until somebody registers. Disabled and unkeyed both mean the
-- same thing to the importer: no acoustic evidence, and validation proceeds on
-- what it had before.
ALTER TABLE import_settings
    ADD COLUMN acoustid_api_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN acoustid_enabled BOOLEAN NOT NULL DEFAULT false,
    -- Enabling it without a key would look like a working check that silently
    -- verifies nothing, which is worse than an honestly absent one.
    ADD CONSTRAINT import_settings_acoustid_needs_key
        CHECK (NOT acoustid_enabled OR btrim(acoustid_api_key) <> '');

-- +goose Down
ALTER TABLE import_settings
    DROP CONSTRAINT IF EXISTS import_settings_acoustid_needs_key,
    DROP COLUMN IF EXISTS acoustid_enabled,
    DROP COLUMN IF EXISTS acoustid_api_key;
