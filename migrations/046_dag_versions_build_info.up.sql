-- 046_dag_versions_build_info.up.sql
-- Where each DAG version was built (#1702): the commit and repository of its
-- sources, the CI pipeline run, the builder, the base image and the CLI
-- version, as the register request sent them. It is metadata of the
-- register, not part of dag.json, so it changes neither the spec, its hash
-- nor the version label; NULL when the register sent none.
--
-- A nullable column without a default: Postgres adds it to the catalog
-- without rewriting or scanning dag_versions. A previous release keeps
-- running against this schema: its inserts leave the column out and get
-- NULL, and it never reads the column.
--
-- lock_timeout bounds the wait for the ACCESS EXCLUSIVE lock behind a long
-- transaction. If it fires, the transaction rolls back, golang-migrate marks
-- version 46 dirty and nothing changed: run `migrate force 45` and retry. The
-- statement is idempotent.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE dag_versions
    ADD COLUMN IF NOT EXISTS build_info JSONB;
COMMIT;
