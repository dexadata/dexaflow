-- 042_task_instance_pool_slots.down.sql
-- Drop the per-instance pool size. The previous release never reads it: the
-- admission gate takes the size from the DAG spec, and its PoolSlotUsage counts
-- task instances again. Dropping the column drops its check with it.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances
    DROP COLUMN IF EXISTS pool_slots;
COMMIT;
