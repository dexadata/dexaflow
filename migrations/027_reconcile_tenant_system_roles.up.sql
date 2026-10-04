-- Reconcile the built-in roles of every tenant with the default tenant (#1305).
--
-- Tenants created through the service API (#1283) copy the built-in roles and
-- their grants from "default" once, when they are created. Migrations up to and
-- including 025_role_ladder changed the built-in roles of "default" only, so a
-- tenant created before such a migration lacks the grants it added, and a grant
-- a migration revokes would survive on every other tenant.
--
-- This migration makes every other tenant's built-in (is_system) roles equal to
-- default's: it creates the built-in roles a tenant is missing, adds the grants
-- default has, and removes the grants default does not have. It only reads and
-- writes roles with is_system = true; custom roles, and a custom role that
-- happens to share a built-in role's name, are the tenant's own and are left
-- untouched. A built-in role default no longer has is left as it is too: there
-- is nothing to compare it with, and deleting a role would also drop the user
-- assignments that reference it. user_roles is never touched, so no account
-- gains or loses a role here.
--
-- From here on, a migration that changes built-in roles applies to every tenant
-- by joining on roles.is_system (see CONTRIBUTING.md, "Writing a migration");
-- migrations/tenant_roles_test.go fails one that filters on the default tenant.
-- Every statement is idempotent, so running this again is a no-op.

-- Built-in roles the tenant is missing.
INSERT INTO roles (tenant_id, name, description, is_system)
SELECT t.id, dr.name, dr.description, true
FROM roles dr
JOIN tenants d ON d.id = dr.tenant_id AND d.name = 'default'
CROSS JOIN tenants t
WHERE dr.is_system AND t.id <> d.id
ON CONFLICT (tenant_id, name) DO NOTHING;

-- Keep the descriptions in step, so the UI shows the same text everywhere.
UPDATE roles r
SET description = dr.description
FROM roles dr
JOIN tenants d ON d.id = dr.tenant_id AND d.name = 'default'
WHERE dr.is_system AND r.is_system
  AND r.name = dr.name AND r.tenant_id <> d.id
  AND r.description IS DISTINCT FROM dr.description;

-- Grants default has that the tenant's twin role is missing.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, rp.permission_id
FROM roles dr
JOIN tenants d ON d.id = dr.tenant_id AND d.name = 'default'
JOIN role_permissions rp ON rp.role_id = dr.id
JOIN roles r ON r.name = dr.name AND r.tenant_id <> d.id AND r.is_system
WHERE dr.is_system
ON CONFLICT DO NOTHING;

-- Grants the tenant's twin role has that default's no longer does.
DELETE FROM role_permissions rp
USING roles r, roles dr, tenants d
WHERE rp.role_id = r.id
  AND r.is_system
  AND d.name = 'default'
  AND dr.tenant_id = d.id
  AND dr.is_system
  AND dr.name = r.name
  AND r.tenant_id <> d.id
  AND NOT EXISTS (
      SELECT 1 FROM role_permissions drp
      WHERE drp.role_id = dr.id AND drp.permission_id = rp.permission_id
  );
