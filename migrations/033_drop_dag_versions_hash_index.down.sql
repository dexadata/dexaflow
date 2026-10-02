-- One CONCURRENTLY statement, no transaction. If the build is interrupted it
-- leaves an INVALID idx_dag_versions_hash that IF NOT EXISTS would keep: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_dag_versions_hash, `migrate force 33`,
-- and retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dag_versions_hash ON dag_versions (spec_hash);
