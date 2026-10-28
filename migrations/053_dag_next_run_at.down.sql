-- 053_dag_next_run_at.down.sql
-- Drop the stored next due time; the scheduler lists every scheduled DAG
-- again.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE dags
    DROP COLUMN IF EXISTS schedule_epoch,
    DROP COLUMN IF EXISTS next_run_at;

COMMIT;
