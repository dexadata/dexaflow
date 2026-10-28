-- 052_tenant_suspension.up.sql
-- A tenant suspension set by the operator through the service API
-- (PUT /api/v2/service/tenants/{tenant}/suspension, #2181). While
-- suspended_at is set, the tenant gets no new run, manual or scheduled, and
-- its DAGs drop out of the scheduler's list; the DAGs' own is_paused is never
-- touched, so lifting the suspension restores exactly what the tenant had.
-- suspension_mode is "hold" today; "drain" and "stop" are reserved by the
-- check so a later release can apply them without another migration.
--
-- The columns are NULL or have a constant default, so Postgres 11+ adds them
-- without rewriting the table; the checks read tenants once, which holds one
-- row per tenant. lock_timeout bounds the wait for the ACCESS EXCLUSIVE lock:
-- past 5 seconds the transaction rolls back, golang-migrate marks version 52
-- dirty and nothing changed; force 51 and retry. Every statement can be
-- repeated.
--
-- 0.5.7 and earlier never read the columns, so a previous release keeps
-- running against this schema; it does not honor a suspension.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS suspension_mode TEXT,
    ADD COLUMN IF NOT EXISTS suspension_reason TEXT NOT NULL DEFAULT '';

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_suspension_mode_known;
ALTER TABLE tenants ADD CONSTRAINT tenants_suspension_mode_known
    CHECK (suspension_mode IS NULL OR suspension_mode IN ('hold', 'drain', 'stop'));

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_suspension_complete;
ALTER TABLE tenants ADD CONSTRAINT tenants_suspension_complete
    CHECK ((suspended_at IS NULL) = (suspension_mode IS NULL));

COMMIT;
