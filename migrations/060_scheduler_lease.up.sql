-- 060_scheduler_lease.up.sql
-- Leader writes fenced by a lease term (ADR 0106, #2329). Every acquisition of
-- scheduler leadership increments scheduler_lease.term. A leader with
-- scheduler.lease_fencing on tags the sessions of its scheduler pool with that
-- term (dexaflow.leader_term), and the statement triggers below refuse a write
-- from a session whose tag is not the current term, so a deposed leader can no
-- longer change runs and task instances once a new leader holds a newer term.
--
-- Inert for an untagged session (ADR 0068, ADR 0090): the API, the agent RPCs,
-- the migration job, the previous release's binary and a server with fencing
-- off never set the tag, and the trigger returns after one current_setting
-- call. The previous binary never increments the term, so its leadership is
-- guarded by the advisory lock alone, as before.
--
-- CREATE TRIGGER takes a SHARE ROW EXCLUSIVE lock on its table, which waits
-- for the writes in flight and holds new writes (not reads) until the file
-- commits; lock_timeout bounds each wait, and the whole file is one
-- transaction. The triggers are new, so there is no DROP TRIGGER first: that
-- would take an ACCESS EXCLUSIVE lock and block the reads of these tables too.
BEGIN;
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS scheduler_lease (
    id          smallint PRIMARY KEY CHECK (id = 1),
    term        bigint NOT NULL DEFAULT 0,
    holder      text NOT NULL DEFAULT '',
    acquired_at timestamptz,
    renewed_at  timestamptz
);
INSERT INTO scheduler_lease (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- Refuses the statement when the session carries a leader term that is not
-- the lease's current one. SQLSTATE DXF01 lets the server count the refusal
-- (stale_leader_term) apart from any other error.
CREATE OR REPLACE FUNCTION dexaflow_fence_leader_term() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path FROM CURRENT
    AS $$
DECLARE
    tag text := current_setting('dexaflow.leader_term', true);
    cur bigint;
BEGIN
    IF tag IS NULL OR tag = '' THEN
        RETURN NULL;
    END IF;
    SELECT term INTO cur FROM scheduler_lease WHERE id = 1;
    IF cur IS DISTINCT FROM tag::bigint THEN
        RAISE EXCEPTION 'stale_leader_term: session term %, lease term %', tag, cur
            USING ERRCODE = 'DXF01';
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER dexaflow_fence_leader_term BEFORE INSERT OR UPDATE OR DELETE ON dag_runs
    FOR EACH STATEMENT EXECUTE FUNCTION dexaflow_fence_leader_term();
CREATE TRIGGER dexaflow_fence_leader_term BEFORE INSERT OR UPDATE OR DELETE ON task_instances
    FOR EACH STATEMENT EXECUTE FUNCTION dexaflow_fence_leader_term();
CREATE TRIGGER dexaflow_fence_leader_term BEFORE INSERT OR UPDATE OR DELETE ON task_instance_history
    FOR EACH STATEMENT EXECUTE FUNCTION dexaflow_fence_leader_term();
CREATE TRIGGER dexaflow_fence_leader_term BEFORE INSERT OR UPDATE OR DELETE ON staging_volumes
    FOR EACH STATEMENT EXECUTE FUNCTION dexaflow_fence_leader_term();

COMMIT;
