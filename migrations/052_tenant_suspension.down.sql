-- 052_tenant_suspension.down.sql
-- Drop the tenant suspension; every suspended tenant runs again.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE tenants
    DROP COLUMN IF EXISTS suspension_reason,
    DROP COLUMN IF EXISTS suspension_mode,
    DROP COLUMN IF EXISTS suspended_at;

COMMIT;
