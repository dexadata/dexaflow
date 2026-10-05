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
--
-- Expand only. Both ADD COLUMNs are metadata changes (no table rewrite), and
-- task_instance_history keeps its (task_instance_id, try_number) unique key:
-- the previous release archives with ON CONFLICT on exactly that key, and it
-- keeps running against this schema during a rolling upgrade (the migration
-- hook runs first) and after a rollback. Widening the key to one row per
-- execution is a later, separately planned change.
--
-- task_instances is altered last so its ACCESS EXCLUSIVE lock is held for the
-- shortest time. lock_timeout bounds the wait behind a long transaction, so the
-- ALTER cannot queue all scheduler traffic behind it. If it fires, the
-- transaction rolls back, golang-migrate marks version 38 dirty and nothing
-- changed: run `migrate force 37` (or the previous applied version) and retry
-- when the tables are quiet.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instance_history
    ADD COLUMN IF NOT EXISTS attempt_epoch INT NOT NULL DEFAULT 0;
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS attempt_epoch INT NOT NULL DEFAULT 0;
COMMIT;
