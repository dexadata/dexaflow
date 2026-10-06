-- 041_tenant_max_task_pool_slots.up.sql
-- Per-tenant ceiling on a task's size (ADR 0066 §5), set by the operator
-- through the service API (PUT /api/v2/service/tenants/{tenant}) like the
-- limits of migration 040. 0 means unlimited, so every existing tenant keeps
-- today's behavior; otherwise registration refuses a DAG with a task whose
-- pool_slots is above it. A platform that sizes each tenant's default pool sets
-- it to that pool's slots, so a task that could never fit is refused when it is
-- pushed instead of waiting forever.
--
-- Constant default: Postgres 11+ adds the column without rewriting the table.

BEGIN;

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS max_task_pool_slots INTEGER NOT NULL DEFAULT 0
        CONSTRAINT tenants_max_task_pool_slots_non_negative CHECK (max_task_pool_slots >= 0);

COMMIT;
