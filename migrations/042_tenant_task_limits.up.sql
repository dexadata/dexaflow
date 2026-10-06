-- 042_tenant_task_limits.up.sql
-- Two more optional per-tenant limits (#1484), set through the service API
-- like those of migrations 040 and 041. 0 means unlimited, so every existing
-- tenant keeps today's behavior:
--
--   max_tasks                tasks across the current version of the tenant's
--                            DAGs, checked at registration
--   max_task_runs_per_month  task runs per UTC calendar month, charged when a
--                            run is created (the run's task count)
--
-- task_runs_month (the first day of a UTC month) and task_runs_month_count
-- count what the tenant was charged that month. Run creation charges them with
-- a conditional UPDATE in the same transaction as the INSERT into dag_runs, as
-- runs_day does, and only while max_task_runs_per_month is set.
--
-- Constant defaults: Postgres 11+ adds these columns without rewriting the table.

BEGIN;

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS max_tasks INTEGER NOT NULL DEFAULT 0
        CONSTRAINT tenants_max_tasks_non_negative CHECK (max_tasks >= 0),
    ADD COLUMN IF NOT EXISTS max_task_runs_per_month INTEGER NOT NULL DEFAULT 0
        CONSTRAINT tenants_max_task_runs_per_month_non_negative CHECK (max_task_runs_per_month >= 0),
    ADD COLUMN IF NOT EXISTS task_runs_month DATE,
    ADD COLUMN IF NOT EXISTS task_runs_month_count INTEGER NOT NULL DEFAULT 0;

COMMIT;
