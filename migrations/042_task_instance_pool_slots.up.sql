-- 042_task_instance_pool_slots.up.sql
-- How many slots of its pool a task instance takes while queued or running
-- (#1499). Since ADR 0066 the admission gate charges each task its pool_slots,
-- but the size lived only in the DAG spec, so PoolSlotUsage (behind
-- /api/v2/pools and the Pools screen) could only count task instances: a pool
-- of 8 holding two tasks of size 4 showed 2 occupied and 6 open while the gate
-- admitted nothing more. Materialization now writes the task's size here and a
-- clear refreshes it from the version the re-run executes, so PoolSlotUsage
-- sums the same weights the gate charges.
--
-- Constant default: Postgres 11+ adds the column without rewriting the table.
-- Every task instance created before the upgrade reads as one slot, its
-- pre-ADR 0066 weight, so the sized tasks of a run that started before the
-- upgrade are under-reported until they settle or are cleared. A previous
-- release keeps running against this schema: its inserts leave the column out
-- and get the default, with the same under-report.
--
-- The check is added NOT VALID inside the ALTER's transaction, so taking the
-- ACCESS EXCLUSIVE lock does not also scan the table; new rows are checked
-- from then on. The VALIDATE runs after the COMMIT, in its own transaction,
-- under a SHARE UPDATE EXCLUSIVE lock that blocks neither readers nor writers.
-- Every existing row holds the default 1, so it cannot fail.
--
-- lock_timeout bounds the wait behind a long transaction. If it fires, the
-- transaction rolls back, golang-migrate marks version 42 dirty and nothing
-- changed: run `migrate force 41` and retry. Every statement is idempotent, so
-- a failed VALIDATE is retried the same way.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS pool_slots INTEGER NOT NULL DEFAULT 1;
ALTER TABLE task_instances
    DROP CONSTRAINT IF EXISTS task_instances_pool_slots_positive;
ALTER TABLE task_instances
    ADD CONSTRAINT task_instances_pool_slots_positive CHECK (pool_slots >= 1) NOT VALID;
COMMIT;
ALTER TABLE task_instances VALIDATE CONSTRAINT task_instances_pool_slots_positive;
