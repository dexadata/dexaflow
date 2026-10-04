-- One CONCURRENTLY statement, no transaction, and no IF NOT EXISTS, so a
-- retry after an interrupted build fails instead of keeping the INVALID
-- idx_dag_versions_spec it leaves behind: run
-- DROP INDEX CONCURRENTLY IF EXISTS idx_dag_versions_spec, `migrate force 34`,
-- and retry.
CREATE INDEX CONCURRENTLY idx_dag_versions_spec ON dag_versions USING GIN (spec);
