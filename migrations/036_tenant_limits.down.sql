-- 036_tenant_limits.down.sql
-- Drop the per-tenant limits and the daily run counter (unreferenced by other
-- objects). The limits an operator set are lost; tenants become unlimited.

BEGIN;

ALTER TABLE tenants
    DROP COLUMN IF EXISTS runs_day_count,
    DROP COLUMN IF EXISTS runs_day,
    DROP COLUMN IF EXISTS min_schedule_interval_seconds,
    DROP COLUMN IF EXISTS max_runs_per_day,
    DROP COLUMN IF EXISTS max_dags;

COMMIT;
