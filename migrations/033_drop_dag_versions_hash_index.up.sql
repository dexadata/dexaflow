-- Drop idx_dag_versions_hash (spec_hash) (performance item D3), replaced by
-- idx_dag_versions_dag_hash (dag_id, spec_hash) from 032. No query looks a
-- version up by spec_hash without its dag_id.
--
-- CONCURRENTLY, one statement, no transaction (see 032). If it is interrupted,
-- version 33 is left dirty and the index may be left INVALID: re-run the
-- statement by hand, then `migrate force 33`.
DROP INDEX CONCURRENTLY IF EXISTS idx_dag_versions_hash;
