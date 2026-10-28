-- 049_installation.up.sql
-- The installation id of ADR 0090 decision 3: one row that tells a replaced
-- or emptied database from the one a release was installed on. The SQL only
-- creates the table. The id is written by the dexaflow-migrate wrapper after
-- `up`, from the value the chart hands the migration Job, and kept in a
-- ConfigMap the wrapper owns; the server compares the two at boot and on
-- every readiness probe.
--
-- singleton is the primary key and can only be true, so the table holds at
-- most one row and the wrapper's upsert targets it by key.
--
-- A new table: no lock on any existing one. A previous release never reads
-- it. One transaction, so an interruption rolls back and leaves version 49
-- dirty with nothing created: run `migrate force 48` and retry. The
-- statement is idempotent.
BEGIN;
CREATE TABLE IF NOT EXISTS installation (
    singleton   BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    id          TEXT NOT NULL CHECK (id <> ''),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMIT;
