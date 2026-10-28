-- name: SetUsageEventsEnabled :execrows
-- Writes the one row of usage_events_switch that the usage event trigger
-- reads (ADR 0096 point 7). It reports a row only when the value moved, so
-- the leader can log a change and stay quiet when it rewrites the same value.
UPDATE usage_events_switch
SET enabled = sqlc.arg(enabled), updated_at = now()
WHERE id AND enabled IS DISTINCT FROM sqlc.arg(enabled);
