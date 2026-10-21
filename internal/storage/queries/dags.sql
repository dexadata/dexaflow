-- name: EnsureDag :execrows
-- Creates the dags row of a DAG registered for the first time, with the
-- metadata of the spec that will become its first version. An existing row is
-- left untouched (no write, no lock): RegisterDagVersion then locks it with
-- LockDagByDagID and rewrites the metadata only when a new version becomes
-- current (UpdateDagMetadata). A concurrent first insert of the same dag_id
-- waits on the unique index until the other transaction ends.
-- is_paused is set here, on creation (dags_are_paused_at_creation or the
-- spec's is_paused_upon_creation, #1705): a redeploy never changes it, unless
-- the register asks for a state (is_paused), which SetDagPaused then applies.
-- The row count tells the register whether it created the DAG (1) or found it (0).
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
-- build_info is NULL when the register sent none (#1702).
INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash, created_by, build_info)
SELECT d.id, sqlc.arg(version)::text, sqlc.arg(image_reference)::text, sqlc.arg(spec)::jsonb,
       sqlc.arg(spec_hash)::text, sqlc.narg(created_by)::uuid, sqlc.narg(build_info)::jsonb
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
-- created_by is the registering user's id (empty when the register had no
-- user), not their email: any reader of the DAG sees it. is_current marks the
-- version new runs are created with. build_info is where the version was
-- built, NULL when its register sent none.
SELECT v.id, v.version, v.created_at, v.image_reference, v.spec_hash, v.build_info,
       COALESCE(v.id = d.current_version_id, false)::boolean AS is_current,
       COALESCE(v.created_by::text, '')::text AS created_by,
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

-- name: GetTenantUserID :one
-- The id of a user of the tenant: no row when the user is gone or belongs to
-- another tenant, so a stale token's subject records no creator.
SELECT id FROM users WHERE id = $1 AND tenant_id = $2;

-- name: ListWarmCurrentVersions :many
-- The current version of the DAGs the warm-pool reconcile looks at (#1707,
-- ADR 0082): with prewarm, every active unpaused DAG whose current version
-- declares min_idle_workers, which gets its pool before its first run; and
-- every DAG in dag_ids (the DAGs of the active runs), whose current version
-- tells which versions of those runs were superseded by a register, a
-- rollback or a promote and so drain. prewarm is true for the first kind. The
-- declared value is read from the spec here, so the reconcile loads no spec
-- of a DAG it will not prewarm. The version is a LATERAL primary-key probe
-- with LIMIT 1, and the first predicate repeats the dags-only half of the
-- second, so the scan is of dags and each probe reads one spec: the cost
-- follows the unpaused DAGs and the DAGs of active runs, never the number of
-- versions (a plain join hashes every row of dag_versions). A cross-tenant
-- read like ListActiveDagRuns: the reconcile serves every tenant, and each
-- row carries its tenant_id.
SELECT d.id, d.tenant_id, d.dag_id, d.current_version_id::uuid AS current_version_id,
  (@prewarm::boolean AND d.is_active AND NOT d.is_paused AND m.min_idle > 0)::boolean AS prewarm
FROM dags d
CROSS JOIN LATERAL (
  SELECT COALESCE((v.spec->>'min_idle_workers')::integer, 0) AS min_idle
  FROM dag_versions v WHERE v.id = d.current_version_id LIMIT 1
) m
WHERE ((@prewarm::boolean AND d.is_active AND NOT d.is_paused) OR d.id = ANY(@dag_ids::uuid[]))
  AND ((@prewarm::boolean AND d.is_active AND NOT d.is_paused AND m.min_idle > 0) OR d.id = ANY(@dag_ids::uuid[]));

-- name: ListDagsWithFilter :many
-- The Airflow 3.3 DAG list's search, tag, owner, favorite and sort controls
-- (#1928). Patterns are SQL LIKE expressions matched case-insensitively; a
-- display name is the dag_id. Sorting by next_dagrun is not done here: the next
-- run is computed per DAG after the page is read, so it falls back to dag_id.
SELECT d.*, v.version AS current_version_label
FROM dags d
LEFT JOIN dag_versions v ON v.id = d.current_version_id
LEFT JOIN LATERAL (
    SELECT r.state, r.started_at
    FROM dag_runs r
    WHERE r.dag_id = d.id
    ORDER BY r.logical_date DESC
    LIMIT 1
) l ON true
WHERE d.tenant_id = $1 AND d.is_active = true
  AND (sqlc.narg('paused')::bool IS NULL OR d.is_paused = sqlc.narg('paused'))
  AND (sqlc.narg('run_state')::dag_run_state IS NULL OR l.state = sqlc.narg('run_state'))
  AND (sqlc.narg('dag_id_pattern')::text IS NULL OR d.dag_id ILIKE '%' || sqlc.narg('dag_id_pattern') || '%')
  AND (sqlc.narg('dag_id_prefix_pattern')::text IS NULL OR d.dag_id ILIKE sqlc.narg('dag_id_prefix_pattern') || '%')
  AND (sqlc.narg('dag_display_name_pattern')::text IS NULL OR d.dag_id ILIKE '%' || sqlc.narg('dag_display_name_pattern') || '%')
  AND (sqlc.narg('dag_display_name_prefix_pattern')::text IS NULL OR d.dag_id ILIKE sqlc.narg('dag_display_name_prefix_pattern') || '%')
  AND (sqlc.narg('owners')::text[] IS NULL OR cardinality(sqlc.narg('owners')::text[]) = 0 OR d.owner = ANY(sqlc.narg('owners')::text[]))
  AND (
      sqlc.narg('tags')::text[] IS NULL
      OR cardinality(sqlc.narg('tags')::text[]) = 0
      OR (
          CASE WHEN sqlc.narg('tags_match_mode')::text = 'all'
               THEN d.tags @> sqlc.narg('tags')::text[]
               ELSE d.tags && sqlc.narg('tags')::text[]
          END
      )
  )
  AND (
      sqlc.narg('is_favorite')::bool IS NULL
      OR (sqlc.narg('is_favorite') = true AND EXISTS (
          SELECT 1 FROM dag_favorites f
          WHERE f.tenant_id = $1 AND f.user_id = sqlc.narg('user_id') AND f.dag_id = d.dag_id
      ))
      OR (sqlc.narg('is_favorite') = false AND NOT EXISTS (
          SELECT 1 FROM dag_favorites f
          WHERE f.tenant_id = $1 AND f.user_id = sqlc.narg('user_id') AND f.dag_id = d.dag_id
      ))
  )
ORDER BY
  CASE WHEN sqlc.narg('order_by')::text IN ('-dag_id', '-dag_display_name') THEN d.dag_id END DESC,
  CASE WHEN sqlc.narg('order_by')::text = 'last_run_state' THEN l.state::text END ASC NULLS LAST,
  CASE WHEN sqlc.narg('order_by')::text = '-last_run_state' THEN l.state::text END DESC NULLS LAST,
  CASE WHEN sqlc.narg('order_by')::text = 'last_run_start_date' THEN l.started_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('order_by')::text = '-last_run_start_date' THEN l.started_at END DESC NULLS LAST,
  d.dag_id ASC
LIMIT $2 OFFSET $3;

-- name: CountDagsWithFilter :one
SELECT count(*)
FROM dags d
LEFT JOIN LATERAL (
    SELECT r.state, r.started_at
    FROM dag_runs r
    WHERE r.dag_id = d.id
    ORDER BY r.logical_date DESC
    LIMIT 1
) l ON true
WHERE d.tenant_id = $1 AND d.is_active = true
  AND (sqlc.narg('paused')::bool IS NULL OR d.is_paused = sqlc.narg('paused'))
  AND (sqlc.narg('run_state')::dag_run_state IS NULL OR l.state = sqlc.narg('run_state'))
  AND (sqlc.narg('dag_id_pattern')::text IS NULL OR d.dag_id ILIKE '%' || sqlc.narg('dag_id_pattern') || '%')
  AND (sqlc.narg('dag_id_prefix_pattern')::text IS NULL OR d.dag_id ILIKE sqlc.narg('dag_id_prefix_pattern') || '%')
  AND (sqlc.narg('dag_display_name_pattern')::text IS NULL OR d.dag_id ILIKE '%' || sqlc.narg('dag_display_name_pattern') || '%')
  AND (sqlc.narg('dag_display_name_prefix_pattern')::text IS NULL OR d.dag_id ILIKE sqlc.narg('dag_display_name_prefix_pattern') || '%')
  AND (sqlc.narg('owners')::text[] IS NULL OR cardinality(sqlc.narg('owners')::text[]) = 0 OR d.owner = ANY(sqlc.narg('owners')::text[]))
  AND (
      sqlc.narg('tags')::text[] IS NULL
      OR cardinality(sqlc.narg('tags')::text[]) = 0
      OR (
          CASE WHEN sqlc.narg('tags_match_mode')::text = 'all'
               THEN d.tags @> sqlc.narg('tags')::text[]
               ELSE d.tags && sqlc.narg('tags')::text[]
          END
      )
  )
  AND (
      sqlc.narg('is_favorite')::bool IS NULL
      OR (sqlc.narg('is_favorite') = true AND EXISTS (
          SELECT 1 FROM dag_favorites f
          WHERE f.tenant_id = $1 AND f.user_id = sqlc.narg('user_id') AND f.dag_id = d.dag_id
      ))
      OR (sqlc.narg('is_favorite') = false AND NOT EXISTS (
          SELECT 1 FROM dag_favorites f
          WHERE f.tenant_id = $1 AND f.user_id = sqlc.narg('user_id') AND f.dag_id = d.dag_id
      ))
  );

-- name: ListTenantDagTags :many
WITH distinct_tags AS (
    SELECT DISTINCT unnest(d.tags)::text AS name
    FROM dags d
    WHERE d.tenant_id = $1 AND d.is_active = true
)
SELECT name
FROM distinct_tags
WHERE (sqlc.narg('tag_name_pattern')::text IS NULL OR name ILIKE '%' || sqlc.narg('tag_name_pattern') || '%')
  AND (sqlc.narg('tag_name_prefix_pattern')::text IS NULL OR name ILIKE sqlc.narg('tag_name_prefix_pattern') || '%')
ORDER BY name
LIMIT $2 OFFSET $3;

-- name: CountTenantDagTags :one
WITH distinct_tags AS (
    SELECT DISTINCT unnest(d.tags)::text AS name
    FROM dags d
    WHERE d.tenant_id = $1 AND d.is_active = true
)
SELECT count(*)
FROM distinct_tags
WHERE (sqlc.narg('tag_name_pattern')::text IS NULL OR name ILIKE '%' || sqlc.narg('tag_name_pattern') || '%')
  AND (sqlc.narg('tag_name_prefix_pattern')::text IS NULL OR name ILIKE sqlc.narg('tag_name_prefix_pattern') || '%');
