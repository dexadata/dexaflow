-- Retention janitor (performance item D5). Every statement is tenant scoped and
-- bounded; the janitor in internal/retention paces and caps them.

-- name: ListTenantIDs :many
SELECT id FROM tenants ORDER BY id;

-- name: LockExpiredSettledRuns :many
-- A tenant's runs that settled (success or failed) before the cutoff, oldest
-- first, that no task instance still holds active and no live staging volume
-- still points at. Any task state outside the settled set counts as active, so
-- a state added later is kept rather than deleted. FOR UPDATE makes a clear or
-- rerun of the same run wait for the batch to commit; SKIP LOCKED lets the
-- janitor pass over a run another transaction holds instead of waiting on it.
SELECT r.id
FROM dag_runs r
WHERE r.tenant_id = sqlc.arg(tenant_id)
  AND r.state IN ('success', 'failed')
  AND r.ended_at < sqlc.arg(cutoff)
  AND NOT EXISTS (
    SELECT 1 FROM task_instances ti
    WHERE ti.dag_run_id = r.id
      AND ti.state NOT IN ('success', 'failed', 'skipped', 'upstream_failed', 'none'))
  AND NOT EXISTS (
    SELECT 1 FROM staging_volumes s
    WHERE s.tenant_id = r.tenant_id AND s.run_id = r.id::text AND s.state = 'active')
ORDER BY r.ended_at
LIMIT sqlc.arg(max_runs)
FOR UPDATE OF r SKIP LOCKED;

-- name: DeleteTaskStateHistoryOfRuns :execrows
DELETE FROM task_state_history
WHERE id IN (
  SELECT h.id FROM task_state_history h
  JOIN task_instances ti ON ti.id = h.task_instance_id
  WHERE ti.tenant_id = sqlc.arg(tenant_id) AND ti.dag_run_id = ANY(sqlc.arg(run_ids)::uuid[])
  LIMIT sqlc.arg(row_limit));

-- name: DeleteTaskInstanceHistoryOfRuns :execrows
DELETE FROM task_instance_history
WHERE history_id IN (
  SELECT h.history_id FROM task_instance_history h
  JOIN task_instances ti ON ti.id = h.task_instance_id
  WHERE ti.tenant_id = sqlc.arg(tenant_id) AND ti.dag_run_id = ANY(sqlc.arg(run_ids)::uuid[])
  LIMIT sqlc.arg(row_limit));

-- name: DeleteXComIndexOfRuns :execrows
DELETE FROM xcom_index
WHERE id IN (
  SELECT x.id FROM xcom_index x
  WHERE x.tenant_id = sqlc.arg(tenant_id) AND x.dag_run_id = ANY(sqlc.arg(run_ids)::uuid[])
  LIMIT sqlc.arg(row_limit));

-- name: DeleteTaskInstancesOfRuns :execrows
DELETE FROM task_instances
WHERE id IN (
  SELECT ti.id FROM task_instances ti
  WHERE ti.tenant_id = sqlc.arg(tenant_id) AND ti.dag_run_id = ANY(sqlc.arg(run_ids)::uuid[])
  LIMIT sqlc.arg(row_limit));

-- name: DeleteDagRunsByID :execrows
DELETE FROM dag_runs
WHERE tenant_id = sqlc.arg(tenant_id) AND id = ANY(sqlc.arg(run_ids)::uuid[]);

-- name: DeleteTenantAuditLog :execrows
DELETE FROM audit_log
WHERE id IN (
  SELECT a.id FROM audit_log a
  WHERE a.tenant_id = sqlc.arg(tenant_id) AND a.occurred_at < sqlc.arg(cutoff)
  ORDER BY a.occurred_at
  LIMIT sqlc.arg(row_limit));

-- name: DeleteSystemAuditLog :execrows
DELETE FROM audit_log
WHERE id IN (
  SELECT a.id FROM audit_log a
  WHERE a.tenant_id IS NULL AND a.occurred_at < sqlc.arg(cutoff)
  ORDER BY a.occurred_at
  LIMIT sqlc.arg(row_limit));

-- name: CountExpiredSettledRuns :one
-- The dry-run count: the same predicate as LockExpiredSettledRuns, across
-- tenants, with no lock.
SELECT count(*) AS runs,
       COALESCE(sum((SELECT count(*) FROM task_instances ti WHERE ti.dag_run_id = r.id)), 0)::bigint AS task_instances
FROM dag_runs r
WHERE r.state IN ('success', 'failed')
  AND r.ended_at < sqlc.arg(cutoff)
  AND NOT EXISTS (
    SELECT 1 FROM task_instances ti
    WHERE ti.dag_run_id = r.id
      AND ti.state NOT IN ('success', 'failed', 'skipped', 'upstream_failed', 'none'))
  AND NOT EXISTS (
    SELECT 1 FROM staging_volumes s
    WHERE s.tenant_id = r.tenant_id AND s.run_id = r.id::text AND s.state = 'active');

-- name: CountExpiredAuditLog :one
SELECT count(*) FROM audit_log WHERE occurred_at < sqlc.arg(cutoff);
