-- Reverses 039: drops the infra confirmation column.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances
    DROP COLUMN IF EXISTS infra_confirmed_at;
COMMIT;
