-- +goose Up
-- Where the listening history lives, in the shape navidrome_settings set (00036).
--
-- Unlike slskd and Navidrome this is a hosted public service, so the address has
-- a right answer and is defaulted: the row exists to hold the *account*, and an
-- installation that never touches base_url is the normal one.
--
-- The token is optional because nothing here needs authentication — every call
-- Schall makes is a public read. It buys a higher rate limit and nothing else,
-- and Schall never writes to ListenBrainz: a dismissal is Schall's own record of
-- the user's taste, not something to publish under their name.
CREATE TABLE listenbrainz_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    base_url TEXT NOT NULL DEFAULT 'https://api.listenbrainz.org',
    labs_url TEXT NOT NULL DEFAULT 'https://labs.api.listenbrainz.org',
    username TEXT NOT NULL,
    user_token TEXT,
    -- The labs similarity algorithm is a closed enum the dataset host may rename
    -- between deployments; storing it keeps a rename a settings edit.
    similarity_algorithm TEXT NOT NULL DEFAULT
        'session_based_days_7500_session_300_contribution_5_threshold_15_limit_50_skip_30',
    enabled BOOLEAN NOT NULL DEFAULT true,
    connection_status TEXT NOT NULL DEFAULT 'unknown',
    connection_error TEXT,
    connection_detail TEXT,
    last_checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT listenbrainz_settings_single_row CHECK (singleton),
    CONSTRAINT listenbrainz_settings_base_url_present CHECK (btrim(base_url) <> ''),
    CONSTRAINT listenbrainz_settings_labs_url_present CHECK (btrim(labs_url) <> ''),
    CONSTRAINT listenbrainz_settings_username_present CHECK (btrim(username) <> ''),
    CONSTRAINT listenbrainz_settings_token_present
        CHECK (user_token IS NULL OR btrim(user_token) <> ''),
    CONSTRAINT listenbrainz_settings_algorithm_present
        CHECK (btrim(similarity_algorithm) <> ''),
    CONSTRAINT listenbrainz_settings_status_valid
        CHECK (connection_status IN ('unknown', 'ok', 'failed'))
);

-- +goose Down
DROP TABLE IF EXISTS listenbrainz_settings;
