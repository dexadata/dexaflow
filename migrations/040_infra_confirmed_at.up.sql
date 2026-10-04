-- Infra confirmation (ADR 0052 amendment, #900 #1124). An agent_lost, pod_lost
-- or dispatch_lost mark is a guess from absence. On Kubernetes the reaper now
-- leaves infra_confirmed_at NULL (provisional) and the reconciler stamps it
-- once the attempt's pods show no SUCCESS record; while a mark is provisional
-- the planner treats the task as active, so a durable SUCCESS can still settle
-- it. Lite stamps it at mark time. Every reset rail clears it.
--
-- Expand only: one nullable column, a metadata change with no table rewrite.
-- Infra failures that exist before the upgrade are stamped confirmed (with
-- their ended_at) so the planner treats them exactly as before. A previous
-- release keeps running against this schema: it never reads the column, and
-- its marks leave it NULL, which the new planner treats as provisional for at
-- most InfraConfirmMaxWait after ended_at.
--
-- lock_timeout bounds the wait behind a long transaction. If it fires, the
-- transaction rolls back, golang-migrate marks version 40 dirty and nothing
-- changed: run `migrate force` to the previous applied version and retry.
--
-- The backfill runs after the COMMIT, in its own transaction. Inside the ALTER's
-- transaction it would scan task_instances (no index covers failed rows) while
-- holding the ACCESS EXCLUSIVE lock, blocking every reader and writer of the
-- table, the running previous release included, for the whole scan. Outside it
-- the UPDATE locks only the rows it stamps. Both statements are idempotent, so
-- if the backfill fails, `migrate force` to the previous version and rerun.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS infra_confirmed_at TIMESTAMPTZ;
COMMIT;
UPDATE task_instances
SET infra_confirmed_at = COALESCE(ended_at, now())
WHERE state = 'failed' AND last_failure_kind = 'infra' AND infra_confirmed_at IS NULL;
