-- Device sign-ins of the CLI (ADR 0081, #1704): `dexaflow auth login
-- --device` on a machine with no browser. The CLI starts one and polls for
-- it; a person approves it in a browser after signing in with the cluster's
-- single sign-on. The two steps can reach different replicas, so the pending
-- sign-in is shared here.
--
-- Neither code is stored: device_code_hash and user_code_hash are the hex
-- HMAC-SHA256 of the codes under a key derived from the JWT secret.
-- requester_hash is the same HMAC of the address that started the sign-in
-- (its /56 for IPv6), so one requester can hold only a few sign-ins at once;
-- requester_addr is that address as the server saw it, shown on the consent
-- page so the person can tell a sign-in someone else started. A row has
-- no tenant until it is approved: the device code is the only credential of
-- a pending sign-in, and the approval names the approver's tenant and user,
-- checked against users in the same statement. Rows are short lived: each is
-- deleted ten minutes after it expires, by the next start and by a sweep
-- every minute on every server, so a row and its requester_addr are kept
-- about 21 minutes after the start at the latest. No token is ever kept; it
-- is minted when the approved sign-in is redeemed.
--
-- Applying this again is a no-op. The table is new, so the lock_timeout
-- only bounds the wait for the tenants and users rows the foreign keys
-- reference.
BEGIN;
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS cli_device_authorizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code_hash TEXT NOT NULL,
    user_code_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    poll_interval_seconds INTEGER NOT NULL,
    requester_hash TEXT NOT NULL,
    requester_addr TEXT NOT NULL,
    last_polled_at TIMESTAMPTZ,
    decided_at TIMESTAMPTZ,
    tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    email TEXT,
    roles TEXT[],
    CONSTRAINT cli_device_device_code_unique UNIQUE (device_code_hash),
    CONSTRAINT cli_device_user_code_unique UNIQUE (user_code_hash),
    CONSTRAINT cli_device_hash_form CHECK (
        device_code_hash ~ '^[0-9a-f]{64}$' AND user_code_hash ~ '^[0-9a-f]{64}$' AND requester_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT cli_device_requester_addr_length CHECK (char_length(requester_addr) BETWEEN 1 AND 64),
    CONSTRAINT cli_device_status CHECK (status IN ('pending', 'approved', 'denied')),
    CONSTRAINT cli_device_interval CHECK (poll_interval_seconds BETWEEN 1 AND 600),
    CONSTRAINT cli_device_expiry CHECK (expires_at > created_at),
    CONSTRAINT cli_device_approved_has_user CHECK (
        (status = 'approved') = (tenant_id IS NOT NULL AND user_id IS NOT NULL AND email IS NOT NULL AND roles IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_cli_device_expires ON cli_device_authorizations (expires_at);
CREATE INDEX IF NOT EXISTS idx_cli_device_requester ON cli_device_authorizations (requester_hash, expires_at);

COMMIT;
