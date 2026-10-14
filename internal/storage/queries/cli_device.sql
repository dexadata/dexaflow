-- name: DeleteExpiredCLIDevices :execrows
-- Forgets device sign-ins that expired more than ten minutes ago, with the
-- address each was started from. Run before each create and by a sweep
-- every minute, so the table holds only what is live or just expired (a CLI
-- polling a just-expired sign-in still hears expired_token).
DELETE FROM cli_device_authorizations WHERE expires_at < now() - interval '10 minutes';

-- name: LockCLIDeviceStarts :exec
-- Serializes starts for the rest of the transaction, so the caps the count
-- below checks hold exactly under concurrent starts on any replica. The key
-- is this table's own; nothing else takes it.
SELECT pg_advisory_xact_lock(1704081);

-- name: CountLiveCLIDevices :one
-- The live sign-ins, in all and from one requester.
SELECT count(*) AS live,
       count(*) FILTER (WHERE requester_hash = sqlc.arg(requester_hash)) AS from_requester
FROM cli_device_authorizations WHERE expires_at > now();

-- name: InsertCLIDevice :one
-- Starts a device sign-in. The expiry is the database's clock, the one every
-- other statement here compares against.
INSERT INTO cli_device_authorizations (device_code_hash, user_code_hash, expires_at, poll_interval_seconds, requester_hash, requester_addr)
VALUES (sqlc.arg(device_code_hash), sqlc.arg(user_code_hash),
        now() + make_interval(secs => sqlc.arg(ttl_seconds)::double precision), sqlc.arg(poll_interval_seconds),
        sqlc.arg(requester_hash), sqlc.arg(requester_addr))
RETURNING id, created_at, expires_at;

-- name: FindPendingCLIDeviceByUserCode :one
-- The sign-in a person typed the user code of, while it can still be
-- approved.
SELECT id, created_at, expires_at, requester_addr FROM cli_device_authorizations
WHERE user_code_hash = $1 AND status = 'pending' AND expires_at > now();

-- name: ApproveCLIDevice :execrows
-- Approves a pending, unexpired sign-in as a user of the named tenant. The
-- join is the tenant check: a user outside that tenant matches no row, and
-- neither does a sign-in already decided, so each is approved once across
-- every replica.
UPDATE cli_device_authorizations d
SET status = 'approved', decided_at = now(), tenant_id = t.id, user_id = u.id,
    email = sqlc.arg(email), roles = sqlc.arg(roles)::text[]
FROM users u
JOIN tenants t ON t.id = u.tenant_id
WHERE d.id = sqlc.arg(id) AND d.status = 'pending' AND d.expires_at > now()
  AND u.id = sqlc.arg(user_id) AND t.name = sqlc.arg(tenant);

-- name: DenyCLIDevice :execrows
-- Records that the person canceled a pending, unexpired sign-in.
UPDATE cli_device_authorizations
SET status = 'denied', decided_at = now()
WHERE id = $1 AND status = 'pending' AND expires_at > now();

-- name: LockCLIDeviceForPoll :one
-- Locks the sign-in a poll names, with what the poll decides from: whether
-- it expired, and whether this poll came sooner than the interval allows
-- (with a fifth of it as slack for network jitter).
SELECT d.id, d.status, d.poll_interval_seconds,
       d.expires_at <= now() AS expired,
       (d.last_polled_at IS NOT NULL
        AND now() < d.last_polled_at + make_interval(secs => (d.poll_interval_seconds * 0.8)::double precision)) AS too_fast,
       d.user_id, d.email, d.roles, t.name AS tenant
FROM cli_device_authorizations d
LEFT JOIN tenants t ON t.id = d.tenant_id
WHERE d.device_code_hash = $1
FOR UPDATE OF d;

-- name: MarkCLIDevicePolled :exec
-- Records a poll, and the longer interval a slow_down answered with.
UPDATE cli_device_authorizations
SET last_polled_at = now(), poll_interval_seconds = sqlc.arg(poll_interval_seconds)
WHERE id = sqlc.arg(id);

-- name: DeleteCLIDevice :execrows
-- Ends a sign-in once its outcome was answered: redeemed or denied. The
-- poll answers that outcome only when this deleted the row, so a sign-in is
-- answered once even if two polls read it.
DELETE FROM cli_device_authorizations WHERE id = $1;
