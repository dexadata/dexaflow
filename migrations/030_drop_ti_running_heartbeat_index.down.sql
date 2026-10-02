-- One CONCURRENTLY statement, no transaction, and no IF NOT EXISTS, so a
-- retry after an interrupted build fails instead of keeping the INVALID
-- idx_ti_running_heartbeat it leaves behind: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_ti_running_heartbeat,
-- `migrate force 30`, and retry.
CREATE INDEX CONCURRENTLY idx_ti_running_heartbeat ON task_instances (last_heartbeat_at)
    WHERE state = 'running';
