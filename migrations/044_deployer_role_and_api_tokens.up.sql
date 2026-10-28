-- The built-in deployer role and long-lived API tokens for CI (ADR 0074,
-- #1708).
--
-- deploy:dag is a narrower grant than write:dag: it allows registering a DAG
-- version and setting the current version (rollback), and nothing else. The
-- two routes accept either grant, so roles that hold write:dag, custom roles
-- included, keep deploying as before.
--
-- The deployer role reads DAGs and their versions and deploys them. It cannot
-- delete a DAG, pause or trigger one, or change anything else. It is created
-- in every tenant as a built-in (is_system) role, unassigned, so no account
-- gains anything on upgrade. A tenant that already has a custom role named
-- deployer keeps it untouched: the grants below only reach is_system roles.
--
-- Every statement is idempotent, so applying this again is a no-op.
BEGIN;

INSERT INTO permissions (action, resource, description) VALUES
    ('deploy', 'dag', 'Register DAG versions and set the current version')
ON CONFLICT (action, resource) DO NOTHING;

INSERT INTO roles (tenant_id, name, description, is_system)
SELECT t.id, 'deployer', 'Register DAG versions, set the current version and read DAGs', true
FROM tenants t
ON CONFLICT (tenant_id, name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN (VALUES ('read', 'dag'), ('deploy', 'dag')) AS g(action, resource) ON true
JOIN permissions p ON p.action = g.action AND p.resource = g.resource
WHERE r.name = 'deployer' AND r.is_system
ON CONFLICT DO NOTHING;

-- API tokens. The token itself is never stored: token_hash is the hex SHA-256
-- of it, the lookup key on every request. scopes are the OAuth scopes the
-- token carries (ADR 0067), which narrow what its owner's roles allow. A
-- revoked token keeps its row, so the audit trail can still name it.
CREATE TABLE IF NOT EXISTS api_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    scopes TEXT[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT api_tokens_hash_unique UNIQUE (token_hash),
    CONSTRAINT api_tokens_hash_form CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT api_tokens_name_length CHECK (char_length(name) BETWEEN 1 AND 64),
    CONSTRAINT api_tokens_has_scope CHECK (cardinality(scopes) > 0),
    CONSTRAINT api_tokens_expiry CHECK (expires_at > created_at)
);

-- One live token per name and owner, so a CI secret is named unambiguously.
CREATE UNIQUE INDEX IF NOT EXISTS api_tokens_live_name ON api_tokens (user_id, name) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_api_tokens_tenant ON api_tokens (tenant_id, created_at);

COMMIT;
