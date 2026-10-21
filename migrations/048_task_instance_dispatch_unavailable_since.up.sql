-- 048_task_instance_dispatch_unavailable_since.up.sql
-- When the current dispatch episode of a task instance first hit an
-- unavailable Kubernetes API (a 503, a 504, a 500 such as a fail-closed
-- admission webhook that is down, a connection refused or reset, or the
-- client deadline; #1606). Those failures no longer spend dispatch_attempts:
-- the task is re-offered on the backpressure cadence, and fails as
-- dispatch_failed only once the API has been unavailable for it longer than
-- the scheduler's window (ADR 0093). The column is set on the
-- first such failure of a try and cleared on the rails that reset
-- dispatch_attempts and on a reschedule re-dispatch. NULL means no outage
-- is being ridden out.
--
-- A nullable column without a default: Postgres adds it to the catalog
-- without rewriting or scanning task_instances. A previous release keeps
-- running against this schema: it never reads or writes the column.
--
-- lock_timeout bounds the wait for the ACCESS EXCLUSIVE lock behind a long
-- transaction. If it fires, the transaction rolls back, golang-migrate marks
-- version 48 dirty and nothing changed: run `migrate force 47` and retry. The
-- statement is idempotent.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE task_instances
    ADD COLUMN IF NOT EXISTS dispatch_unavailable_since TIMESTAMPTZ;
COMMIT;
