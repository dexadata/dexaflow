-- name: InsertTenantIfMissing :execrows
-- Service API (#1283): create a tenant by name, or do nothing when it exists.
-- Zero rows affected means it already existed.
INSERT INTO tenants (name, display_name)
VALUES ($1, $2)
ON CONFLICT (name) DO NOTHING;

-- name: CopyDefaultSystemRoles :exec
-- Give a tenant the built-in roles the migrations seed for "default". Copying
-- keeps every tenant's ladder equal to default's as later migrations change it.
INSERT INTO roles (tenant_id, name, description, is_system)
SELECT sqlc.arg(tenant_id)::uuid, r.name, r.description, true
FROM roles r
JOIN tenants d ON d.id = r.tenant_id AND d.name = 'default'
WHERE r.is_system
ON CONFLICT (tenant_id, name) DO NOTHING;

-- name: CopyDefaultRolePermissions :exec
-- Grant each copied built-in role the same permissions as its "default" twin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT nr.id, rp.permission_id
FROM roles dr
JOIN tenants d ON d.id = dr.tenant_id AND d.name = 'default'
JOIN role_permissions rp ON rp.role_id = dr.id
JOIN roles nr ON nr.tenant_id = sqlc.arg(tenant_id)::uuid AND nr.name = dr.name
WHERE dr.is_system
ON CONFLICT DO NOTHING;

-- name: DeleteStaleSystemRolePermissions :exec
-- Remove a built-in role's grants that its "default" twin no longer has, so
-- re-running ensure undoes drift (#1305). Only system roles with a twin in
-- "default" are touched; custom roles keep every grant. For "default" itself
-- each role is its own twin, so nothing is deleted.
DELETE FROM role_permissions rp
USING roles nr, roles dr, tenants d
WHERE rp.role_id = nr.id
  AND nr.tenant_id = sqlc.arg(tenant_id)::uuid
  AND nr.is_system
  AND d.name = 'default'
  AND dr.tenant_id = d.id
  AND dr.name = nr.name
  AND dr.is_system
  AND NOT EXISTS (
      SELECT 1 FROM role_permissions drp
      WHERE drp.role_id = dr.id AND drp.permission_id = rp.permission_id
  );

-- name: InsertDefaultPool :exec
-- The implicit default pool every tenant needs (migration 023 seeds it for
-- "default"), copied from default's so the slot count stays in step.
INSERT INTO pools (tenant_id, name, slots, description, is_default)
SELECT sqlc.arg(tenant_id)::uuid, p.name, p.slots, p.description, true
FROM pools p
JOIN tenants d ON d.id = p.tenant_id AND d.name = 'default'
WHERE p.is_default
ON CONFLICT (tenant_id, name) DO UPDATE SET is_default = true, updated_at = now()
  WHERE NOT pools.is_default;

-- name: UpsertDefaultPoolSlots :exec
-- Sizes a tenant's default pool to an explicit slot count: inserts it under the
-- given name with the seed description, or re-sizes the row already there. A
-- row with that name left without is_default (a tenant created before the
-- default pool was seeded per tenant) is marked default, so the delete guard
-- and the pools view treat it as the pool the scheduler falls back to.
INSERT INTO pools (tenant_id, name, slots, description, is_default)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(name), sqlc.arg(slots), 'Default pool', true)
ON CONFLICT (tenant_id, name) DO UPDATE SET slots = EXCLUDED.slots, is_default = true, updated_at = now();

-- name: ListTenantRolePermissions :many
-- "role:action:resource" for every grant of a tenant's built-in roles.
SELECT (r.name || ':' || p.action || ':' || p.resource)::text AS grant_key
FROM roles r
JOIN tenants t ON t.id = r.tenant_id
JOIN role_permissions rp ON rp.role_id = r.id
JOIN permissions p ON p.id = rp.permission_id
WHERE t.name = $1 AND r.is_system;

-- name: TenantHasDefaultPool :one
SELECT EXISTS (
    SELECT 1 FROM pools p JOIN tenants t ON t.id = p.tenant_id
    WHERE t.name = $1 AND p.is_default
)::bool AS has_default;
