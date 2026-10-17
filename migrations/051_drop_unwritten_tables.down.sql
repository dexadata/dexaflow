-- 051_drop_unwritten_tables.down.sql
-- Restore the three tables 051 dropped (#1186) exactly as 003 and 004 created
-- them (no later migration changed them), empty: their columns, defaults,
-- keys, indexes and the trim function. 0.5.5 and later do not need them; the
-- retention janitor of 0.5.4 and earlier deletes from task_state_history, so
-- a rollback to one of those releases needs the table back.
--
-- The foreign key from task_state_history takes a SHARE ROW EXCLUSIVE lock on
-- task_instances, which blocks writes to it (not reads) until the transaction
-- commits; lock_timeout bounds the wait for it. Every statement can be
-- repeated.

BEGIN;

SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS replicas (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname TEXT NOT NULL,
    process_id INT NOT NULL,
    version TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_leader BOOLEAN NOT NULL DEFAULT false,
    role TEXT NOT NULL,
    metadata JSONB DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_replicas_heartbeat ON replicas(last_heartbeat_at DESC);
CREATE INDEX IF NOT EXISTS idx_replicas_leader ON replicas(is_leader) WHERE is_leader = true;

CREATE TABLE IF NOT EXISTS scheduler_loops (
    id BIGSERIAL PRIMARY KEY,
    replica_id UUID NOT NULL REFERENCES replicas(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ NOT NULL,
    duration_ms INT NOT NULL,
    dags_examined INT NOT NULL DEFAULT 0,
    runs_created INT NOT NULL DEFAULT 0,
    tasks_scheduled INT NOT NULL DEFAULT 0,
    errors INT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_scheduler_loops_replica ON scheduler_loops(replica_id, started_at DESC);

CREATE OR REPLACE FUNCTION trim_scheduler_loops() RETURNS void AS $$
BEGIN
    DELETE FROM scheduler_loops WHERE started_at < now() - INTERVAL '24 hours';
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS task_state_history (
    id BIGSERIAL PRIMARY KEY,
    task_instance_id UUID NOT NULL REFERENCES task_instances(id) ON DELETE CASCADE,
    from_state task_state,
    to_state task_state NOT NULL,
    transitioned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason TEXT,
    actor TEXT
);

CREATE INDEX IF NOT EXISTS idx_tsh_ti ON task_state_history(task_instance_id, transitioned_at DESC);

COMMIT;
