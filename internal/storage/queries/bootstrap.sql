-- name: CountUsers :one
SELECT count(*) FROM users WHERE tenant_id = $1;

-- name: CreateUser :one
INSERT INTO users (tenant_id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING id;

-- name: GetRoleByName :one
SELECT id FROM roles WHERE tenant_id = $1 AND name = $2;

-- name: AssignUserRole :exec
INSERT INTO user_roles (user_id, role_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: GetRolePermissionsByID :many
-- The grants of one role, for a CI OIDC session that acts with that role
-- alone (ADR 0092). The caller resolved the role in its tenant first
-- (GetRoleByName), so the tenant predicate is on that lookup.
SELECT p.action, p.resource
FROM role_permissions rp
JOIN permissions p ON p.id = rp.permission_id
WHERE rp.role_id = $1
ORDER BY p.action, p.resource;
