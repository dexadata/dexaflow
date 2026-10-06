-- name: InsertTenantIfMissing :execrows
-- Service API (#1283): create a tenant by name, or do nothing when it exists.
-- Zero rows affected means it already existed.
INSERT INTO tenants (name, display_name)
VALUES ($1, $2)
ON CONFLICT (name) DO NOTHING;

-- name: CopyDefaultSystemRoles :exec
-- Give a tenant the built-in roles the migrations seed for "default". Copying
-- keeps every tenant's ladder equal to default's as later migrations change it.
INSERT INTO roles (tenant_id, name, description, is_system)
SELECT sqlc.arg(tenant_id)::uuid, r.name, r.description, true
FROM roles r
JOIN tenants d ON d.id = r.tenant_id AND d.name = 'default'
WHERE r.is_system
ON CONFLICT (tenant_id, name) DO NOTHING;

-- name: CopyDefaultRolePermissions :exec
-- Grant each copied built-in role the same permissions as its "default" twin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT nr.id, rp.permission_id
FROM roles dr
JOIN tenants d ON d.id = dr.tenant_id AND d.name = 'default'
JOIN role_permissions rp ON rp.role_id = dr.id
JOIN roles nr ON nr.tenant_id = sqlc.arg(tenant_id)::uuid AND nr.name = dr.name
WHERE dr.is_system
ON CONFLICT DO NOTHING;

-- name: InsertDefaultPool :exec
-- The implicit default pool every tenant needs (migration 023 seeds it for
-- "default"), copied from default's so the slot count stays in step.
INSERT INTO pools (tenant_id, name, slots, description, is_default)
SELECT sqlc.arg(tenant_id)::uuid, p.name, p.slots, p.description, true
FROM pools p
JOIN tenants d ON d.id = p.tenant_id AND d.name = 'default'
WHERE p.is_default
ON CONFLICT (tenant_id, name) DO UPDATE SET is_default = true, updated_at = now()
  WHERE NOT pools.is_default;

-- name: UpsertDefaultPoolSlots :exec
-- Sizes a tenant's default pool to an explicit slot count: inserts it under the
-- given name with the seed description, or re-sizes the row already there. A
-- row with that name left without is_default (a tenant created before the
-- default pool was seeded per tenant) is marked default, so the delete guard
-- and the pools view treat it as the pool the scheduler falls back to.
INSERT INTO pools (tenant_id, name, slots, description, is_default)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(name), sqlc.arg(slots), 'Default pool', true)
ON CONFLICT (tenant_id, name) DO UPDATE SET slots = EXCLUDED.slots, is_default = true, updated_at = now();

-- name: ListTenantRolePermissions :many
-- "role:action:resource" for every grant of a tenant's built-in roles.
SELECT (r.name || ':' || p.action || ':' || p.resource)::text AS grant_key
FROM roles r
JOIN tenants t ON t.id = r.tenant_id
JOIN role_permissions rp ON rp.role_id = r.id
JOIN permissions p ON p.id = rp.permission_id
WHERE t.name = $1 AND r.is_system;

-- name: TenantHasDefaultPool :one
SELECT EXISTS (
    SELECT 1 FROM pools p JOIN tenants t ON t.id = p.tenant_id
    WHERE t.name = $1 AND p.is_default
)::bool AS has_default;

-- name: UpdateTenantLimits :exec
-- Sets the limits given and keeps the others: a NULL argument leaves that
-- column as it is, 0 makes the limit unlimited (migrations 040 to 042).
UPDATE tenants
SET max_dags = COALESCE(sqlc.narg(max_dags)::int, max_dags),
    max_runs_per_day = COALESCE(sqlc.narg(max_runs_per_day)::int, max_runs_per_day),
    min_schedule_interval_seconds = COALESCE(sqlc.narg(min_schedule_interval_seconds)::int, min_schedule_interval_seconds),
    max_task_pool_slots = COALESCE(sqlc.narg(max_task_pool_slots)::int, max_task_pool_slots),
    max_tasks = COALESCE(sqlc.narg(max_tasks)::int, max_tasks),
    max_task_runs_per_month = COALESCE(sqlc.narg(max_task_runs_per_month)::int, max_task_runs_per_month),
    updated_at = now()
WHERE id = sqlc.arg(tenant_id)::uuid;

-- name: GetTenantLimits :one
SELECT max_dags, max_runs_per_day, min_schedule_interval_seconds, max_task_pool_slots,
       max_tasks, max_task_runs_per_month
FROM tenants
WHERE id = $1;

-- name: CountTenantDags :one
SELECT count(*) FROM dags WHERE tenant_id = $1;

-- name: CountTenantTasksExcept :one
-- Tasks in the current version of each of the tenant's active DAGs other than
-- dag_id: what max_tasks (migration 042) compares a registration against.
SELECT COALESCE(sum(jsonb_array_length(dv.spec->'tasks')), 0)::bigint
FROM dags d
JOIN dag_versions dv ON dv.id = d.current_version_id
WHERE d.tenant_id = sqlc.arg(tenant_id)::uuid
  AND d.is_active
  AND d.dag_id <> sqlc.arg(dag_id)::text;

-- name: CurrentVersionTaskCount :one
-- The task count of a DAG's current version: what a new run of it charges to
-- max_task_runs_per_month.
SELECT COALESCE(jsonb_array_length(dv.spec->'tasks'), 0)::int
FROM dags d
JOIN dag_versions dv ON dv.id = d.current_version_id
WHERE d.tenant_id = sqlc.arg(tenant_id)::uuid
  AND d.dag_id = sqlc.arg(dag_id)::text;

-- name: ReserveTenantMonthlyTaskRuns :execrows
-- Charges a run's tasks to the tenant's current UTC month, starting the count
-- again when the month has turned. Zero rows means the tenant has no monthly
-- cap or the tasks do not fit what is left; the caller tells the two apart
-- from the limit it read. It runs in the run's transaction, before the INSERT,
-- like ReserveTenantDailyRun, so a refusal writes nothing and the row lock
-- keeps concurrent runs from passing the cap.
UPDATE tenants
SET task_runs_month_count = CASE
        WHEN task_runs_month = date_trunc('month', now() AT TIME ZONE 'UTC')::date
        THEN task_runs_month_count + sqlc.arg(tasks)::int
        ELSE sqlc.arg(tasks)::int
    END,
    task_runs_month = date_trunc('month', now() AT TIME ZONE 'UTC')::date
WHERE id = sqlc.arg(tenant_id)::uuid
  AND max_task_runs_per_month > 0
  AND (CASE
        WHEN task_runs_month = date_trunc('month', now() AT TIME ZONE 'UTC')::date
        THEN task_runs_month_count::bigint
        ELSE 0
    END) + sqlc.arg(tasks)::int <= max_task_runs_per_month;

-- name: ReserveTenantDailyRun :execrows
-- Takes one of the tenant's runs for the current UTC day, resetting the count
-- when the day has turned. Zero rows means the tenant has no daily cap or has
-- reached it; the caller tells the two apart from the limit it read. It runs
-- before the run's INSERT in the same transaction, so a refusal writes nothing.
-- The UPDATE locks the tenant row until the transaction ends, and Postgres
-- re-checks the WHERE against the row a concurrent winner committed, so the
-- count can never pass the cap.
UPDATE tenants
SET runs_day_count = CASE WHEN runs_day = (now() AT TIME ZONE 'UTC')::date THEN runs_day_count + 1 ELSE 1 END,
    runs_day = (now() AT TIME ZONE 'UTC')::date
WHERE id = $1
  AND max_runs_per_day > 0
  AND (runs_day IS DISTINCT FROM (now() AT TIME ZONE 'UTC')::date OR runs_day_count < max_runs_per_day);
