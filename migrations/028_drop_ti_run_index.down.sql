-- One CONCURRENTLY statement, no transaction, and no IF NOT EXISTS, so a
-- retry after an interrupted build fails instead of keeping the INVALID
-- idx_ti_run it leaves behind: run DROP INDEX CONCURRENTLY IF EXISTS idx_ti_run,
-- `migrate force 28`, and retry.
CREATE INDEX CONCURRENTLY idx_ti_run ON task_instances (dag_run_id);
