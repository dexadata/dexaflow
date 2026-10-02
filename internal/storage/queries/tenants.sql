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

-- name: InsertDefaultPool :exec
-- The implicit default pool every tenant needs (migration 023 seeds it for
-- "default"), copied from default's so the slot count stays in step.
INSERT INTO pools (tenant_id, name, slots, description, is_default)
SELECT sqlc.arg(tenant_id)::uuid, p.name, p.slots, p.description, true
FROM pools p
JOIN tenants d ON d.id = p.tenant_id AND d.name = 'default'
WHERE p.is_default
ON CONFLICT (tenant_id, name) DO NOTHING;

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
