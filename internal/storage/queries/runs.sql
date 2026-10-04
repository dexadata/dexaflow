-- name: CreateDagRun :one
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger, note, conf)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetDagRun :one
SELECT * FROM dag_runs WHERE dag_id = $1 AND run_id = $2;

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
WHERE r.dag_id = $1 AND r.run_id = $2;

-- name: ListDagRunsByDagWithVersion :many
-- See GetDagRunWithVersion. One join rather than a lookup per row.
SELECT r.*, v.version AS dag_version_label
FROM dag_runs r
LEFT JOIN dag_versions v ON v.id = r.dag_version_id
WHERE r.dag_id = $1
ORDER BY r.logical_date DESC
LIMIT $2 OFFSET $3;

-- name: DeleteDagRun :execrows
-- Removes one run; its task_instances and XCom rows cascade (ON DELETE CASCADE).
DELETE FROM dag_runs WHERE dag_id = $1 AND run_id = $2;

-- name: ListDagRunsByDag :many
SELECT * FROM dag_runs
WHERE dag_id = $1
ORDER BY logical_date DESC
LIMIT $2 OFFSET $3;

-- name: CountDagRunsByDag :one
SELECT count(*) FROM dag_runs WHERE dag_id = $1;

-- name: CountActiveDagRunsByDagID :one
-- Counts queued+running runs for a single DAG; used by the manual-trigger
-- path to enforce max_active_runs (#200) before insert.
SELECT count(*) FROM dag_runs
WHERE dag_id = $1 AND state IN ('queued', 'running');

-- name: ListStaleQueuedTaskInstances :many
-- Lists every TI currently in `queued` alongside its queued_at timestamp for
-- the dispatch-lost reaper (#202). The reaper applies the threshold per
-- candidate so the SQL stays simple and the decision is purely in Go. The
-- LIMIT bounds a tick's reap work after a long outage; the rest are picked
-- up next tick (backstop, not sprint).
-- warm_worker_id rides along (ADR 0058 N1d-a2, review finding H3): a queued warm
-- attempt whose serving warm worker is still alive is just slow to transition
-- queued->running, so the dispatch-lost reaper must DEFER it rather than fail it
-- (the double-run bug). It is NULL for a dedicated attempt and for a warm attempt
-- not yet acked, in which case the reaper falls back to its existing pod-liveness
-- gate unchanged.
SELECT ti.id AS task_instance_id,
       ti.dag_run_id,
       d.dag_id AS dag_id_text,
       ti.task_id,
       ti.try_number,
       ti.queued_at,
       ti.warm_worker_id
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE ti.state = 'queued'
ORDER BY ti.queued_at NULLS LAST
LIMIT 100;

-- name: MarkTaskDispatchLost :exec
-- Fails one queued TI with a dispatch_lost error. The WHERE state='queued'
-- guard makes the operation idempotent: a second call on a TI that has
-- since transitioned (real dispatch landed, or already failed) is a no-op,
-- never overwriting a more meaningful state.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    error_message = 'dispatch_lost: scheduler crashed before dispatch landed; will be retried by the run reaper'
WHERE id = $1 AND state = 'queued';

-- name: ListActiveDagRuns :many
SELECT * FROM dag_runs
WHERE state IN ('queued', 'running')
ORDER BY queued_at;

-- name: UpdateDagRunState :one
UPDATE dag_runs
SET state = $2, started_at = $3, ended_at = $4
WHERE id = $1
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
WHERE id = $1;

-- name: ResetDagRunToVersion :exec
-- Clear with run_on_latest_version re-binds the run to the DAG's current
-- registered version (ADR 0020; opt-in since the 2026-09-15 amendment): a
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
WHERE id = $1;

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

-- name: StampDagRunState :exec
-- Transitions a run's state and stamps the run's own timestamps so the UI can
-- show its duration: started_at on first entry into 'running', ended_at on a
-- terminal state. Other timestamps are preserved (the scheduler may re-run).
UPDATE dag_runs
SET state = sqlc.arg(state)::dag_run_state,
    started_at = CASE WHEN sqlc.arg(state)::dag_run_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    ended_at = CASE WHEN sqlc.arg(state)::dag_run_state IN ('success', 'failed') THEN now() ELSE ended_at END
WHERE id = sqlc.arg(id);

-- name: CreateTaskInstance :one
-- try_number starts at 1 to match Airflow (1-based attempts): the first run's
-- logs live at .../1.log, which is where the UI's log view looks. Retries bump
-- it via ResetForRetry.
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, operator, max_tries, state, pool, try_number)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1)
RETURNING *;

-- name: CreateTaskInstances :copyfrom
-- Batched form of CreateTaskInstance: materializes every task of one run in a
-- single COPY instead of T INSERT round-trips. The caller supplies one param row
-- per task with try_number pinned to 1 (matching CreateTaskInstance's literal)
-- and pool carried through so cross-DAG pool occupancy is attributed correctly;
-- columns omitted from the list take their table defaults, so the rows are
-- byte-identical to the loop — only the statement count changes (T INSERTs → 1 COPY).
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, operator, max_tries, state, pool, try_number)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListTaskInstancesByRun :many
SELECT * FROM task_instances
WHERE dag_run_id = $1
ORDER BY task_id;

-- name: ListTaskInstanceAttempts :many
-- Returns every attempt for (run, task), oldest first. UNIONs the current
-- task_instances row with all archived task_instance_history rows so the UI's
-- /tries endpoint can render one navigable tab per attempt (Lima bug #241).
-- Each row carries the per-attempt fields PLUS the run-constant fields
-- (operator, max_tries, map_index) copied from the current TI; archived rows
-- get them via the JOIN.
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
    h.note
FROM task_instance_history h
JOIN task_instances ti ON ti.id = h.task_instance_id
WHERE ti.dag_run_id = $1 AND ti.task_id = $2
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
    ti.note
FROM task_instances ti
WHERE ti.dag_run_id = $1 AND ti.task_id = $2
ORDER BY try_number;

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

-- name: CreateScheduledRunByDagID :exec
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger)
SELECT d.tenant_id, d.id, d.current_version_id, sqlc.arg(run_id), sqlc.arg(logical_date), 'queued', 'scheduled'
FROM dags d
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.dag_id = sqlc.arg(dag_id) AND d.current_version_id IS NOT NULL
ON CONFLICT (dag_id, run_id) DO NOTHING;

-- name: GetDagVersionByID :one
SELECT * FROM dag_versions WHERE id = $1;

-- name: GetDagRunByID :one
SELECT * FROM dag_runs WHERE id = $1;

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

-- name: UpdateTaskInstanceStatesByRunTasks :exec
-- Batched form of UpdateTaskInstanceStateByRunTask: applies ONE target state to
-- every listed task of a run in a single statement. The per-row stamping is
-- identical (scheduled_at/queued_at/started_at set on first entry only), as is
-- the task_state cast, so a batch of R same-target transitions is byte-identical
-- to R single-row updates — only the statement count changes (R UPDATEs → 1 per
-- distinct target state). The scheduler groups a tick's plain state-set
-- transitions (scheduled/skipped/upstream_failed/up_for_retry) by target state
-- and flushes each group through this query. task_id = ANY keeps every row's CASE
-- self-referential, so an already-stamped row is never re-stamped.
UPDATE task_instances
SET state = sqlc.arg(state)::task_state,
    scheduled_at = CASE WHEN sqlc.arg(state)::task_state = 'scheduled' AND scheduled_at IS NULL THEN now() ELSE scheduled_at END,
    queued_at = CASE WHEN sqlc.arg(state)::task_state = 'queued' AND queued_at IS NULL THEN now() ELSE queued_at END,
    started_at = CASE WHEN sqlc.arg(state)::task_state = 'running' AND started_at IS NULL THEN now() ELSE started_at END
WHERE dag_run_id = sqlc.arg(dag_run_id) AND task_id = ANY(sqlc.arg(task_ids)::text[]);

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
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note
    FROM task_instances src
    WHERE src.dag_run_id = $1 AND src.task_id = $2
    ON CONFLICT (task_instance_id, try_number) DO NOTHING
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
    reschedule_at = NULL,
    first_reschedule_at = NULL,
    last_failure_kind = NULL,
    warm_worker_id = NULL,
    try_number = ti.try_number + 1
WHERE ti.dag_run_id = $1 AND ti.task_id = $2;

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
        exit_code, error_message, hostname, pod_name, node_name, note
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note
    FROM task_instances src
    WHERE src.dag_run_id = $1 AND src.task_id = $2 AND src.state = 'up_for_retry'
    ON CONFLICT (task_instance_id, try_number) DO NOTHING
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
    reschedule_at = NULL,
    first_reschedule_at = NULL,
    last_failure_kind = NULL,
    warm_worker_id = NULL,
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
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note
    FROM task_instances src
    WHERE src.dag_run_id = $1 AND src.task_id = $2
      AND src.state = 'failed' AND src.last_failure_kind = 'infra'
    ON CONFLICT (task_instance_id, try_number) DO NOTHING
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
    reschedule_at = NULL,
    first_reschedule_at = NULL,
    last_failure_kind = NULL,
    warm_worker_id = NULL,
    infra_attempts = ti.infra_attempts + 1
WHERE ti.dag_run_id = $1 AND ti.task_id = $2
  AND ti.state = 'failed' AND ti.last_failure_kind = 'infra';

-- name: RedispatchRescheduledTaskInstance :exec
-- Re-dispatch a task parked in up_for_reschedule once its reschedule_at has passed:
-- back to 'none' with the per-attempt timestamps cleared (so the next dispatch
-- stamps fresh — queued_at MUST be NULL or the dispatch-lost reaper re-flags it)
-- and reschedule_at cleared. Unlike ResetTaskInstanceToNone (retry), try_number is
-- PRESERVED and no task_instance_history row is archived: reschedule is not a retry,
-- it consumes no attempt (#380). Guarded to the parked state so it is idempotent.
UPDATE task_instances
SET state = 'none',
    released_at = now(),
    started_at = NULL,
    ended_at = NULL,
    queued_at = NULL,
    scheduled_at = NULL,
    reschedule_at = NULL,
    last_failure_kind = NULL,
    warm_worker_id = NULL
WHERE dag_run_id = $1 AND task_id = $2 AND state = 'up_for_reschedule';

-- name: TaskInstanceFirstRescheduleAt :one
-- The time a reschedule-mode sensor first entered reschedule (NULL until it does).
-- Delivered to each re-dispatched pod so get_first_reschedule_date returns the real
-- value and the sensor honors its cumulative timeout across pokes (#380).
SELECT first_reschedule_at FROM task_instances
WHERE dag_run_id = $1 AND task_id = $2;

-- name: FailTaskInstanceIfActive :exec
-- Settle a task instance failed from the pod reconciler, guarded by BOTH id and
-- try_number (ADR 0052): try_number bumps IN PLACE on retry (same row id), so a
-- stale reconciler acting on a previous attempt's lingering pod must not match the
-- new running attempt and clobber it. The active-state guard prevents clobbering a
-- terminal row.
UPDATE task_instances
SET state = 'failed', ended_at = now(), error_message = sqlc.arg(error_message)
WHERE id = sqlc.arg(id) AND try_number = sqlc.arg(try_number)
  AND state IN ('scheduled', 'queued', 'running');

-- name: SucceedTaskInstanceIfActive :exec
-- Settle a task instance succeeded from its durable outcome record (ADR 0052),
-- recovering a success whose report was lost. Guarded by id AND try_number so a
-- stale reconciler never marks a LIVE retry succeeded — which would fire downstream
-- tasks on incomplete work, strictly worse than the bug being fixed. The
-- active-state guard prevents clobbering a terminal row.
UPDATE task_instances
SET state = 'success', ended_at = now(), error_message = NULL
WHERE id = sqlc.arg(id) AND try_number = sqlc.arg(try_number)
  AND state IN ('scheduled', 'queued', 'running');

-- name: RescheduleTaskInstanceByIDIfActive :exec
-- Settle a lost reschedule from the durable outcome record (ADR 0052): park the TI
-- in up_for_reschedule with the record's next-poke time, guarded by id AND
-- try_number (never clobber a different attempt or a terminal row), consuming no
-- retry budget. Mirrors RescheduleTaskInstance but keyed by id, for the reconciler.
UPDATE task_instances
SET state = 'up_for_reschedule'::task_state,
    reschedule_at = sqlc.arg(reschedule_at),
    first_reschedule_at = COALESCE(first_reschedule_at, now())
WHERE id = sqlc.arg(id) AND try_number = sqlc.arg(try_number)
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
  AND try_number = sqlc.arg(try_number)
  AND state IN ('none', 'scheduled', 'queued', 'running');

-- name: RescheduleTaskInstance :exec
-- A reschedule-mode sensor (mode='reschedule') poked not-ready: park the active TI
-- in up_for_reschedule with its next-poke time ($3) so the scheduler re-dispatches
-- it once reschedule_at passes (#380), without consuming retry budget. Guarded to
-- the active states so a late report never clobbers a terminal row. ended_at is
-- left untouched (the task is not finished); started_at is preserved.
UPDATE task_instances
SET state = 'up_for_reschedule'::task_state,
    reschedule_at = $3,
    -- Stamp the FIRST reschedule once and preserve it across re-dispatches so the
    -- delivered get_first_reschedule_date lets the sensor honor cumulative timeout.
    first_reschedule_at = COALESCE(first_reschedule_at, now())
WHERE dag_run_id = $1 AND task_id = $2
  AND state IN ('running', 'queued', 'scheduled');

-- name: ResolveRunRef :one
SELECT t.id AS tenant_id, dr.id AS dag_run_id
FROM dag_runs dr
JOIN dags d ON d.id = dr.dag_id
JOIN tenants t ON t.id = d.tenant_id
WHERE t.name = $1 AND d.dag_id = $2 AND dr.run_id = $3;

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
SELECT lr.state AS state, count(*) AS n
FROM (
    SELECT DISTINCT ON (r.dag_id) r.state
    FROM dag_runs r
    JOIN dags d ON d.id = r.dag_id
    WHERE d.tenant_id = $1
    ORDER BY r.dag_id, r.logical_date DESC
) lr
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
        exit_code, error_message, hostname, pod_name, node_name, note
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note
    FROM task_instances src
    WHERE src.dag_run_id = $1 AND src.task_id = $2
      AND src.state IN ('failed', 'upstream_failed', 'up_for_retry')
    ON CONFLICT (task_instance_id, try_number) DO NOTHING
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
    last_failure_kind = NULL,
    warm_worker_id = NULL,
    try_number = ti.try_number + 1
WHERE ti.dag_run_id = $1 AND ti.task_id = $2
  AND ti.state IN ('failed', 'upstream_failed', 'up_for_retry');

-- name: ResetAllFailedTaskInstances :execrows
-- Archives every failed attempt in the run into task_instance_history then
-- resets. See ResetTaskInstanceToNone for the per-attempt rationale.
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note
    FROM task_instances src
    WHERE src.dag_run_id = $1
      AND src.state IN ('failed', 'upstream_failed', 'up_for_retry')
    ON CONFLICT (task_instance_id, try_number) DO NOTHING
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
    last_failure_kind = NULL,
    warm_worker_id = NULL,
    try_number = ti.try_number + 1
WHERE ti.dag_run_id = $1
  AND ti.state IN ('failed', 'upstream_failed', 'up_for_retry');

-- name: SetTaskInstanceNote :exec
UPDATE task_instances
SET note = $3
WHERE dag_run_id = $1 AND task_id = $2;

-- name: ListOrphanCandidates :many
-- Lists dag_runs currently in 'running' whose task instances are ALL terminal
-- or never-started (every TI in none/success/failed/skipped/upstream_failed),
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
        AND ti2.state NOT IN ('none', 'success', 'failed', 'skipped', 'upstream_failed')
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
UPDATE task_instances
SET last_heartbeat_at = now()
WHERE dag_run_id = $1
  AND task_id = $2
  AND try_number = $3
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
SELECT EXISTS (
    SELECT 1 FROM task_instances
    WHERE dag_run_id = $1
      AND task_id = $2
      AND try_number = $3
      AND state IN ('queued', 'running')
);

-- name: BindWarmAttempt :execrows
-- Records the durable warm-attempt binding (ADR 0058 N1d-a1): the warm worker
-- pod ($4, its own downward-API pod name) that acked THIS attempt (dag_run_id,
-- task_id, try_number) as started. A later failover reaper matches warm_worker_id
-- against the live warm-pod set to find attempts a dead warm pod held.
--
-- Guarded on state IN ('queued', 'running') — the same active predicate the
-- heartbeat and liveness queries use — so a settled attempt is never bound: an
-- ack that races a reaper settling the row must not stamp a worker onto a
-- terminal TI. Bounded by (dag_run_id, task_id, try_number) to match exactly the
-- attempt the assignment named. Returns the affected row count; zero means the
-- attempt already moved on (terminal or superseded), and the caller treats that
-- as a benign no-op, never an error.
UPDATE task_instances
SET warm_worker_id = $4
WHERE dag_run_id = $1
  AND task_id = $2
  AND try_number = $3
  AND state IN ('queued', 'running');

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
SELECT ti.id AS task_instance_id,
       ti.dag_run_id AS dag_run_id,
       ti.task_id AS task_id,
       ti.try_number AS try_number,
       ti.warm_worker_id AS warm_worker_id
FROM task_instances ti
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
-- BindWarmAttempt guard binds exactly the queued/running window, and every
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
       ti.last_heartbeat_at AS last_heartbeat_at
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
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    error_message = $3
WHERE dag_run_id = $1
  AND task_id = $2
  AND state IN ('scheduled', 'queued');

-- name: MarkTaskAgentLost :execrows
-- Fails a TI whose agent went silent. The WHERE state='running' guard
-- prevents overwriting a TI the agent's last terminal report finally
-- delivered between our list and our write (defense in depth — a late
-- report wins over the reaper). Idempotent on a second call.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    error_message = 'agent_lost: no heartbeat within the threshold — see #128'
WHERE id = $1 AND state = 'running';

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
-- heartbeat, which the agent-lost query never lists (#916). The
-- LIMIT bounds a single tick's reap work even after a large outage; the rest
-- are picked up next tick.
SELECT ti.id AS task_instance_id,
       ti.tenant_id AS tenant_id,
       ti.dag_run_id AS dag_run_id,
       d.dag_id AS dag_id_text,
       ti.task_id AS task_id,
       ti.try_number AS try_number,
       ti.started_at AS started_at,
       (ti.last_heartbeat_at IS NOT NULL)::boolean AS heartbeated
FROM task_instances ti
JOIN dag_runs dr ON dr.id = ti.dag_run_id
JOIN dags d ON d.id = dr.dag_id
WHERE ti.state = 'running'
  AND ti.warm_worker_id IS NULL
  AND ti.started_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::float8)
ORDER BY ti.started_at
LIMIT 100;

-- name: MarkTaskPodLost :execrows
-- Fails a running TI whose pod has vanished (deleted/evicted/node lost). The
-- WHERE state='running' guard makes it idempotent and prevents overwriting a
-- late terminal report that landed between our list and our write (a live
-- report wins over the reaper). Idempotent on a second call.
UPDATE task_instances
SET state = 'failed',
    ended_at = now(),
    last_failure_kind = 'infra',
    error_message = 'pod_lost: the task pod vanished with no live pod past the grace period — see #527'
WHERE id = $1 AND state = 'running';

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
        AND ti2.state NOT IN ('none', 'success', 'failed', 'skipped', 'upstream_failed')
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
UPDATE task_instances
SET dispatch_attempts = dispatch_attempts + 1,
    next_dispatch_at = $3
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
-- failure.
UPDATE task_instances
SET next_dispatch_at = $3
WHERE dag_run_id = $1 AND task_id = $2 AND state = 'scheduled';

-- name: RequeueForRedispatch :execrows
-- Re-place a reclaimed warm assignment (ADR 0058 N1d-c, H2): a warm worker was
-- handed this attempt but demonstrably will NOT run it (its stream ended holding
-- an unacked lease, or it acked started=false), so the attempt sits `queued`
-- having never run. Move it back to `scheduled` so the planner re-admits it and
-- re-dispatches (the planner plans scheduled->queued; it never re-plans a TI
-- that is already `queued`, which is why a no-op reclaim left it stuck until the
-- 3-minute dispatch-lost reaper).
--
-- Guarded to state='queued' — bounded by (dag_run_id, task_id, try_number) to the
-- exact attempt the assignment named — so it never disturbs a running or settled
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
UPDATE task_instances
SET state = 'scheduled',
    warm_worker_id = NULL
WHERE dag_run_id = $1 AND task_id = $2 AND try_number = $3 AND state = 'queued';

-- name: FailDispatchExhausted :exec
-- The dispatch-attempt budget is spent (ADR 0031 Amendment A): fail the task with
-- a dispatch_failed reason so the run can finalize instead of looping forever.
-- error_message carries the underlying cause. Guarded to 'scheduled' for the same
-- reason as RecordDispatchFailure. This is distinct from dispatch_lost (a TI that
-- reached 'queued' then vanished) and from a task's own 'failed' (the code ran).
UPDATE task_instances
SET state = 'failed', ended_at = now(), error_message = $3,
    next_dispatch_at = NULL
WHERE dag_run_id = $1 AND task_id = $2 AND state = 'scheduled';
