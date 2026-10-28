-- name: LockAPITokenOwner :one
-- Locks the user row a token is created for, so concurrent creates for one
-- user run one at a time and the live-token cap holds (ADR 0074). FOR NO KEY
-- UPDATE does not block the key-share locks that inserts referencing the user
-- take. No row: the user is not in the tenant. The email names the owner in
-- the audit rows the create writes.
SELECT id, email FROM users WHERE id = $1 AND tenant_id = $2 FOR NO KEY UPDATE;

-- name: RetireExpiredAPITokenName :many
-- Revokes the user's expired, unrevoked tokens with this name, so an expired
-- token does not hold its name forever, and returns them so the create audits
-- each one as api_token.retire_expired rather than leaving it to read as a
-- manual revoke (#1768).
UPDATE api_tokens SET revoked_at = now()
WHERE user_id = $1 AND tenant_id = $2 AND name = $3 AND revoked_at IS NULL AND expires_at <= now()
RETURNING id, expires_at;

-- name: InsertAPIToken :one
-- Stores a new API token (ADR 0074). The caller holds LockAPITokenOwner and
-- has checked the cap in the same transaction.
INSERT INTO api_tokens (tenant_id, user_id, name, token_hash, scopes, expires_at)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(scopes)::text[], sqlc.arg(expires_at))
RETURNING id, created_at;

-- name: CountLiveAPITokens :one
SELECT count(*) FROM api_tokens
WHERE user_id = $1 AND tenant_id = $2 AND revoked_at IS NULL AND expires_at > now();

-- name: GetAPITokenByHash :one
-- The per-request lookup of an API token. Revoked and expired tokens are
-- returned too, so the authenticator decides, and says why in one place.
SELECT k.id, t.name AS tenant, k.user_id, k.name, k.scopes, k.created_at,
       k.expires_at, k.last_used_at, k.revoked_at
FROM api_tokens k
JOIN tenants t ON t.id = k.tenant_id
WHERE k.token_hash = $1;

-- name: TouchAPIToken :execrows
-- The five-minute throttle is this predicate, so concurrent requests write
-- once and an older time never moves the last use back.
UPDATE api_tokens SET last_used_at = sqlc.arg(at)
WHERE id = sqlc.arg(id)
  AND (last_used_at IS NULL OR last_used_at < sqlc.arg(at)::timestamptz - interval '5 minutes');

-- name: ListAPITokens :many
-- The tenant's unrevoked tokens, newest first; owner_id NULL lists every
-- owner's. Never selects token_hash.
SELECT k.id, k.user_id, u.email AS user_email, k.name, k.scopes, k.created_at,
       k.expires_at, k.last_used_at
FROM api_tokens k
JOIN users u ON u.id = k.user_id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.revoked_at IS NULL
  AND (sqlc.narg(owner_id)::uuid IS NULL OR k.user_id = sqlc.narg(owner_id)::uuid)
ORDER BY k.created_at DESC, k.id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountAPITokens :one
SELECT count(*) FROM api_tokens k
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.revoked_at IS NULL
  AND (sqlc.narg(owner_id)::uuid IS NULL OR k.user_id = sqlc.narg(owner_id)::uuid);

-- name: RevokeAPIToken :one
-- Revokes a live token of the tenant; owner_id NULL revokes any owner's. A
-- revoked token matches no row, so it is not revoked or audited twice.
UPDATE api_tokens
SET revoked_at = now()
WHERE id = sqlc.arg(id) AND tenant_id = sqlc.arg(tenant_id) AND revoked_at IS NULL
  AND (sqlc.narg(owner_id)::uuid IS NULL OR user_id = sqlc.narg(owner_id)::uuid)
RETURNING id, name, user_id;
