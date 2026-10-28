-- name: SetUsageEventsEnabled :execrows
-- Writes the one row of usage_events_switch that the usage event trigger
-- reads (ADR 0096 point 7). It reports a row only when the value moved, so
-- the leader can log a change and stay quiet when it rewrites the same value.
UPDATE usage_events_switch
SET enabled = sqlc.arg(enabled), updated_at = now()
WHERE id AND enabled IS DISTINCT FROM sqlc.arg(enabled);

-- name: DeleteExpiredUsageEvents :one
-- Deletes the oldest usage events in serving order, (xid, id), up to
-- row_limit, stopping at the first one recorded at or after the cutoff, so the
-- deleted rows are always a prefix of what the service API serves. The pruned
-- mark moves to the last one in the same statement.
WITH head AS (
    SELECT h.xid, h.id, bool_and(h.recorded_at < sqlc.arg(cutoff)::timestamptz) OVER (ORDER BY h.xid, h.id) AS expired
    FROM (
        SELECT e.xid, e.id, e.recorded_at FROM usage_events e
        ORDER BY e.xid, e.id
        LIMIT sqlc.arg(row_limit)::int
    ) h
),
gone AS (
    DELETE FROM usage_events e
    USING head
    WHERE e.id = head.id AND head.expired
    RETURNING e.xid, e.id
),
last_gone AS (
    SELECT g.xid, g.id FROM gone g ORDER BY g.xid DESC, g.id DESC LIMIT 1
),
mark AS (
    UPDATE usage_events_pruned p
    SET xid = l.xid, event_id = l.id, pruned_at = now()
    FROM last_gone l
    WHERE p.id
    RETURNING p.id
)
SELECT count(*) FROM gone;

-- name: CountExpiredUsageEvents :one
-- Counts what DeleteExpiredUsageEvents would delete, looking at no more than
-- max_rows events.
SELECT count(*) FROM (
    SELECT bool_and(h.recorded_at < sqlc.arg(cutoff)::timestamptz) OVER (ORDER BY h.xid, h.id) AS expired
    FROM (
        SELECT e.xid, e.id, e.recorded_at FROM usage_events e
        ORDER BY e.xid, e.id
        LIMIT sqlc.arg(max_rows)::int
    ) h
) x
WHERE x.expired;

-- name: GetUsageEventsState :one
-- Whether recording is on and the pruned mark, read in the snapshot of the
-- page they decide.
SELECT s.enabled, p.xid AS pruned_xid, p.event_id AS pruned_id
FROM usage_events_switch s
CROSS JOIN usage_events_pruned p
WHERE s.id AND p.id;

-- name: ListUsageEvents :many
-- The events after the cursor in serving order, (xid, id), among those whose
-- writing transaction is below the snapshot's xmin: every transaction that
-- wrote them has finished, and every row written later sorts after them. The
-- DAG and run ids are resolved here; they are empty once the run is deleted.
SELECT e.id, e.xid, t.name AS tenant, COALESCE(d.dag_id, '')::text AS dag_id, COALESCE(r.run_id, '')::text AS run_id,
       e.task_instance_id, e.task_id, e.map_index, e.try_number, e.attempt_epoch, e.revision,
       e.state, e.previous_state, e.pool_slots, e.started_at, e.ended_at, e.last_failure_kind, e.recorded_at
FROM usage_events e
JOIN tenants t ON t.id = e.tenant_id
LEFT JOIN dag_runs r ON r.id = e.dag_run_id
LEFT JOIN dags d ON d.id = r.dag_id
WHERE e.xid < pg_snapshot_xmin(pg_current_snapshot())
  AND (e.xid, e.id) > (sqlc.arg(after_xid)::xid8, sqlc.arg(after_id)::bigint)
ORDER BY e.xid, e.id
LIMIT sqlc.arg(row_limit)::int;

-- name: UsageEventsLagSeconds :one
-- The age in seconds of the oldest event the route cannot serve yet, because
-- a transaction older than it is still open; 0 when there is none.
SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(e.recorded_at)), 0)::float8 AS lag_seconds
FROM usage_events e
WHERE e.xid >= pg_snapshot_xmin(pg_current_snapshot());

-- name: UsageEventsHorizon :one
-- The oldest transaction still running, or the next to start: every event
-- served now has a lower xid, and every event committed later a higher or
-- equal one.
SELECT pg_snapshot_xmin(pg_current_snapshot())::xid8 AS horizon;
