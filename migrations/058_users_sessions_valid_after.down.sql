-- 058_users_sessions_valid_after.down.sql
-- Drops the column: sessions from before a deactivation work again once the
-- user is reactivated, as they did before 058.
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE users DROP COLUMN IF EXISTS sessions_valid_after;
COMMIT;
