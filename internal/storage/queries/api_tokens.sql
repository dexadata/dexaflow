-- name: InsertAPIToken :one
-- Stores a new API token for a user of the tenant (ADR 0074). The SELECT
-- yields no row, so nothing is inserted, when the user is not in the tenant
-- or already holds max_live live tokens: the caller reads pgx.ErrNoRows and
-- tells the two apart with CountLiveAPITokens.
INSERT INTO api_tokens (tenant_id, user_id, name, token_hash, scopes, expires_at)
SELECT u.tenant_id, u.id, sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(scopes)::text[], sqlc.arg(expires_at)
FROM users u
WHERE u.id = sqlc.arg(user_id) AND u.tenant_id = sqlc.arg(tenant_id)
  AND (
      SELECT count(*) FROM api_tokens l
      WHERE l.user_id = u.id AND l.revoked_at IS NULL AND l.expires_at > now()
  ) < sqlc.arg(max_live)::bigint
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

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = sqlc.arg(at)
WHERE id = sqlc.arg(id) AND (last_used_at IS NULL OR last_used_at < sqlc.arg(at));

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
-- Revokes a token of the tenant; owner_id NULL revokes any owner's. Revoking
-- a revoked token keeps its first revocation time.
UPDATE api_tokens
SET revoked_at = COALESCE(revoked_at, now())
WHERE id = sqlc.arg(id) AND tenant_id = sqlc.arg(tenant_id)
  AND (sqlc.narg(owner_id)::uuid IS NULL OR user_id = sqlc.narg(owner_id)::uuid)
RETURNING id, name;
