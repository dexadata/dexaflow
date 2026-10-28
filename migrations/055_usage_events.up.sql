-- 055_usage_events.up.sql
-- One row per finished task attempt, written by the database in the
-- transaction that finishes it (ADR 0096, #2174). This trigger is the one
-- piece of logic in the schema: every write that moves a task instance into
-- an attempt-ending state goes through it, whichever Go path made the write,
-- so an event exists if and only if that change committed.
--
-- An attempt is (task_instance_id, try_number, attempt_epoch). Its first event
-- has revision 0; a later move of the same attempt into another
-- attempt-ending state (a user's mark over the agent's result) gets the next
-- revision. The row lock the UPDATE already holds on task_instances serializes
-- two writers of one attempt, so revisions do not collide. failed to
-- up_for_retry is not attempt-ending and writes nothing; a reschedule bumps
-- attempt_epoch, so each poke that ends in up_for_reschedule is its own
-- attempt.
--
-- xid is the writing transaction's id. Ids are taken at insert and become
-- visible at commit, so they commit out of order; a reader serves only rows
-- whose xid is below the snapshot's xmin, ordered by (xid, id), and every row
-- written later sorts after them.
--
-- Recording is off until usage_events_switch says enabled. The leader writes
-- that row from usage.events.enabled; the WHEN clause keeps the trigger from
-- being called for any other write, and with the switch off the function
-- returns before reading anything else.
--
-- CREATE TRIGGER takes a SHARE ROW EXCLUSIVE lock on task_instances. The
-- statement is instant, but it waits behind every open transaction on the
-- table and writes queue behind the wait. lock_timeout bounds it: past 5
-- seconds the transaction rolls back, golang-migrate marks version 55 dirty
-- and nothing changed; force 54 and retry when the table is quiet. Every
-- statement can be repeated.
--
-- 0.5.7 and earlier never read the new tables, and with the switch off the
-- trigger writes nothing, so a previous release keeps running against this
-- schema.

BEGIN;

SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS usage_events_switch (
    id         BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO usage_events_switch (id) VALUES (TRUE) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS usage_events (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    xid               XID8 NOT NULL,
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    dag_run_id        UUID NOT NULL,
    task_instance_id  UUID NOT NULL,
    task_id           TEXT NOT NULL,
    map_index         INTEGER NOT NULL,
    try_number        INTEGER NOT NULL,
    attempt_epoch     INTEGER NOT NULL,
    revision          INTEGER NOT NULL,
    state             task_state NOT NULL,
    previous_state    task_state NOT NULL,
    pool_slots        INTEGER NOT NULL,
    started_at        TIMESTAMPTZ,
    ended_at          TIMESTAMPTZ,
    last_failure_kind TEXT,
    recorded_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_events_cursor ON usage_events (xid, id);
CREATE INDEX IF NOT EXISTS idx_usage_events_attempt
    ON usage_events (task_instance_id, try_number, attempt_epoch);
CREATE INDEX IF NOT EXISTS idx_usage_events_recorded ON usage_events (recorded_at);

CREATE OR REPLACE FUNCTION record_usage_event() RETURNS trigger AS $$
DECLARE
    next_revision INTEGER;
BEGIN
    IF NOT COALESCE((SELECT enabled FROM usage_events_switch WHERE id), FALSE) THEN
        RETURN NULL;
    END IF;
    SELECT COALESCE(max(revision) + 1, 0) INTO next_revision
    FROM usage_events
    WHERE task_instance_id = NEW.id
      AND try_number = NEW.try_number
      AND attempt_epoch = NEW.attempt_epoch;
    INSERT INTO usage_events (
        xid, tenant_id, dag_run_id, task_instance_id, task_id, map_index,
        try_number, attempt_epoch, revision, state, previous_state, pool_slots,
        started_at, ended_at, last_failure_kind
    ) VALUES (
        pg_current_xact_id(), NEW.tenant_id, NEW.dag_run_id, NEW.id, NEW.task_id, NEW.map_index,
        NEW.try_number, NEW.attempt_epoch, next_revision, NEW.state, OLD.state, NEW.pool_slots,
        NEW.started_at, NEW.ended_at, NEW.last_failure_kind
    );
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS task_instances_usage_event ON task_instances;
CREATE TRIGGER task_instances_usage_event
    AFTER UPDATE OF state ON task_instances
    FOR EACH ROW
    WHEN (NEW.state IS DISTINCT FROM OLD.state AND NEW.state IN
        ('success', 'failed', 'skipped', 'upstream_failed', 'removed', 'up_for_reschedule'))
    EXECUTE FUNCTION record_usage_event();

COMMIT;
