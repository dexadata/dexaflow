-- 055_uuidv7_run_and_task_ids.down.sql
-- New dag_runs and task_instances rows get a random UUIDv4 key again. Ids
-- already made as UUIDv7 stay as they are: both are uuid, so they keep
-- working.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE task_instances ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE dag_runs ALTER COLUMN id SET DEFAULT gen_random_uuid();

DROP FUNCTION IF EXISTS dexaflow_uuidv7();

COMMIT;
