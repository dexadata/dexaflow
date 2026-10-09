---
title: "ADR 0064: Airflow 3 branching and skip: in-pod decision, scheduler-side skip cascade"
linkTitle: "0064 · Branching and skip: in-pod decision, scheduler-side skip cascade"
weight: 640
description: "ADR 0064: How @task.branch, BranchPythonOperator and ShortCircuitOperator report their decision to the control plane, how the Go scheduler cascades skipped with Airflow 3 trigger-rule semantics, and what stays a loud reject."
---

**Status:** Accepted
**Date:** 2026-10-04 (proposed and accepted the same day by the project owner)
**Decided at acceptance:** (a) trigger-rule parity with Airflow ships in the
next minor release, with a **Changed** changelog entry and an upgrade note
(D2); (b) the two-release phased rollout of branching is accepted, and
upgrading across both releases in a single rolling jump is documented as
unsupported (D3, Phased path).
**Relates:** ADR 0036 (Airflow runtime compatibility shim: one model, one policy seam), ADR 0040 (Airflow operator support; branching was parked as "Phase D"), ADR 0043 (TaskGroup; the generic construct is still rejected), ADR 0048 (no user code in the control plane), ADR 0051 (orchestration vs execution state machines), ADR 0052 (durable task outcome), ADR 0058 (warm worker pools, in-band outcome)
**Issues:** #787 (this ADR), #225 (the compile-time reject this ADR eventually retires)

> **Numbering note.** 0062 is held by open PR #1307 and 0063 by open PR #1365. If
> either merges in a different order, or another ADR lands first, this file is
> renumbered before merge; nothing in it depends on the number.

## Context

Airflow 3 branching lets a task decide at run time which of its downstream tasks
run. `@task.branch` and `BranchPythonOperator` return one task id, a list of ids,
or `None`; every *direct* downstream task that was not chosen is set to
`skipped`, and the skip then flows further through trigger rules.
`ShortCircuitOperator` (and `@task.short_circuit`) evaluates a condition and, when
it is falsy, skips its downstream. A post-branch join conventionally uses
`trigger_rule="none_failed_min_one_success"` so that it runs after whichever
branch ran.

Dexaflow refuses all of this at compile time today, on purpose:

- `parser/leoflow_parser/compiler.py:520-539` raises for any operator class whose
  name contains `Branch` or `ShortCircuit` (#225). Before that gate existed, the
  substring match on `Python` translated `BranchPythonOperator` into a plain
  `python` task, so every "skipped" branch actually *executed*. A loud reject is
  the correct behavior until real support exists.
- The shim gives `@task.branch` a `Branch`-named class precisely so it hits that
  gate (`parser/leoflow_parser/_shim/airflow/sdk/__init__.py:24-29`, `:77-103`).
  `BranchPythonOperator` and `ShortCircuitOperator` are not exported by the
  shim's `airflow.providers.standard.operators.python`
  (`.../_shim/airflow/providers/standard/operators/python.py:1-5`), so importing
  them fails as an `ImportError` and is reported as unsupported
  (`compiler.py:196-197`).
- Only five trigger rules compile (`compiler.py:24-30`, enforced at `:567-571`),
  mirrored by the Go enum (`internal/domain/dag.go:42-57`) and the schema enum
  (`docs/api/dag-schema.json:218-227`; `internal/domain/schemas/dag-schema.json`
  is an identical embedded copy).

What already exists, and what does not:

- **The `skipped` state exists.** `domain.TaskStateSkipped`
  (`internal/domain/state.go:21-22`) is terminal (`state.go:35-42`), the
  database enum carries it, the planner already emits `none -> skipped`
  (`internal/scheduler/plan.go:294-309`, legal per
  `internal/scheduler/state_machine.go:24`), `FinalizeRun` counts it as a
  non-failure (`plan.go:367-395`), the agent protocol has
  `TASK_STATE_SKIPPED` (`proto/agent.proto:208-218`) mapped by
  `internal/agentrpc/server.go:700-713`, the Airflow compatible API renders it
  (`internal/api/ui_dashboard.go:27-44`), and "mark skipped" is already accepted
  (`internal/api/resources.go:866-868`, `:898-900`).
- **A skip cascade exists, partially.** `all_success` already turns a skipped
  upstream into `skipped` (`state_machine.go:113-124`), so a skip set on a
  branch's direct child already propagates down an `all_success` chain.
- **Edges are static.** `TaskSpec.DependsOn` (`internal/domain/dag.go:163`) is
  the only edge; `PlanRun` builds its upstream map from it (`plan.go:28-31`).
  There is no notion of a task whose success *selects* among its children.
- **There is no decision channel.** A task reports only a terminal state, exit
  code and error message (`proto/agent.proto:198-206`, persisted by
  `internal/storage/agent_store.go:151-178` through the attempt-guarded
  `ReportTaskResult`, `internal/storage/queries/runs.sql:506`). Its return value
  travels separately as an XCom (`internal/agent/runner.go:651-664`), and the
  XCom backend may be Redis (`internal/xcom/redis_backend.go`), so XCom is not
  transactional with `task_instances`.
- **The trigger-rule evaluator diverges from Airflow in ways that matter once
  skip is common.** `EvaluateTriggerRule` turns any `upstream_failed` upstream
  into `upstream_failed` for every rule except `all_done`
  (`state_machine.go:92-96`), which is wrong for `all_failed`, `one_failed`,
  `one_success`, `none_skipped` and `always`. `one_success` / `one_failed` wait
  for *every* upstream before firing and skip (rather than `upstream_failed`)
  when no upstream succeeded (`state_machine.go:147-156`).

### Airflow 3 semantics, verified against source

The rules below were read from the apache/airflow **3.2.0** tag (Dexaflow targets
Airflow 3.2.x API compatibility), not from memory:

- `airflow-core/src/airflow/ti_deps/deps/trigger_rule_dep.py` (state changes at
  lines 389-446, readiness at 465-600, `ALWAYS` short-circuit at 114-116).
- `airflow-core/src/airflow/ti_deps/deps/not_previously_skipped_dep.py`
  (re-skip of a cleared child from the parent's recorded decision).
- `providers/standard/src/airflow/providers/standard/utils/skipmixin.py`
  (`SkipMixin.skip_all_except`, `XCOM_SKIPMIXIN_KEY = "skipmixin_key"`).
- `providers/standard/src/airflow/providers/standard/operators/branch.py`
  (`BranchMixIn.do_branch`, `BaseBranchOperator`).
- `providers/standard/src/airflow/providers/standard/operators/python.py`
  (`BranchPythonOperator`, `ShortCircuitOperator`, the virtualenv / external
  Python branch variants).
- `task-sdk/src/airflow/sdk/execution_time/task_runner.py:1287-1291` and
  `airflow-core/src/airflow/api_fastapi/execution_api/routes/task_instances.py:614-670`
  (`DownstreamTasksSkipped` becomes a `SkipDownstreamTasks` message; the API
  server skips only rows not already `running`, `success` or `failed`).

The facts this ADR relies on:

1. **The decision is made in the task process.** `skip_all_except` normalises the
   callable's result (a `str`, an iterable of `str`, or `None`), rejects
   non-string members and ids that are **not task ids of the DAG** (any task, not
   only downstream ones), then computes the follow set.
2. **Only direct downstream tasks are skipped by the branch itself.** The follow
   set is the chosen ids plus *all* of their transitive downstream; each direct
   child not in it is skipped. That is the "empty branch" rule: in
   `branch -> join` plus `branch -> task1 -> join`, choosing `task1` does not skip
   `join`, because `join` is downstream of `task1`. Everything further down is
   reached by trigger rules, not by the branch.
3. **`None` skips every direct child.** The return value is still pushed as the
   task's `return_value`. When the branch task has downstream tasks,
   `{"followed": [...]}` is pushed under `skipmixin_key`, where `followed` is the
   list of **direct children** inside the follow set (not the raw chosen ids).
4. **The branch task itself ends `success`.** Skipping is a side effect of a
   successful run, not a state of the branch task.
5. **Clear re-applies the decision.** `NotPreviouslySkippedDep` re-skips a cleared
   child whose SkipMixin parent already finished and did not follow it. It looks
   only at **direct** parents, and it is a separate dependency from
   `TriggerRuleDep`, so it applies to every trigger rule, `always` included.
6. **`ShortCircuitOperator`** on a falsy condition skips all transitive
   downstream when `ignore_downstream_trigger_rules=True` (the default), or only
   direct children when `False` (letting trigger rules decide below). A truthy
   condition skips nothing. A falsy condition pushes `{"skipped": [...]}` (not
   `followed`) under `skipmixin_key`, and only when the task has downstream tasks.

## Decision

**The branch decision is made in the task pod, reported atomically with the
task's terminal success, persisted on the task instance row, and applied by the
Go planner as a pure function of the pinned DAG spec and that row. The trigger
rule evaluator is aligned with Airflow 3.2 for every rule Dexaflow accepts. The
first slice ships `@task.branch`, `BranchPythonOperator`, `@task.short_circuit`
and `ShortCircuitOperator`; everything else that skips stays a loud reject.**

### D1: The runtime contract for the in-pod decision

**Where the decision is made.** In the pod, by the runtime, exactly where Airflow
makes it. The control plane never runs the callable (ADR 0048).

1. The runtime runs the branch callable like any python task
   (`runtime/python/leoflow_runtime/runner.py`, the `fn(**kwargs)` call and
   `_write_return` at `:177-191`). The return value is still written as the
   `return_value` XCom, unchanged (Airflow parity: `do_branch` returns it).
2. The runtime normalises the result with Airflow's rules: a `str` becomes a
   one-element list, an iterable of `str` becomes a sorted, de-duplicated list,
   `None` becomes `[]`; anything else, or a non-`str` member, raises (the task
   fails, consuming a retry, as in Airflow). It then checks every id against the
   DAG's task ids, which the agent passes in `LEOFLOW_BRANCH_TASK_IDS`, so the pod
   does not need to re-import `dag.py`. The agent's `TaskSpec` message
   (`proto/agent.proto:117-143`) carries neither the DAG's task ids nor the
   task's direct children today, so `GetTaskSpec` gains two fields filled by the
   control plane from the pinned spec: `repeated string dag_task_ids` and
   `repeated string downstream_task_ids` (the latter for the warning below and
   for building `followed`). An unknown id
   raises with Airflow's message ("'branch_task_ids' must contain only valid
   task_ids"). A valid id that is **not** a direct child is accepted, as in
   Airflow, and logged as a warning in the task log, because it can never select
   anything.
   For a short-circuit task the runtime instead records `truthy` / `falsy` of the
   result.
3. The runtime writes the normalised decision to a file named by
   `LEOFLOW_BRANCH_PATH`, the same file-based handoff already used for the return
   value and for reschedule (`RESCHEDULE_PATH_ENV`, `runner.py:252`).
4. The agent reads that file in the success path (`internal/agent/runner.go:651-664`)
   and carries it **inside the terminal report**: `ReportStateRequest` gains

   ```proto
   // Set only by a branch / short-circuit task on TASK_STATE_SUCCESS.
   BranchDecision branch = 6;

   message BranchDecision {
       repeated string follow_task_ids = 1;  // branch: chosen ids; may be empty (None)
       bool short_circuit_falsy = 2;          // short-circuit: condition was falsy
   }
   ```

   A proto3 message field has presence, so "no decision" and "skip everything"
   (`follow_task_ids` empty) are distinguishable on the wire.
5. The agent also pushes `skipmixin_key` as a custom XCom so the XCom tab matches
   Airflow: `{"followed": [...]}` (the direct children in the follow set) for a
   branch task, `{"skipped": [...]}` for a falsy short-circuit, and nothing when
   the task has no downstream tasks or the short-circuit condition was truthy
   (facts 3 and 6). That XCom is **display only**; nothing reads it back.

**Why the terminal report and not a reserved XCom.** The decision must become
true in the same write that makes the branch task `success`, or there is a
window where the parent is `success`, the decision is missing, and the planner
would have to guess. `ReportTaskResult` is already guarded on state **and**
`try_number` (`runs.sql:506-531`), so a late report from a superseded attempt
can never land a stale decision on a newer attempt. An XCom write cannot join
that guard: it is a separate RPC, and with the Redis backend it is not even the
same datastore. The XCom route was considered and rejected (see Alternatives).

**Persistence.** A new nullable column `task_instances.branch_decision jsonb`
(next free migration number at implementation time; several open PRs also add
migrations, so expect a renumber). `ReportTaskResult` sets it from the request
on `success` and leaves it `NULL` otherwise. `ResetTaskInstanceToNone`
(`runs.sql:323`) clears it and archives it into `task_instance_history` with the
rest of the attempt, so the tries view can show which branch each attempt took.

**Server-side validation (authoritative).** The pod runs tenant code and is not
trusted to have validated. In `ExecutionStore.ReportState`
(`internal/storage/agent_store.go:151`) the control plane re-checks the
decision against the task spec of the run's pinned version:

| Report | Task has `branch` in spec | Outcome |
|---|---|---|
| `success` + decision | yes | validate ids against the DAG's task ids; persist; `success` |
| `success` + decision with an unknown id | yes | settle `failed`, reason `invalid_branch_decision` (a classified reason, not raw text) |
| `success`, no decision | yes | settle `failed`, reason `branch_decision_missing` |
| any + decision | no | ignore the decision, log a warning, settle as reported |

Failing closed is the point: **a branch task can never reach `success` through
the agent path without a persisted decision**, because "success, no decision"
would otherwise run every branch, which is exactly the #225 mistranslation.

### D2: Skip cascade in the Go scheduler

The planner applies the decision; nothing writes `skipped` at report time. This
keeps `PlanRun` the single place that moves `none` tasks
(`plan.go:56-64`), makes the behavior a deterministic pure function that is unit
testable without a database, and gives clear-task the right behavior for free
(D6).

**Branch filter (the `NotPreviouslySkippedDep` analog).** `RunState` gains
`BranchDecision map[string]*domain.BranchDecision`, loaded with the other
per-task maps. In `decideStart` (`plan.go:294-309`), *before* the trigger rule
is evaluated, a `none` task `T` is set to `skipped` when any direct upstream `P`:

- has `branch` in its spec, **and**
- is `success` with a non-`NULL` decision, **and**
- for kind `branch`: `T` is not in `follow(P)`, where `follow(P)` is the chosen
  ids plus every transitive downstream of a chosen id (Airflow's empty-branch
  rule, fact 2 above), computed over the run's `Tasks`;
- for kind `short_circuit` with `ignore_downstream_trigger_rules=false`: the
  condition was falsy.

For kind `short_circuit` with `ignore_downstream_trigger_rules=true` and a falsy
condition, `T` is skipped when `P` is **any ancestor**, not only a direct parent,
which is Airflow's "all flat relatives" behavior (Dexaflow has no teardown tasks,
so Airflow's teardown exclusion has nothing to exclude yet). On the first run
this matches Airflow exactly. It is slightly stricter after a clear: Airflow's
`NotPreviouslySkippedDep` checks only direct parents, so a cleared
*grandchild* of a falsy short-circuit is re-evaluated by its trigger rule
there (and runs under, say, `all_done`), while this filter re-skips it. The
stricter reading is kept on purpose (it never runs work the short-circuit
meant to stop) and is documented.

The filter applies to every `none` task, whatever its trigger rule, `always`
included, because `NotPreviouslySkippedDep` is a separate dependency that
Airflow evaluates for `always` tasks too. Airflow's skip endpoint also
overwrites `scheduled` and `queued` rows (it spares only `running`,
`success` and `failed`); this filter touches only `none` rows, so a child
that a non-`all_success` rule already promoted when the parent decides is
not pulled back. That is a race either way and is documented, not modelled.

A `success` branch parent with a `NULL` decision imposes no filter. The only way
to get there is an operator's explicit "mark success" in the UI, and that is
also Airflow's behavior (no `skipmixin_key` XCom, so `NotPreviouslySkippedDep`
does nothing). The reconciler path is closed separately (D6).

**Trigger rules, aligned with Airflow 3.2.** Notation: `S` success, `F` failed,
`UF` upstream_failed, `K` skipped, `A` still active (none / scheduled / queued /
running / up_for_retry / up_for_reschedule, plus a failure that can still retry
or re-place, which `PlanRun` already folds into `up_for_retry` at
`plan.go:165-224`), `N` the number of upstreams. A rule is checked top to bottom;
the first matching row wins.

| Rule | Becomes `upstream_failed` | Becomes `skipped` | Waits | Runs |
|---|---|---|---|---|
| `all_success` (default) | `F+UF > 0` | `K > 0` | `A > 0` | otherwise |
| `all_failed` | never | `S+K > 0` | `A > 0` | otherwise (every upstream `F` or `UF`) |
| `all_done` | never | never | `A > 0` | otherwise |
| `one_success` | `A = 0` and `S = 0` and `K < N` | `A = 0` and `K = N` | `S = 0` and `A > 0` | `S > 0` (immediately) |
| `one_failed` | never | `A = 0` and `F+UF = 0` | `F+UF = 0` and `A > 0` | `F+UF > 0` (immediately) |
| `none_failed` | `F+UF > 0` | never | `A > 0` | otherwise (all `S` or `K`, including all `K`) |
| `none_failed_min_one_success` | `F+UF > 0` | `K = N` | `A > 0` | otherwise (at least one `S`) |
| `none_skipped` | never | `K > 0` | `A > 0` | otherwise (even if upstreams failed) |
| `always` | never | never | never | on the first tick, regardless of upstreams |

Each row is the 3.2.0 `trigger_rule_dep.py` logic restated; the
`upstream_failed` and `skipped` columns come from its `flag_upstream_failed`
block (lines 389-427) and the waits / runs columns from its readiness checks
(lines 465-560). Consequences for the code:

- The blanket `upstream_failed` short-circuit at `state_machine.go:92-96` is
  removed; each evaluator owns its `UF` handling as in the table.
- `one_success` and `one_failed` fire as soon as one upstream qualifies instead
  of waiting for all. That changes **timing** for existing DAGs that use them,
  and changes the **final state** in two cases: a `one_success` task whose
  upstreams all finished without success becomes `upstream_failed` (was
  `skipped`); an upstream `upstream_failed` no longer condemns `all_failed` /
  `one_failed` / `one_success`. `all_failed` also skips as soon as one upstream
  succeeds or is skipped, instead of after all upstreams finish (timing only).
  These are bug fixes toward parity; they ship as their own change with a
  changelog entry, before branching.
- **Release targeting.** The alignment makes tasks run that did not run before
  on the same DAG and the same inputs (an `all_failed` / `one_failed` cleanup
  or alert task now fires after an `upstream_failed` upstream; a `one_success`
  task now starts while its siblings are still running). That is an observable
  behavior change for unchanged DAGs, so it does **not** ship in a patch
  release (v0.5.x). **Decided at acceptance:** it ships in the next minor
  release under **Changed** in the changelog, with an upgrade note listing the
  affected rules and the timing and final-state changes above.
  Branching itself is a new feature and is minor-release material anyway.
- `always` is not special-cased by the branch filter. In Airflow `TriggerRuleDep`
  passes immediately (`trigger_rule_dep.py:114-116`), so an `always` child is
  normally scheduled on the first tick, before its branch parent decides, and
  the skip endpoint does not overwrite a running or finished row. The same holds
  here because the filter only touches `none` rows. A cleared `always` child of
  a branch that did not follow it is re-skipped, as `NotPreviouslySkippedDep`
  does. This is Airflow's behavior and is documented, not "fixed".
- `one_done`, `all_skipped`, `all_done_min_one_success` and
  `all_done_setup_success` stay rejected at compile (`compiler.py:24-30`). They
  are cheap to add later on the same evaluator, but no reported DAG needs them,
  and `all_done_setup_success` needs setup / teardown, which Dexaflow does not
  model.
- `FinalizeRun` (`plan.go:367-395`) is unchanged: a run whose tasks are all
  terminal with no `failed` / `upstream_failed` is `success`, so a run where a
  branch skipped half the graph succeeds, as in Airflow. Note that Airflow
  decides the run state from **leaf** tasks only
  (`airflow-core/src/airflow/models/dagrun.py`, `_tis_for_dagrun_state`), while
  `FinalizeRun` looks at every task. Skipped tasks count as success in both, so
  branching gives the same run state; the existing divergence for a failed
  non-leaf followed by a successful `one_failed` / `all_done` leaf is out of
  scope here and tracked separately.
- The in-memory transition table needs no change: every skip the planner
  writes is `none -> skipped` (`state_machine.go:24`).

### D3: `dag.json` schema additions and compatibility

One optional task property, not a new task type:

```json
{"task_id": "pick", "type": "python", "entrypoint": "...",
 "branch": {"kind": "branch"}}

{"task_id": "gate", "type": "python", "entrypoint": "...",
 "branch": {"kind": "short_circuit", "ignore_downstream_trigger_rules": true}}
```

- `branch.kind` is `branch` or `short_circuit`;
  `ignore_downstream_trigger_rules` is allowed only with `short_circuit` and
  defaults to `true` (Airflow's default). `additionalProperties: false` like
  every other object in the schema.
- **Why not a `branch` task type.** The issue sketched a new task type. Every
  switch on `TaskSpec.Type` (executor dispatch, the agent's env building, the
  UI's operator name at `internal/api/ui_tasks.go:112-115`) would have to learn
  that a branch is "a python task, plus something". Branching is a property of
  how a *successful* python task's result is interpreted, so it is modelled as
  an attribute. A future `airflow_operator` branch (D4, deferred) reuses the
  same attribute.
- The `trigger_rule` enum (`dag-schema.json:218-227`, `dag.go:42-57`,
  `compiler.py:24-30`) gains `none_failed`, `none_failed_min_one_success`,
  `none_skipped` and `always`. Both schema copies change together.
- `depends_on` is unchanged. Branching does **not** introduce conditional
  edges: the graph stays static and acyclic (`internal/domain/graph.go`), and
  selection is runtime data on the parent's row. Graph view, clear's
  include-upstream / include-downstream expansion
  (`internal/api/resources.go:778`) and cycle detection keep working untouched.
- **Backward compatibility.** `schema_version` stays `"1.0"`
  (`dag-schema.json:16-20`), following the precedent of `params`, `pool` and
  `max_active_tasks`, which were added as optional properties. A `dag.json`
  without `branch` and with the old five rules compiles to the same canonical
  JSON, so its `CanonicalHash` and DAG version are unchanged. The trigger-rule
  alignment (D2) does change the behavior of existing `one_success` /
  `one_failed` / `all_failed` DAGs in the edge cases listed there; that is
  intentional and called out in the changelog.
- **Forward compatibility is loud at registration only.** An older control
  plane that receives a `dag.json` with `branch` or a new rule rejects it at
  registration, because task objects are `additionalProperties: false` and the
  rule is an enum. It does **not** protect versions that are already stored:
  the scheduler decodes a pinned spec with a plain `json.Unmarshal`
  (`internal/storage/spec_cache.go:83`), which drops an unknown `branch` field
  silently, and `EvaluateTriggerRule` returns `DecisionWait` for an unknown rule
  (`internal/scheduler/state_machine.go:108-109`). So a binary that predates the
  planner filter, running against a database where branch DAGs are already
  registered (a Helm rollback, or an old replica during a rolling upgrade; boot
  only warns on a schema that is ahead, `internal/storage/schema_check.go:171-176`),
  would run **every** branch of those DAGs, and would leave tasks on the new
  rules waiting forever. For `branch` this is handled by ordering, not by a
  runtime check (see Phased path): `TaskSpec.Branch` and the planner filter
  ship one release **before** the schema and the compiler accept `branch`, so
  the oldest binary a supported rollback can reach already applies decisions.
  Rolling back below that release after branch DAGs are registered is
  unsupported and the upgrade notes say so. The new trigger rules ship in the
  same release as their evaluator (Phased path step 1); rolling back below it
  leaves those tasks visibly stuck in `none`, never run wrongly, and the
  upgrade notes say that too.
- **Agent / control-plane skew.** An old agent never sends `branch` on its
  report, so a new control plane fails a branch task closed
  (`branch_decision_missing`, D1). This only happens if a DAG that uses
  branching runs on an image with an old agent; the remedy (rebuild the image)
  is in the error. An old control plane ignores the unknown proto field. That
  matters during a rolling upgrade, where an agent can report to an old replica
  for a DAG a new replica registered: the old replica would write `success`
  with no decision, which the planner reads as "no filter" (the mark-success
  case). The release ordering above closes it, because the oldest replica in a
  rollout that enables the compiler already persists decisions; upgrading
  across both releases in one rolling step is unsupported. **Decided at
  acceptance:** the two-release rollout is the accepted plan, and the upgrade
  notes of the release that enables the compiler state that a single rolling
  jump from a release before the planner filter is unsupported: the operator
  upgrades to the planner-filter release first and completes that rollout.

### D4: Scope of the first slice, and what stays a loud reject

**In the first slice:**

- `@task.branch` and `BranchPythonOperator(python_callable=...)`: compiled to
  `type: python` + `branch: {kind: branch}`.
- `@task.short_circuit` and `ShortCircuitOperator(python_callable=...,
  ignore_downstream_trigger_rules=...)`: compiled to `type: python` +
  `branch: {kind: short_circuit, ...}`. This is cheap because it reuses the
  whole D1 channel and only adds a planner predicate, so it is included.
- The four new trigger rules and the alignment of the existing five (D2).

Compiler changes: the `Branch` / `ShortCircuit` substring gate
(`compiler.py:520-539`) becomes an **allow-list** keyed on the exact shim
classes (`_TaskBranchOperator`, `BranchPythonOperator`, the short-circuit pair).
Any other class whose name or MRO contains `Branch`, `ShortCircuit`, `SkipMixin`
or `LatestOnly` keeps the #225 reject with an updated message that names the
supported spellings.

**Still a loud reject after the first slice:**

| Construct | Why deferred |
|---|---|
| `BranchPythonVirtualenvOperator`, `BranchExternalPythonOperator`, `@task.branch_virtualenv`, `@task.branch_external_python` | need the virtualenv execution path, which Dexaflow does not run (each task already has its own image, ADR 0003) |
| `BaseBranchOperator` subclasses (user `choose_branch`), `BranchSQLOperator`, `BranchDateTimeOperator`, `BranchDayOfWeekOperator` | run through the generic `airflow_operator` executor, which would need to translate `DownstreamTasksSkipped` into the D1 decision; a separate, later step |
| `LatestOnlyOperator` | same; and it **escapes today's name gate**, because its name contains neither marker. Today it is captured generically (`parser/leoflow_parser/_shim/airflow/_generic.py`) and, when not the latest run, fails at runtime because `run_operator` re-raises any exception (`runtime/python/leoflow_runtime/runner.py:513-528`). That is loud, but late; the slice moves it to a compile-time reject. |
| branching to a TaskGroup id (Airflow expands it to the group's roots) | generic TaskGroup is itself rejected (ADR 0043 status) |
| branch tasks inside dynamic task mapping (`.expand`) | mapping is rejected |
| `AirflowSkipException` (a task skipping *itself*) | adjacent but separate: needs `running -> skipped` in `state_machine.go:27` and the success-path handling in the agent. It fails the task today, which is loud. Tracked as a follow-up. |
| `one_done`, `all_skipped`, `all_done_min_one_success`, `all_done_setup_success` | see D2 |

### D5: UI and API

No new API surface is needed; `skipped` is already part of Dexaflow's
Airflow 3.2.x `TaskInstanceState` vocabulary.

- Task instance and grid responses already carry `skipped`
  (`internal/api/ui_dashboard.go:27-44`, `docs/api/openapi.yaml:1030`), so the
  stock Airflow UI colours skipped tasks without change.
- The structure (graph) endpoint keeps emitting the static `depends_on` edges
  (`internal/api/ui_structure.go`); Airflow's graph also draws all edges and
  relies on node state to show the path not taken.
- `operatorName` (`internal/api/ui_tasks.go:112-115`) reports
  `BranchPythonOperator` / `ShortCircuitOperator` for a python task with
  `branch`, and the class reference points at
  `airflow.providers.standard.operators.python`, so the task details panel
  matches Airflow.
- The XCom tab shows `return_value` and `skipmixin_key` for the branch task,
  as in Airflow (D1 step 5).
- "Mark skipped" already exists. "Mark success" does not touch
  `branch_decision`: on a branch task that never succeeded it records no
  decision, so every child is eligible to run; on one that succeeded before and
  was then marked failed, the earlier decision is still on the row and is
  applied again. Both match Airflow, where mark success writes no XCom and an
  earlier `skipmixin_key` XCom survives (D2).

### D6: Clear, retries, warm pools and the durable outcome record

**Clear task** (`internal/storage/repository.go:591-652`):

- *Clear the branch task.* `ResetTaskInstanceToNone` clears `branch_decision`
  with the rest of the attempt. Children that were already skipped stay skipped
  unless they are cleared too (include-downstream), which is Airflow's behavior.
  With include-downstream, the children go back to `none` and wait for the new
  decision.
- *Clear a skipped child alone.* It goes back to `none`; the planner re-applies
  the parent's persisted decision and skips it again. This is
  `NotPreviouslySkippedDep` parity, and it falls out of D2 with no code in the
  clear path.
- *Clear with "run on latest version"* (ADR 0020 rebind). The filter is computed
  against the run's current pinned spec. A chosen id that no longer exists in
  the new version selects nothing; a child that no longer has the branch task as
  a parent is no longer filtered. This is the natural reading of "re-run against
  the newest code" and is documented as such.

**Retries and infra re-place.** A branch task that raises, or returns an invalid
decision, fails like any task and takes the normal retry rail
(`plan.go:165-224`). Its children see it as active (`up_for_retry`) and wait. A
decision exists only on a `success` row, so no retry, re-place or reschedule can
leave a partial decision behind. Each attempt's decision is archived with that
attempt.

**Durable outcome record (ADR 0052).** The reconciler already recovers a lost
success from the termination message (`internal/executor/reconcile.go:64-80`,
settled by `SucceedTaskInstanceIfActive`, `runs.sql:483-492`). Without a change,
a branch task killed after writing its success record but before its report
landed would be settled `success` with no decision, and every branch would run.
Therefore:

- `taskoutcome.Record` gains an optional `branch` field mirroring
  `BranchDecision`, written by `recordOutcome` (`internal/agent/runner.go:878-892`)
  for a branch task. The record stays versioned `v: 1`; the field is additive and
  ignored by readers that do not know it.
- The termination message is capped at about 4 KiB. If the encoded record with
  the decision would exceed the budget, the agent writes **no** success record,
  and the task degrades to ADR 0052's no-record path, never to a decision-less
  success. Concretely, a record-less `Succeeded` pod settles nothing
  (`internal/executor/reconcile.go:85-86`); if the report was also lost, the row
  stays active until the agent-lost reaper fails it and the retry rail re-runs
  it. That costs an attempt, which is the accepted price of the rare case.
- The reconciler settles a branch task's recovered success through a new
  `SucceedBranchTaskInstanceIfActive` (same `id` + `try_number` + active-state
  guard) that also sets `branch_decision`. A success record **without** a
  decision for a task whose pod is labelled as a branch task
  (`leoflow.io/branch=true`, set by `BuildPod` from the spec) is settled
  `failed` with reason `branch_decision_missing`, through the existing
  `FailTaskInstanceIfActive` guard, the same outcome as the D1 table. Treating
  it as "no record" instead would fall into `settleNothing` above and leave
  the row to the reaper.

**Warm pools (ADR 0058).** A warm worker cannot use the termination message; it
reports each attempt in-band through the same `ReportState` RPC (ADR 0058 D3).
The decision rides that report (D1), so the warm path needs no extra channel.
When the per-attempt durable record of ADR 0058 D3 lands, it carries the same
`branch` field under the same rule: no decision, no recoverable success.

**Lite.** Lite runs the same agent and the same report path with no termination
log (`writeOutcome` is a no-op without a path, `runner.go:894-897`), so it gets
D1 and D2 unchanged.

### D7: Test plan

Strict TDD (ADR 0011): every step below starts with the failing test.

1. **Trigger-rule table** (`internal/scheduler/state_machine_test.go`). A
   table-driven test with one row per cell of the D2 table, for all nine rules,
   including the mixed cases (`UF` with `all_failed`, `one_success` with a
   success and a still-running sibling, all-skipped `none_failed`,
   `none_skipped` with a failed upstream, `always` with active upstreams). The
   expected values are taken from 3.2.0 `trigger_rule_dep.py` and the test
   comment cites the line range. This lands first, as the parity fix.
2. **Branch filter** (`internal/scheduler/plan_test.go`). Pure `PlanRun` cases:
   single choice; multiple choices; `None`; the empty-branch join
   (`branch -> join`, `branch -> t1 -> join`, choose `t1`, `join` not skipped);
   the canonical diamond with a `none_failed_min_one_success` join; a chosen id
   that is not a direct child; `NULL` decision (mark success) filters nothing;
   short-circuit falsy with `ignore_downstream_trigger_rules` true (every
   descendant skipped, including a descendant also reachable from a non-skipped
   path) and false (only direct children, cascade by rule); a cleared child
   re-skipped from the persisted decision; an `always` child
   scheduled before the parent decides, and re-skipped when cleared after;
   parent in `up_for_retry` keeps children waiting; determinism (same input, same output).
3. **Compiler** (`parser/tests/test_compiler.py`, `test_shim_edges.py`).
   `@task.branch`, `BranchPythonOperator`, `@task.short_circuit`,
   `ShortCircuitOperator` compile to the D3 shape and validate against the
   schema; the existing reject tests for #225 are rewritten to target the
   still-rejected list in D4 (virtualenv variants, `BaseBranchOperator`
   subclass, `LatestOnlyOperator`, branch to a TaskGroup) so the guard against
   silent mistranslation is kept, not deleted; the new trigger rules compile and
   the other four still reject.
4. **Runtime** (`runtime/python/tests/test_runner.py`). Normalisation of `str`,
   list, tuple, generator, set, `None`; rejection of non-string members and
   unknown ids with Airflow's message; warning on a non-child id; truthiness for
   short-circuit; the decision file is written and `return_value` is unchanged.
5. **Agent** (`internal/agent`). The decision file becomes
   `ReportStateRequest.branch`; it is absent for a non-branch task; it is written
   into the durable record; an over-budget record is not written.
6. **Control plane** (`internal/agentrpc`, `internal/storage` with the
   integration tag against Postgres). Every row of the D1 validation table; the
   decision is persisted only with a `success` that passes the attempt guard; a
   stale report from a previous `try_number` cannot set a decision; clear resets
   it and archives it to history; the reconciler settles a recovered branch
   success with its decision, and settles one without a decision `failed`
   (`branch_decision_missing`).
7. **Schema and domain** (`internal/domain`). `branch` round-trips; the hash of
   a `dag.json` without `branch` is unchanged (golden test); `ignore_downstream_trigger_rules`
   with `kind: branch` is rejected.
8. **End to end** (Lite e2e and the k3d e2e). The canonical Airflow branching
   example (choose one of N, join with `none_failed_min_one_success`) runs with
   the unchosen tasks `skipped`, the join `success` and the run `success`, as seen
   through the Airflow compatible API; a short-circuit DAG; a clear of a skipped
   child that stays skipped; a branch task killed after its success record
   (the ADR 0052 fault-injection seam at `runner.go:96`) whose recovered success
   still skips the right children.

## Consequences

- Airflow 3 branching DAGs, which are common in the wild, compile and run
  correctly instead of being refused. The #225 guard stays, narrowed to what is
  still unsupported.
- The trigger-rule evaluator becomes a faithful port of Airflow's, which fixes
  existing divergences for `all_failed`, `one_success` and `one_failed`. That is
  a visible behavior change for some existing DAGs and is announced as such.
- One new column, one new proto message (plus two repeated fields on the
  agent `TaskSpec`) and one optional schema property. No
  new RPC, no new datastore, no dynamic edges, no change to graph validation.
- The durable outcome record grows one optional field, and the reconciler gains
  one guarded settle. The invariant "a branch task is never `success` without a
  decision, except by an explicit human mark" is stated and tested on every
  path that can write `success`.
- The decision is tenant-supplied data; it is only ever compared against task
  ids of the run's own spec, so it cannot address another DAG or run.

## Alternatives considered

- **A reserved XCom (`skipmixin_key`) as the source of truth.** Closest to
  Airflow's own storage. Rejected: the XCom push is a separate RPC from the
  terminal report and may live in Redis, so it cannot share the
  `ReportTaskResult` state + `try_number` guard; a crash between the two leaves
  `success` without a decision, the exact silent-mistranslation class this ADR
  exists to prevent. The XCom is still written, for display.
- **Writing `skipped` on the children at report time** (Airflow 3's
  `skip-downstream` endpoint). Rejected for Dexaflow: it adds a second writer
  of `none` rows beside the planner, needs its own guard against overwriting
  running rows, and still needs a separate re-skip mechanism for clear. The
  planner-side filter gets both from one pure function.
- **Conditional edges in `dag.json`** (edges labelled with the branch value that
  enables them). Rejected: the selection is runtime data, not structure; static
  edges keep graph validation, clear expansion and the UI graph unchanged, which
  is also how Airflow models it.
- **Requiring chosen ids to be direct children.** Stricter than Airflow and
  would catch some typos, but it rejects DAGs that run on Airflow 3.2 today. We
  validate membership in the DAG (parity) and warn on non-children.
- **Evaluating the callable in the control plane.** Rejected outright by ADR
  0048.
- **Keep the reject.** Correct but leaves a large class of real DAGs
  unmigratable; the issue's revised assessment found the state machine already
  carries most of what is needed.

## Phased path

1. **Trigger-rule parity.** Align the five existing rules and add the four new
   ones (D2 table), parser and schema included. Shippable on its own; branching
   stays rejected. Ships in the next minor release with a **Changed**
   changelog entry and an upgrade note (decided at acceptance, D2).
2. **Decision channel and planner filter (dark).** Proto `BranchDecision` and the
   new `TaskSpec` fields, runtime normalisation and file, agent report,
   `branch_decision` column, server validation, durable record and reconciler
   settle, `TaskSpec.Branch` in the Go domain type, and the D2 branch filter
   (both kinds). The schema still rejects `branch` and the compiler still
   rejects branch operators, so no DAG can carry a decision yet and nothing
   user-visible changes.
3. **Schema and compiler, one release later.** `branch` in both schema copies,
   the D4 allow-list for `@task.branch` / `BranchPythonOperator`, UI operator
   naming, e2e. Shipping this in a **later release** than step 2 is what makes
   rollback safe (D3): every binary a supported rollback reaches already
   decodes `branch` and applies the filter. The schema must never accept
   `branch` in a release whose planner does not apply it, or a hand-written
   `dag.json` would run every branch. A single rolling jump across the
   releases of steps 2 and 3 is unsupported, and that release's upgrade notes
   say so (decided at acceptance, D3).
4. **Short-circuit compiler.** `@task.short_circuit` / `ShortCircuitOperator`
   on the same channel and filter (D4 counts it in the first slice; it can ride
   step 3 or follow it).
5. **Later, separately:** generic `airflow_operator` branch support (translate
   `DownstreamTasksSkipped`), `LatestOnlyOperator`, `AirflowSkipException`, the
   remaining trigger rules, branch-to-TaskGroup once ADR 0043's generic TaskGroup
   ships.

## References

- #787 (this proposal), #225 (the silent-mistranslation reject)
- apache/airflow 3.2.0: `airflow-core/src/airflow/ti_deps/deps/trigger_rule_dep.py`,
  `airflow-core/src/airflow/ti_deps/deps/not_previously_skipped_dep.py`,
  `providers/standard/src/airflow/providers/standard/utils/skipmixin.py`,
  `providers/standard/src/airflow/providers/standard/operators/branch.py`,
  `providers/standard/src/airflow/providers/standard/operators/python.py`,
  `task-sdk/src/airflow/sdk/execution_time/task_runner.py`,
  `airflow-core/src/airflow/api_fastapi/execution_api/routes/task_instances.py`
- `parser/leoflow_parser/compiler.py`, `parser/leoflow_parser/_shim/airflow/sdk/__init__.py`,
  `internal/domain/{state,dag}.go`, `internal/scheduler/{state_machine,plan}.go`,
  `proto/agent.proto`, `internal/agent/runner.go`, `internal/agentrpc/server.go`,
  `internal/storage/agent_store.go`, `internal/storage/queries/runs.sql`,
  `internal/executor/reconcile.go`, `docs/api/dag-schema.json`
