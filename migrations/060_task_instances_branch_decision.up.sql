-- 060_task_instances_branch_decision.up.sql
-- The branch decision a branch or short-circuit task reported with its
-- success (ADR 0064 D1): {"follow_task_ids": [...]} for a branch task,
-- {"short_circuit_falsy": true|false} for a short-circuit one. The planner
-- skips the children the decision did not follow. NULL on every other row,
-- and on a branch task that has not succeeded or was marked success by hand.
-- task_instance_history keeps each archived attempt's decision, so the tries
-- view can show which branch each attempt took.
--
-- Additive and nullable (ADR 0090): the previous release does not read either
-- column and keeps running on this schema. ADD COLUMN without a default
-- rewrites nothing; lock_timeout bounds the wait for its brief ACCESS
-- EXCLUSIVE lock.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances ADD COLUMN IF NOT EXISTS branch_decision jsonb;
ALTER TABLE task_instance_history ADD COLUMN IF NOT EXISTS branch_decision jsonb;
COMMIT;
