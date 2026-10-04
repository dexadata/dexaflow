-- One CONCURRENTLY statement, no transaction: see the up migration. If it is
-- interrupted, re-run the drop by hand, then `migrate force 26`.
DROP INDEX CONCURRENTLY IF EXISTS idx_dag_runs_version;
