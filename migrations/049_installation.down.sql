-- 049_installation.down.sql
-- Drop the installation id. A previous release never reads it. Roll the
-- control plane back first: a server of this release reads a database without
-- the id as a replaced one (ADR 0090 decision 3).
BEGIN;
DROP TABLE IF EXISTS installation;
COMMIT;
