-- Drop idx_ti_task (performance item D3). Its columns (dag_run_id, task_id) are
-- the leading columns of the task_instances_unique constraint
-- (dag_run_id, task_id, map_index, try_number), which serves the same lookups.
--
-- CONCURRENTLY, one statement, no transaction: see 028. If it is interrupted,
-- re-run the statement by hand, then `migrate force 29`.
DROP INDEX CONCURRENTLY IF EXISTS idx_ti_task;
