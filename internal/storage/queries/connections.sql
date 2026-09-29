-- name: ListConnections :many
SELECT conn_id, conn_type, host, conn_schema, login, port, extra, description
FROM connections
WHERE tenant_id = $1
ORDER BY conn_id
LIMIT $2 OFFSET $3;

-- name: CountConnections :one
SELECT count(*) FROM connections WHERE tenant_id = $1;

-- name: ListConnectionSecrets :many
-- All of a tenant's connections WITH the encrypted password, for delivering
-- credentials to task pods (ADR 0021). Never use this for UI/API responses,
-- which must mask the password.
SELECT conn_id, conn_type, host, conn_schema, login, password, port, extra
FROM connections
WHERE tenant_id = $1
ORDER BY conn_id;

-- name: ListConnectionSecretsScoped :many
-- The tenant's connections WITH the encrypted password, restricted to the given
-- conn_ids and filtered in the query (ADR 0055 D1: scope in the SQL, never
-- post-filter the decrypted whole vault). Never use this for UI/API responses,
-- which must mask the password. Used under secret_scoping: enforce so a task
-- receives only the connections it declared. An empty conn_id set never reaches
-- here — the handler returns nothing without a query.
SELECT conn_id, conn_type, host, conn_schema, login, password, port, extra
FROM connections
WHERE tenant_id = $1 AND conn_id = ANY(sqlc.arg(conn_ids)::text[])
ORDER BY conn_id;

-- name: GetConnection :one
SELECT conn_id, conn_type, host, conn_schema, login, password, port, extra, description
FROM connections
WHERE tenant_id = $1 AND conn_id = $2;

-- name: ExistingConnectionIDs :many
-- The subset of the given conn_ids that exist for the tenant. Used to reject a
-- DAG that declares an unknown connection at registration (ADR 0055 D6); a name
-- absent from the result does not exist.
SELECT conn_id FROM connections
WHERE tenant_id = $1 AND conn_id = ANY(sqlc.arg(conn_ids)::text[]);

-- name: UpsertConnection :exec
-- Tri-state write (#887): COALESCE(EXCLUDED.col, connections.col) preserves the
-- stored value when the param is NULL, overwrites it when the param is a value,
-- and clears it when the param is a non-NULL empty string. A NULL param is how a
-- partial `set` stays safe — changing only --host must not wipe the (unreadable)
-- password — while an explicit empty string now clears a field, and a masked
-- secret ("***") is mapped to NULL by the caller so it preserves rather than
-- overwrites (#874). conn_type is required on every write, so it is a plain
-- overwrite.
INSERT INTO connections (tenant_id, conn_id, conn_type, host, conn_schema, login, password, port, extra, description)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (tenant_id, conn_id) DO UPDATE SET
    conn_type = EXCLUDED.conn_type,
    host = COALESCE(EXCLUDED.host, connections.host),
    conn_schema = COALESCE(EXCLUDED.conn_schema, connections.conn_schema),
    login = COALESCE(EXCLUDED.login, connections.login),
    password = COALESCE(EXCLUDED.password, connections.password),
    port = COALESCE(EXCLUDED.port, connections.port),
    extra = COALESCE(EXCLUDED.extra, connections.extra),
    description = COALESCE(EXCLUDED.description, connections.description),
    updated_at = now();

-- name: DeleteConnection :execrows
DELETE FROM connections WHERE tenant_id = $1 AND conn_id = $2;

-- name: ListEncryptedConnectionSecrets :many
-- Every connection's ciphertext, across every tenant, for the key-rotation
-- pass. The id is returned because the rewrite targets a row, not a
-- (tenant, conn_id) pair, and nothing here decrypts: the caller holds the keys.
SELECT id, tenant_id, conn_id, password, extra
FROM connections
WHERE password IS NOT NULL OR extra IS NOT NULL
ORDER BY id;

-- name: UpdateConnectionCiphertext :execrows
-- Rewrite one row's ciphertext in place during a key rotation. It touches only
-- the two encrypted columns, so a re-encryption can never alter a connection's
-- identity, host, or any field a user set.
--
-- The WHERE carries the ciphertext we read, so a row a user changed between our
-- read and our write is NOT overwritten. Without it the rotation re-seals stale
-- plaintext over a password the user just set, and bumps updated_at so the row
-- looks freshly written. Reachable whenever more than one replica serves, which
-- the chart calls the recommended production posture.
UPDATE connections
SET password = $2, extra = $3, updated_at = now()
WHERE id = $1
  AND password IS NOT DISTINCT FROM sqlc.arg(expect_password)
  AND extra IS NOT DISTINCT FROM sqlc.arg(expect_extra);
