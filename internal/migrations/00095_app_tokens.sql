-- +goose Up
-- The tokens a phone authenticates with, one row per phone.
--
-- Only the SHA-256 of a token is stored, so the lookup is by hash and the plain
-- token exists in one HTTP response and nowhere else. Losing it means minting
-- another one, which is the point: a database read cannot hand anybody a
-- working credential.
--
-- A token is never deleted. Revoking sets revoked_at, so a phone that was
-- removed still has a name and a last use to read afterwards.
CREATE TABLE app_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS app_tokens;
