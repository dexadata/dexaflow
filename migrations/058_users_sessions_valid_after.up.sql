-- 058_users_sessions_valid_after.up.sql
-- When a user was last deactivated. A session token (JWT) whose session began
-- at or before this time is refused even after the user is reactivated, so
-- reactivating a user does not revive the sessions it had before (#2244
-- follow-up). NULL for a user never deactivated, which refuses nothing.
--
-- Additive and nullable (ADR 0090): the previous release does not read it and
-- keeps running on this schema. ADD COLUMN without a default rewrites nothing;
-- lock_timeout bounds the wait for its brief ACCESS EXCLUSIVE lock.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE users ADD COLUMN IF NOT EXISTS sessions_valid_after timestamptz;
COMMIT;
