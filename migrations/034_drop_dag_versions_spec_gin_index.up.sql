-- Drop the GIN index on dag_versions.spec (from 002, performance item D3). No
-- query filters spec by JSONB containment (@>, <@), key existence (?, ?|, ?&)
-- or jsonpath (@?, @@); the spec is only ever read whole, by version id. The
-- index only made every version insert decompose the full DAG document into
-- GIN entries.
--
-- CONCURRENTLY, one statement, no transaction (see 032). If it is interrupted,
-- version 34 is left dirty and the index may be left INVALID: re-run the
-- statement by hand, then `migrate force 34`.
DROP INDEX CONCURRENTLY IF EXISTS idx_dag_versions_spec;
