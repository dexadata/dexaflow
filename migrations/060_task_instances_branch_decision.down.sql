-- 060_task_instances_branch_decision.down.sql
-- Drops the decisions: a binary before 060 does not apply them, so the
-- planner of that binary runs every child of a branch task.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instance_history DROP COLUMN IF EXISTS branch_decision;
ALTER TABLE task_instances DROP COLUMN IF EXISTS branch_decision;
COMMIT;
