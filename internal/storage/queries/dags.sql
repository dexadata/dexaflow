-- name: EnsureDag :exec
-- Creates the dags row of a DAG registered for the first time, with the
-- metadata of the spec that will become its first version. An existing row is
-- left untouched (no write, no lock): RegisterDagVersion then locks it with
-- LockDagByDagID and rewrites the metadata only when a new version becomes
-- current (UpdateDagMetadata). A concurrent first insert of the same dag_id
-- waits on the unique index until the other transaction ends.
-- is_paused is set only here, on creation (dags_are_paused_at_creation or the
-- spec's is_paused_upon_creation, #1705): a redeploy never changes it.
INSERT INTO dags (tenant_id, dag_id, description, owner, tags, schedule, schedule_timezone, start_date, max_active_runs, catchup, is_paused)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (tenant_id, dag_id) DO NOTHING;

-- name: LockDagByDagID :one
-- Locks the DAG's row for the rest of the transaction, so registers of the same
-- DAG are serialized: the spec-hash check of one register sees the version the
-- previous one committed. FOR NO KEY UPDATE, not FOR UPDATE: it still excludes
-- another register, but not the KEY SHARE lock every dag_runs insert takes on
-- its DAG, so run creation is not blocked while a version registers.
SELECT * FROM dags WHERE tenant_id = $1 AND dag_id = $2 FOR NO KEY UPDATE;

-- name: UpdateDagMetadata :exec
-- Applies the spec of the version that is becoming current to the dags row, in
-- the same transaction as SetCurrentDagVersion. is_paused is never touched:
-- pausing is an operator decision, not a deploy one.
UPDATE dags
SET description = $3,
    owner = $4,
    tags = $5,
    schedule = $6,
    schedule_timezone = $7,
    start_date = $8,
    max_active_runs = $9,
    catchup = $10,
    updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: GetDagByDagID :one
SELECT * FROM dags WHERE tenant_id = $1 AND dag_id = $2;

-- name: ListDags :many
SELECT * FROM dags
WHERE tenant_id = $1 AND is_active = true
ORDER BY dag_id
LIMIT $2 OFFSET $3;

-- name: ListDagsWithVersion :many
-- The DAG plus the LABEL of its current version. The UI's clear dialog compares
-- this against the RUN's bundle_version and offers "Run with latest bundle
-- version" only when they differ, so leaving it null either hides the control or
-- shows it unconditionally — neither of which tells the operator the truth.
SELECT d.*, v.version AS current_version_label
FROM dags d
LEFT JOIN dag_versions v ON v.id = d.current_version_id
WHERE d.tenant_id = $1 AND d.is_active = true
ORDER BY d.dag_id
LIMIT $2 OFFSET $3;

-- name: GetDagWithVersion :one
-- See ListDagsWithVersion.
SELECT d.*, v.version AS current_version_label
FROM dags d
LEFT JOIN dag_versions v ON v.id = d.current_version_id
WHERE d.tenant_id = $1 AND d.dag_id = $2;

-- name: CountDags :one
SELECT count(*) FROM dags WHERE tenant_id = $1 AND is_active = true;

-- name: SetDagPaused :one
UPDATE dags SET is_paused = $3, updated_at = now()
WHERE tenant_id = $1 AND dag_id = $2
RETURNING *;

-- name: GetDagVersionByHash :one
-- dag_versions has no tenant_id: the tenant is checked on the parent dag (#508).
SELECT * FROM dag_versions
WHERE dag_versions.dag_id = $1 AND dag_versions.spec_hash = $2
  AND EXISTS (SELECT 1 FROM dags d WHERE d.id = dag_versions.dag_id AND d.tenant_id = $3);

-- name: InsertDagVersion :one
-- The row is inserted only under a dag of the given tenant (#508): a dag id of
-- another tenant inserts nothing and the query returns no row.
INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash, created_by)
SELECT d.id, sqlc.arg(version)::text, sqlc.arg(image_reference)::text, sqlc.arg(spec)::jsonb,
       sqlc.arg(spec_hash)::text, sqlc.narg(created_by)::uuid
FROM dags d
WHERE d.id = sqlc.arg(dag_id) AND d.tenant_id = sqlc.arg(tenant_id)
RETURNING *;

-- name: SetCurrentDagVersion :exec
UPDATE dags SET current_version_id = $2, updated_at = now() WHERE id = $1 AND tenant_id = $3;

-- name: GetCurrentDagSpec :one
SELECT v.spec
FROM dags d
JOIN dag_versions v ON v.id = d.current_version_id
WHERE d.tenant_id = $1 AND d.dag_id = $2;

-- name: ListDagVersions :many
SELECT v.id, v.version, v.created_at,
       row_number() OVER (ORDER BY v.created_at, v.version) AS version_number
FROM dag_versions v
JOIN dags d ON d.id = v.dag_id
WHERE d.tenant_id = $1 AND d.dag_id = $2
ORDER BY version_number DESC;

-- name: DeleteDag :execrows
DELETE FROM dags
WHERE tenant_id = $1 AND dag_id = $2;

-- name: ListDagsFiltered :many
-- The newest run is looked up per listed DAG through idx_dag_runs_dag_logical,
-- one index probe each, instead of a DISTINCT ON over every run in the table.
-- LEFT JOIN keeps DAGs without runs; they match no run_state filter.
SELECT d.*
FROM dags d
LEFT JOIN LATERAL (
    SELECT r.state
    FROM dag_runs r
    WHERE r.dag_id = d.id
    ORDER BY r.logical_date DESC
    LIMIT 1
) l ON true
WHERE d.tenant_id = $1 AND d.is_active = true
  AND (sqlc.narg('paused')::bool IS NULL OR d.is_paused = sqlc.narg('paused'))
  AND (sqlc.narg('run_state')::dag_run_state IS NULL OR l.state = sqlc.narg('run_state'))
ORDER BY d.dag_id
LIMIT $2 OFFSET $3;

-- name: CountDagsFiltered :one
-- Same newest-run lookup as ListDagsFiltered.
SELECT count(*)
FROM dags d
LEFT JOIN LATERAL (
    SELECT r.state
    FROM dag_runs r
    WHERE r.dag_id = d.id
    ORDER BY r.logical_date DESC
    LIMIT 1
) l ON true
WHERE d.tenant_id = $1 AND d.is_active = true
  AND (sqlc.narg('paused')::bool IS NULL OR d.is_paused = sqlc.narg('paused'))
  AND (sqlc.narg('run_state')::dag_run_state IS NULL OR l.state = sqlc.narg('run_state'));

-- name: ClearDagRuns :execrows
DELETE FROM dag_runs WHERE tenant_id = $1 AND dag_id = $2;
