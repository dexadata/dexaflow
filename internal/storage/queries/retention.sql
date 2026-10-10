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
-- time. FOR UPDATE keeps the run row itself from changing, and SKIP LOCKED
-- lets the janitor pass over a run another transaction holds. It does NOT stop
-- a clear: a clear resets task_instances before it touches the run row, so the
-- janitor then locks and re-checks the task instances
-- (LockTaskInstancesOfRuns) before it deletes anything.
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

-- name: LockTaskInstancesOfRuns :many
-- Locks every task instance of the locked runs and reports whether each is
-- still settled. A clear resets task instances without locking the run row
-- first, so the run lock alone does not keep it out: a clear that committed
-- since LockExpiredSettledRuns shows here as an unsettled state, and the
-- janitor drops that run from the batch. Once these locks are held no clear can
-- change the rows until the batch commits. NOWAIT: a task instance another
-- transaction is writing (a clear in flight) fails the lock with
-- lock_not_available instead of waiting, because that clear will next update
-- the run row the janitor holds, and waiting would deadlock; the janitor skips
-- the batch and retries on a later cycle.
SELECT ti.dag_run_id,
       (ti.state IN ('success', 'failed', 'skipped', 'upstream_failed'))::boolean AS settled
FROM task_instances ti
WHERE ti.tenant_id = sqlc.arg(tenant_id) AND ti.dag_run_id = ANY(sqlc.arg(run_ids)::uuid[])
FOR UPDATE OF ti NOWAIT;

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

-- name: CountExpiredSettledRunsOfTenant :one
-- The dry-run count for one tenant: the same predicate as
-- LockExpiredSettledRuns, with no lock, through the same partial index, and
-- stopped after max_runs runs so its cost stays bounded however long the
-- history is. The janitor reports a total that reached the cap as a lower bound.
SELECT count(*) AS runs, COALESCE(sum(e.task_instances), 0)::bigint AS task_instances
FROM (
  SELECT (SELECT count(*) FROM task_instances ti WHERE ti.dag_run_id = r.id) AS task_instances
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
  LIMIT sqlc.arg(max_runs)
) e;

-- name: CountExpiredTenantAuditLog :one
-- Audit rows of one tenant older than the cutoff, at most max_rows.
SELECT count(*) FROM (
  SELECT 1 FROM audit_log a
  WHERE a.tenant_id = sqlc.arg(tenant_id) AND a.occurred_at < sqlc.arg(cutoff)
  LIMIT sqlc.arg(max_rows)
) e;

-- name: CountExpiredSystemAuditLog :one
-- System audit rows (no tenant) older than the cutoff, at most max_rows.
SELECT count(*) FROM (
  SELECT 1 FROM audit_log a
  WHERE a.tenant_id IS NULL AND a.occurred_at < sqlc.arg(cutoff)
  LIMIT sqlc.arg(max_rows)
) e;

-- name: RecordRetentionPurge :exec
-- One audit entry per scope a retention cycle purged audit rows from, so the
-- purge itself is on the record. tenant_id NULL is the system rows.
INSERT INTO audit_log (tenant_id, action, resource_type, metadata)
VALUES (sqlc.narg(tenant_id), 'retention.purge', 'audit_log',
        jsonb_build_object('table', 'audit_log', 'rows', sqlc.arg(rows)::bigint, 'cutoff', sqlc.arg(cutoff)::timestamptz));
