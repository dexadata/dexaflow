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
