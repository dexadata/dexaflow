-- 041_tenant_max_task_pool_slots.down.sql
-- Drop the per-tenant task size ceiling; tenants become unlimited again.

BEGIN;

ALTER TABLE tenants
    DROP COLUMN IF EXISTS max_task_pool_slots;

COMMIT;
