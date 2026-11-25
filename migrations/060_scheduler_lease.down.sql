-- 060_scheduler_lease.down.sql
-- Drops the leader term fence of ADR 0106 (#2329). The triggers go first so
-- no statement runs a function that is gone. A server with
-- scheduler.lease_fencing on must be rolled back to a binary without it, or
-- have it turned off, before this runs: its leadership needs the lease row.
BEGIN;
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON staging_volumes;
DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON task_instance_history;
DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON task_instances;
DROP TRIGGER IF EXISTS dexaflow_fence_leader_term ON dag_runs;
DROP FUNCTION IF EXISTS dexaflow_fence_leader_term();
DROP TABLE IF EXISTS scheduler_lease;

COMMIT;
