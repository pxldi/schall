-- +goose Up
CREATE TABLE slskd_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    search_timeout_seconds INTEGER NOT NULL DEFAULT 20,
    connection_status TEXT NOT NULL DEFAULT 'unknown',
    connection_error TEXT,
    connection_detail TEXT,
    last_checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT slskd_settings_single_row CHECK (singleton),
    CONSTRAINT slskd_settings_base_url_present CHECK (btrim(base_url) <> ''),
    CONSTRAINT slskd_settings_api_key_present CHECK (btrim(api_key) <> ''),
    CONSTRAINT slskd_settings_status_valid
        CHECK (connection_status IN ('unknown', 'ok', 'failed')),
    CONSTRAINT slskd_settings_timeout_range
        CHECK (search_timeout_seconds BETWEEN 5 AND 120)
);

-- +goose Down
DROP TABLE IF EXISTS slskd_settings;
