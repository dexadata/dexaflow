-- 048_task_instance_dispatch_unavailable_since.down.sql
-- Drop the start of the unavailable-API dispatch window. The previous release
-- never reads it; a task riding out an outage at the time of the rollback
-- falls back to the dispatch_attempts budget.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances
    DROP COLUMN IF EXISTS dispatch_unavailable_since;
COMMIT;
