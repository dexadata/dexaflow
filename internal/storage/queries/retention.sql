-- Retention janitor (performance item D5). Every statement is tenant scoped and
-- bounded; the janitor in internal/retention paces and caps them.

-- name: ListTenantIDs :many
SELECT id FROM tenants ORDER BY id;

-- name: LockExpiredSettledRuns :many
-- A tenant's runs that settled (success or failed) before the cutoff, oldest
-- first, whose task instances are all settled and that no live staging volume
-- still points at. "Settled" is the predicate the pod reconciler's settled-run
-- collection uses too: run in success or failed, and no task instance outside
-- success, failed, skipped and upstream_failed. none is unsettled on purpose:
-- it is a task a clear just reset, waiting to be scheduled, while the run row
-- still says success until the clear reopens it. Any state added later counts
-- as unsettled, so it is kept rather than deleted. The janitor calls this once
-- per batch, so a run whose children span several batches is re-checked each
-- time. FOR UPDATE makes a clear or
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
      AND ti.state NOT IN ('success', 'failed', 'skipped', 'upstream_failed'))
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
-- Removes the locked runs whose children are all gone, up to row_limit.
DELETE FROM dag_runs
WHERE id IN (
  SELECT r.id FROM dag_runs r
  WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.id = ANY(sqlc.arg(run_ids)::uuid[])
    AND NOT EXISTS (SELECT 1 FROM task_instances ti WHERE ti.dag_run_id = r.id)
    AND NOT EXISTS (SELECT 1 FROM xcom_index x WHERE x.dag_run_id = r.id)
  LIMIT sqlc.arg(row_limit));

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
      AND ti.state NOT IN ('success', 'failed', 'skipped', 'upstream_failed'))
  AND NOT EXISTS (
    SELECT 1 FROM staging_volumes s
    WHERE s.tenant_id = r.tenant_id AND s.run_id = r.id::text AND s.state = 'active');

-- name: CountExpiredAuditLog :one
SELECT count(*) FROM audit_log WHERE occurred_at < sqlc.arg(cutoff);

-- name: RecordRetentionPurge :exec
-- One audit entry per scope a retention cycle purged audit rows from, so the
-- purge itself is on the record. tenant_id NULL is the system rows.
INSERT INTO audit_log (tenant_id, action, resource_type, metadata)
VALUES (sqlc.narg(tenant_id), 'retention.purge', 'audit_log',
        jsonb_build_object('table', 'audit_log', 'rows', sqlc.arg(rows)::bigint, 'cutoff', sqlc.arg(cutoff)::timestamptz));
