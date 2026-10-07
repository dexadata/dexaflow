-- Reverses 038: drops the attempt epoch columns. The history unique key was
-- never changed, so nothing else needs restoring.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instance_history
    DROP COLUMN IF EXISTS attempt_epoch;
ALTER TABLE task_instances
    DROP COLUMN IF EXISTS attempt_epoch;
COMMIT;
