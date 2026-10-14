-- Undo 045: drop the device sign-ins of the CLI. Every pending device
-- sign-in is lost, which costs at most one retried `dexaflow auth login
-- --device`; no token lives here.
BEGIN;
SET LOCAL lock_timeout = '5s';

DROP TABLE IF EXISTS cli_device_authorizations;

COMMIT;
