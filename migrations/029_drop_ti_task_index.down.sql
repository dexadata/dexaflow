-- One CONCURRENTLY statement, no transaction. If the build is interrupted it
-- leaves an INVALID idx_ti_task that IF NOT EXISTS would keep: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_ti_task, `migrate force 29`, and retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ti_task ON task_instances (dag_run_id, task_id);
