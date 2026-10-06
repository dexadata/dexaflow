-- 042_tenant_task_limits.down.sql
-- Drop the task limits and the monthly task run counter; tenants become
-- unlimited again.

BEGIN;

ALTER TABLE tenants
    DROP COLUMN IF EXISTS task_runs_month_count,
    DROP COLUMN IF EXISTS task_runs_month,
    DROP COLUMN IF EXISTS max_task_runs_per_month,
    DROP COLUMN IF EXISTS max_tasks;

COMMIT;
