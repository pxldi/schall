-- name: CreateAppToken :one
-- Mint a token for one phone. The hash arrives already computed; the plain
-- token never reaches the database.
INSERT INTO app_tokens (name, token_hash, created_by)
VALUES (
    sqlc.arg('name'),
    sqlc.arg('token_hash'),
    sqlc.arg('created_by')
)
RETURNING id, name, created_by, created_at, last_used_at;

-- name: AppTokenByHash :one
-- The token a request presented, revoked or not. A revoked one is read rather
-- than filtered out so the middleware can tell a withdrawn phone from a token
-- nothing ever issued; both are refused, and only one of them is worth a log
-- line.
SELECT id, name, revoked_at
FROM app_tokens
WHERE token_hash = sqlc.arg('token_hash');

-- name: ListAppTokens :many
-- The phones Settings shows. Revoked rows are kept in the table and are not a
-- phone any more, so they are not listed.
SELECT id, name, created_by, created_at, last_used_at
FROM app_tokens
WHERE revoked_at IS NULL
ORDER BY created_at DESC;

-- name: RevokeAppToken :one
-- Withdraw one phone. Returning the id says whether there was a live token to
-- withdraw, so pressing Remove twice is a 404 rather than a second revocation
-- that moves the timestamp.
UPDATE app_tokens
SET revoked_at = now()
WHERE id = sqlc.arg('id')
  AND revoked_at IS NULL
RETURNING id;

-- name: TouchAppToken :exec
-- Record that this token was used. Written at most once a minute per token by
-- the middleware, because the alternative is one UPDATE per request.
UPDATE app_tokens
SET last_used_at = now()
WHERE id = sqlc.arg('id');
