-- 059_tenant_row_security.down.sql
-- Undoes 059. Row level security is switched off on every table first: a
-- table that keeps it on with its policy dropped would refuse every row to
-- every role but its owner.
BEGIN;
SET LOCAL lock_timeout = '5s';

ALTER TABLE api_tokens NO FORCE ROW LEVEL SECURITY;
ALTER TABLE api_tokens DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON api_tokens;

ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_log DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON audit_log;

ALTER TABLE cli_device_authorizations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE cli_device_authorizations DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON cli_device_authorizations;

ALTER TABLE connections NO FORCE ROW LEVEL SECURITY;
ALTER TABLE connections DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON connections;

ALTER TABLE dag_favorites NO FORCE ROW LEVEL SECURITY;
ALTER TABLE dag_favorites DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON dag_favorites;

ALTER TABLE dag_runs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE dag_runs DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON dag_runs;

ALTER TABLE dags NO FORCE ROW LEVEL SECURITY;
ALTER TABLE dags DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON dags;

ALTER TABLE import_errors NO FORCE ROW LEVEL SECURITY;
ALTER TABLE import_errors DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON import_errors;

ALTER TABLE pools NO FORCE ROW LEVEL SECURITY;
ALTER TABLE pools DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON pools;

ALTER TABLE roles NO FORCE ROW LEVEL SECURITY;
ALTER TABLE roles DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON roles;

ALTER TABLE staging_volumes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE staging_volumes DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON staging_volumes;

ALTER TABLE task_instances NO FORCE ROW LEVEL SECURITY;
ALTER TABLE task_instances DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON task_instances;

ALTER TABLE usage_events NO FORCE ROW LEVEL SECURITY;
ALTER TABLE usage_events DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON usage_events;

ALTER TABLE users NO FORCE ROW LEVEL SECURITY;
ALTER TABLE users DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON users;

ALTER TABLE variables NO FORCE ROW LEVEL SECURITY;
ALTER TABLE variables DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON variables;

ALTER TABLE xcom_index NO FORCE ROW LEVEL SECURITY;
ALTER TABLE xcom_index DISABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dexaflow_tenant_barrier ON xcom_index;

DROP FUNCTION IF EXISTS dexaflow_bound_tenant();
COMMIT;
