-- 059_tenant_row_security.up.sql
-- Row level security as a second tenant barrier (ADR 0104, #2180): a
-- function that reads the tenant a connection is bound to, and one policy per
-- tenant keyed table that limits a bound connection to that tenant's rows.
-- Two tables keep rows with no tenant: an audit_log row of a global event,
-- which a bound connection may write but not read, and a CLI sign-in that has
-- not picked its tenant yet, which a bound connection may read and approve
-- (its codes, not its tenant, protect it).
--
-- Inert as shipped (ADR 0068, ADR 0090): a policy does nothing until row
-- level security is enabled on its table, and this migration enables it on
-- none. `dexaflow-migrate rls on` does, and only then does a server with
-- database.row_level_security bind its connections. A connection that is not
-- bound (the scheduler, maintenance, the previous release's binary) passes
-- every policy, so enabling it never changes what those paths read.
--
-- CREATE POLICY takes an ACCESS EXCLUSIVE lock on its table for an instant;
-- lock_timeout bounds the wait, and the whole file is one transaction.
BEGIN;
SET LOCAL lock_timeout = '5s';

-- The tenant this connection is bound to, NULL when it is not bound. The
-- server sets dexaflow.tenant_id on a connection it hands to a request of one
-- tenant and clears it otherwise; a tenant name that does not exist binds the
-- nil UUID, which matches no row.
CREATE OR REPLACE FUNCTION dexaflow_bound_tenant() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT NULLIF(current_setting('dexaflow.tenant_id', true), '')::uuid $$;

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON api_tokens;
CREATE POLICY dexaflow_tenant_barrier ON api_tokens
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON audit_log;
CREATE POLICY dexaflow_tenant_barrier ON audit_log
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()) OR tenant_id IS NULL);

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON cli_device_authorizations;
CREATE POLICY dexaflow_tenant_barrier ON cli_device_authorizations
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()) OR tenant_id IS NULL)
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()) OR tenant_id IS NULL);

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON connections;
CREATE POLICY dexaflow_tenant_barrier ON connections
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON dag_favorites;
CREATE POLICY dexaflow_tenant_barrier ON dag_favorites
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON dag_runs;
CREATE POLICY dexaflow_tenant_barrier ON dag_runs
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON dags;
CREATE POLICY dexaflow_tenant_barrier ON dags
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON import_errors;
CREATE POLICY dexaflow_tenant_barrier ON import_errors
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON pools;
CREATE POLICY dexaflow_tenant_barrier ON pools
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON roles;
CREATE POLICY dexaflow_tenant_barrier ON roles
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON staging_volumes;
CREATE POLICY dexaflow_tenant_barrier ON staging_volumes
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON task_instances;
CREATE POLICY dexaflow_tenant_barrier ON task_instances
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON usage_events;
CREATE POLICY dexaflow_tenant_barrier ON usage_events
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON users;
CREATE POLICY dexaflow_tenant_barrier ON users
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON variables;
CREATE POLICY dexaflow_tenant_barrier ON variables
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

DROP POLICY IF EXISTS dexaflow_tenant_barrier ON xcom_index;
CREATE POLICY dexaflow_tenant_barrier ON xcom_index
    USING ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()))
    WITH CHECK ((SELECT dexaflow_bound_tenant()) IS NULL OR tenant_id = (SELECT dexaflow_bound_tenant()));

COMMIT;
