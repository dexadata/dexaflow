-- 053_dag_next_run_at.up.sql
-- When each scheduled DAG is next due (#2175). The scheduler stores it after
-- each tick that settles the DAG, and lists only the DAGs with no stored time
-- or a time at or before now, so a tick reads the DAGs that may be due instead
-- of every scheduled DAG. The time is at most an hour ahead, so a DAG that is
-- never due (a manual, continuous or spent @once DAG) is looked at hourly and
-- a change that did not clear the time is picked up. NULL means not computed
-- yet, so every existing DAG is listed on the first tick after the upgrade,
-- which computes and stores its time: there is nothing to backfill.
--
-- schedule_epoch counts the changes that make a stored time wrong: a new
-- current version (its schedule, start_date or catchup can change) and a
-- deleted run (the latest run can go back). Each such change clears
-- next_run_at and bumps the epoch in the same statement, and the scheduler
-- stores a time only while the epoch is the one it listed, so a change that
-- lands during a tick is never overwritten by a time computed before it.
--
-- NULL and a constant default: Postgres 11+ adds both columns without
-- rewriting the table. lock_timeout bounds the wait for the ACCESS EXCLUSIVE
-- lock: past 5 seconds the transaction rolls back, golang-migrate marks
-- version 53 dirty and nothing changed; force 52 and retry when the table is
-- quiet. Every statement can be repeated. The index is built by 054.
--
-- 0.5.7 and earlier never read the columns, so a previous release keeps
-- running against this schema.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE dags
    ADD COLUMN IF NOT EXISTS next_run_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS schedule_epoch BIGINT NOT NULL DEFAULT 0;

COMMIT;
