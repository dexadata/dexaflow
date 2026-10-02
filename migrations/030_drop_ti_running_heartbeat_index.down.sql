-- One CONCURRENTLY statement, no transaction. If the build is interrupted it
-- leaves an INVALID idx_ti_running_heartbeat that IF NOT EXISTS would keep: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_ti_running_heartbeat,
-- `migrate force 30`, and retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ti_running_heartbeat ON task_instances (last_heartbeat_at)
    WHERE state = 'running';
