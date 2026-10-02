-- One CONCURRENTLY statement, no transaction. If the build is interrupted it
-- leaves an INVALID idx_dag_versions_spec that IF NOT EXISTS would keep: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_dag_versions_spec, `migrate force 34`,
-- and retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dag_versions_spec ON dag_versions USING GIN (spec);
