-- name: ListPools :many
SELECT name, slots, description, is_default
FROM pools
WHERE tenant_id = $1
ORDER BY name
LIMIT $2 OFFSET $3;

-- name: CountPools :one
SELECT count(*) FROM pools WHERE tenant_id = $1;

-- name: GetPool :one
SELECT name, slots, description, is_default
FROM pools
WHERE tenant_id = $1 AND name = $2;

-- name: UpsertPool :exec
INSERT INTO pools (tenant_id, name, slots, description)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, name) DO UPDATE SET
    slots = EXCLUDED.slots,
    description = EXCLUDED.description,
    updated_at = now();

-- name: DeletePool :execrows
-- The implicit default pool is never deletable (Airflow parity): the guard is in
-- the query so a direct call cannot orphan the fallback pool the gate resolves to.
DELETE FROM pools WHERE tenant_id = $1 AND name = $2 AND is_default = false;

-- name: PoolBudgets :many
-- Every named pool's slot cap across all tenants, for the scheduler's per-tick
-- cross-DAG admission budget (ADR 0053 Stage 3). Keyed by (tenant_id, name) so a
-- pool name is scoped to its tenant. Pro-only: Lite never calls this.
SELECT tenant_id, name, slots FROM pools;

-- name: PoolSlotUsage :many
-- Per-pool occupancy for a tenant: how many slots the tenant's task instances
-- take in each non-terminal state, grouped by the instance's pool (a NULL pool
-- is the implicit default_pool). Each instance weighs its pool_slots, the size
-- the admission gate charges it (ADR 0066, #1499), not 1. Feeds the Airflow
-- PoolResponse occupancy fields; the gate itself counts queued+running as the
-- occupied slots.
SELECT COALESCE(pool, 'default_pool') AS pool, state, sum(pool_slots)::bigint AS n
FROM task_instances
WHERE tenant_id = $1 AND state IN ('scheduled', 'queued', 'running', 'deferred')
GROUP BY COALESCE(pool, 'default_pool'), state;
