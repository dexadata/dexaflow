-- name: CreateDagRun :one
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger, note, conf)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetDagRun :one
-- tenant_id is a predicate here, not only a property of the caller's resolved
-- dag id (#508): a dag id of another tenant finds no run.
SELECT * FROM dag_runs WHERE tenant_id = $1 AND dag_id = $2 AND run_id = $3;

-- name: GetDagRunWithVersion :one
-- The run plus the LABEL of the version it is pinned to. The API needs the label
-- to populate bundle_version, which is what makes the UI's clear dialog render
-- its "Run with latest bundle version" control: the SPA shows that control only
-- when the run's bundle_version differs from the DAG's, and hides it when either
-- is null. A null there means the operator has no way to ask for the current
-- version, whatever the API supports.
--
-- LEFT JOIN is defensive, not load-bearing today: dag_runs.dag_version_id is NOT
-- NULL with a plain FK, so the version row cannot disappear under a live run. It
-- is written this way so that a future ON DELETE SET NULL degrades to a null
-- label instead of dropping the run from the UI.
SELECT r.*, v.version AS dag_version_label
FROM dag_runs r
LEFT JOIN dag_versions v ON v.id = r.dag_version_id
WHERE r.tenant_id = $1 AND r.dag_id = $2 AND r.run_id = $3;

-- name: ListDagRunsByDagWithVersion :many
-- See GetDagRunWithVersion. One join rather than a lookup per row.
SELECT r.*, v.version AS dag_version_label
FROM dag_runs r
LEFT JOIN dag_versions v ON v.id = r.dag_version_id
WHERE r.tenant_id = $1 AND r.dag_id = $2
ORDER BY r.logical_date DESC, r.run_id DESC
LIMIT $3 OFFSET $4;

-- name: ListDagRunsByDagAfter :many
-- Keyset form of ListDagRunsByDagWithVersion: the runs strictly before the
-- cursor (logical_date, run_id) in the same order, so a deep page costs the
-- same as the first. run_id is unique per DAG, so it makes the order total. An
-- empty states array keeps every state.
SELECT r.*, v.version AS dag_version_label
FROM dag_runs r
LEFT JOIN dag_versions v ON v.id = r.dag_version_id
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.dag_id = sqlc.arg(dag_id)
  AND (cardinality(sqlc.arg(states)::text[]) = 0 OR r.state::text = ANY(sqlc.arg(states)::text[]))
  AND (r.logical_date, r.run_id) < (sqlc.arg(after_logical_date)::timestamptz, sqlc.arg(after_run_id)::text)
ORDER BY r.logical_date DESC, r.run_id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountDagRunsByDagStates :one
SELECT count(*) FROM dag_runs
WHERE tenant_id = sqlc.arg(tenant_id) AND dag_id = sqlc.arg(dag_id) AND state::text = ANY(sqlc.arg(states)::text[]);

-- name: DeleteDagRun :execrows
-- Removes one run; its task_instances and XCom rows cascade (ON DELETE CASCADE).
DELETE FROM dag_runs WHERE tenant_id = $1 AND dag_id = $2 AND run_id = $3;

-- name: ListDagRunsByDag :many
SELECT * FROM dag_runs
WHERE tenant_id = $1 AND dag_id = $2
ORDER BY logical_date DESC
LIMIT $3 OFFSET $4;

-- name: CountDagRunsByDag :one
-- The tenant is checked on the parent dag, once, rather than on every run row:
-- a per-row tenant_id filter would turn the index-only count over
-- idx_dag_runs_dag_logical into a heap visit per run. A dag_runs row always
-- carries its dag's tenant, so a dag id of another tenant counts zero either
-- way (#508).
SELECT count(*) FROM dag_runs
WHERE dag_runs.dag_id = $1
  AND EXISTS (SELECT 1 FROM dags d WHERE d.id = $1 AND d.tenant_id = $2);

-- name: CountActiveDagRunsByDagID :one
-- Counts queued+running runs for a single DAG; used by the manual-trigger
-- path to enforce max_active_runs (#200) before insert.
SELECT count(*) FROM dag_runs
WHERE tenant_id = $1 AND dag_id = $2 AND state IN ('queued', 'running');

-- name: ListStaleQueuedTaskInstances :many
-- Lists one page of the TIs in `queued` for at least the dispatch-lost
-- threshold, with their queued_at, for the dispatch-lost reaper (#202). The
-- threshold is applied here, before the LIMIT, so attempts inside it never
-- take the slots of those past it; a NULL queued_at is never listed (too
-- poorly observed to reap). The reaper re-checks the threshold in Go.
-- The page is keyset-paged on (queued_at, id) after the reaper's cursor
-- (R-07, #1597): with a fixed "oldest 100", 100 queued attempts whose pods
-- are Pending (a capacity shortage) hid a newer lost dispatch forever. The
-- reaper keeps the cursor across sweeps and passes '-infinity' and the nil
-- uuid to start over. The LIMIT bounds a sweep's reap work.
-- warm_worker_id rides along (ADR 0058 N1d-a2, review finding H3): a queued warm
-- attempt whose serving warm worker is still alive is just slow to transition
-- queued->running, so the dispatch-lost reaper must DEFER it rather than fail it
-- (the double-run bug). It is NULL for a dedicated attempt and for a warm attempt
-- not yet acked, in which case the reaper falls back to its existing pod-liveness
-- gate unchanged.
-- attempt_epoch rides along (ADR 0051 amendment) so the mark and the pod
-- teardown name exactly the attempt that was listed.
-- tenant_id rides along so a start_expired reap can append its marker to the
-- attempt's log (#1126).
SELECT ti.id AS task_instance_id,
       ti.dag_run_id,
       ti.tenant_id,
       d.dag_id AS dag_id_text,
       ti.task_id,
       ti.try_number,
       ti.attempt_epoch,
       ti.queued_at,
       ti.warm_worker_id
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE ti.state = 'queued'
  AND ti.queued_at <= now() - make_interval(secs => sqlc.arg(threshold_seconds)::float8)
  AND (ti.queued_at, ti.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY ti.queued_at, ti.id
LIMIT sqlc.arg(page_size);

-- name: MarkTaskDispatchLost :execrows
-- Fails one queued TI with a dispatch_lost error. The WHERE state='queued'
-- guard makes the operation idempotent: a second call on a TI that has
-- since transitioned (real dispatch landed, or already failed) is a no-op,
-- never overwriting a more meaningful state. It is also pinned to the listed
-- attempt, (try_number, attempt_epoch) (ADR 0051 amendment): a row that was
-- re-placed and re-dispatched between the list and this write is a different
-- attempt, and a mark computed for the old one must not fail it.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    -- Provisional on Kubernetes (the reconciler confirms it), confirmed at
    -- once on Lite (ADR 0052 amendment, part 2).
    infra_confirmed_at = CASE WHEN sqlc.arg(provisional)::bool THEN NULL ELSE now() END,
    error_message = 'dispatch_lost: scheduler crashed before dispatch landed; will be retried by the run reaper'
WHERE id = sqlc.arg(id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state = 'queued';

-- name: MarkTaskStartExpired :execrows
-- Fails one queued TI whose task pod is still Pending (image pull, node
-- provisioning, init containers) when the attempt credential minted at dispatch
-- is about to run out (#1126). The agent has not started, so it could only
-- start with a dead credential: it would be refused at Register, stream no log
-- and fail the attempt against the user's retry budget. Marked infra instead,
-- so the planner re-places it off-budget with a fresh credential. Same guards
-- and provisional rule as MarkTaskDispatchLost: state='queued' (an agent that
-- reported RUNNING wins) and the listed attempt, (try_number, attempt_epoch).
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    infra_confirmed_at = CASE WHEN sqlc.arg(provisional)::bool THEN NULL ELSE now() END,
    error_message = 'start_expired: the task pod did not start before its attempt credential ran out (slow image pull, node provisioning or init containers); will be re-placed with a fresh credential'
WHERE id = sqlc.arg(id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state = 'queued';

-- name: ListActiveDagRuns :many
-- The runs the scheduler reads on a tick, and the warm-pool reconcile reads:
-- every queued and running run, each with dag_paused, the is_paused of its DAG.
-- No run is filtered out. The scheduler leaves a queued run of a paused DAG
-- waiting (not started, nothing materialized or dispatched) until the DAG is
-- unpaused, whoever created it (a manual trigger, a clear), as Airflow 3 does
-- when it moves queued runs to running (#1705); a running run of a paused DAG
-- advances so the tasks it started finish. The waiting run stays in the result
-- because a clear on a running run puts it back in queued while other tasks of
-- it still run, and those tasks must keep counting against max_active_runs,
-- max_active_tasks and the pool caps. dag_max_active_runs is the DAG's
-- max_active_runs: the tick starts a queued run only while fewer than that
-- many runs of the DAG are running, as Airflow does (#1766). Both come from one
-- primary-key probe of dags per active run; a run whose DAG row is gone reads
-- unpaused with 0, which the scheduler takes as unlimited. The probe is a
-- LATERAL subquery with LIMIT 1 on purpose: a plain LEFT JOIN lets the planner
-- hash every row of dags, a cost that grows with the number of DAGs (about 10
-- ms against 2 ms for 500 active runs over 20000 DAGs), while the scan stays
-- on idx_dag_runs_state and the probe keeps the cost proportional to the
-- active runs.
SELECT sqlc.embed(dag_runs),
  COALESCE(d.is_paused, false)::boolean AS dag_paused,
  COALESCE(d.max_active_runs, 0)::integer AS dag_max_active_runs
FROM dag_runs
LEFT JOIN LATERAL (
  SELECT dags.is_paused, dags.max_active_runs FROM dags WHERE dags.id = dag_runs.dag_id LIMIT 1
) d ON true
WHERE dag_runs.state IN ('queued', 'running')
ORDER BY dag_runs.queued_at;

-- name: UpdateDagRunState :one
UPDATE dag_runs
SET state = $2, started_at = $3, ended_at = $4
WHERE id = $1 AND tenant_id = sqlc.arg(tenant_id)
RETURNING *;

-- name: ReopenDagRunKeepingVersion :exec
-- The same re-open as ResetDagRunToVersion, WITHOUT touching dag_version_id: the
-- run stays pinned to the version it was created with, so the re-run executes the
-- image that produced the original attempt.
--
-- Two separate decisions used to be one. Re-opening a run (state, timestamps,
-- alert bookkeeping) is what `reset_dag_runs` means; WHICH version the re-run
-- executes is what Airflow calls `run_on_latest_version`. Folding the second into
-- the first left no way to re-run last week's task as it was last week, and left
-- `reset_dag_runs=false` meaning "do not re-open either", which strands the
-- cleared task instance in a terminal run the scheduler never looks at again.
--
-- Alert bookkeeping is cleared here for the same reason it is there: the clear
-- starts a new failure episode, so a genuine re-failure re-pages, and a spent
-- retry budget is not carried into an episode that has not been attempted.
UPDATE dag_runs
SET state = 'queued', started_at = NULL, ended_at = NULL, alerted_at = NULL,
    alert_attempts = 0, next_alert_attempt_at = NULL
WHERE id = $1 AND tenant_id = $2;

-- name: GetDagRunForUpdate :one
-- A finished run a clear may move to the DAG's current version, read and
-- locked inside the clear's transaction before any of its task instances is
-- written (ADR 0086). The move depends on the run's state, so it is decided
-- on the locked row: a second clear of the same run waits for the first
-- instead of deciding from the same state. Only a finished run is locked:
-- the state predicate is checked again after a wait, so a run another clear
-- re-opened meanwhile is not returned and not held. The orphan reaper
-- share-locks a running run's task instances and then updates the run row,
-- the opposite order. NO KEY UPDATE is enough, since no key changes, and it
-- lets through the key-share lock an XCom insert of a live attempt takes on
-- the run. The retention janitor locks finished runs with SKIP LOCKED and
-- their task instances with NOWAIT, so it skips or gives way and never waits
-- on a clear.
SELECT * FROM dag_runs
 WHERE id = $1 AND tenant_id = $2 AND state IN ('success', 'failed')
   FOR NO KEY UPDATE;

-- name: MarkTaskInstancesRemoved :exec
-- A clear that moves a finished run to a version that no longer declares
-- these tasks marks their task instances removed (ADR 0086), as Airflow's
-- verify_integrity does. Their history, logs and XCom stay. A queued or
-- running task instance is never marked: the clear does not move a run that
-- has one, and the guard keeps a racing attempt's row as it is.
UPDATE task_instances
SET state = 'removed'
WHERE tenant_id = sqlc.arg(tenant_id) AND dag_run_id = sqlc.arg(dag_run_id)
  AND task_id = ANY(sqlc.arg(task_ids)::text[])
  AND state NOT IN ('queued', 'running', 'removed');

-- name: ResetDagRunToVersion :exec
-- Clear with run_on_latest_version re-binds the run to the DAG's current
-- registered version (ADR 0020; opt-in since the 2026-09-15 amendment; only a
-- finished run with no queued or running task instance, ADR 0086): a
-- re-run after a code/yaml fix picks up the newest image and config — in dev that
-- is the last hot-reload, in prod the last deploy — while everything within a
-- version stays reproducible. Clearing the alert bookkeeping (#431) makes the clear
-- a new failure episode, so a genuine re-failure re-pages while a re-tick of the
-- same failed state does not. All three columns reset together: leaving
-- alert_attempts behind would carry a spent retry budget into an episode that has
-- not been attempted, so a cleared run could exhaust its attempts without ever
-- having tried.
UPDATE dag_runs
SET state = 'queued', started_at = NULL, ended_at = NULL, alerted_at = NULL,
    alert_attempts = 0, next_alert_attempt_at = NULL,
    dag_version_id = $2
WHERE id = $1 AND tenant_id = sqlc.arg(tenant_id);

-- name: ClaimAlertAttempt :one
-- Atomically claim one on-failure ATTEMPT for a run, returning the row iff this
-- call won it. Replaces the old claim-then-send (#431), which set alerted_at
-- before the send and so lost the page whenever the send failed.
--
-- Three predicates, one per way an attempt should be refused:
--   * alerted_at IS NULL  — already delivered; never page twice for one episode.
--   * alert_attempts < $2 — the budget is spent; a dead endpoint stops being
--     retried instead of being hit once per tick for the life of the run.
--   * next_alert_attempt_at — backoff has not elapsed yet.
--
-- The attempt is consumed up front, before the send, so a crash mid-send costs one
-- attempt rather than looping. A clear resets all three (ResetDagRunToVersion),
-- making the next genuine failure a fresh episode with a fresh budget.
UPDATE dag_runs
SET alert_attempts = alert_attempts + 1,
    next_alert_attempt_at = now() + sqlc.arg(backoff)::interval
WHERE id = $1
  AND alerted_at IS NULL
  AND alert_attempts < sqlc.arg(max_attempts)
  AND (next_alert_attempt_at IS NULL OR next_alert_attempt_at <= now())
RETURNING id, alert_attempts;

-- name: MarkRunAlertDelivered :exec
-- Stamp a run's on-failure alert as DELIVERED. Called only after a successful
-- send, which is the whole point of the split: alerted_at now answers "did the
-- page get through", not "did we try".
--
-- Guarded on the attempt it is reporting for. The send runs in a goroutine
-- detached from the tick, so an operator clear can land between the claim and the
-- stamp: the clear resets alert_attempts to 0 and starts a NEW failure episode,
-- and an unguarded stamp from the old in-flight send would mark that new episode
-- delivered without ever paging it. Requiring the attempt to still match means a
-- stamp from a superseded episode simply matches no row. Same shape as the guard
-- on ReportTaskResult: a late writer must never clobber newer state.
UPDATE dag_runs
SET alerted_at = now(), next_alert_attempt_at = NULL
WHERE id = $1 AND alert_attempts = sqlc.arg(attempt);

-- name: StampDagRunState :execrows
-- Transitions a run's state and stamps the run's own timestamps so the UI can
-- show its duration: started_at on first entry into 'running', ended_at on a
-- terminal state. Other timestamps are preserved (the scheduler may re-run).
-- Guarded on the state the caller read (from_state), as the tick's task-level
-- writes are (#1605): a run a clear re-opened or a user marked after the tick's
-- snapshot is left alone, and no row is affected (#2047).
UPDATE dag_runs
SET state = sqlc.arg(state)::dag_run_state,
    started_at = CASE WHEN sqlc.arg(state)::dag_run_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    ended_at = CASE WHEN sqlc.arg(state)::dag_run_state IN ('success', 'failed') THEN now() ELSE ended_at END
WHERE id = sqlc.arg(id) AND state = sqlc.arg(from_state)::dag_run_state;

-- name: StartQueuedRun :one
-- Moves a queued run to running when the tick admits it (#1766), guarded on
-- the run still being queued: a run a user marked success or failed, or
-- deleted, after the tick read it is left alone, and no row comes back.
-- started_at is stamped on the first start only, as StampDagRunState does; a
-- clear resets it, so a re-queued run gets a new one.
UPDATE dag_runs
SET state = 'running', started_at = COALESCE(started_at, now())
WHERE id = sqlc.arg(id) AND tenant_id = sqlc.arg(tenant_id) AND state = 'queued'
RETURNING id;

-- name: CreateTaskInstance :one
-- try_number starts at 1 to match Airflow (1-based attempts): the first run's
-- logs live at .../1.log, which is where the UI's log view looks. Retries bump
-- it via ResetForRetry. pool_slots is the task's EffectivePoolSlots (#1499).
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, operator, max_tries, state, pool, try_number, pool_slots)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8)
RETURNING *;

-- name: CreateTaskInstances :copyfrom
-- Batched form of CreateTaskInstance: materializes every task of one run in a
-- single COPY instead of T INSERT round-trips. The caller supplies one param row
-- per task with try_number pinned to 1 (matching CreateTaskInstance's literal)
-- and pool carried through so cross-DAG pool occupancy is attributed correctly;
-- pool_slots carries the task's EffectivePoolSlots so that occupancy is
-- weighted like the admission gate's (#1499). Columns omitted from the list
-- take their table defaults, so the rows are byte-identical to the loop: only
-- the statement count changes (T INSERTs → 1 COPY).
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, operator, max_tries, state, pool, try_number, pool_slots)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListTaskInstancesByRun :many
SELECT * FROM task_instances
WHERE tenant_id = $1 AND dag_run_id = $2
ORDER BY task_id;

-- name: ListTaskInstancesByRuns :many
-- The batched form of ListTaskInstancesByRun for the scheduler tick: every
-- active run's task instances in one round trip instead of one per run. Rows
-- come grouped by run and, within a run, in the same task_id order the per-run
-- query returns, so the caller can split them without re-sorting.
SELECT * FROM task_instances
WHERE dag_run_id = ANY(sqlc.arg(dag_run_ids)::uuid[])
ORDER BY dag_run_id, task_id;

-- name: ListTaskInstanceAttempts :many
-- Returns every attempt for (run, task), oldest first. UNIONs the current
-- task_instances row with all archived task_instance_history rows so the UI's
-- /tries endpoint can render one navigable tab per attempt (Lima bug #241).
-- One entry per try (ADR 0051 amendment, #863): an infra re-place archives the
-- try and keeps it on the current row, so a history row for the current try is
-- left out and the current row, the try's latest execution, stands for it.
-- Each row carries the per-attempt fields PLUS the run-constant fields
-- (operator, max_tries, map_index) copied from the current TI; archived rows
-- get them via the JOIN. pool and pool_slots are the current TI's too: history
-- does not record them, so every attempt reports the pool the task is charged
-- to now (#1589).
SELECT
    ti.id AS task_instance_id,
    ti.map_index,
    ti.operator,
    ti.max_tries,
    h.try_number,
    h.state,
    h.queued_at,
    h.scheduled_at,
    h.started_at,
    h.ended_at,
    h.duration_seconds,
    h.exit_code,
    h.error_message,
    h.hostname,
    h.pod_name,
    h.node_name,
    h.note,
    ti.pool,
    ti.pool_slots
FROM task_instance_history h
JOIN task_instances ti ON ti.id = h.task_instance_id
WHERE ti.tenant_id = $1 AND ti.dag_run_id = $2 AND ti.task_id = $3
  AND h.try_number <> ti.try_number
UNION ALL
SELECT
    ti.id AS task_instance_id,
    ti.map_index,
    ti.operator,
    ti.max_tries,
    ti.try_number,
    ti.state,
    ti.queued_at,
    ti.scheduled_at,
    ti.started_at,
    ti.ended_at,
    ti.duration_seconds,
    ti.exit_code,
    ti.error_message,
    ti.hostname,
    ti.pod_name,
    ti.node_name,
    ti.note,
    ti.pool,
    ti.pool_slots
FROM task_instances ti
WHERE ti.tenant_id = $1 AND ti.dag_run_id = $2 AND ti.task_id = $3
ORDER BY try_number;

-- name: TryAttemptEpochBounds :one
-- The range of attempt epochs one try's executions can have, for the log
-- reader (ADR 0051 amendment, #863). Epochs grow monotonically per row and the
-- try only grows, so every execution of try N has an epoch above the latest
-- epoch archived for an earlier try (low, exclusive) and at most the try's own
-- latest epoch (high, inclusive): the current row's when it is still on try N,
-- else the try's history row, which holds its latest archived execution. A row
-- the previous release archived carries epoch 0, so its try reads as epoch 0
-- alone. Epoch 0 is always read as well, since every pre-upgrade log has it.
SELECT
    COALESCE((
        SELECT max(h.attempt_epoch)
        FROM task_instance_history h
        JOIN task_instances lt ON lt.id = h.task_instance_id
        WHERE lt.tenant_id = sqlc.arg(tenant_id) AND lt.dag_run_id = sqlc.arg(dag_run_id) AND lt.task_id = sqlc.arg(task_id)
          AND h.try_number < sqlc.arg(try_number)
    ), 0)::int AS low,
    COALESCE((
        SELECT cur.attempt_epoch FROM task_instances cur
        WHERE cur.tenant_id = sqlc.arg(tenant_id) AND cur.dag_run_id = sqlc.arg(dag_run_id) AND cur.task_id = sqlc.arg(task_id)
          AND cur.try_number = sqlc.arg(try_number)
    ), (
        SELECT h.attempt_epoch
        FROM task_instance_history h
        JOIN task_instances ht ON ht.id = h.task_instance_id
        WHERE ht.tenant_id = sqlc.arg(tenant_id) AND ht.dag_run_id = sqlc.arg(dag_run_id) AND ht.task_id = sqlc.arg(task_id)
          AND h.try_number = sqlc.arg(try_number)
    ), 0)::int AS high;

-- name: UpdateTaskInstanceState :one
UPDATE task_instances
SET state = $2, started_at = $3, ended_at = $4
WHERE id = $1
RETURNING *;

-- name: ListScheduledDags :many
-- Returns each cron-scheduled DAG with the bits the scheduler needs to decide
-- both "is there a slot due?" (schedule + last_logical), "how many slots
-- should I backfill on this tick?" (catchup + start_date, see #129), and
-- "may this DAG take another active run?" (max_active_runs, see #200). The
-- owning tenant is returned because a dag_id is unique only within its tenant
-- (#209).
SELECT d.tenant_id, d.dag_id, d.schedule, d.catchup, d.start_date, d.max_active_runs,
  (SELECT max(dr.logical_date) FROM dag_runs dr WHERE dr.dag_id = d.id) AS last_logical
FROM dags d
WHERE d.is_active = true AND d.is_paused = false
  AND d.schedule IS NOT NULL AND d.current_version_id IS NOT NULL;

-- name: CreateScheduledRunByDagID :execrows
-- Zero rows means the slot's run already exists (or the DAG has no current
-- version): the caller then rolls back the charge it took from the tenant's
-- daily run cap.
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger)
SELECT d.tenant_id, d.id, d.current_version_id, sqlc.arg(run_id), sqlc.arg(logical_date), 'queued', 'scheduled'
FROM dags d
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.dag_id = sqlc.arg(dag_id) AND d.current_version_id IS NOT NULL
ON CONFLICT (dag_id, run_id) DO NOTHING;

-- name: DagRunExistsByDagID :one
-- Whether run_id of the DAG dag_id in tenant tenant_id exists: the read behind
-- a refused daily run cap charge, so a run that already exists keeps its usual
-- answer (a no-op for a scheduled slot, a conflict for a manual run id) instead
-- of a refusal, and nothing is written (createRunWithinDailyLimit).
SELECT EXISTS (
    SELECT 1 FROM dag_runs r JOIN dags d ON d.id = r.dag_id
    WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.dag_id = sqlc.arg(dag_id) AND r.run_id = sqlc.arg(run_id)
)::bool AS run_exists;

-- name: GetDagVersionByID :one
-- Unscoped: for the spec cache the scheduler and the dispatcher share, which
-- hold version ids from their own runs. A request path reads a version with
-- GetDagVersionInTenant (#508).
SELECT * FROM dag_versions WHERE id = $1;

-- name: GetDagVersionsByIDs :many
-- Unscoped, like GetDagVersionByID: the spec cache's batch fill for a warm
-- pool reconcile tick, which reads the versions of its own targets (#1707).
SELECT * FROM dag_versions WHERE id = ANY(@ids::uuid[]);

-- name: GetDagVersionInTenant :one
-- A version by id, only when its DAG belongs to the given tenant (#508).
-- dag_versions has no tenant_id of its own; the tenant is its DAG's.
SELECT * FROM dag_versions
WHERE dag_versions.id = $1
  AND EXISTS (SELECT 1 FROM dags d WHERE d.id = dag_versions.dag_id AND d.tenant_id = $2);

-- name: GetDagRunByID :one
-- Unscoped: for the scheduler and the dispatcher, which hold run ids from
-- their own listings. A run id that arrives in an agent token is read with
-- GetDagRunInTenant instead (#508).
SELECT * FROM dag_runs WHERE id = $1;

-- name: GetDagRunInTenant :one
-- A run by id, only inside the given tenant: the agent RPC reads the run its
-- token names with the token's tenant, so a token of one tenant never loads
-- another tenant's run (#508).
SELECT * FROM dag_runs WHERE tenant_id = $1 AND id = $2;

-- name: UpdateTaskInstanceStateByRunTask :exec
-- Stamps the per-state entry timestamps the UI shows (scheduled_when /
-- queued_when / start_date). Each is set on first entry only ("IS NULL"), so a
-- re-emitted transition does not move the recorded time. $3 is cast to
-- task_state (see ReportTaskResult for why the cast is required).
UPDATE task_instances
SET state = sqlc.arg(state)::task_state,
    scheduled_at = CASE WHEN sqlc.arg(state)::task_state = 'scheduled' AND scheduled_at IS NULL THEN now() ELSE scheduled_at END,
    queued_at = CASE WHEN sqlc.arg(state)::task_state = 'queued' AND queued_at IS NULL THEN now() ELSE queued_at END,
    started_at = CASE WHEN sqlc.arg(state)::task_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END
WHERE dag_run_id = sqlc.arg(dag_run_id) AND task_id = sqlc.arg(task_id);

-- name: SetTaskInstanceStateByUser :exec
-- The mark-state endpoint's write (mark success / mark failed). Same stamping
-- as UpdateTaskInstanceStateByRunTask, and a user's state is a verdict, not a
-- guess: it clears last_failure_kind and confirms the row, so a reaped task a
-- user marks failed is neither re-placed nor overridden by a late SUCCESS
-- record (ADR 0052 amendment).
UPDATE task_instances
SET state = sqlc.arg(state)::task_state,
    scheduled_at = CASE WHEN sqlc.arg(state)::task_state = 'scheduled' AND scheduled_at IS NULL THEN now() ELSE scheduled_at END,
    queued_at = CASE WHEN sqlc.arg(state)::task_state = 'queued' AND queued_at IS NULL THEN now() ELSE queued_at END,
    started_at = CASE WHEN sqlc.arg(state)::task_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    last_failure_kind = NULL,
    infra_confirmed_at = now()
WHERE tenant_id = sqlc.arg(tenant_id) AND dag_run_id = sqlc.arg(dag_run_id) AND task_id = sqlc.arg(task_id);

-- name: UpdateTaskInstanceStatesByRunTasks :many
-- Batched form of UpdateTaskInstanceStateByRunTask: applies ONE target state to
-- every listed task of a run in a single statement. The per-row stamping is
-- identical (scheduled_at/queued_at/started_at set on first entry only), as is
-- the task_state cast. The scheduler groups a tick's plain state-set transitions
-- (scheduled/skipped/upstream_failed from none, up_for_retry from failed) by
-- source and target state and flushes each group through this query.
-- The write is guarded on the source state the tick's snapshot read
-- (from_state), so a row a concurrent writer moved after the snapshot (a user's
-- mark or clear, an agent's RUNNING report under an overlapping leader) is left
-- alone instead of being overwritten (#1605, ADR 0031 leader-overlap
-- correctness). The ids it returns are the rows that actually moved, so the
-- tick records only those.
UPDATE task_instances
SET state = sqlc.arg(state)::task_state,
    scheduled_at = CASE WHEN sqlc.arg(state)::task_state = 'scheduled' AND scheduled_at IS NULL THEN now() ELSE scheduled_at END,
    queued_at = CASE WHEN sqlc.arg(state)::task_state = 'queued' AND queued_at IS NULL THEN now() ELSE queued_at END,
    started_at = CASE WHEN sqlc.arg(state)::task_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END
WHERE dag_run_id = sqlc.arg(dag_run_id) AND task_id = ANY(sqlc.arg(task_ids)::text[])
  AND state = sqlc.arg(from_state)::task_state
RETURNING task_id;

-- name: ResetTaskInstanceToNone :exec
-- Resets a TI for retry: snapshot the current per-attempt state into
-- task_instance_history (so the UI's /tries endpoint can render one tab per
-- attempt, Lima bug #241), then state back to `none`, all per-attempt
-- timestamps cleared (including queued_at), and try_number bumped. queued_at
-- MUST be NULLed so the next TransitionTaskState(queued) stamps a fresh now()
-- — without that, the dispatch-lost reaper sees the stale pre-clear timestamp
-- and re-marks the TI dispatch_lost on every tick (Lima Bug 1).
-- This is the UNCONDITIONAL clear primitive: the admin "clear task" action
-- (ClearTaskInstances, onlyFailed=false) resets a TI to none from ANY state, so
-- it carries no source-state guard. The scheduler's retry rail uses the guarded
-- ResetTaskInstanceForRetry instead — do not add a guard here or clear-task
-- silently no-ops on non-up_for_retry tasks.
-- last_heartbeat_at is cleared on this and every other rail that starts a new
-- execution of the row (ADR 0051 amendment, A0): the next attempt reports
-- RUNNING one heartbeat interval before its first beat, and an inherited value
-- from the previous attempt would make the agent-lost reaper fail it in that
-- window.
-- The same rails bump attempt_epoch (ADR 0051 amendment, A1), so a reset alone
-- already fences the attempt it superseded before the next dispatch claims its
-- own epoch (ClaimAttemptEpoch). The archive row records the superseded
-- attempt's epoch. The archive key stays (task_instance_id, try_number) for
-- compatibility with the previous release until PR A6 widens it, so one try
-- keeps one history row: a later execution of the try (higher epoch) replaces
-- the row an earlier one archived, and the row always holds the try's latest
-- execution, which is what the tries endpoint shows (ADR 0051 amendment, A5).
-- An older binary archives with DO NOTHING against the same key, which still
-- plans and runs. Each execution's log is kept apart by its epoch-keyed object.
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note, attempt_epoch
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note, src.attempt_epoch
    FROM task_instances src
    WHERE src.tenant_id = sqlc.arg(tenant_id) AND src.dag_run_id = sqlc.arg(dag_run_id) AND src.task_id = sqlc.arg(task_id)
    ON CONFLICT (task_instance_id, try_number) DO UPDATE
    SET state = EXCLUDED.state,
        queued_at = EXCLUDED.queued_at,
        scheduled_at = EXCLUDED.scheduled_at,
        started_at = EXCLUDED.started_at,
        ended_at = EXCLUDED.ended_at,
        duration_seconds = EXCLUDED.duration_seconds,
        exit_code = EXCLUDED.exit_code,
        error_message = EXCLUDED.error_message,
        hostname = EXCLUDED.hostname,
        pod_name = EXCLUDED.pod_name,
        node_name = EXCLUDED.node_name,
        note = EXCLUDED.note,
        attempt_epoch = EXCLUDED.attempt_epoch
    WHERE task_instance_history.attempt_epoch < EXCLUDED.attempt_epoch
    RETURNING task_instance_id
)
UPDATE task_instances ti
SET state = 'none',
    released_at = now(),
    started_at = NULL,
    ended_at = NULL,
    queued_at = NULL,
    scheduled_at = NULL,
    dispatch_attempts = 0,
    next_dispatch_at = NULL,
    dispatch_unavailable_since = NULL,
    reschedule_at = NULL,
    first_reschedule_at = NULL,
    last_failure_kind = NULL,
    infra_confirmed_at = NULL,
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = ti.attempt_epoch + 1,
    infra_attempts = 0,
    -- Restore the retry budget from the task (#1131), as Airflow's
    -- clear_task_instances does with max_tries = try_number + task.retries. Here
    -- max_tries counts retries + 1 and try_number is bumped by this statement, so
    -- the same rule is (new try_number) + retries. A task the executing version no
    -- longer declares gets no retries, but never a budget below the attempt
    -- being made.
    -- spec_task_ids and spec_retries are parallel arrays; a task absent from
    -- spec_task_ids indexes to NULL and falls through to the GREATEST.
    max_tries = COALESCE(
        ti.try_number + 1 + (sqlc.arg(spec_retries)::int[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)],
        GREATEST(ti.max_tries, ti.try_number + 1)),
    -- The size the admission gate charges the re-run is the executing version's
    -- (#1499, ADR 0066), so PoolSlotUsage sums the same weight; spec_pool_slots
    -- is parallel to spec_task_ids, and a task the version no longer declares
    -- keeps the size it had.
    pool_slots = COALESCE(
        (sqlc.arg(spec_pool_slots)::int[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)],
        ti.pool_slots),
    -- The pool is re-bound the same way (#1589), so a task the version moved to
    -- another pool is charged there and not to the pool it left. spec_pools is
    -- parallel to spec_task_ids with '' for the implicit default_pool, stored as
    -- NULL like materialization does; a task the version no longer declares
    -- keeps the pool it had.
    pool = CASE
        WHEN array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id) IS NULL THEN ti.pool
        ELSE NULLIF((sqlc.arg(spec_pools)::text[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)], '')
    END,
    try_number = ti.try_number + 1
WHERE ti.tenant_id = sqlc.arg(tenant_id) AND ti.dag_run_id = sqlc.arg(dag_run_id) AND ti.task_id = sqlc.arg(task_id);

-- name: ResetTaskInstanceForRetry :execrows
-- The scheduler retry rail's reset: identical to ResetTaskInstanceToNone but
-- GUARDED to state='up_for_retry' on both the history snapshot and the update.
-- The planner emits up_for_retry → none, but by apply time the TI may have been
-- re-dispatched (a stale/concurrent decision); without this guard the reset
-- would yank a now-running TI back to none and bump try_number, orphaning its
-- live pod. Mirrors the source-state guard on RedispatchRescheduledTaskInstance.
-- The admin clear-task path deliberately uses the unguarded primitive above.
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note, attempt_epoch
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note, src.attempt_epoch
    FROM task_instances src
    WHERE src.dag_run_id = $1 AND src.task_id = $2 AND src.state = 'up_for_retry'
    ON CONFLICT (task_instance_id, try_number) DO UPDATE
    SET state = EXCLUDED.state,
        queued_at = EXCLUDED.queued_at,
        scheduled_at = EXCLUDED.scheduled_at,
        started_at = EXCLUDED.started_at,
        ended_at = EXCLUDED.ended_at,
        duration_seconds = EXCLUDED.duration_seconds,
        exit_code = EXCLUDED.exit_code,
        error_message = EXCLUDED.error_message,
        hostname = EXCLUDED.hostname,
        pod_name = EXCLUDED.pod_name,
        node_name = EXCLUDED.node_name,
        note = EXCLUDED.note,
        attempt_epoch = EXCLUDED.attempt_epoch
    WHERE task_instance_history.attempt_epoch < EXCLUDED.attempt_epoch
    RETURNING task_instance_id
)
UPDATE task_instances ti
SET state = 'none',
    released_at = now(),
    started_at = NULL,
    ended_at = NULL,
    queued_at = NULL,
    scheduled_at = NULL,
    dispatch_attempts = 0,
    next_dispatch_at = NULL,
    dispatch_unavailable_since = NULL,
    reschedule_at = NULL,
    first_reschedule_at = NULL,
    last_failure_kind = NULL,
    infra_confirmed_at = NULL,
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = ti.attempt_epoch + 1,
    try_number = ti.try_number + 1
WHERE ti.dag_run_id = $1 AND ti.task_id = $2 AND ti.state = 'up_for_retry';

-- name: ResetTaskInstanceInfraReplace :execrows
-- The scheduler infra-fault rail's reset (ADR 0051 Phase 1): re-place a task that
-- a reaper failed as infra (agent/pod/dispatch lost, last_failure_kind='infra')
-- back to 'none' WITHOUT consuming the retry budget — try_number is left
-- untouched and infra_attempts is bumped instead, mirroring how dispatch_attempts
-- counts synchronous-dispatch faults. GUARDED to state='failed' AND
-- last_failure_kind='infra' on both the history snapshot and the update: by apply
-- time a late terminal report may have moved the TI, and only a genuine infra
-- failure may re-place off-budget (an app failure at state='failed' must fall to
-- the normal retry rail). last_failure_kind is cleared so the next attempt's
-- outcome is classified fresh.
-- last_heartbeat_at is cleared and attempt_epoch bumped for the reasons given on
-- ResetTaskInstanceToNone.
-- A provisional mark (infra_confirmed_at NULL, ADR 0052 amendment) is not
-- re-placed until the reconciler confirms it or InfraConfirmMaxWait has passed
-- since ended_at (the liveness valve), so a durable SUCCESS record can still
-- settle the attempt it guessed lost. Both halves carry the guard, so a refused
-- re-place archives nothing.
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note, attempt_epoch
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note, src.attempt_epoch
    FROM task_instances src
    WHERE src.dag_run_id = sqlc.arg(dag_run_id) AND src.task_id = sqlc.arg(task_id)
      AND src.state = 'failed' AND src.last_failure_kind = 'infra'
      AND (src.infra_confirmed_at IS NOT NULL OR src.ended_at IS NULL
           OR src.ended_at <= now() - make_interval(secs => sqlc.arg(confirm_max_wait_seconds)::float8))
    ON CONFLICT (task_instance_id, try_number) DO UPDATE
    SET state = EXCLUDED.state,
        queued_at = EXCLUDED.queued_at,
        scheduled_at = EXCLUDED.scheduled_at,
        started_at = EXCLUDED.started_at,
        ended_at = EXCLUDED.ended_at,
        duration_seconds = EXCLUDED.duration_seconds,
        exit_code = EXCLUDED.exit_code,
        error_message = EXCLUDED.error_message,
        hostname = EXCLUDED.hostname,
        pod_name = EXCLUDED.pod_name,
        node_name = EXCLUDED.node_name,
        note = EXCLUDED.note,
        attempt_epoch = EXCLUDED.attempt_epoch
    WHERE task_instance_history.attempt_epoch < EXCLUDED.attempt_epoch
    RETURNING task_instance_id
)
UPDATE task_instances ti
SET state = 'none',
    released_at = now(),
    started_at = NULL,
    ended_at = NULL,
    queued_at = NULL,
    scheduled_at = NULL,
    dispatch_attempts = 0,
    next_dispatch_at = NULL,
    dispatch_unavailable_since = NULL,
    reschedule_at = NULL,
    first_reschedule_at = NULL,
    last_failure_kind = NULL,
    infra_confirmed_at = NULL,
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = ti.attempt_epoch + 1,
    infra_attempts = ti.infra_attempts + 1
WHERE ti.dag_run_id = sqlc.arg(dag_run_id) AND ti.task_id = sqlc.arg(task_id)
  AND ti.state = 'failed' AND ti.last_failure_kind = 'infra'
  AND (ti.infra_confirmed_at IS NOT NULL OR ti.ended_at IS NULL
       OR ti.ended_at <= now() - make_interval(secs => sqlc.arg(confirm_max_wait_seconds)::float8));

-- name: ConfirmInfraFailure :execrows
-- The reconciler confirms a provisional infra mark (ADR 0052 amendment, part
-- 2): the attempt's pods show no SUCCESS record and no task container still
-- running, so the guess stands and the planner may re-place. Guarded on the
-- exact attempt and the provisional mark, so a confirmation computed for a
-- superseded attempt, or a second one, is a no-op.
UPDATE task_instances
SET infra_confirmed_at = now()
WHERE id = sqlc.arg(id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state = 'failed' AND last_failure_kind = 'infra'
  AND infra_confirmed_at IS NULL;

-- name: ListProvisionalInfraFailures :many
-- Provisional infra marks for the reconciler's confirmation pass (ADR 0052
-- amendment, part 2), oldest first. The LIMIT bounds one sweep's work; the
-- rest are picked up next sweep. Only queued or running runs: the planner reads
-- no other run, and the valve is shorter than the orphan threshold, so a mark
-- of a finished run gates nothing. The join keeps this per-sweep query on
-- idx_dag_runs_state and idx_ti_run instead of a scan of every failed task
-- instance, and keeps old unconfirmed marks (a rollback window, marks written
-- by a previous release) from filling the LIMIT ahead of live ones.
SELECT ti.id AS task_instance_id,
       ti.dag_run_id,
       ti.task_id,
       ti.try_number,
       ti.attempt_epoch
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id AND dr.state IN ('queued', 'running')
WHERE ti.state = 'failed'
  AND ti.last_failure_kind = 'infra'
  AND ti.infra_confirmed_at IS NULL
ORDER BY ti.ended_at NULLS FIRST
LIMIT 100;

-- name: RedispatchRescheduledTaskInstance :exec
-- Re-dispatch a task parked in up_for_reschedule once its reschedule_at has passed:
-- back to 'none' with the per-attempt timestamps cleared (so the next dispatch
-- stamps fresh — queued_at MUST be NULL or the dispatch-lost reaper re-flags it)
-- and reschedule_at cleared. Unlike ResetTaskInstanceToNone (retry), try_number is
-- PRESERVED and no task_instance_history row is archived: reschedule is not a retry,
-- it consumes no attempt (#380). Guarded to the parked state so it is idempotent.
-- last_heartbeat_at is cleared and attempt_epoch bumped for the reasons given on
-- ResetTaskInstanceToNone.
UPDATE task_instances
SET state = 'none',
    dispatch_unavailable_since = NULL,
    released_at = now(),
    started_at = NULL,
    ended_at = NULL,
    queued_at = NULL,
    scheduled_at = NULL,
    reschedule_at = NULL,
    last_failure_kind = NULL,
    infra_confirmed_at = NULL,
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = attempt_epoch + 1
WHERE dag_run_id = $1 AND task_id = $2 AND state = 'up_for_reschedule';

-- name: TaskInstanceAttemptFields :one
-- The per-attempt fields the agent spec carries from the task instance row.
-- first_reschedule_at is the time a reschedule-mode sensor first entered
-- reschedule (NULL until it does), delivered to each re-dispatched pod so
-- get_first_reschedule_date returns the real value and the sensor honors its
-- cumulative timeout across pokes (#380). max_tries is the attempt budget the
-- scheduler enforces, which a clear moves past the spec's retries + 1 (#1131),
-- so the runtime's on_failure_callback gate must read it from here (#424).
SELECT first_reschedule_at, max_tries FROM task_instances
WHERE tenant_id = $1 AND dag_run_id = $2 AND task_id = $3;

-- name: FailTaskInstanceIfActive :exec
-- Settle a task instance failed from the pod reconciler, guarded by id,
-- try_number and attempt_epoch (ADR 0052, ADR 0051 amendment): try_number bumps
-- IN PLACE on retry (same row id), and an infra re-place or reschedule keeps the
-- try but bumps the epoch, so a stale reconciler acting on a previous attempt's
-- lingering pod must not match the new attempt and clobber it. The pod's epoch
-- comes from its leoflow.io/attempt-epoch label, absent meaning 0. The
-- active-state guard prevents clobbering a terminal row.
UPDATE task_instances
SET state = 'failed', ended_at = now(), error_message = sqlc.arg(error_message)
WHERE id = sqlc.arg(id) AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state IN ('scheduled', 'queued', 'running');

-- name: FailTaskInstanceInfraIfActive :exec
-- Settle a task instance failed for an infrastructure reason from the pod
-- reconciler (ADR 0051, #1595): the cluster stopped the pod (an eviction, a
-- graceful node shutdown, a preemption), so the attempt goes to the infra
-- re-place rail and keeps the task's own retries. Confirmed at once: the
-- reconciler read the terminal pod and found no SUCCESS record, which is what
-- the confirmation pass would check (ADR 0052 amendment, part 2). Guarded like
-- FailTaskInstanceIfActive on id, try_number, attempt_epoch and the active
-- states.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    infra_confirmed_at = now(),
    error_message = sqlc.arg(error_message)
WHERE id = sqlc.arg(id) AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state IN ('scheduled', 'queued', 'running');

-- name: SucceedTaskInstanceIfActive :execrows
-- Settle a task instance succeeded from its durable outcome record (ADR 0052),
-- recovering a success whose report was lost. Guarded by id, try_number and
-- attempt_epoch so a stale reconciler never marks a LIVE retry or re-place
-- succeeded (#1130), which would fire downstream
-- tasks on incomplete work, strictly worse than the bug being fixed. The
-- active-state guard prevents clobbering a terminal row. It reports the rows
-- it changed, so the reconciler can tell a settle that found nothing active
-- (a reaper may have marked the attempt meanwhile) and try
-- SucceedTaskInstanceOverInfraMark (ADR 0052 amendment).
UPDATE task_instances
SET state = 'success', ended_at = now(), error_message = NULL
WHERE id = sqlc.arg(id) AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state IN ('scheduled', 'queued', 'running');

-- name: SucceedTaskInstanceOverInfraMark :one
-- A durable SUCCESS overrides a reaper's infra guess (ADR 0052 amendment,
-- part 1). Admits only a provisional infra mark (agent_lost, pod_lost,
-- dispatch_lost) of exactly the attempt the pod's labels name, while the
-- planner still treats it as active: not yet confirmed by the reconciler and
-- inside the confirmation valve. A run that already finalized is never
-- touched. An application failure, a user's verdict (SetTaskInstanceStateByUser
-- clears the infra kind) and every other state are left alone. Returns the
-- overridden mark (the reaper's reason prefix) and the attempt's log location;
-- no row means nothing was overridden.
UPDATE task_instances ti
SET state = 'success', ended_at = now(), error_message = NULL,
    exit_code = 0, last_failure_kind = NULL
FROM dag_runs dr, dags d,
     (SELECT p.id, p.error_message FROM task_instances p WHERE p.id = sqlc.arg(id)) prev
WHERE ti.id = sqlc.arg(id) AND prev.id = ti.id
  AND ti.try_number = sqlc.arg(try_number)
  AND ti.attempt_epoch = sqlc.arg(attempt_epoch)
  AND ti.state = 'failed' AND ti.last_failure_kind = 'infra'
  AND ti.infra_confirmed_at IS NULL
  AND ti.ended_at > now() - make_interval(secs => sqlc.arg(confirm_max_wait_seconds)::float8)
  AND dr.id = ti.dag_run_id AND dr.state = 'running'
  AND d.id = dr.dag_id
RETURNING split_part(COALESCE(prev.error_message, ''), ':', 1)::text AS mark,
          ti.tenant_id, d.dag_id AS dag_id_text, ti.dag_run_id, ti.task_id;

-- name: RescheduleTaskInstanceByIDIfActive :exec
-- Settle a lost reschedule from the durable outcome record (ADR 0052): park the TI
-- in up_for_reschedule with the record's next-poke time, guarded by id,
-- try_number and attempt_epoch (never clobber a different attempt or a terminal
-- row), consuming no
-- retry budget. Mirrors RescheduleTaskInstance but keyed by id, for the reconciler.
UPDATE task_instances
SET state = 'up_for_reschedule'::task_state,
    reschedule_at = sqlc.arg(reschedule_at),
    first_reschedule_at = COALESCE(first_reschedule_at, now())
WHERE id = sqlc.arg(id) AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state IN ('running', 'queued', 'scheduled');

-- name: ReportTaskResult :execrows
-- $3 is cast to task_state in every usage: without the cast Postgres deduces an
-- enum type from `state = $3` but text from the literal comparisons below and
-- rejects the parameter as having inconsistent types (SQLSTATE 42P08). The pod
-- agent path is the first to exercise this query end-to-end.
--
-- Guarded on both the source state and the attempt, matching the other three
-- writes to this table (FailTaskInstanceIfActive, RescheduleTaskInstance,
-- RecordHeartbeat). Two writers touch task_instances — the scheduler tick and
-- this report — so an unguarded UPDATE lets a report that arrives late land
-- wherever the row happens to be:
--   * after a reaper settled the row, it resurrects a terminal state (a run
--     reports success on work the system already abandoned, and downstream
--     tasks fire on it);
--   * after a retry, it lands on the next attempt, because ResetTaskInstanceToNone
--     bumps try_number in place rather than inserting a new row.
-- The agent token already carries the try_number it was dispatched with, so the
-- value that tells the attempts apart is present at the call site.
--
-- try_number alone is not enough: an infra re-place, a reschedule poke and a
-- repeated dispatch reuse it. attempt_epoch (ADR 0051 amendment) is the
-- execution within the try. A token minted without the claim is read as epoch
-- 0: it matches its own pre-upgrade attempt (rows migrate at 0) and never an
-- attempt dispatched after the upgrade (the dispatch claim makes those >= 1).
-- That keeps a superseded agent's RUNNING report off its replacement (#911).
-- Returns the affected row count so the caller can tell a real write from a
-- rejected late report instead of dropping it silently.
--
-- The state set is deliberately wider than the siblings' ('scheduled','queued',
-- 'running'): it also admits 'none'. Those writes are driven by the scheduler,
-- which only touches rows it has already advanced; this one is driven by the
-- agent, which starts reporting earlier. launchQueued dispatches the pod BEFORE
-- recording `queued`, so between those two statements a fast-starting task —
-- routine under the Lite subprocess executor — legitimately reports `running`
-- while the row is still `none`. Excluding it would reject a correct report,
-- trading a rare silent corruption for a frequent one. The settled states
-- (success/failed/skipped/upstream_failed) are what this guard is for.
UPDATE task_instances
SET state = $3::task_state,
    exit_code = $4,
    error_message = $5,
    started_at = CASE WHEN $3::task_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    ended_at = CASE WHEN $3::task_state IN ('success', 'failed', 'skipped', 'upstream_failed') THEN now() ELSE ended_at END,
    duration_seconds = CASE WHEN $3::task_state IN ('success', 'failed') AND started_at IS NOT NULL
        THEN EXTRACT(EPOCH FROM (now() - started_at)) ELSE duration_seconds END
WHERE dag_run_id = $1 AND task_id = $2
  AND tenant_id = sqlc.arg(tenant_id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = COALESCE(sqlc.narg(attempt_epoch)::int, 0)
  AND state IN ('none', 'scheduled', 'queued', 'running');

-- name: RescheduleTaskInstance :execrows
-- A reschedule-mode sensor (mode='reschedule') poked not-ready: park the active TI
-- in up_for_reschedule with its next-poke time ($3) so the scheduler re-dispatches
-- it once reschedule_at passes (#380), without consuming retry budget. Guarded to
-- the active states so a late report never clobbers a terminal row, and on the
-- attempt (try_number and attempt_epoch, the same rule as ReportTaskResult) so a
-- poke from a superseded attempt never parks its replacement. Returns the row
-- count; zero is a stale report. ended_at is left untouched (the task is not
-- finished); started_at is preserved.
UPDATE task_instances
SET state = 'up_for_reschedule'::task_state,
    reschedule_at = $3,
    -- Stamp the FIRST reschedule once and preserve it across re-dispatches so the
    -- delivered get_first_reschedule_date lets the sensor honor cumulative timeout.
    first_reschedule_at = COALESCE(first_reschedule_at, now())
WHERE dag_run_id = $1 AND task_id = $2
  AND tenant_id = sqlc.arg(tenant_id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = COALESCE(sqlc.narg(attempt_epoch)::int, 0)
  AND state IN ('running', 'queued', 'scheduled');

-- name: ResolveRunRef :one
SELECT t.id AS tenant_id, dr.id AS dag_run_id
FROM dag_runs dr
JOIN dags d ON d.id = dr.dag_id
JOIN tenants t ON t.id = d.tenant_id
WHERE t.name = $1 AND d.dag_id = $2 AND dr.run_id = $3 AND dr.tenant_id = t.id;

-- name: LatestRunsForDags :many
SELECT d.dag_id AS dag_id_text,
       r.run_id, r.logical_date, r.state, r.trigger, r.queued_at, r.started_at, r.ended_at
FROM dags d
JOIN LATERAL (
    SELECT dr.run_id, dr.logical_date, dr.state, dr.trigger, dr.queued_at, dr.started_at, dr.ended_at
    FROM dag_runs dr
    WHERE dr.dag_id = d.id
    ORDER BY dr.logical_date DESC
    LIMIT $3
) r ON true
WHERE d.tenant_id = $1 AND d.dag_id = ANY($2::text[])
ORDER BY d.dag_id, r.logical_date DESC;

-- name: TaskInstancesForDagRuns :many
SELECT dr.run_id, ti.task_id, ti.try_number, ti.state,
       ti.started_at, ti.ended_at
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE d.tenant_id = $1 AND d.dag_id = $2 AND dr.run_id = ANY($3::text[])
ORDER BY dr.run_id, ti.task_id, ti.try_number;

-- name: CountDagsByLatestRunState :many
-- One index probe per DAG of the tenant for its newest run. DAGs without runs
-- drop out of the CROSS JOIN, so they are not counted.
SELECT lr.state AS state, count(*) AS n
FROM dags d
CROSS JOIN LATERAL (
    SELECT r.state
    FROM dag_runs r
    WHERE r.dag_id = d.id
    ORDER BY r.logical_date DESC
    LIMIT 1
) lr
WHERE d.tenant_id = $1
GROUP BY lr.state;

-- name: CountDagRunStatesInWindow :many
SELECT r.state AS state, count(*) AS n
FROM dag_runs r
JOIN dags d ON d.id = r.dag_id
WHERE d.tenant_id = $1 AND r.logical_date >= $2 AND r.logical_date <= $3
GROUP BY r.state;

-- name: CountTaskInstanceStatesInWindow :many
SELECT ti.state AS state, count(*) AS n
FROM task_instances ti
JOIN dag_runs r ON r.id = ti.dag_run_id
JOIN dags d ON d.id = r.dag_id
WHERE d.tenant_id = $1 AND r.logical_date >= $2 AND r.logical_date <= $3
GROUP BY ti.state;

-- name: ResetFailedTaskInstance :execrows
-- Archives the current attempt into task_instance_history then resets the
-- live row. See ResetTaskInstanceToNone for the per-attempt rationale.
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note, attempt_epoch
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note, src.attempt_epoch
    FROM task_instances src
    WHERE src.tenant_id = sqlc.arg(tenant_id) AND src.dag_run_id = sqlc.arg(dag_run_id) AND src.task_id = sqlc.arg(task_id)
      AND src.state IN ('failed', 'upstream_failed', 'up_for_retry')
    ON CONFLICT (task_instance_id, try_number) DO UPDATE
    SET state = EXCLUDED.state,
        queued_at = EXCLUDED.queued_at,
        scheduled_at = EXCLUDED.scheduled_at,
        started_at = EXCLUDED.started_at,
        ended_at = EXCLUDED.ended_at,
        duration_seconds = EXCLUDED.duration_seconds,
        exit_code = EXCLUDED.exit_code,
        error_message = EXCLUDED.error_message,
        hostname = EXCLUDED.hostname,
        pod_name = EXCLUDED.pod_name,
        node_name = EXCLUDED.node_name,
        note = EXCLUDED.note,
        attempt_epoch = EXCLUDED.attempt_epoch
    WHERE task_instance_history.attempt_epoch < EXCLUDED.attempt_epoch
    RETURNING task_instance_id
)
UPDATE task_instances ti
SET state = 'none',
    released_at = now(),
    started_at = NULL,
    ended_at = NULL,
    queued_at = NULL,
    scheduled_at = NULL,
    dispatch_attempts = 0,
    next_dispatch_at = NULL,
    dispatch_unavailable_since = NULL,
    last_failure_kind = NULL,
    infra_confirmed_at = NULL,
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = ti.attempt_epoch + 1,
    infra_attempts = 0,
    -- Restore the retry budget from the task (#1131), as Airflow's
    -- clear_task_instances does with max_tries = try_number + task.retries. Here
    -- max_tries counts retries + 1 and try_number is bumped by this statement, so
    -- the same rule is (new try_number) + retries. A task the executing version no
    -- longer declares gets no retries, but never a budget below the attempt
    -- being made.
    -- spec_task_ids and spec_retries are parallel arrays; a task absent from
    -- spec_task_ids indexes to NULL and falls through to the GREATEST.
    max_tries = COALESCE(
        ti.try_number + 1 + (sqlc.arg(spec_retries)::int[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)],
        GREATEST(ti.max_tries, ti.try_number + 1)),
    -- The size the admission gate charges the re-run is the executing version's
    -- (#1499, ADR 0066), so PoolSlotUsage sums the same weight; spec_pool_slots
    -- is parallel to spec_task_ids, and a task the version no longer declares
    -- keeps the size it had.
    pool_slots = COALESCE(
        (sqlc.arg(spec_pool_slots)::int[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)],
        ti.pool_slots),
    -- The pool is re-bound the same way (#1589), so a task the version moved to
    -- another pool is charged there and not to the pool it left. spec_pools is
    -- parallel to spec_task_ids with '' for the implicit default_pool, stored as
    -- NULL like materialization does; a task the version no longer declares
    -- keeps the pool it had.
    pool = CASE
        WHEN array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id) IS NULL THEN ti.pool
        ELSE NULLIF((sqlc.arg(spec_pools)::text[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)], '')
    END,
    try_number = ti.try_number + 1
WHERE ti.tenant_id = sqlc.arg(tenant_id) AND ti.dag_run_id = sqlc.arg(dag_run_id) AND ti.task_id = sqlc.arg(task_id)
  AND ti.state IN ('failed', 'upstream_failed', 'up_for_retry');

-- name: ResetAllFailedTaskInstances :many
-- Archives every failed attempt in the run into task_instance_history then
-- resets. See ResetTaskInstanceToNone for the per-attempt rationale. Returns the
-- task ids it reset, so the clear can delete exactly their XCom.
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note, attempt_epoch
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note, src.attempt_epoch
    FROM task_instances src
    WHERE src.tenant_id = sqlc.arg(tenant_id) AND src.dag_run_id = sqlc.arg(dag_run_id)
      AND src.state IN ('failed', 'upstream_failed', 'up_for_retry')
    ON CONFLICT (task_instance_id, try_number) DO UPDATE
    SET state = EXCLUDED.state,
        queued_at = EXCLUDED.queued_at,
        scheduled_at = EXCLUDED.scheduled_at,
        started_at = EXCLUDED.started_at,
        ended_at = EXCLUDED.ended_at,
        duration_seconds = EXCLUDED.duration_seconds,
        exit_code = EXCLUDED.exit_code,
        error_message = EXCLUDED.error_message,
        hostname = EXCLUDED.hostname,
        pod_name = EXCLUDED.pod_name,
        node_name = EXCLUDED.node_name,
        note = EXCLUDED.note,
        attempt_epoch = EXCLUDED.attempt_epoch
    WHERE task_instance_history.attempt_epoch < EXCLUDED.attempt_epoch
    RETURNING task_instance_id
)
UPDATE task_instances ti
SET state = 'none',
    released_at = now(),
    started_at = NULL,
    ended_at = NULL,
    queued_at = NULL,
    scheduled_at = NULL,
    dispatch_attempts = 0,
    next_dispatch_at = NULL,
    dispatch_unavailable_since = NULL,
    last_failure_kind = NULL,
    infra_confirmed_at = NULL,
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = ti.attempt_epoch + 1,
    infra_attempts = 0,
    -- Restore the retry budget from the task (#1131), as Airflow's
    -- clear_task_instances does with max_tries = try_number + task.retries. Here
    -- max_tries counts retries + 1 and try_number is bumped by this statement, so
    -- the same rule is (new try_number) + retries. A task the executing version no
    -- longer declares gets no retries, but never a budget below the attempt
    -- being made.
    -- spec_task_ids and spec_retries are parallel arrays; a task absent from
    -- spec_task_ids indexes to NULL and falls through to the GREATEST.
    max_tries = COALESCE(
        ti.try_number + 1 + (sqlc.arg(spec_retries)::int[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)],
        GREATEST(ti.max_tries, ti.try_number + 1)),
    -- The size the admission gate charges the re-run is the executing version's
    -- (#1499, ADR 0066), so PoolSlotUsage sums the same weight; spec_pool_slots
    -- is parallel to spec_task_ids, and a task the version no longer declares
    -- keeps the size it had.
    pool_slots = COALESCE(
        (sqlc.arg(spec_pool_slots)::int[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)],
        ti.pool_slots),
    -- The pool is re-bound the same way (#1589), so a task the version moved to
    -- another pool is charged there and not to the pool it left. spec_pools is
    -- parallel to spec_task_ids with '' for the implicit default_pool, stored as
    -- NULL like materialization does; a task the version no longer declares
    -- keeps the pool it had.
    pool = CASE
        WHEN array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id) IS NULL THEN ti.pool
        ELSE NULLIF((sqlc.arg(spec_pools)::text[])[array_position(sqlc.arg(spec_task_ids)::text[], ti.task_id)], '')
    END,
    try_number = ti.try_number + 1
WHERE ti.tenant_id = sqlc.arg(tenant_id) AND ti.dag_run_id = sqlc.arg(dag_run_id)
  AND ti.state IN ('failed', 'upstream_failed', 'up_for_retry')
RETURNING ti.task_id;

-- name: SetTaskInstanceNote :exec
UPDATE task_instances
SET note = $3
WHERE dag_run_id = $1 AND task_id = $2;

-- name: ListOrphanCandidates :many
-- Lists dag_runs currently in 'running' whose task instances are ALL terminal
-- or never-started (every TI in none/success/failed/skipped/upstream_failed/removed),
-- alongside the timestamp of their most recent observable activity. The "no
-- live TI" filter is the critical safety guarantee: a run that is still
-- progressing stays out of the candidate set, so the reaper can never kill a
-- live execution. Live means any state outside that settled set, which covers
-- a legitimately-active task (scheduled/queued/running: slow image pull,
-- long-running job) AND a task parked for the scheduler to bring back
-- (up_for_retry during its retry_delay, up_for_reschedule between sensor pokes,
-- the reserved deferred state). Parked states stamp no fresh timestamp on entry,
-- so last_activity cannot protect them: a retry_delay or poke_interval longer
-- than the reaper threshold would otherwise fail a healthy run. Listing the
-- settled states (rather than the live ones) keeps any future non-terminal
-- state on the safe side by default. `none` stays reapable: a never-started TI
-- whose upstreams are all settled is decided on the next scheduler tick, and one
-- whose upstream is still pending already has a live sibling keeping the run
-- out. A TI released back to `none` for another attempt (the retry release,
-- the reschedule re-dispatch, the infra re-place, an operator clear) has its
-- per-attempt timestamps cleared and only becomes `scheduled` on the next
-- tick, so each release stamps released_at and last_activity counts it: the
-- release itself is activity, and a run is never reaped in the tick between a
-- release and none -> scheduled. An infra-failed TI parked in its re-place backoff is `failed` and is covered by
-- the threshold itself, which sits above that backoff. The shape this catches
-- is the post-crash one: TIs settled (success/failed/skipped/upstream_failed)
-- but FinalizeRun did not transition the dag_run, e.g. the server died between
-- the last TI report and the next scheduler tick. The LIMIT bounds a single
-- tick's reap work even after a multi-hour outage; the rest are picked up on
-- the next tick (the reaper is a backstop, not a sprint). The list is only a
-- snapshot: MarkRunOrphanedRun re-checks the same predicate atomically, so keep
-- the two in step.
SELECT dr.id AS id,
       d.dag_id AS dag_id_text,
       GREATEST(
           COALESCE(MAX(ti.ended_at), 'epoch'::timestamptz),
           COALESCE(MAX(ti.started_at), 'epoch'::timestamptz),
           COALESCE(MAX(ti.released_at), 'epoch'::timestamptz),
           COALESCE(dr.started_at, 'epoch'::timestamptz),
           dr.queued_at
       )::timestamptz AS last_activity
FROM dag_runs dr
JOIN dags d ON d.id = dr.dag_id
LEFT JOIN task_instances ti ON ti.dag_run_id = dr.id
WHERE dr.state = 'running'
  AND NOT EXISTS (
      SELECT 1 FROM task_instances ti2
      WHERE ti2.dag_run_id = dr.id
        AND ti2.state NOT IN ('none', 'success', 'failed', 'skipped', 'upstream_failed', 'removed')
  )
GROUP BY dr.id, d.dag_id, dr.started_at, dr.queued_at
ORDER BY dr.queued_at
LIMIT 100;

-- name: MarkRunOrphanedTaskInstances :exec
-- Fails any still-active task instance under an orphaned run. Called together
-- with MarkRunOrphanedRun inside a single transaction (the repository owns the
-- atomicity); split because sqlc cannot generate a CTE+UPDATE that reuses one
-- parameter across an UPDATE-inside-WITH and the outer UPDATE.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    error_message = 'orphaned: scheduler restart left this task without a runner'
WHERE dag_run_id = $1
  AND state IN ('scheduled', 'queued', 'running');

-- name: RecordTaskHeartbeat :execrows
-- Stamps last_heartbeat_at on the active TI of an attempt. Bounded by the
-- (dag_run_id, task_id, try_number) tuple to match the agent's identity. The
-- state IN guard avoids stamping a heartbeat on a TI the scheduler already
-- transitioned to terminal between the agent's last heartbeat and now — a
-- terminal TI must stay terminal even if a late heartbeat arrives.
--
-- Returns the affected row count. Zero means the heartbeating agent's attempt
-- no longer matches the live row — its try_number is behind, or a reaper
-- already settled the row terminal — the same "moved on" predicate the state
-- report is guarded by (#467). The agent RPC turns a zero here into a
-- should_terminate signal so a reaped-but-alive pod stops itself (#474).
--
-- A token carrying attempt_epoch must match it exactly (ADR 0051 amendment). A
-- claim-less token matches on try_number alone: it is either a pre-upgrade
-- token or one an old replica re-minted during a rolling upgrade, which drops
-- the claim it does not know. Fencing its heartbeat would kill a live attempt
-- for a missing claim; its reports are still fenced as epoch 0.
UPDATE task_instances
SET last_heartbeat_at = now()
WHERE dag_run_id = $1
  AND task_id = $2
  AND try_number = $3
  AND tenant_id = sqlc.arg(tenant_id)
  AND (sqlc.narg(attempt_epoch)::int IS NULL OR attempt_epoch = sqlc.narg(attempt_epoch)::int)
  AND state IN ('queued', 'running');

-- name: IsTaskInstanceLive :one
-- Reports whether the attempt (dag_run_id, task_id, try_number) is still live:
-- present and in an active (non-terminal) state. This is exactly the predicate
-- RecordTaskHeartbeat stamps on — the same (dag_run_id, task_id, try_number)
-- tuple and the same state IN ('queued', 'running') guard — but as a pure READ
-- with no write. It is the read-only revocation signal the secret path consults
-- (ADR 0055): a terminal or superseded attempt (try_number moved on) is not
-- live, so a token that carries it stops resolving secrets, even while its
-- signature is still valid.
--
-- It MUST derive ONLY from (run, task, try) + active state. It must NEVER gain a
-- "run is not current / archived / logical_date in the past" clause: a recency
-- term would deny a legitimate clear-and-rerun of an old run — credential
-- lifetime binds to the attempt, never to the run's age or logical date.
--
-- The epoch term is the heartbeat's: exact for a token carrying attempt_epoch,
-- absent for a claim-less one (ADR 0051 amendment).
SELECT EXISTS (
    SELECT 1 FROM task_instances
    WHERE dag_run_id = $1
      AND task_id = $2
      AND try_number = $3
      AND tenant_id = sqlc.arg(tenant_id)
      AND (sqlc.narg(attempt_epoch)::int IS NULL OR attempt_epoch = sqlc.narg(attempt_epoch)::int)
      AND state IN ('queued', 'running')
);

-- name: ClaimAttemptEpoch :one
-- The dispatcher's claim of a new execution attempt (ADR 0051 amendment, A1).
-- launchQueued creates the pod BEFORE it records `queued`, so a dispatch whose
-- queued write failed is dispatched again on a later tick with no reset rail in
-- between; without a claim here both pods would share (try_number,
-- attempt_epoch). Bumping at dispatch gives every execution its own epoch
-- whatever path led to it. The token (A2) and the pod label (A4) are minted
-- from the value returned.
--
-- Guarded to the pre-dispatch states. 'queued' is included because the
-- buffered dispatcher records queued before its worker resolves the row. A row
-- that is running or settled is never claimed (zero rows), so a late or
-- duplicate dispatch cannot move the epoch of the live attempt. The row is the
-- task's latest try, matching how the dispatcher has always resolved it.
UPDATE task_instances
SET attempt_epoch = attempt_epoch + 1
WHERE id = (
    SELECT latest.id FROM task_instances latest
    WHERE latest.dag_run_id = $1 AND latest.task_id = $2
    ORDER BY latest.try_number DESC
    LIMIT 1
)
  AND state IN ('none', 'scheduled', 'queued')
RETURNING id, tenant_id, try_number, attempt_epoch;

-- name: BindWarmAttempt :execrows
-- Records the durable warm-attempt binding (ADR 0058 N1d-a1): the warm worker
-- pod ($4, its own downward-API pod name) that acked THIS attempt (dag_run_id,
-- task_id, try_number) as started. A later failover reaper matches warm_worker_id
-- against the live warm-pod set to find attempts a dead warm pod held.
--
-- Guarded on state IN ('scheduled', 'queued', 'running'), so a settled attempt
-- is never bound: an ack that races a reaper settling the row must not stamp a
-- worker onto a terminal TI. 'scheduled' is admitted because with synchronous
-- dispatch the warm placer hands the assignment out inside Dispatch, before the
-- tick records queued, and the worker can ack (and report RUNNING) first. Missing
-- that bind left a running warm attempt unbound, and the pod-lost reaper failed it
-- while it ran (#1600). The epoch pin makes this safe: a scheduled row carries the
-- claimed epoch only for the attempt the dispatcher just handed out, and every
-- re-dispatch bumps it. Bounded by (dag_run_id, task_id, try_number,
-- attempt_epoch) to match exactly the attempt the assignment named. Returns the
-- affected row count; zero means the attempt already moved on (terminal or
-- superseded), and the caller treats that as a benign no-op, never an error.
UPDATE task_instances
SET warm_worker_id = $4
WHERE dag_run_id = $1
  AND task_id = $2
  AND try_number = $3
  AND attempt_epoch = COALESCE(sqlc.narg(attempt_epoch)::int, 0)
  AND state IN ('scheduled', 'queued', 'running');

-- name: ListWarmBoundRunningTIs :many
-- Lists every `running` TI that is durably bound to a warm worker (ADR 0058
-- N1d-a2): warm_worker_id names the warm pod that acked and is serving this
-- attempt. The failover reaper matches that name against the live warm-pod set;
-- a bound TI whose worker is no longer live has lost its serving pod and is
-- routed to infra via MarkTaskPodLost (fan-out: every attempt a dead worker
-- held is marked). Only `running` rows carry a live attempt, and the
-- IS NOT NULL filter keeps dedicated (non-warm) tasks out of this reaper
-- entirely — with warm pools off no TI is ever bound, so this returns empty and
-- the reaper is inert. The LIMIT bounds a single tick's work after a large
-- outage; the rest are picked up next tick.
-- tenant_id and dag_id locate the attempt's log stream for the reaper's
-- killed marker (#861).
SELECT ti.id AS task_instance_id,
       ti.tenant_id AS tenant_id,
       ti.dag_run_id AS dag_run_id,
       d.dag_id AS dag_id_text,
       ti.task_id AS task_id,
       ti.try_number AS try_number,
       ti.attempt_epoch AS attempt_epoch,
       ti.warm_worker_id AS warm_worker_id
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE ti.state = 'running'
  AND ti.warm_worker_id IS NOT NULL
ORDER BY ti.started_at NULLS LAST
LIMIT 100;

-- name: ListBusyWarmWorkerPods :many
-- Lists the DISTINCT warm-worker pod names currently serving an active attempt
-- (ADR 0058 N1d-b): a warm worker is BUSY iff some task_instance in an active
-- state (`queued` or `running`) is durably bound to it (warm_worker_id = the
-- pod's own name — the binding landed in N1d-a1/a2). The busy-aware warm-pool
-- reconciler reads this once per tick to classify each live warm pod as busy
-- (serving an attempt) or idle, so scale-down and drain delete only IDLE workers
-- and never kill an in-flight attempt (review findings M1/M2).
--
-- The `queued` state MUST be in the busy set, not just `running`: BindWarmAttempt
-- stamps warm_worker_id the instant a warm worker ACKs an assignment as started,
-- while the TI is still `queued`, and the child only reports RUNNING
-- (queued->running) a moment later. Between the ack and that report the worker is
-- actively STARTING the attempt; counting only `running` would classify it IDLE
-- in that window and let the excess-idle-tail delete remove a worker that is
-- starting an attempt — an M1 violation that kills a just-started attempt. The
-- BindWarmAttempt guard binds the scheduled/queued/running window (a scheduled
-- bound row moves to queued or running within the same tick), and every
-- re-dispatch/reset that moves a bound row back out of that window clears
-- warm_worker_id, so a queued+bound row here is always a live starting attempt,
-- never a stale binding.
--
-- With warm pools off no TI is ever bound, so this is always empty and every
-- worker classifies as idle — byte-for-byte today's dedicated pod-per-task
-- behavior.
SELECT DISTINCT ti.warm_worker_id AS warm_worker_id
FROM task_instances ti
WHERE ti.state IN ('queued', 'running')
  AND ti.warm_worker_id IS NOT NULL;

-- name: ListAgentLostCandidates :many
-- Lists running TIs that have heartbeated at least once and whose latest
-- heartbeat is non-null, alongside enough identity to log + observe.
-- The "non-null" filter is the safety guarantee: a TI that never heartbeated
-- is either inline (no agent ever exists) or fresh — out of scope for this
-- reaper. The LIMIT bounds a single tick's reap work even after a large
-- outage; the rest are picked up on the next tick.
SELECT ti.id AS task_instance_id,
       ti.tenant_id AS tenant_id,
       ti.dag_run_id AS dag_run_id,
       d.dag_id AS dag_id_text,
       ti.task_id AS task_id,
       ti.try_number AS try_number,
       ti.attempt_epoch AS attempt_epoch,
       ti.last_heartbeat_at AS last_heartbeat_at,
       -- started_at lets the reaper tell an attempt that outlived
       -- auth.max_attempt_credential_lifetime from a lost agent (#1461).
       ti.started_at AS started_at
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE ti.state = 'running'
  AND ti.last_heartbeat_at IS NOT NULL
ORDER BY ti.last_heartbeat_at
LIMIT 100;

-- name: MarkTaskDispatchFailed :exec
-- Fails a TI whose asynchronous dispatch (BufferedDispatcher worker) errored
-- inside the inner dispatcher. Targets the active row by (dag_run_id,
-- task_id) and the active states (scheduled/queued) — a TI that already
-- moved to running/terminal between the worker accepting the request and
-- the dispatch failing is left alone (defense in depth).
-- The failure is dispatch_failed, which is terminal: the retry budget is
-- spent as in FailDispatchExhausted, so the planner does not retry it.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    error_message = $3,
    max_tries = LEAST(max_tries, try_number)
WHERE dag_run_id = $1
  AND task_id = $2
  AND state IN ('scheduled', 'queued');

-- name: MarkTaskAgentLost :execrows
-- Fails a TI whose agent went silent. The WHERE state='running' guard
-- prevents overwriting a TI the agent's last terminal report finally
-- delivered between our list and our write (defense in depth — a late
-- report wins over the reaper). Idempotent on a second call. Pinned to the
-- listed attempt, (try_number, attempt_epoch) (ADR 0051 amendment), so a mark
-- computed for a superseded attempt never fails its replacement.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    -- Provisional on Kubernetes (the reconciler confirms it), confirmed at
    -- once on Lite (ADR 0052 amendment, part 2).
    infra_confirmed_at = CASE WHEN sqlc.arg(provisional)::bool THEN NULL ELSE now() END,
    error_message = 'agent_lost: no heartbeat within the threshold — see #128'
WHERE id = sqlc.arg(id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state = 'running';

-- name: MarkTaskCredentialCeiling :execrows
-- Fails a TI whose agent went silent after the attempt outlived
-- auth.max_attempt_credential_lifetime (#1461). Renewal stops at the ceiling,
-- so the silence is the credential lapsing, not a lost agent: this is a TASK
-- failure (last_failure_kind NULL, the retry policy applies), never an infra
-- mark the planner would re-place with a fresh credential. Same guards as
-- MarkTaskAgentLost: state='running' (a late report wins) and the listed
-- attempt, (try_number, attempt_epoch) (ADR 0051 amendment).
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = NULL,
    infra_confirmed_at = NULL,
    error_message = 'credential_ceiling: attempt outlived auth.max_attempt_credential_lifetime'
WHERE id = sqlc.arg(id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state = 'running';

-- name: ListRunningTasks :many
-- Lists every TI currently in `running` alongside the timestamp it entered
-- running, for the pod-lost reaper (#527). A running TI whose backing pod
-- vanished before its first heartbeat is invisible to the agent-lost reaper
-- (its null-heartbeat zero-guard) and to the reconciler (which only sees pods
-- that still exist), so it would sit `running` until the 5-minute orphan reaper.
-- Warm-pool attempts (warm_worker_id set) are excluded: they run in a shared
-- warm pod with no per-task labels, so the reaper's presence check would always
-- read them as lost; the warm-worker-lost reaper owns them. The grace period is
-- applied here, before the LIMIT, so attempts still inside it never take the
-- slots of those past it; a NULL started_at is never listed (too poorly observed
-- to reap). The reaper re-checks grace and pod liveness per candidate in Go.
-- heartbeated lets Lite (no pods) judge a TI whose agent died before its first
-- heartbeat, which the agent-lost query never lists (#916). The page is
-- keyset-paged on (started_at, id) after the reaper's cursor (R-07, #1597):
-- with a fixed "oldest 100", 100 attempts with live pods hid a newer attempt
-- whose pod vanished forever. The reaper keeps the cursor across sweeps and
-- passes '-infinity' and the nil uuid to start over. The LIMIT bounds a single
-- sweep's reap work.
SELECT ti.id AS task_instance_id,
       ti.tenant_id AS tenant_id,
       ti.dag_run_id AS dag_run_id,
       d.dag_id AS dag_id_text,
       ti.task_id AS task_id,
       ti.try_number AS try_number,
       ti.attempt_epoch AS attempt_epoch,
       ti.started_at AS started_at,
       (ti.last_heartbeat_at IS NOT NULL)::boolean AS heartbeated
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE ti.state = 'running'
  AND ti.warm_worker_id IS NULL
  AND ti.started_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::float8)
  AND (ti.started_at, ti.id) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY ti.started_at, ti.id
LIMIT sqlc.arg(page_size);

-- name: MarkTaskPodLost :execrows
-- Fails a running TI whose pod has vanished (deleted/evicted/node lost). The
-- WHERE state='running' guard makes it idempotent and prevents overwriting a
-- late terminal report that landed between our list and our write (a live
-- report wins over the reaper). Idempotent on a second call. Pinned to the
-- listed attempt, (try_number, attempt_epoch) (ADR 0051 amendment), so a mark
-- computed for a superseded attempt never fails its replacement.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    -- Provisional on Kubernetes (the reconciler confirms it), confirmed at
    -- once on Lite (ADR 0052 amendment, part 2).
    infra_confirmed_at = CASE WHEN sqlc.arg(provisional)::bool THEN NULL ELSE now() END,
    -- error_message overrides the default reason; the warm-worker-lost reaper
    -- passes one naming an OOM kill (#1218). It keeps the pod_lost: prefix the
    -- infra override reads the mark from.
    error_message = COALESCE(sqlc.narg(error_message)::text, 'pod_lost: the task pod vanished with no live pod past the grace period — see #527')
WHERE id = sqlc.arg(id)
  AND try_number = sqlc.arg(try_number)
  AND attempt_epoch = sqlc.arg(attempt_epoch)
  AND state = 'running';

-- name: LockRunTaskInstancesForReap :exec
-- Share-locks every task instance of a run inside the reap transaction, before
-- MarkRunOrphanedRun re-checks the orphan predicate, so the re-check (a fresh
-- snapshot under READ COMMITTED) sees committed state and no TI of the run can
-- change until the reap commits or rolls back. NOWAIT: a TI another transaction
-- is writing right now is activity, so the reap gives up (lock_not_available,
-- treated as a no-op by ReapRun) instead of waiting. Waiting would let these
-- share locks, taken in scan order, form a cycle with a writer that locks
-- several TIs of the run in another order (a multi-task clear, a batched
-- scheduler transition); a reap that never waits on a TI cannot be in one.
SELECT id FROM task_instances
WHERE dag_run_id = $1
FOR SHARE NOWAIT;

-- name: MarkRunOrphanedRun :execrows
-- Fails an orphaned dag run, but only if it is STILL orphaned: the same
-- predicate ListOrphanCandidates applies (running, no live TI, last activity at
-- or before the reaper's cutoff) is re-checked in this statement, because the
-- list is a snapshot and a TI may have moved (failed -> up_for_retry, none ->
-- scheduled) or fresh activity may have landed since. Keep the two in step. The
-- `state = 'running'` guard also makes the reap a safety net, never a takeover:
-- a competing finalizer (the normal scheduler path) cannot be overwritten.
-- Zero rows means the run is no longer an orphan and nothing may be touched.
-- Idempotent: a second call on a run already failed updates zero rows.
UPDATE dag_runs dr
SET state = 'failed',
    ended_at = now(),
    note = 'orphaned: no scheduler activity within the orphan window — see #120'
WHERE dr.id = sqlc.arg(id)
  AND dr.state = 'running'
  AND NOT EXISTS (
      SELECT 1 FROM task_instances ti2
      WHERE ti2.dag_run_id = dr.id
        AND ti2.state NOT IN ('none', 'success', 'failed', 'skipped', 'upstream_failed', 'removed')
  )
  AND GREATEST(
          COALESCE((SELECT GREATEST(MAX(ti.ended_at), MAX(ti.started_at), MAX(ti.released_at))
                    FROM task_instances ti WHERE ti.dag_run_id = dr.id), 'epoch'::timestamptz),
          COALESCE(dr.started_at, 'epoch'::timestamptz),
          dr.queued_at
      ) <= sqlc.arg(quiet_before)::timestamptz;

-- name: RecordDispatchFailure :exec
-- A synchronous dispatch attempt failed (ADR 0031 Amendment A). Increment the
-- consecutive-failure counter and back off the next attempt to $3, so the planner
-- (which gates scheduled->queued on next_dispatch_at) does not re-attempt every
-- tick. Guarded to 'scheduled' so a report that raced the dispatch cannot clobber
-- a row that has since progressed. try_number is untouched: this is infra, not a
-- task failure.
-- last_heartbeat_at is cleared and attempt_epoch bumped for the reasons given on
-- ResetTaskInstanceToNone. The apiserver answered the create, so the outage
-- window of ADR 0093 is closed: dispatch_unavailable_since is cleared, and a
-- later unavailable failure starts a new window.
UPDATE task_instances
SET dispatch_attempts = dispatch_attempts + 1,
    next_dispatch_at = $3,
    dispatch_unavailable_since = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = attempt_epoch + 1
WHERE dag_run_id = $1 AND task_id = $2 AND state = 'scheduled';

-- name: RecordDispatchBackpressure :exec
-- A pod CREATE was refused by cluster backpressure — a ResourceQuota 403 or an
-- API Priority & Fairness 429 (ADR 0053). Back off the next attempt to $3 WITHOUT
-- touching dispatch_attempts: backpressure is a temporary "no room," retriable
-- forever, and must never accumulate toward the dispatch_failed budget the way a
-- permanent failure does (RecordDispatchFailure). The planner gates
-- scheduled->queued on next_dispatch_at alone, so setting it holds and re-offers
-- the task without a counter bump. Guarded to 'scheduled' for the same reason as
-- RecordDispatchFailure; try_number is untouched — this is infra, not a task
-- failure. The apiserver answered the create, so dispatch_unavailable_since is
-- cleared as in RecordDispatchFailure.
UPDATE task_instances
SET next_dispatch_at = $3,
    dispatch_unavailable_since = NULL
WHERE dag_run_id = $1 AND task_id = $2 AND state = 'scheduled';

-- name: RecordDispatchUnavailable :one
-- A pod CREATE failed because the Kubernetes API was unavailable: a 503, a
-- 504, a 500 (a fail-closed admission webhook that is down), a connection
-- refused or reset, or the client deadline (#1606). Re-offer the task the way
-- backpressure is re-offered, back to 'scheduled' and held until
-- next_dispatch_at, WITHOUT touching dispatch_attempts, and stamp the start of
-- the outage window on the first such failure (COALESCE keeps the first one).
-- The caller fails the task as dispatch_failed once the window it returns is
-- older than the scheduler's bound (ADR 0093), so an API that never comes back
-- cannot hold the task forever.
--
-- The hold grows with the outage: the next attempt waits as long as the task
-- has already been riding it out, at least min_delay and at most max_delay.
-- That doubles the interval without a counter, so a recovering apiserver is
-- not hit by every due task every few seconds, and a recovery is still seen
-- within max_delay.
--
-- The create may have succeeded on the server before the response was lost,
-- so this is a rail like RequeueDispatch: attempt_epoch is bumped, and the
-- re-dispatch claims a new epoch on top, so an orphan pod from the ambiguous
-- create is fenced (its reports get ErrStaleReport). Guarded to
-- scheduled/queued: the sync path re-offers a scheduled row, a buffered
-- worker a scheduled or queued one, and a row the agent has since reported on
-- (the orphan pod won the race and runs the attempt) is left alone. The other
-- fields are cleared for the reasons given on RequeueDispatch.
UPDATE task_instances
SET state = 'scheduled',
    next_dispatch_at = sqlc.arg(now)::timestamptz + LEAST(
        sqlc.arg(max_delay)::interval,
        GREATEST(sqlc.arg(min_delay)::interval,
                 sqlc.arg(now)::timestamptz - COALESCE(dispatch_unavailable_since, sqlc.arg(now)::timestamptz))),
    dispatch_unavailable_since = COALESCE(dispatch_unavailable_since, sqlc.arg(now)::timestamptz),
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = attempt_epoch + 1,
    queued_at = NULL
WHERE dag_run_id = sqlc.arg(dag_run_id)
  AND task_id = sqlc.arg(task_id)
  AND state IN ('scheduled', 'queued')
RETURNING dispatch_unavailable_since::timestamptz AS unavailable_since, next_dispatch_at;

-- name: MarkTaskInstanceQueued :execrows
-- The scheduler's scheduled -> queued write after a dispatch was accepted.
-- Guarded to the exact slot the tick planned: still 'scheduled', with the
-- next_dispatch_at the tick read. Under buffered dispatch the worker can finish
-- before this write lands: it may have failed the task (MarkTaskDispatchFailed),
-- re-offered it with a new backoff (RequeueDispatch), or the agent may already
-- have reported running. Each of those moves the row off the planned slot, so
-- this write touches zero rows instead of overwriting the newer outcome.
UPDATE task_instances
SET state = 'queued',
    queued_at = COALESCE(queued_at, now())
WHERE dag_run_id = sqlc.arg(dag_run_id)
  AND task_id = sqlc.arg(task_id)
  AND state = 'scheduled'
  AND next_dispatch_at IS NOT DISTINCT FROM sqlc.narg(expect_next_dispatch_at)::timestamptz;

-- name: RequeueDispatch :execrows
-- A buffered dispatch failed inside the worker for a retriable reason: re-offer
-- the task the way a synchronous failure is re-offered (RecordDispatchFailure,
-- RecordDispatchBackpressure). Back to 'scheduled' with the next attempt held
-- until next_dispatch_at, adding one dispatch attempt only when counted
-- (backpressure is not). Guarded to scheduled/queued, so a task the agent has
-- since reported on is left alone. warm_worker_id is cleared as in
-- RequeueForRedispatch: the attempt never ran. last_heartbeat_at is cleared
-- as on every rail that starts a new execution of the row (ADR 0051
-- amendment, A0). The requeue is a rail, so attempt_epoch is bumped: a late
-- start or report from the abandoned dispatch is fenced (ADR 0051, A1).
-- queued_at is cleared too, so the next queued episode stamps a fresh one:
-- MarkTaskInstanceQueued only stamps a NULL queued_at, and a kept one would
-- make the dispatch-lost reaper fail the re-offered task as soon as it is
-- queued again. dispatch_unavailable_since is cleared when clear_unavailable
-- is set, for a failure the apiserver answered (a rejection, backpressure), as
-- in RecordDispatchFailure. It is kept for a failure that says nothing about
-- the API (a buffer wait past its bound), so a saturated buffer during an
-- outage cannot reset the window. It is not cleared when the row is marked
-- queued either: a buffered row is queued before its create is sent.
UPDATE task_instances
SET state = 'scheduled',
    next_dispatch_at = sqlc.arg(next_dispatch_at),
    dispatch_attempts = dispatch_attempts + sqlc.arg(attempt_increment)::int,
    dispatch_unavailable_since = CASE WHEN sqlc.arg(clear_unavailable)::bool
                                      THEN NULL ELSE dispatch_unavailable_since END,
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = attempt_epoch + 1,
    queued_at = NULL
WHERE dag_run_id = sqlc.arg(dag_run_id)
  AND task_id = sqlc.arg(task_id)
  AND state IN ('scheduled', 'queued');

-- name: DispatchAttemptsForActive :one
-- The consecutive dispatch-failure count of a task still waiting to run
-- (scheduled or queued); no row means it has moved on.
SELECT dispatch_attempts FROM task_instances
WHERE dag_run_id = sqlc.arg(dag_run_id)
  AND task_id = sqlc.arg(task_id)
  AND state IN ('scheduled', 'queued');

-- name: RequeueForRedispatch :execrows
-- Re-place a reclaimed warm assignment (ADR 0058 N1d-c, H2): a warm worker was
-- handed this attempt but demonstrably will NOT run it (its stream ended holding
-- an unacked lease, or it acked started=false), so the attempt sits `queued`
-- having never run. Move it back to `scheduled` so the planner re-admits it and
-- re-dispatches (the planner plans scheduled->queued; it never re-plans a TI
-- that is already `queued`, which is why a no-op reclaim left it stuck until the
-- 3-minute dispatch-lost reaper).
--
-- Guarded to state='queued' and bounded by (dag_run_id, task_id, try_number,
-- attempt_epoch) to the exact attempt the assignment named, so it never disturbs a running or settled
-- TI: zero rows is the guard working, a benign no-op, never an error. It does NOT
-- bump try_number or infra_attempts: the attempt never ran, this is a re-offer of
-- the SAME attempt, and the existing dispatch_attempts/backoff on the re-dispatch
-- bounds a reclaim loop.
--
-- warm_worker_id is CLEARED: the reclaimed worker demonstrably will not run this
-- attempt, so the SAME row must not carry its stale binding back into the next
-- queued/running window — with the busy set now spanning queued+running
-- (ListBusyWarmWorkerPods), a stale binding would falsely mark the OLD (gone)
-- worker busy. This is a same-row re-dispatch (the try_number is preserved), so
-- the clear must happen here; a fresh try lands on a new row that is already NULL.
-- last_heartbeat_at is cleared and attempt_epoch bumped for the reasons given on
-- ResetTaskInstanceToNone.
-- queued_at is cleared for the reason given on RequeueDispatch: the re-dispatch
-- starts a new queued episode, and a kept stamp would let the dispatch-lost
-- reaper fail it as soon as it is queued again (#1604).
UPDATE task_instances
SET state = 'scheduled',
    warm_worker_id = NULL,
    last_heartbeat_at = NULL,
    attempt_epoch = attempt_epoch + 1,
    queued_at = NULL
WHERE dag_run_id = $1 AND task_id = $2 AND try_number = $3
  AND attempt_epoch = COALESCE(sqlc.narg(attempt_epoch)::int, 0)
  AND state = 'queued';

-- name: FailDispatchExhausted :exec
-- The dispatch-attempt budget is spent (ADR 0031 Amendment A): fail the task with
-- a dispatch_failed reason so the run can finalize instead of looping forever.
-- error_message carries the underlying cause. Guarded to 'scheduled' for the same
-- reason as RecordDispatchFailure. This is distinct from dispatch_lost (a TI that
-- reached 'queued' then vanished) and from a task's own 'failed' (the code ran).
-- dispatch_failed is terminal: a dispatch failure never consumes try_number
-- (ADR 0031 Amendment A), so the user's retry budget is not a second dispatch
-- budget. It is spent the way FailDispatchRefused spends it, by lowering
-- max_tries to the current try, so the planner does not move the task to
-- up_for_retry and dispatch it again on a new try. A clear restores the
-- budget from the task, as after a refusal.
UPDATE task_instances
SET state = 'failed', ended_at = now(), error_message = $3,
    next_dispatch_at = NULL,
    max_tries = LEAST(max_tries, try_number)
WHERE dag_run_id = $1 AND task_id = $2 AND state = 'scheduled';

-- name: FailDispatchRefused :exec
-- A dispatch the executor refused (ADR 0066 section 3: the task is larger than
-- its size, or above executor.unit.max_size) fails the task for good. The
-- verdict is permanent, so no retry can change it: the retry budget is spent by
-- lowering max_tries to the current try, which makes the planner's
-- try_number < max_tries check false without counting a try that never ran. A
-- clear restores the budget from the task as usual (#1131), so an operator who
-- fixes the DAG or the unit can run it again. Guarded to the dispatch states:
-- the sync path refuses a scheduled task, the buffered path a scheduled or
-- queued one, and a row that moved on is left alone.
UPDATE task_instances
SET state = 'failed', ended_at = now(), error_message = $3,
    next_dispatch_at = NULL,
    max_tries = LEAST(max_tries, try_number)
WHERE dag_run_id = $1 AND task_id = $2 AND state IN ('scheduled', 'queued');

-- name: ListSettledRunIDs :many
-- Of the given (tenant, run) pairs, the settled runs: run in success or failed
-- and no task instance outside success, failed, skipped, upstream_failed and
-- removed.
-- This is the same "settled" the retention janitor's LockExpiredSettledRuns
-- uses (duplicated there on purpose, keep the two identical). A run marked
-- failed while a task still runs is not settled, so the reconciler never
-- collects a pod whose outcome it may not have recorded yet. The pairs come
-- from the pods' tenant and run labels; a pair whose tenant does not own the
-- run matches nothing.
SELECT r.id, r.tenant_id FROM dag_runs r
WHERE (r.tenant_id, r.id) IN (
    SELECT unnest(sqlc.arg(tenant_ids)::uuid[]), unnest(sqlc.arg(run_ids)::uuid[]))
  AND r.state IN ('success', 'failed')
  AND NOT EXISTS (
    SELECT 1 FROM task_instances ti
    WHERE ti.dag_run_id = r.id
      AND ti.state NOT IN ('success', 'failed', 'skipped', 'upstream_failed', 'removed'));
