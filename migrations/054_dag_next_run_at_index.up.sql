-- Index the scheduled DAGs by their next due time (#2175), for the list the
-- scheduler reads on every tick: only the DAGs with no stored time or a time
-- at or before now. The predicate is the list's own filter, so the index
-- holds only DAGs the scheduler can act on, and a paused, inactive or manual
-- DAG costs nothing.
--
-- CONCURRENTLY so the build does not block writes to dags. It cannot run
-- inside a transaction, so this file holds this one statement and no
-- BEGIN/COMMIT, like 050.
--
-- No IF NOT EXISTS, on purpose: if the build is interrupted, Postgres leaves
-- an INVALID index behind and golang-migrate marks version 54 dirty, and a
-- retry must fail loudly instead of keeping the invalid index. Drop it first:
--
--   DROP INDEX CONCURRENTLY IF EXISTS idx_dags_next_run;
--   migrate -path migrations -database "$DATABASE_URL" force 53
--   migrate -path migrations -database "$DATABASE_URL" up
CREATE INDEX CONCURRENTLY idx_dags_next_run ON dags (next_run_at)
    WHERE is_active = true AND is_paused = false AND schedule IS NOT NULL AND current_version_id IS NOT NULL;
