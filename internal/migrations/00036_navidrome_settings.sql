-- +goose Up
-- Where the player lives, in the shape slskd_settings set (00005).
--
-- Navidrome reads the same directory Schall writes and is never told what the
-- music is, only that there is more of it: nothing here describes a recording,
-- a release or a file, because the library on disc is the whole message.
--
-- Subsonic authenticates every request by salting and hashing the password, so
-- the password itself has to be stored rather than a token derived from it.
CREATE TABLE navidrome_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    base_url TEXT NOT NULL,
    username TEXT NOT NULL,
    password TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    connection_status TEXT NOT NULL DEFAULT 'unknown',
    connection_error TEXT,
    connection_detail TEXT,
    last_checked_at TIMESTAMPTZ,
    -- When Navidrome last accepted a request to look at the library again. It
    -- says nothing about what it then found: a scan that changed nothing never
    -- asks, so a timestamp older than the last import is ordinary rather than a
    -- sign that anything failed.
    last_notified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT navidrome_settings_single_row CHECK (singleton),
    CONSTRAINT navidrome_settings_base_url_present CHECK (btrim(base_url) <> ''),
    CONSTRAINT navidrome_settings_username_present CHECK (btrim(username) <> ''),
    CONSTRAINT navidrome_settings_password_present CHECK (btrim(password) <> ''),
    CONSTRAINT navidrome_settings_status_valid
        CHECK (connection_status IN ('unknown', 'ok', 'failed'))
);

-- +goose Down
DROP TABLE IF EXISTS navidrome_settings;
