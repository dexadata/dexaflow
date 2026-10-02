-- name: GetDefaultTenant :one
SELECT id, name FROM tenants WHERE name = 'default';

-- name: GetTenantByName :one
SELECT id, name FROM tenants WHERE name = $1;

-- name: InsertUser :one
-- Mirrors the bootstrap CreateUser insert (tenant_id, email, password_hash) but
-- returns the columns the admin create-user API echoes back to the caller.
INSERT INTO users (tenant_id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING id, email, is_active, created_at;

-- name: GetUserByEmail :one
SELECT u.id, u.tenant_id, u.email, u.password_hash, u.is_active
FROM users u
JOIN tenants t ON t.id = u.tenant_id
WHERE t.name = $1 AND u.email = $2;

-- name: GetUserByOIDCSubject :one
-- Resolve an OIDC identity to a Dexaflow user by its immutable (provider,
-- subject) pair (the trusted link key). Returns the tenant name (not the uuid)
-- so the reconstructed principal matches the login path's User.TenantID, plus
-- the active flag the login gates on. Never selects password_hash.
SELECT u.id, t.name AS tenant, u.email, u.is_active
FROM users u
JOIN tenants t ON t.id = u.tenant_id
WHERE u.oidc_provider = $1 AND u.oidc_subject = $2;

-- name: InsertOIDCUser :one
-- Just-in-time provisioning insert for a first OIDC login: an OIDC-only user
-- (NULL password) linked by (oidc_provider, oidc_subject). The unique
-- (oidc_provider, oidc_subject) constraint makes a concurrent double-provision
-- surface as a conflict rather than a duplicate identity.
INSERT INTO users (tenant_id, email, oidc_provider, oidc_subject)
VALUES ($1, $2, $3, $4)
RETURNING id, email, is_active, created_at;

-- name: GetUserByID :one
-- The by-id lookup backing the per-request authz reload. Returns the tenant
-- name (not the uuid) so the reconstructed principal matches the login path's
-- User.TenantID, plus the active flag the authenticator gates on.
SELECT u.id, t.name AS tenant, u.email, u.is_active
FROM users u
JOIN tenants t ON t.id = u.tenant_id
WHERE u.id = $1;

-- name: UpdateUserPassword :execrows
UPDATE users
SET password_hash = $3
WHERE email = $2
  AND tenant_id = (SELECT id FROM tenants WHERE name = $1);

-- name: ListUsers :many
-- One row per user in the tenant, newest first, with every granted role name
-- aggregated into a text array (empty when the user holds none). Paged by the
-- caller. Never selects password_hash — the list must not expose secrets.
SELECT u.id, u.email, u.is_active, u.created_at,
    COALESCE(array_remove(array_agg(r.name), NULL), '{}')::text[] AS roles
FROM users u
LEFT JOIN user_roles ur ON ur.user_id = u.id
LEFT JOIN roles r ON r.id = ur.role_id
WHERE u.tenant_id = $1
GROUP BY u.id, u.email, u.is_active, u.created_at
ORDER BY u.created_at DESC, u.id DESC
LIMIT $2 OFFSET $3;

-- name: GetUserRoles :many
SELECT r.name
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1;

-- name: GetRoleIDForUserTenant :one
-- Resolve a role name to its id within the user's OWN tenant, so an OIDC login
-- can reconcile the user's grants to the group-mapped set without the caller
-- passing the tenant separately. A name that is not a role in that tenant yields
-- no row, which the reconcile turns into a fail-closed error.
SELECT r.id
FROM roles r
JOIN users u ON u.tenant_id = r.tenant_id
WHERE u.id = $1 AND r.name = $2;

-- name: DeleteUserRoles :exec
-- Remove every role grant for a user: the delete half of the IdP-authoritative
-- reconcile that sets the grants to exactly the group-mapped set on each login.
DELETE FROM user_roles WHERE user_id = $1;

-- name: GetUserPermissions :many
SELECT DISTINCT p.action, p.resource
FROM user_roles ur
JOIN role_permissions rp ON rp.role_id = ur.role_id
JOIN permissions p ON p.id = rp.permission_id
WHERE ur.user_id = $1;

-- name: CountLocalPasswordUser :one
-- Does this address have a usable LOCAL password login in the tenant? The boot
-- check on auth.oidc.break_glass_emails asks it: an address on that allowlist
-- with no password row is an escape hatch that does not open, which is worse
-- than an empty allowlist because the operator believes they have one.
--
-- password_hash IS NOT NULL is the point, not merely that the row exists. A user
-- created by OIDC just-in-time provisioning has a NULL password (the table's
-- users_has_auth check permits it because the OIDC subject is the other half),
-- so the row can exist while no password can ever be verified against it.
SELECT COUNT(*)
FROM users u
JOIN tenants t ON t.id = u.tenant_id
WHERE t.name = $1 AND lower(u.email) = lower($2) AND u.password_hash IS NOT NULL AND u.is_active;
