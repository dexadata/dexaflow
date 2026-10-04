-- Attempt epoch (ADR 0051 amendment, #1130 #911 #901 #863). try_number is the
-- user's retry count, but an infra re-place, a reschedule re-dispatch, a warm
-- requeue and a repeated dispatch all reuse it, so it cannot tell two
-- executions of one try apart. attempt_epoch is a per-row counter that does:
-- every rail that can start a new execution bumps it, and the dispatcher
-- claims a fresh value when it resolves the row, so (try_number,
-- attempt_epoch) identifies exactly one execution. It is never reset.
--
-- DEFAULT 0 needs no backfill: every row, token and pod that exists before the
-- upgrade is epoch 0, and the first post-upgrade dispatch of any row claims 1.
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS attempt_epoch INT NOT NULL DEFAULT 0;

-- The archive keeps one row per execution, not one per try: before this, the
-- second infra re-place of a try hit ON CONFLICT DO NOTHING and was dropped
-- (#863). (task_instance_id, try_number) was already unique, so widening the
-- key cannot fail on existing rows.
ALTER TABLE task_instance_history
    ADD COLUMN IF NOT EXISTS attempt_epoch INT NOT NULL DEFAULT 0;
ALTER TABLE task_instance_history
    DROP CONSTRAINT task_instance_history_unique;
ALTER TABLE task_instance_history
    ADD CONSTRAINT task_instance_history_unique UNIQUE (task_instance_id, try_number, attempt_epoch);
