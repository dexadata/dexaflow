-- Reverses 038. The old unique key is one row per try, so history rows that
-- archived several executions of one try are collapsed first, keeping the
-- latest epoch of each try (the one the old key would have kept had the
-- archive recorded the latest attempt).
DELETE FROM task_instance_history h
USING task_instance_history newer
WHERE newer.task_instance_id = h.task_instance_id
  AND newer.try_number = h.try_number
  AND newer.attempt_epoch > h.attempt_epoch;

ALTER TABLE task_instance_history
    DROP CONSTRAINT task_instance_history_unique;
ALTER TABLE task_instance_history
    ADD CONSTRAINT task_instance_history_unique UNIQUE (task_instance_id, try_number);
ALTER TABLE task_instance_history
    DROP COLUMN IF EXISTS attempt_epoch;

ALTER TABLE task_instances
    DROP COLUMN IF EXISTS attempt_epoch;
