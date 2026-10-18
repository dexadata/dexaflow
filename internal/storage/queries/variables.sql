-- name: ListVariables :many
SELECT key, value, description
FROM variables
WHERE tenant_id = $1
ORDER BY key
LIMIT $2 OFFSET $3;

-- name: ListVariablesScoped :many
-- The tenant's variables restricted to the given keys, filtered in the query
-- (ADR 0055 D1: scope in the SQL, never post-filter the decrypted whole vault in
-- the handler). Used under secret_scoping: enforce so a task receives only the
-- Variables it declared. An empty key set never reaches here — the handler
-- returns nothing without a query.
SELECT key, value, description
FROM variables
WHERE tenant_id = $1 AND key = ANY(sqlc.arg(keys)::text[])
ORDER BY key;

-- name: CountVariables :one
SELECT count(*) FROM variables WHERE tenant_id = $1;

-- name: ListVariablesFiltered :many
SELECT key, value, description
FROM variables
WHERE tenant_id = $1
  AND (sqlc.narg('key_pattern')::text IS NULL OR key ILIKE '%' || sqlc.narg('key_pattern') || '%')
  AND (sqlc.narg('key_prefix_pattern')::text IS NULL OR key ILIKE sqlc.narg('key_prefix_pattern') || '%')
ORDER BY key
LIMIT $2 OFFSET $3;

-- name: CountVariablesFiltered :one
SELECT count(*) FROM variables
WHERE tenant_id = $1
  AND (sqlc.narg('key_pattern')::text IS NULL OR key ILIKE '%' || sqlc.narg('key_pattern') || '%')
  AND (sqlc.narg('key_prefix_pattern')::text IS NULL OR key ILIKE sqlc.narg('key_prefix_pattern') || '%');

-- name: GetVariable :one
SELECT key, value, description
FROM variables
WHERE tenant_id = $1 AND key = $2;

-- name: ExistingVariableKeys :many
-- The subset of the given keys that exist for the tenant. Used to reject a DAG
-- that declares an unknown Variable at registration (ADR 0055 D6); a name absent
-- from the result does not exist.
SELECT key FROM variables
WHERE tenant_id = $1 AND key = ANY(sqlc.arg(keys)::text[]);

-- name: UpsertVariable :exec
-- value is always supplied (the variable IS its value, and the `value` column is
-- NOT NULL, so a NULL param cannot reach the COALESCE — Postgres rejects it on
-- the INSERT arm). The tri-state write (#887) therefore resolves value in the
-- handler: an omitted or masked ("***") value on update is replaced with the
-- stored value before it reaches here, so this always overwrites value.
-- description is nullable, so it keeps the COALESCE preserve semantics: a NULL
-- param keeps the stored description rather than wiping it.
INSERT INTO variables (tenant_id, key, value, description)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, key) DO UPDATE SET
    value = EXCLUDED.value,
    description = COALESCE(EXCLUDED.description, variables.description),
    updated_at = now();

-- name: DeleteVariable :execrows
DELETE FROM variables WHERE tenant_id = $1 AND key = $2;

-- name: ListVariablesForEncryptionSweep :many
-- Every variable across every tenant whose stored value does (sealed = true) or
-- does not (sealed = false) start with the ciphertext tag (#507). It backs the
-- variable_encryption boot sweeps, which seal plaintext rows ("on") or restore
-- them ("decrypt"), and the key rotation sweep, which reads the sealed ones.
SELECT id, key, value FROM variables
WHERE starts_with(value, sqlc.arg(prefix)::text) = sqlc.arg(sealed)::boolean
ORDER BY id;

-- name: UpdateVariableValueGuarded :execrows
-- Rewrites one stored value only if it is still the one the sweep read, so a
-- concurrent write wins and the sweep counts the row as skipped. updated_at is
-- left alone: the variable's value did not change, only its stored form.
UPDATE variables SET value = sqlc.arg(value)
WHERE id = sqlc.arg(id) AND value = sqlc.arg(expect);
