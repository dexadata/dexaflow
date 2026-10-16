-- 047_removed_task_state.up.sql
-- The 'removed' task state (ADR 0086, #1894). A clear with
-- run_on_latest_version that moves a finished run to the DAG's current
-- version marks removed the task instances of the tasks that version no
-- longer declares, as Airflow's verify_integrity does. It is terminal, and
-- the scheduler never counts it: a run's outcome reads only the tasks of the
-- run's version.
--
-- ADD VALUE only touches the catalog: no table is rewritten or locked
-- beyond the type. The new value is not used in this transaction. A previous
-- release keeps running against this schema, since it never writes the value
-- and plans only the tasks of the run's version. IF NOT EXISTS makes the
-- statement safe to apply again.

BEGIN;

ALTER TYPE task_state ADD VALUE IF NOT EXISTS 'removed';

COMMIT;
