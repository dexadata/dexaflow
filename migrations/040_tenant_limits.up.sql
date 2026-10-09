-- 040_tenant_limits.up.sql
-- Optional per-tenant limits, set by the operator through the service API
-- (PUT /api/v2/service/tenants/{tenant}). 0 means unlimited, so every existing
-- tenant keeps today's behavior:
--
--   max_dags                       DAGs the tenant may register
--   max_runs_per_day               DAG runs (manual and scheduled) per UTC day
--   min_schedule_interval_seconds  shortest gap a DAG schedule may leave
--
-- runs_day and runs_day_count count the runs the tenant created on runs_day
-- (a UTC date). Run creation bumps them with a conditional UPDATE in the same
-- transaction as the INSERT into dag_runs, which locks the tenant row, so two
-- concurrent triggers cannot both take the last slot of the day. The counter
-- moves only while max_runs_per_day is set, so tenants without the limit never
-- write their tenant row on the run path.
--
-- Constant defaults: Postgres 11+ adds these columns without rewriting the table.

BEGIN;

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS max_dags INTEGER NOT NULL DEFAULT 0
        CONSTRAINT tenants_max_dags_non_negative CHECK (max_dags >= 0),
    ADD COLUMN IF NOT EXISTS max_runs_per_day INTEGER NOT NULL DEFAULT 0
        CONSTRAINT tenants_max_runs_per_day_non_negative CHECK (max_runs_per_day >= 0),
    ADD COLUMN IF NOT EXISTS min_schedule_interval_seconds INTEGER NOT NULL DEFAULT 0
        CONSTRAINT tenants_min_schedule_interval_non_negative CHECK (min_schedule_interval_seconds >= 0),
    ADD COLUMN IF NOT EXISTS runs_day DATE,
    ADD COLUMN IF NOT EXISTS runs_day_count INTEGER NOT NULL DEFAULT 0;

COMMIT;
