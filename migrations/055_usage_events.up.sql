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
-- revision. The last revision of each attempt is kept in
-- usage_event_revisions, not looked up among the events, so a re-mark after
-- retention deleted the earlier events still gets a new revision; its rows go
-- with their task instance. The row lock the UPDATE already holds on
-- task_instances serializes two writers of one attempt, so revisions do not
-- collide. failed to
-- up_for_retry is not attempt-ending and writes nothing; a reschedule bumps
-- attempt_epoch, so each poke that ends in up_for_reschedule is its own
-- attempt.
--
-- ended_at is the task instance's end time, or, for a task that started but
-- has none (a reschedule poke, a mark over a running task), the time of the
-- change that ended the attempt.
--
-- tenant_id carries no foreign key: a key would lock the tenant row on every
-- event, the same row for every settle of a single-tenant install.
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
-- CREATE TRIGGER, and the foreign key of usage_event_revisions, take a SHARE
-- ROW EXCLUSIVE lock on task_instances. The statements are instant, but it waits behind every open transaction on the
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
    tenant_id         UUID NOT NULL,
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

CREATE TABLE IF NOT EXISTS usage_event_revisions (
    task_instance_id UUID NOT NULL REFERENCES task_instances(id) ON DELETE CASCADE,
    try_number       INTEGER NOT NULL,
    attempt_epoch    INTEGER NOT NULL,
    revision         INTEGER NOT NULL,
    PRIMARY KEY (task_instance_id, try_number, attempt_epoch)
);

CREATE OR REPLACE FUNCTION record_usage_event() RETURNS trigger AS $$
DECLARE
    next_revision INTEGER;
BEGIN
    IF NOT COALESCE((SELECT enabled FROM usage_events_switch WHERE id), FALSE) THEN
        RETURN NULL;
    END IF;
    INSERT INTO usage_event_revisions AS r (task_instance_id, try_number, attempt_epoch, revision)
    VALUES (NEW.id, NEW.try_number, NEW.attempt_epoch, 0)
    ON CONFLICT (task_instance_id, try_number, attempt_epoch)
        DO UPDATE SET revision = r.revision + 1
    RETURNING r.revision INTO next_revision;
    INSERT INTO usage_events (
        xid, tenant_id, dag_run_id, task_instance_id, task_id, map_index,
        try_number, attempt_epoch, revision, state, previous_state, pool_slots,
        started_at, ended_at, last_failure_kind
    ) VALUES (
        pg_current_xact_id(), NEW.tenant_id, NEW.dag_run_id, NEW.id, NEW.task_id, NEW.map_index,
        NEW.try_number, NEW.attempt_epoch, next_revision, NEW.state, OLD.state, NEW.pool_slots,
        NEW.started_at,
        COALESCE(NEW.ended_at, CASE WHEN NEW.started_at IS NOT NULL THEN now() END),
        NEW.last_failure_kind
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
