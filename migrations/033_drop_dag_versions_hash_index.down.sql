-- One CONCURRENTLY statement, no transaction, and no IF NOT EXISTS, so a
-- retry after an interrupted build fails instead of keeping the INVALID
-- idx_dag_versions_hash it leaves behind: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_dag_versions_hash, `migrate force 33`,
-- and retry.
CREATE INDEX CONCURRENTLY idx_dag_versions_hash ON dag_versions (spec_hash);
