-- 051_drop_unwritten_tables.up.sql
-- Drop the three tables nothing writes (#1186): scheduler_loops and replicas
-- (004, "purely for observability") and task_state_history (003, "audit +
-- observability"). No code ever inserted a row into any of them, so a schema
-- that carries them only suggests data that does not exist. Leader election
-- uses an advisory lock, not replicas; the scheduler tick is measured by
-- dexaflow_scheduler_loop_duration_seconds; a task instance's attempts are in
-- task_instance_history. A row someone wrote by hand is lost.
--
-- Dropping task_state_history removes its foreign key trigger from
-- task_instances, which takes an ACCESS EXCLUSIVE lock on task_instances for
-- the rest of this transaction. The drop itself is instant (the tables are
-- empty), but the lock waits for every open transaction that touched
-- task_instances, and queries on it queue behind the wait. lock_timeout bounds
-- that wait: past 5 seconds the transaction rolls back, golang-migrate marks
-- version 51 dirty and nothing changed; force 50 and retry when the table is
-- quiet. Every statement can be repeated.
--
-- 0.5.5 and every later release never read or write the three tables, so the
-- previous release keeps running against this schema. 0.5.4 and earlier delete from
-- task_state_history in retention: with retention.dag_runs_days set, they
-- fail their retention cycles against this schema until they are replaced, or
-- until the down migration restores the table (see the 0.5.8 upgrade notes).

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP FUNCTION IF EXISTS trim_scheduler_loops();
DROP TABLE IF EXISTS scheduler_loops;
DROP TABLE IF EXISTS replicas;
DROP TABLE IF EXISTS task_state_history;

COMMIT;
