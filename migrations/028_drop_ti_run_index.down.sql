-- One CONCURRENTLY statement, no transaction. If the build is interrupted it
-- leaves an INVALID idx_ti_run that IF NOT EXISTS would keep: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_ti_run, `migrate force 28`, and retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ti_run ON task_instances (dag_run_id);
