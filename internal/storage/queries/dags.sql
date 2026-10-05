-- name: UpsertDag :one
INSERT INTO dags (tenant_id, dag_id, description, owner, tags, schedule, schedule_timezone, start_date, max_active_runs, catchup)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (tenant_id, dag_id) DO UPDATE
SET description = EXCLUDED.description,
    owner = EXCLUDED.owner,
    tags = EXCLUDED.tags,
    schedule = EXCLUDED.schedule,
    schedule_timezone = EXCLUDED.schedule_timezone,
    start_date = EXCLUDED.start_date,
    max_active_runs = EXCLUDED.max_active_runs,
    catchup = EXCLUDED.catchup,
    updated_at = now()
RETURNING *;

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
SELECT * FROM dag_versions WHERE dag_id = $1 AND spec_hash = $2;

-- name: InsertDagVersion :one
INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: SetCurrentDagVersion :exec
UPDATE dags SET current_version_id = $2, updated_at = now() WHERE id = $1;

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
DELETE FROM dag_runs WHERE dag_id = $1;
