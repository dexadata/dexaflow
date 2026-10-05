---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /scheduler-resilience.html
# --- end AUTO redirect aliases ---
title: Scheduler resilience
weight: 70
description: "How the scheduler survives restarts, leader loss and partial failure."
---

How Dexaflow keeps the scheduler honest when something goes wrong: a process
dies, an agent goes silent, a dispatch is lost in flight. The control plane
ships **five reapers** — small, single-purpose backstops that turn stuck state
back into observable terminal state, so the dashboard never lies about what's
actually running.

This applies to **both editions**: Lite (single-process) and Pro
(multi-replica with [leader election](/project/adrs/0009-leader-election/)). The
reapers run only on the leader — reaping writes state, and we want one writer
across the fleet. On Kubernetes they run from the leader's **maintenance loop**
(every 30 s, after the pod reconciler's sweep; see below). Lite runs its own
maintenance loop with the subset of reapers that mean something without pods;
see [Lite: which reapers run](#lite-which-reapers-run).

## Recovery SLAs

| Failure mode | Detected by | Default SLA | What happens |
|---|---|---|---|
| **Task code wedged past its declared `execution_timeout_seconds`** | **Agent itself** ([#194](https://github.com/dexadata/dexaflow/issues/194)) | **`execution_timeout_seconds`** (per-task) | **TI failed with `execution_timeout: task exceeded N`. Retries kick in if budget remains.** |
| Agent process crashed mid-task (TI in `running`, no heartbeat) | TI heartbeat reaper ([#128](https://github.com/dexadata/dexaflow/issues/128)) | **90 s** | TI failed with `agent_lost`; the TI's pod is deleted so a partitioned-but-alive container stops — unless it has already reached a terminal phase, in which case it is left for the reconciler (see teardown below). Retries kick in if budget remains. |
| Scheduler crashed before dispatching (TI stuck in `queued`) | Dispatch-lost reaper ([#202](https://github.com/dexadata/dexaflow/issues/202)) | **3 min** | TI failed with `dispatch_lost` — but only if no live pod for it exists (see below); any pod still Pending/Running for the attempt is torn down, a finished one is left for the reconciler. Frees the run for the orphan reaper on the next maintenance cycle. |
| Task pod vanished (TI in `running`, no pod at all for its attempt) | Pod-lost reaper | **60 s** after the running transition, then a live pod read | TI failed with `pod_lost`. Only when the apiserver holds no pod for the attempt: a pod that is still there in a terminal phase is left for the reconciler to settle from its termination log (`pod_lost_terminal_pod_defer`). |
| Warm worker died holding attempts (warm pools only) | Warm-worker-lost reaper | next maintenance cycle | Each attempt bound to the dead worker is failed `pod_lost`; refill of the pool is the warm-pool reconciler's job, not the reaper's. |
| Run stuck `running` with no live TIs (post-crash limbo) | Orphan-run reaper ([#120](https://github.com/dexadata/dexaflow/issues/120)) | **5 min** | Run failed with `orphaned`; any remaining active TIs flipped to `failed` and every still-live pod of the run is deleted (its finished pods keep their outcome records for the reconciler). |

Every SLA above is a floor: the reapers run every **30 s**, so detection lands
up to one cycle after the threshold elapses. Worst case end-to-end: a mid-tick
scheduler crash that leaves TIs queued is fully reaped within
**`max(3 min, 5 min) + 30 s`** — the dispatch-lost reaper runs first, then the
orphan-run reaper picks up the now-no-active-TI run on a later cycle.

### Who enforces `execution_timeout` — and why the pod outlives it

A task that declares `execution_timeout_seconds` and runs in its **own** pod has
**two** clocks that could kill it, and only one of them can explain itself. (A
task served by a **warm pool** has only the agent's: warm pods carry no
`activeDeadlineSeconds` at all, and their per-attempt wall-clock bound is the
warm worker's attempt watchdog, derived from
`auth.max_attempt_credential_lifetime` — so everything below about the kubelet
racing the agent does not apply to them.) The **agent** owns the semantic
timeout: at the declared boundary it **SIGKILLs the task's whole process
group**, not just the process it started, and reports
`execution_timeout: task exceeded Ns limit`.

The group matters. Signalling only the direct child left the timeout
defeatable by anything that outlived it — a backgrounded daemon, a `nohup`, a
shell that forks — and worse, a descendant that inherited the task's stdout
kept the agent's own wait open, so the timeout fired and *nothing died*
(#943). The group kill is un-trappable on purpose: a task that handles SIGTERM
and declines to exit would otherwise be exempt from its own declared limit.

Three consequences worth knowing:

- **A descendant that escapes the group** (by calling `setsid`) is not
  reached. The agent stops waiting for it after a bounded 10s and reports the
  timeout anyway, rather than hanging. If your pod declares a
  `terminationGracePeriodSeconds` shorter than that, the kubelet can still win
  the race and you get the degraded reason described above.
- **Output written before the kill is kept.** The tail still in flight when
  the group dies is drained within the same bound; only a descendant holding
  the pipe past it can cost you the last lines, and that is logged.
- **A task with no declared timeout that backgrounds a process** now costs an
  extra 10s at exit while the agent waits out the inherited pipe. Previously
  that case hung indefinitely. The task pod's
`activeDeadlineSeconds` is only the **backstop** for an agent that can no
longer enforce anything (crashed, wedged, partitioned); when the kubelet gets
there first, the pod is gone and the failure reason degrades to what Kubernetes
saw from outside, which cannot name a timeout.

The two clocks do not start together. The kubelet counts from the pod's
`status.startTime`, stamped **before** the image pull. The agent's own clock
starts much later, inside its execute step — after the image pull, the volume
attach and mount, container start, its token bootstrap and exchange, the gRPC
dial, `Register`, `GetTaskSpec`, the environment build (XCom fan-in and secret
resolution, possibly over calls to an external secret backend) and its `RUNNING`
report, whose retry has no budget of its own. On a cold node the image pull
dominates all of it. So the pod deadline is deliberately set **longer** than the
declared timeout:

```text
activeDeadlineSeconds = execution_timeout_seconds
                      + 3 min   (startup headroom = the dispatch-lost threshold)
                      + the pod's terminationGracePeriodSeconds
                        (default 30 s, and capped at 60 s here)
```

The headroom is the dispatch-lost threshold on purpose: that is the window in
which the control plane still presumes healthy startup and defers reaping, so
the kubelet should not be spending the author's execution budget inside it. Note
what the threshold is **not** — a bound on how long startup may take. The
dispatch-lost reaper checks the pod before failing anything and defers while it
is `Pending` or `Running`, so a pod pulling an image for twenty minutes is never
reaped for being slow; the threshold is where the control plane stops assuming a
dispatch landed and starts looking.

The termination grace is added on top rather than folded in — it covers the
shutdown tail (stopping the child, delivering the report), not the startup head.
Only up to 60 s of it is added, however much the DAG declares:
`termination_grace_period_seconds` is unvalidated, and adding an hour of it
verbatim would put the pod's deadline an hour past the declared timeout while
the kubelet grants that same hour of `SIGTERM` grace again on top. The pod spec
still carries the declared value verbatim; only this arithmetic is capped. The
sum is also clamped to `2147483647` (`math.MaxInt32`), the largest
`activeDeadlineSeconds` the apiserver accepts — otherwise a declared timeout
near that bound would be rejected at pod `CREATE` rather than merely never
reached.

Two consequences worth knowing. A pod may outlive its declared timeout by a few
minutes when its agent is dead — that is the backstop doing its job, and the
reapers above still settle the task instance on their own schedule. And a
**pathological** startup (a cold node pulling a multi-gigabyte image, a
throttling registry, or a control-plane outage spanning the `RUNNING`
pre-flight) can still exceed any fixed headroom; the task then fails with the
kubelet's generic reason rather than the timeout diagnosis. If you see that,
the image pull is the thing to fix — and it is worth measuring
`status.startTime` → the agent's `task started` log line on a cold node.

The pod deadline of a task that declares **no** `execution_timeout` is a
different mechanism: a floor derived from `auth.max_attempt_credential_lifetime`
(see below), which takes no headroom because no clock inside the pod races it.

## Cadence: reconcile, then reap

The reapers do **not** run from the scheduler's 1 s tick. They run from the
leader's maintenance loop, which every 30 s performs **one ordered cycle**:

1. the **pod reconciler** sweeps the task pods, recovers each finished pod's
   durable outcome record (a success whose report was lost during an outage is
   recorded as a success — [ADR 0052](/project/adrs/0052-durable-task-outcome/)),
   and garbage-collects old finished pods;
2. **then** the five reapers run.

Each phase runs under its own budget of one interval (30 s): a sweep hung on a
slow apiserver cannot starve the reap, and a reap pass over a large namespace
cannot starve the sweep it depends on. Overrunning a budget is a load signal,
logged at `WARN` with the budget, not an error; a cycle can take up to two
intervals and the ticker coalesces the ticks it overruns.

The order is structural, not a matter of timers lining up. Before this, the
reapers ticked at 1 s and the reconciler at 30 s on an independent clock, so a
pod-lost verdict could land on a pod the reconciler would have recovered as
succeeded at its next sweep. The cost is up to 30 s of extra detection latency
on top of thresholds of 60 s–5 min; reaping is a backstop, never the primary
path, so that trade is the right one.

### The leader-settling gate

A control-plane restart manufactures the very signals the reapers act on:
every in-flight heartbeat looks stale (the receiver was down), a task pod that
finished during the outage looks lost (its terminal report found no server), a
run looks quiet. So after this instance acquires leadership **no reaper fires**
until the leader has **settled** — all three of:

- the **settling grace** (180 s, twice the agent-lost threshold) has elapsed
  since leadership, giving the whole fleet time to re-heartbeat;
- the **pod informer cache has synced**, so the fleet view is complete;
- **a reconciler sweep has completed under this leadership**, so every finished
  pod's true outcome has been recovered before anything is declared lost.

One gate, at the entry of the reaper pass, covers all five reapers (the
warm-worker-lost reaper too: delaying it by the grace only postpones recovering
a dead worker's attempts; pool refill is a separate loop and is not held).
Measured from leadership acquisition, so a re-election resets it. While it
holds, every cycle records `reap_settling_skip`.

**Liveness valve.** A gate that could hold forever would trade "reap wrong" for
"never reap". If the leader has not settled after **2 × grace (360 s)** — the
reconciler cannot list pods, the informer never syncs — the reapers proceed
anyway, with a `WARN` log and a `reap_settling_valve_open` decision on every
cycle the valve stays open. By then the reconciler has had at least four cycles,
so a valve that opens means the sweep really is broken; treat it as an alert.

**Why opening it is safe.** The usual reason a sweep never completes is an
apiserver that cannot be read — unreachable, unauthorized, throttled. The
reconciler's pod LIST and each reaper's own pod-presence LIST hit that same
apiserver, so that failure usually denies every pod-dependent
reaper its authorization too: pod-lost and dispatch-lost defer
(`pod_lost_pod_query_error`, `dispatch_lost_pod_query_error`) and the
warm-worker-lost reaper aborts its cycle with zero marks. They fail closed on
their own, valve or no valve. The two reads can still diverge, though — a broad
namespace-wide LIST can time out where a narrow, server-side-filtered one
succeeds, an informer that never syncs needs `watch` where the reapers need only
`list`, and API Priority and Fairness can starve the heavy request while the
cheap one gets through. And because the valve is *designed* to open, it is
never the only guard: the pod-lost reaper fails a task only when the attempt has
**no pod object at all**, a state no grace, cadence or election timing can
manufacture. A finished pod is a present pod, and stays the reconciler's.

The server validates the timing ladder these depend on at boot and refuses to
start if a constant was moved out of order:
`heartbeat (15 s) < agent-lost threshold (90 s) < settling grace (180 s) < attempt token TTL (10 min)`,
and `2 × maintenance interval (60 s) < settling grace`, so at least two whole
reconcile-then-reap cycles complete inside the grace.

## Lite: which reapers run

Lite (the subprocess executor behind `dexaflow lite`) has no pods, no pod
informer and no pod reconciler. Its agent is a host process, detached from the
server so it survives a server restart. Before
[#916](https://github.com/dexadata/dexaflow/issues/916) Lite ran no reaper at
all, so a run whose agent died without reporting stayed `running` forever. Lite
now starts a maintenance loop of its own: every 30 s, on the leader only, it
runs the reaper pass under the **same leader-settling gate** (the 180 s grace
since leadership; the informer and sweep conditions do not exist in Lite and
are satisfied). There is no reconcile phase because there are no pods to sweep.

| Reaper | Lite | Why |
|---|---|---|
| Orphan-run | **Runs** | Purely a metadatabase signal: a `running` run with no active TI. |
| TI heartbeat (agent-lost) | **Runs, gated on the agent process** | Fails a silent `running` TI only when its agent process is gone. A silent agent that is still alive (for example a laptop resuming from sleep) is deferred. Also covers a TI that never heartbeated (see below). |
| Dispatch-lost | **Runs, gated on the agent process** | Fails a stale `queued` TI only when no agent process for the attempt is alive. |
| Pod-lost | No-op | There is no pod to lose. A "no pod" signal would read every live subprocess as lost. |
| Warm-worker-lost | No-op | Warm pools are Kubernetes-only. |

**Why the process gate.** On Kubernetes a reaper that fails a TI also deletes
its pod, so the abandoned agent stops. Lite has no equivalent: it never kills
an agent. The infra re-place that follows an `agent_lost` or `dispatch_lost`
keeps the try number, so if the old agent were still alive it could have its
`RUNNING` report accepted and run user code next to the new agent
([#911](https://github.com/dexadata/dexaflow/issues/911)). Lite therefore
reaps an attempt only when its agent is provably dead. The server records each
spawned agent's PID, one file per `(run, task, try)` attempt, under
`$TMPDIR/dexaflow-agent-pids-<uid>` (a directory only that user can write;
the server refuses one that is a symlink, owned by someone else, or writable by
others); the record lives on disk so a restarted server
still sees the agents it spawned before the restart. The reaper probes the PID
with signal 0: alive defers (`agent_lost_process_alive`,
`dispatch_lost_process_alive`), a probe or read error defers
(`*_process_query_error`), and a missing record or a dead PID lets the reap
proceed. If the server cannot write the record when it spawns an agent, it
stops that agent and fails the dispatch rather than run an agent the reapers
cannot see.

**The task's process group counts too.** The agent runs the user task as the
leader of its own process group, so a task can outlive an agent that is killed
outright (`kill -9`, a crash, the OOM killer): it is reparented and keeps
running. Only the agent learns the group id, so the agent writes it into the
same directory as soon as the task starts, one `.pgid` file per attempt next to
the agent's `.pid` file, together with its own PID and the group leader's start
time. A task whose group the agent cannot record is stopped and its run fails,
the same rule as for the agent record. An attempt reads alive while its agent
is alive **or** while any process of its recorded task group exists (signal 0
to the whole group; a member owned by another user still counts as alive).
A group id whose leader PID now belongs to a process with a different start
time names an unrelated group (the OS never reuses a PID while it is still a
group id, so nothing of the recorded group is left), and reads dead.

**An agent that dies before its first heartbeat.** Agent-lost normally judges
only a `running` TI that has heartbeated at least once, and on Kubernetes the
window before the first heartbeat belongs to pod-lost, which has no signal in
Lite. So in Lite, and only there, agent-lost also looks at a `running` TI that
never heartbeated and entered `running` longer ago than the agent-lost
threshold (90 s): it is failed as `agent_lost` (`agent_lost_never_heartbeated`)
only when its agent **and** its task process group both read dead. Anything
alive defers (`agent_lost_never_heartbeated_process_alive`), a liveness error
defers (`agent_lost_never_heartbeated_process_query_error`), and such an
attempt's orphaned task group is never stopped, only waited for. The Kubernetes
path is unchanged.

**An orphaned task is stopped before the attempt is re-placed.** When an
attempt reads alive only because of its task group, and the agent that
recorded that group is confirmed dead, the reaper stops the group before it
reaps: `SIGTERM` to the whole group, up to 10 s to exit, then `SIGKILL`, then
up to 5 s more. Only once no process of the group is left does the reap go
ahead (`agent_lost_orphan_stopped`, `dispatch_lost_orphan_stopped`), so the
re-placed attempt never runs beside the old copy. The server signals only a
group recorded in its own record directory, and only when the group's leader is
still the very process that was recorded (same PID, same start time); a group
it cannot verify that way (its leader exited while other members run on, or
the start time could not be read) is never signaled, and the attempt keeps
deferring as alive until the group exits. A stop that fails defers too
(`*_orphan_stop_error`). This runs on Linux and macOS; elsewhere a task group
cannot be probed, so the reapers keep deferring.

What this does **not** cover, plainly:

- **An agent that is alive but wedged** (the process exists, its heartbeat
  stopped) is never reaped by Lite. Stop the process yourself; the next cycle
  then reaps the TI as `agent_lost`.
- **PID reuse.** If the agent died while the server was down and the OS hands
  its PID to an unrelated process, that attempt reads alive and is deferred
  until that process exits. This can only delay a reap, never cause a false
  one.
- **A task process that leaves its process group.** A descendant that calls
  `setsid` (or is handed to a service manager) is outside the recorded group,
  so it neither keeps the attempt alive nor is stopped with the orphan. This is
  the same boundary the agent's own execution timeout has.
- **An agent killed in the instant between starting its task and recording the
  task's group.** That task has no record, so it is invisible to the reapers,
  exactly as before. The window is the few microseconds between the fork and
  one small file write.
- **A record directory that changes across a restart.** The record is found
  through the server's `$TMPDIR`; a server restarted with a different `TMPDIR`
  (or after the temp directory was cleaned) sees no record for agents it
  spawned earlier and treats them as gone. That only matters for an agent that
  is also silent past the agent-lost threshold or still `queued` past the
  dispatch-lost threshold.
- **A task that hangs inside a live agent** is the agent's own
  `execution_timeout_seconds` to stop, exactly as on Kubernetes.

## Tuning the thresholds

The thresholds and the settling grace are **build-time constants**
(`executor.DefaultReaperConfig`), not operator knobs: they sit on the ladder
above, and the boot-time validator exists precisely because moving one out of
order turns a restart into a false reap. The one operator-tunable rung is
`auth.max_attempt_credential_lifetime`, which must stay above the attempt token
TTL; boot fails naming the key if it does not. Setting it non-positive is
accepted but disables the renewal ceiling, the task-pod `activeDeadlineSeconds`
floor and the warm-pool attempt watchdog together, so boot logs a `WARN` naming
the key.

The defaults are conservative on purpose: too-tight thresholds risk reaping a
legitimately slow dispatch (Kubernetes pod-pull latency under contention) or a
busy agent.

## The "do no harm" rule

Each reaper requires a **positive observable signal** before failing
anything:

- **TI heartbeat reaper** — only fires on TIs that *did* heartbeat at least
  once and then went silent. A TI that never heartbeated (e.g. a pod that never
  started, so no agent ever reported) is left alone. "At least once" means
  in the current attempt: every rail that starts a new execution of a task
  (retry, clear, infra re-place, reschedule re-dispatch, warm requeue,
  dispatch-failure backoff) clears the previous attempt's heartbeat, so a new
  attempt is not reaped in the interval between reporting `running` and its
  first heartbeat. In Lite, a TI that never heartbeated is judged only on the
  positive signal that its agent and its task process group are both gone (see
  "Lite: which reapers run").
- **Dispatch-lost reaper** — requires a non-zero `queued_at` older than the
  threshold AND, on Kubernetes, confirmation that no live pod for the TI
  exists. If a pod for the TI is `Pending`/`Running`, the dispatch actually
  landed and the node is just slow to pull the image (a cold-node false
  positive, [#461](https://github.com/dexadata/dexaflow/issues/461)) — the
  reaper defers. If pod liveness can't be determined (K8s API error), it also
  defers. A TI without a `queued_at` stamp is too poorly observed to reap.
- **Pod-lost reaper** — requires a `running` TI past its 60 s liveness floor
  AND a live read confirming no Pending/Running pod exists for exactly that
  attempt. A cached "pod present" defers without an API call; a cache miss is
  never trusted and falls through to the live read; a read error defers. And,
  like every reaper, it waits for the leader-settling gate — so a pod that
  finished during an outage is recovered by the reconciler, not failed.
- **Warm-worker-lost reaper** — requires a live LIST of the warm pods (not the
  cache) showing the bound worker gone; a LIST error aborts the pass with zero
  marks. It never deletes a pod: a warm worker outlives its attempts.
- **Orphan-run reaper**: requires `state = 'running'` AND no live TI on
  the run: every TI must be settled (`success`/`failed`/`skipped`/
  `upstream_failed`) or never started (`none`). A run with any TI in
  `scheduled`/`queued`/`running` is left alone (the dispatch-lost reaper
  unblocks this case by failing the stuck queued TIs first, so a later cycle
  sees no active TIs). So is a run whose TI is parked waiting for the
  scheduler to bring it back: `up_for_retry` during its `retry_delay`,
  `up_for_reschedule` between the pokes of a reschedule-mode sensor, or the
  reserved `deferred` state. Those states stamp no fresh activity timestamp,
  so without this rule a `retry_delay` or `poke_interval` of 5 minutes or more
  would get a healthy run failed as `orphaned`. A TI in `none` does not keep
  the run alive: once its upstreams settle, the next scheduler tick decides
  it. Releasing a TI back to `none` for another attempt (a retry, a reschedule
  poke, an infra re-place, an operator clear) clears its per-attempt
  timestamps, and it only becomes `scheduled` on the next tick; the release
  stamps `released_at` (migration 036), which counts as run activity, so the
  run never looks orphaned in that tick.

  The list is only a snapshot, so the reap re-checks the whole predicate
  atomically: it share-locks the run's TIs without waiting, then fails the run
  only if it is still `running`, still has no live TI and its last activity is
  still older than the threshold. If a TI moved in between (say a retriable
  failure went to `up_for_retry`, or a TI was scheduled), fresh activity
  landed, or another transaction is writing one of the run's TIs at that
  moment, the reap is a no-op: nothing is written, no pod is torn down, and the reaper records
  `orphan_reap_noop`.

## Tearing down the reaped task's pod

Failing a TI in the metadatabase is not enough on its own: a reaped task's
pod can still be running user code, which breaks at-most-once execution if
that work commits or a retry runs it again
([#474](https://github.com/dexadata/dexaflow/issues/474)). So, **after** the
durable DB transition, each reaper tears the pod down:

- The **heartbeat** and **dispatch-lost** reapers delete exactly the reaped
  TI's pod, pinned by `(run-id, task-id, try-number)` labels — a retry
  dispatches a new pod with a new try-number, so a newer live attempt can
  never be the one deleted.
- The **orphan-run** reaper deletes every pod of the abandoned run (the
  run-id is unique per run, so no other run's pod can match).
- **A pod that already reached a terminal phase (`Succeeded`/`Failed`) is
  skipped, not deleted** ([#928](https://github.com/dexadata/dexaflow/issues/928)).
  It has no container left to stop, so deleting it would buy nothing and cost
  the durable outcome record on its termination message — the only evidence the
  reconciler can settle the attempt from
  ([ADR 0052](/project/adrs/0052-durable-task-outcome/)). Collecting those pods
  is the reconciler's job: it settles each one, then garbage-collects it once it
  ages past the 10-minute grace. The skip is enforced once, at the teardown
  itself, so it holds for every reaper — including the heartbeat reaper, which
  fires on heartbeat staleness alone at 90 s and reads no pod state, and the
  orphan-run reaper, which applies it per pod inside the run. No reaper's
  decision changes: a TI the reaper marked stays marked. Look for
  `reap teardown: task pod is already in a terminal phase` at INFO to see which
  pods were left behind; a failed *delete* is the `*_pod_delete_error` decision
  below, which is a different thing.
- A pod in phase `Unknown` **is** deleted. It may still be running a container,
  which is the case teardown exists for, and the reconciler treats `Unknown` as
  non-terminal — it neither settles nor collects it — so nothing else would.
- Belt and suspenders: the control plane also answers a **stale** agent
  `ReportState`/`Heartbeat` — one whose attempt no longer matches the live
  row — with `should_terminate`, so a reaped-but-still-alive pod that we
  couldn't delete (e.g. during a K8s API outage) cancels its own work. The
  "stale" test is exactly the source-state + `try_number` guard the state
  write already uses ([#467](https://github.com/dexadata/dexaflow/issues/467)):
  the report applies for the live, matching attempt, so a live execution is
  never told to stop.

These teardown steps are best-effort and off the critical path: a delete
failure is logged and metered but never undoes the DB reap, and the pod's own
`activeDeadlineSeconds` plus the reconciler's GC remain backstops. In Lite
(subprocess executor) there are no pods and no teardown: the reapers only fail
an attempt whose agent process is already dead (see
[Lite: which reapers run](#lite-which-reapers-run)), so there is nothing left to
stop, and a run failed by the orphan-run reaper stops any agent still attached
to it through the `should_terminate` signal.

A terminal pod the teardown skips is collected by the reconciler, so a
reconciler that is not sweeping leaves those pod objects behind. That is not a
new dependency: every normally-finished task pod has always been the
reconciler's to collect, and the reap-time delete only ever covered the subset
belonging to a reaped TI or run. A terminated pod holds no node CPU or memory —
it costs an API object and a slot against any `count/pods` quota — and
Kubernetes' own terminated-pod GC (`--terminated-pod-gc-threshold`, 12500 by
default) is the cluster-level floor under it. A reconciler stuck for longer
than that shows up first as `reap_settling_valve_open`, which is the label to
alert on.

## The load-bearing invariant

Recovery is bounded by the slowest reaper that applies, not the fastest —
usually fine, sometimes worth tuning
(ADR [0031](/project/adrs/0031-scheduler-architecture/)). The invariant that governs
every reap decision is **never fail or tear down the live current attempt —
only one that is genuinely stale or lost**, and **never destroy the evidence of
one that already finished**. It is preserved end-to-end: the DB
transitions are guarded on source state (`WHERE state IN (...)`), pod deletes
are pinned to the exact `(run, task, try)` reaped and skip a pod already in a
terminal phase, the dispatch-lost reaper
defers whenever a pod is live or its liveness is unknown, and the
`should_terminate` signal fires only when the reporting attempt has provably
moved on. When in doubt, a reaper defers rather than reap.

## Observability

Each reap action is metered as a scheduler decision. Watch these labels in
your Prometheus dashboard:

| Metric label | Meaning |
|---|---|
| `agent_lost` | TI failed by the heartbeat reaper |
| `dispatch_lost` | TI failed by the dispatch-lost reaper |
| `dispatch_lost_deferred` | Dispatch-lost skipped because the TI's pod is live (slow start, [#461](https://github.com/dexadata/dexaflow/issues/461)) — a healthy signal, not a fault |
| `pod_lost` | TI failed by the pod-lost reaper (no pod at all for the attempt) |
| `pod_lost_terminal_pod_defer` | Pod-lost skipped because the attempt's pod is still there in a terminal phase — the reconciler settles it from its termination log; reaping would delete that evidence. Healthy as a *transient*. Sustained past two maintenance cycles means the reconciler is not settling and those task instances are stranded `running`, not about to settle: correlate with `reap_settling_valve_open` and `pod_lost_pod_query_error` |
| `warm_worker_lost` | TI failed by the warm-worker-lost reaper (its warm worker is gone) |
| `orphan_reaped` | Run failed by the orphan-run reaper |
| `orphan_reap_noop` | A listed orphan candidate was no longer orphaned when the reap re-checked it (a TI moved, fresh activity landed, or a TI was being written); nothing was written and no pod was torn down |
| `reap_settling_skip` | The whole reaper pass was held because the leader has not settled yet (grace, informer sync, or a post-leadership reconciler sweep still pending) — expected for ~3 min after every (re-)election |
| `reap_settling_valve_open` | The leader never settled within 2 × grace and the reapers ran anyway; the reconciler sweep or the pod informer is broken — **alert on this** |
| `reap_gate_skip` | The pass was skipped because this instance is stepping down, no longer leads, or is shutting down — a healthy signal during rollouts |
| `agent_lost_list_error`, `dispatch_lost_list_error`, `orphan_list_error`, `pod_lost_list_error`, `warm_worker_lost_list_error` | Reaper's list query failed; the next cycle will retry |
| `dispatch_lost_pod_query_error`, `pod_lost_pod_query_error` | Pod liveness could not be read (K8s API error); the reaper deferred rather than risk a false positive |
| `agent_lost_process_alive`, `dispatch_lost_process_alive` | Lite only: the attempt's agent process, or a process of its recorded task group that could not be stopped as an orphan, is still alive, so the reaper deferred. Sustained `agent_lost_process_alive` for one attempt means a wedged agent; stop it by hand |
| `agent_lost_process_query_error`, `dispatch_lost_process_query_error` | Lite only: the agent's PID record could not be read or probed; the reaper deferred |
| `agent_lost_orphan_stopped`, `dispatch_lost_orphan_stopped` | Lite only: the attempt's agent was dead but its task process group was still running; the reaper stopped that group (SIGTERM, then SIGKILL) and then reaped |
| `agent_lost_orphan_stop_error`, `dispatch_lost_orphan_stop_error` | Lite only: stopping an orphaned task process group failed; the reaper deferred and retries next cycle |
| `agent_lost_never_heartbeated` | Lite only: a `running` TI that never heartbeated, past the agent-lost threshold, whose agent and task process group are both gone, was failed as `agent_lost` |
| `agent_lost_never_heartbeated_process_alive`, `agent_lost_never_heartbeated_process_query_error` | Lite only: such a TI's agent or task group is alive, or its liveness could not be read; the reaper deferred |
| `agent_lost_pod_delete_error`, `dispatch_lost_pod_delete_error`, `orphan_pod_delete_error`, `pod_lost_pod_delete_error` | Pod teardown after a reap failed; the DB reap stands and the pod's `activeDeadlineSeconds`/GC are backstops |

A sustained non-zero rate on any of these is worth investigating — reapers
are backstops, not the primary path; if they fire often, something upstream
is broken.

## What's NOT a scheduler concern

- **Postgres unreachable** — the scheduler's `Heartbeat()` goes unhealthy;
  the `/monitor/health` endpoint surfaces it; runs queue up and resume when
  the DB returns.
- **Agent's task container OOM-killed** — surfaces as a non-zero exit code
  through the agent (if it survived) or as `agent_lost` (if the agent went
  with it).
- **K8s API outage** — pods stay where they are; new dispatches fail at the
  executor layer (visible as `dispatch_failed` metric on the
  [BufferedDispatcher](/project/adrs/0031-scheduler-architecture/)); the
  dispatch-lost reaper does NOT fail TIs during the outage — it cannot read
  pod liveness, so it defers (`dispatch_lost_pod_query_error`) rather than
  risk a false positive. Reap-time pod teardown is likewise skipped and
  retried once the API returns; a reaped-but-alive pod stops itself via the
  `should_terminate` signal on its next heartbeat.

See also: [ADR 0009 (leader election)](/project/adrs/0009-leader-election/),
[ADR 0031 (scheduler architecture)](/project/adrs/0031-scheduler-architecture/).
