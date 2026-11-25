-- 060_scheduler_lease.down.sql
-- Drops the leader term fence of ADR 0106 (#2329). The triggers go first so
-- no statement runs a function that is gone. Turn scheduler.lease_fencing off
-- (or roll back to a binary without it) before this runs: a server with
-- fencing on needs the lease row to lead, while one with fencing off leads on
-- the advisory lock alone when the table is gone.
BEGIN;
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON staging_volumes;
DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON task_instance_history;
DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON task_instances;
DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON dag_runs;
DROP FUNCTION IF EXISTS dexaflow_fence_leader_term();
DROP TABLE IF EXISTS scheduler_lease;

COMMIT;
