# Soak battery: long-running resilience

A battery that runs a realistic, scheduled workload for hours or days and asserts,
continuously, that the scheduler is still behaving. It exists because the gates we
already have answer a different question: `test/e2e/` proves a path works once,
`test/load/` measures one cost at one instant, and `test/e2e/chaos-runtime.sh`
injects a fault and checks the recovery. None of them tell you whether a control
plane that has been dispatching work since Friday is still dispatching it on
Monday, or whether the cost of a tick has quietly started tracking the size of the
history table.

**The bar this is built to:** a soak that only reports "it did not crash" is
worthless. Everything below is organized around two questions: what does "the
scheduler did not wedge" mean as a *measurement*, and what does a *failing* soak
look like.

```sh
bash test/soak/soak.sh                                    # 30 min, no faults
bash test/soak/soak.sh --duration 12h --faults standard   # overnight with faults
bash test/soak/soak.sh --duration 6m  --faults selftest-red   # must go RED
make soak            # the default run
make soak-selftest   # proves the assertions can fail: must exit exactly 1
```

Everything runs locally: one Postgres container and one `dexaflow lite` process.
No cluster, no cloud, no paid service. See [Cost budget](#cost-budget) for the
numbers.

---

## The credential question, and what soak_token does and does not answer

A task pod is dispatched with a short-lived agent credential that the control
plane renews on each heartbeat, bounded by
`auth.max_attempt_credential_lifetime` (24h by default). Renewal is what lets an
attempt outlive its own token, and until `soak_token` nothing exercised it: the
longest task in the battery ran for seven minutes.

**The shape that matters is not "a task ran for a long time".** It is "a task ran
for a long time AND THEN used its credential". A task that sleeps and exits
proves nothing, because the agent may need the token only at dispatch. So
`soak_token` resolves the managed connection `soak_http` three times: at the
start, at the midpoint, and after the sleep. The last one is the assertion; the
first exists so a deployment that was broken from the outset says so in a second
rather than in forty minutes.

**Retries are off, deliberately.** A retry is dispatched with a FRESH credential,
so a task that died of a lapsed token would pass on attempt 2 and the run would
end green. The single failure this DAG exists to catch is the one a retry would
erase.

**The ceiling has its own run, and it is inverted.** `make soak-credential-ceiling`
lowers `auth.max_attempt_credential_lifetime` to four minutes, sets
`soak_token`'s body to nine, and **requires the task to fail**. Green there would
mean the bound did not fire, so an attempt can hold a live credential past the
ceiling, which is the thing the setting exists to prevent.

It asserts the REASON and not just the failure, reading the task's recorded state
and error from the database rather than a log line: `soak_token` has retries off
and an execution timeout far above its body, so the credential is the only
expected way for it to die, and letting a timeout or an import error pass as
success here would make the whole check decorative.

The normal soak still runs `soak_token` at the shipped ceiling, where it must
SUCCEED. The two runs assert opposite things about the same DAG, which is why
they are separate modes rather than one flag.

**Why hourly.** The body defaults to forty minutes. On a shorter schedule
`max_active_runs=1` would block the next run and the cadence check would report
the DAG as starved, which would be a true reading of the wrong thing.

## Scheduler punctuality, and what the cadence check cannot see

`cadence_starved` asks whether the runs EXIST. A scheduler running twenty
minutes behind still creates every run it owes, so volume alone reports that
deployment as healthy while the thing an operator feels, a pipeline due at 02:00
that starts at 02:40, goes unmeasured.

`schedule_late` measures the gap between `logical_date` (when the schedule was
due) and `queued_at` (when the scheduler created the run), per DAG, worst case
in the window. Only `trigger = 'scheduled'` runs count: a manual trigger has no
schedule to be late for and would otherwise flatter the number.

The budget is **half the DAG's own period**, not one constant. Thirty seconds is
nothing for an hourly schedule and most of the interval for a two-minute one, so
a single threshold would be noise on the short schedules or blind on the long
ones. Past half an interval a run is closer to the next slot than to its own,
and two consecutive late runs begin colliding.

It is silent inside a fault window, where being late is the correct behavior.


## 1. What "the scheduler did not wedge" means as a measurement

Six signals are asserted. Each one is named for the failure it catches, and each
was chosen over an alternative that is listed in [Rejected signals](#rejected-signals).

| # | Signal | Measured as | Red when |
|---|---|---|---|
| S1 | **A task stopped moving** | age of the oldest `task_instances` row in `queued`, and separately in `scheduled` | queued > 180 s, scheduled > 300 s |
| | *note on the queued threshold* | 180 s is also the product's own dispatch-lost threshold (`internal/executor/reaper.go` `defaultDispatchLostThreshold`), and the reaper runs every tick while the monitor samples every 10 s. So in a healthy control plane the reaper always gets there first and this check is really "the dispatch was lost AND the reaper did not act". That is a useful thing to assert and it is not the same thing as "a task stopped moving"; the re-placements the reaper does perform are recorded as `infra_replacements_total` and asserted on by nothing, because a stalled laptop can cause one |
| S2 | **A run stopped moving** | age of the oldest `dag_runs` row in `running` | > 30 min |
| S3 | **The scheduler stopped being the scheduler** | `.scheduler.status` from `GET /api/v2/monitor/health`, which reads the advisory-lock leader liveness | not `healthy` outside a declared fault window |
| S4 | **The scheduler kept its heartbeat but stopped creating work** | runs created per DAG in a rolling window, against the DAG's declared cron period | fewer than half the due runs |
| S5 | **A tick started costing more than it used to** | wall time of `storage.SchedulerStore.ActiveRuns`, recorded against both the active-run count and the total historical row count | reported as a curve, not a threshold (see [Scalability](#3-scalability-the-shape-of-the-curve-not-the-size-of-n)) |
| S6 | **The leader churned** | `dexaflow_scheduler_step_downs_total`, scraped from the control plane's **metrics listener** (Lite: `--port + 1010`; the API port does not serve `/metrics` at all) | any non-zero value |

Plus four correctness invariants that carry no timing at all. These are wrong the
instant they are non-zero, fault window or not:

| # | Invariant | Query |
|---|---|---|
| C1 | No task instance is still active on a run the control plane already called terminal | `task_instances` in a non-terminal state joined to a `dag_runs` row in `success`/`failed` |
| C2 | No task instance exceeded its retry budget | `try_number > max_tries` |
| C3 | No successful attempt was archived (reported as `success_archived`) | any `task_instance_history` row in state `success` |
| C4 | No task whose upstream always fails ever executed | `soak_flaky.never_runs` with `started_at IS NOT NULL` |

C1 is the "the dashboard is lying about what is running" class.

**C3 is weaker than it looks, and the honest statement of its scope is this.**
Four queries archive into `task_instance_history` (`internal/storage/queries/runs.sql`):
the retry rail (`ResetTaskInstanceForRetry`, guarded to `up_for_retry`), the infra
re-placement rail (guarded to `failed` + `last_failure_kind='infra'`), the
clear-failed rails (guarded to `failed`/`upstream_failed`/`up_for_retry`), and the
UNGUARDED admin clear (`ResetTaskInstanceToNone`, reachable only through the
clear-task API). So a `success` row can appear only when an operator clears a
successful task, which this battery never does. And the replay shape it was meant
to catch (the same attempt executed twice without a reset in between) writes no
history row at all. C3 therefore costs nothing and holds, but it is **not** an
at-most-once proof and no soak result should be quoted as one. A reachable
at-most-once probe needs a per-attempt execution ledger written by the task
itself, compared against `try_number + infra_attempts`; the DAGs already write
half of it (`soak_flaky` counts its own attempts on disk) and nothing reads it
yet.

C4 keys on `started_at`, not on a state, and that is deliberate: `never_runs`
raises on entry, so it can never reach `success` even when the scheduler wrongly
dispatches it. A query for a successful `never_runs` is a query whose answer is
always zero. The durable evidence that it executed is that the control plane
stamped it running.

### Why the tick-cost probe is a probe and not a metric

`dexaflow_scheduler_loop_duration_seconds` is **declared** in
`internal/observability/metrics.go` and is **never observed inside the scheduler
loop**. `grep -rn SchedulerLoopDuration --include='*.go'` returns the metric
definition, the metrics unit test, and `test/load/scheduler_ceiling`, which
observes it by hand precisely because the product does not. The tables
`scheduler_loops` and `replicas` (migration `004_scheduler_control.up.sql`,
commented "purely for observability") are written by no Go code at all.

So there is no in-product signal for tick duration. The soak therefore times
`SchedulerStore.ActiveRuns` itself, from its own read-only connection. That call
is the read half of a tick and the thing `test/load/scheduler_ceiling` identifies
as its dominant cost (`1 + 2N` queries plus `N` spec decodes). It is a proxy, it
is labeled as one everywhere it appears, and it is honest about what it does not
include (the per-run advance and the dispatch enqueue).

### Rejected signals

* **Task throughput (tasks completed per minute).** Rejected as a pass/fail
  signal because it is dominated by the laptop, not the scheduler. A background
  Xcode build would turn the soak red. It is recorded, never asserted.
* **Scheduler process CPU and RSS.** A symptom, not an invariant, and extremely
  noisy on a developer machine. Not collected: the harness has no reliable
  cross-platform way to attribute it, and a number nobody can act on is worse
  than no number.
* **Wall-clock duration per DAG run.** Same problem as throughput, with the extra
  flaw that the long-running DAG dominates the distribution.
* **Prometheus histogram quantiles from the metrics listener.** Attractive, but the two
  histograms that would matter (`dexaflow_scheduler_loop_duration_seconds`,
  `dexaflow_task_cold_start_seconds`) are not observed in Lite's hot path, so they
  would report empty. The soak reads the three *counters* that are wired
  (`step_downs_total`, `tasks_undispatchable_total`, `dispatch_at_capacity_total`)
  and gets everything else from the database, which cannot lie about state.

  Where they are read from is not a detail. `/metrics` is served on the metrics
  listener and deliberately **not** on the API port
  (`internal/api/server.go`: "/metrics is intentionally NOT served here"), and
  Lite puts that listener on `--port + 1010` (`internal/cli/dev.go`
  `devMetricsPort`). Scraping the API port returns a 404 whose body parses to no
  samples, which reads as three zeros and makes S6 and `undispatchable_task`
  permanently unfalsifiable. The monitor therefore takes `--metrics`, derives it
  from `--api` with that same offset when it is not given, and **refuses to
  start** unless the endpoint answers 200 with at least one `leoflow_` family. An
  unreadable counter is recorded as JSON `null`, never as 0.
* **Log-line scraping.** Considered for the reaper decisions (`reap_settling_skip`
  and friends). Rejected because the decisions are metered by label on a metric
  Lite does not expose on a path the soak can reach, and because an assertion
  built on a log string breaks on a wording change rather than on a behavior
  change.

---

## 2. What a failing soak looks like

If nothing can make it red, it is decoration. Three things can.

**a. The unconditional invariants.** C1, C2 and C4 need no fault at all. They go
red the moment the database says something impossible. C3 holds unconditionally
too, but read its scope above before counting it as coverage.

**b. Injected faults, with recovery as the assertion.** `--faults standard`
injects three real faults, spread across the run:

| At | Fault | What it models |
|---|---|---|
| 25% | `docker pause` the Postgres container for 45 s | the metadatabase goes away under a live scheduler |
| 55% | `SIGSTOP` the `leoflow-server` process for 45 s | a frozen scheduler that is neither dead nor working |
| 80% | `SIGKILL` and restart the control plane | the crash-and-resume path, which is `chaos-runtime.sh` scenario A without the cluster |

The harness appends each window to `faults.jsonl` as it happens, and the monitor
reads that file live. The wedge checks (S1, S2) and the health check (S3) are
suppressed inside a declared window plus the recovery budget, and only inside it.
The assertion is on the recovery, not on the absence of disturbance.

**c. The deliberate red: `--faults selftest-red`.** This is the proof the
machinery fires. It injects a *real* fault (a 300 s `docker pause` of Postgres)
and declares a *45 s* window for it. Nothing is faked and no assertion is
weakened: the harness simply claims the incident was short, and the monitor
observes an outage nobody told it about, exactly as it would for an incident an
operator believed was over. `scheduler_unhealthy` fires at declared end plus the
recovery budget, and the run exits 1.

This shape was chosen over the alternatives on purpose:

* Injecting a fake row into the database would test the SQL, not the system.
* Tightening a threshold until something fails would be weakening an assertion in
  reverse, and this repo does not do that.
* Stopping Postgres and never restarting it would go red, but the run would also
  end with the fault still open, which is a less interesting shape than one that
  recovers and is still caught.

---

## 3. Scalability: the shape of the curve, not the size of N

The instruction was to evaluate scalability without over-scaling. The way to
honor both is to measure the **shape** of the cost curve at small N rather than
to chase a big N, because the shape at small N predicts the ceiling without
paying for it.

**The chosen ceiling is 6 DAGs, at most 6 concurrent runs, and about 30 task
instances per minute at peak.** That is deliberately small. The justification:

* A tick's read cost is `1 + 2N` queries in `N` = active runs
  (`storage.SchedulerStore.ActiveRuns`, and `test/load/scheduler_ceiling` already
  measured that dimension directly up to N=1000). Re-measuring width here would
  duplicate Experiment 1 at worse fidelity.
* The dimension Experiment 1 **cannot** reach is *age*: whether a tick's cost
  grows with the number of rows the database has accumulated since Friday, at a
  constant active set. That needs time, not width. A 48 h run at this cadence
  accumulates on the order of 30k `task_instances` rows and 4k `dag_runs` rows
  while the active set stays under 10.
* So the soak holds N roughly constant and lets the history grow by two orders of
  magnitude, then plots `tick_probe_ms` against both. The `summary.md` shape table
  buckets the run into ten slices and prints the median of each.

Read the table this way:

* Probe flat while `total runs` grows 100x: a tick costs what the **active** set
  costs. The ceiling is set by concurrency, and the capacity question is answered
  by Experiment 1.
* Probe tracking `total runs`: the ceiling arrives with **age**, and the fix is
  retention (or an index), not capacity. That is a different and much more
  serious finding, because no amount of provisioning postpones it.

What this deliberately does not do is push N until a tick exceeds the loop
interval. `test/load/scheduler_ceiling` owns that measurement, it costs a
1000-DAG seed, and running it inside a weekend soak would make the age signal
unreadable.

---

## 4. Operator coverage: chosen, not accidental

`internal/domain` carries four executable task types. A spread across them matters
more than many instances of one, because the failure modes differ: a `python` task
is materialized and run by the runtime directly, while an `airflow_operator` task
has to resolve a class path, import a provider package, construct the operator and
hand it a rendered Connection.

| Task type | Where it runs in the battery | Why |
|---|---|---|
| `python` | every DAG; the bulk of `soak_ingest`, `soak_fanout`, `soak_flaky`, `soak_long` | the native baseline every other measurement is compared against |
| `bash` | `soak_chain`, five `BashOperator` hops | Dexaflow compiles `BashOperator` to its own native `bash` type and renders `{{ ds }}` with its own templater, not Airflow's. A different code path from `python`, and the one that would silently regress if the templater changed |
| `airflow_operator` | `soak_operators`, four tasks | the non-native path, and the one with the most moving parts |
| `dbt_group` | **not run.** See [What this does not cover](#8-what-this-deliberately-does-not-cover) | |

### The non-native path gets the most attention

`soak_operators` runs four provider tasks per run, chosen so that nothing leaves
the machine:

| Task | Class | Talks to | Why this one |
|---|---|---|---|
| `http_get` | `airflow.providers.http.operators.http.HttpOperator` | the soak's own fixture server on 127.0.0.1 | the simplest provider round trip, and the one whose Connection is resolved from Dexaflow's encrypted store into `AIRFLOW_CONN_SOAK_HTTP` |
| `sql_upsert` | `airflow.providers.common.sql.operators.sql.SQLExecuteQueryOperator` | the soak Postgres, `soak_warehouse` database | a real write through a provider hook, with `{{ run_id }}` rendered into the statement |
| `sql_count` | same class | same | the read-back. A silently failing write cannot pass as a green run |
| `wait_a_moment` | `airflow.providers.http.sensors.http.HttpSensor`, `mode="reschedule"` | the fixture's poke-counter endpoint | `up_for_reschedule` is a scheduler state with its own re-dispatch path; a battery that never enters it leaves that path uncovered for the whole run |

`native_anchor` is a plain `@task` in the *same run*, so the native and non-native
dispatch latencies are measured under identical scheduler conditions. `summary.md`
prints them side by side, per operator type.

**No public endpoint is contacted anywhere in the battery.** Not one. A weekend of
scheduled requests against a stranger's service is not something we do, and an
outage on their side would turn the report red for a reason that says nothing
about our scheduler. That is why `examples/http_operator` (which points at
`jsonplaceholder.typicode.com`) and every `gcp*` example are excluded, and why the
fixture server exists.

### What a long run is supposed to reveal about the non-native path

The open questions, which the shape table and the per-operator latency split are
there to answer:

* Does `airflow_operator` dispatch-to-start latency stay flat over the run, or
  does provider resolution get more expensive as the process ages?
* Does the gap between `python` and `airflow_operator` widen? In Lite each attempt
  is a fresh subprocess, so the provider import cost is paid every time and should
  be a roughly constant offset. A widening gap means something is accumulating.
* Under warm pools the pod is reused, so an import done once on attempt 1 must
  still be correct on attempt N. That question can only be asked on Kubernetes;
  see the next section.

---

## 5. Warm pools: the comparison method

Warm pools (ADR 0058) are Kubernetes-only: `internal/executor/warmpool_k8s.go`
is the only implementation, and Lite's subprocess executor has no pool at all. So
the with/without comparison **cannot** run inside the Lite soak. It is a separate,
bounded k3d experiment: `test/soak/warmpool-ab.sh`.

**The comparison is not a one-variable experiment, and pretending otherwise would
be dishonest.** `internal/config/server.go` fails boot closed unless
`warm_pools_enabled` is accompanied by `agent_token_transport=exchange` **and**
`secret_liveness_mode=enforce` (ADR 0058 D2). Warm pools are a security decision
before they are a performance one. So the two arms are:

* **Arm A (off):** the shipped default. `warm_pools_enabled=false`,
  `agent_token_transport=envvar`, `secret_liveness_mode=observe`.
* **Arm B (on):** `warm_pools_enabled=true` plus the two settings its boot
  validator requires.

The honest framing of the result is therefore "warm pools together with their
mandatory security posture, against dedicated pods at the shipped default", not
"warm pools against not-warm-pools".

**Fairness rules, all enforced by the script:**

1. The same two DAG images, the same task bodies, the same data volume.
2. The same operator mix in both arms (one `python` DAG, one `airflow_operator`
   DAG), so the comparison measures the pools and not the mix.
3. **Interleaved A/B/A/B blocks**, not A-then-B. A single crossover would let
   cluster warm-up, image-cache state and laptop thermal drift all load onto the
   second arm.
4. A discarded warm-up block at the head of each arm, so arm B is not credited
   for a pool that does not exist yet and arm A is not charged for a cold node.
5. The same cluster, created once, for the whole experiment.

**What is compared:** queued-to-running latency per task (the cold start the pools
exist to amortize), pods created per completed task, wall-clock per run, and
attempts served per warm worker. Plus one correctness assertion that matters more
than any of the numbers: on a reused pod, attempt N must resolve the same
Connection values as attempt 1, and a superseded attempt's token must not.

> **Status: written, not yet executed.** The script is in the tree and reuses the
> k3d patterns from `test/e2e/chaos-runtime.sh`, but it has not been run to
> completion. Treat its output format as a proposal until the first green run.
> It prints this warning itself.

---

## 6. Cost budget

The maintainer's personal card is the exposure. A budget that is merely
optimistic is worse than no budget, so every number below is either measured or
derived from a measured number, and the derivation is stated.

### 6.1 Cloud: zero, structurally

| Line | Cost | How it is made structurally true |
|---|---|---|
| Cloud provider | **0** | Nothing in `test/soak/` imports a cloud SDK, reads cloud credentials or names a cloud endpoint. The two Connections the battery creates point at `127.0.0.1` |
| Managed database | **0** | Postgres is a local container from `docker-compose.soak.yaml` |
| Registry pulls | **0 beyond a normal build** | One image: `postgres:16-alpine`, pulled once and cached. The Lite soak builds no images at all (the subprocess executor runs tasks in a local venv) |
| Outbound HTTP | **~0** | The HttpOperator and HttpSensor point at the loopback fixture. The only egress is PyPI, once, when a per-DAG venv is first provisioned |
| SaaS / API quota | **0** | No public endpoint is contacted. This is checked by reading the DAGs: `grep -r 'https\?://' test/soak/dags` returns nothing but loopback |

The one place a paid service could sneak in is the k3d warm-pool experiment, if
somebody pointed it at a remote registry. It uses `k3d image import` (no registry), and
it neither reads nor writes the operator's kubeconfig: it warns when `KUBECONFIG`
is set, creates the cluster with `--kubeconfig-update-default=false
--kubeconfig-switch-context=false` (k3d otherwise merges the throwaway cluster
into `~/.kube/config` and switches the current context to it), and exports its
own kubeconfig file for the duration.

### 6.2 Disk, which is the line that actually bites

Measured on the first smoke run (macOS, 6 DAGs, default volumes):

| Item | Measured | Growth over a weekend |
|---|---|---|
| Per-DAG Python venvs | **1.4 GiB total** (222 to 261 MiB each, x6) | **none.** Provisioned once into the private soak HOME and reused across runs |
| Built binaries (`leoflow`, `leoflow-server`, `leoflow-agent`, fixture, monitor) | **368 MiB** | none, rebuilt in place |
| `postgres:16-alpine` image | ~80 MiB | none |
| DuckDB scratch data | **7.4 MiB** | **bounded by design.** Each DAG writes to one fixed path per DAG and overwrites it every run, so the parquet files do not accumulate. What does grow is one small JSON receipt per run: on the order of 100 bytes per run, so under 1 MiB at 72 h |
| Soak Postgres | see below | the real variable |
| Evidence (`samples.jsonl`) | **1.4 KiB per sample** (measured over 109 samples) | at 10 s sampling, **11.5 MiB per 24 h**, 35 MiB at 72 h |
| Lite task logs (`<soak HOME>/.leoflow/dev/logs`) | not measured on the first runs | one log per attempt at roughly 420 attempts/hour. Now sized every sample and counted against the scratch budget, because nothing else bounds it |
| Harness process logs (`lite.log` in the evidence directory) | not measured on the first runs | append-only for the whole run. Also counted against the scratch budget now |

**Fixed floor before a single run: about 1.9 GiB.** That floor, not the growth,
is what a laptop actually has to have free.

**Postgres growth.** The database is where a weekend was expected to accumulate.
It does not, at this cadence. Measured over the last two thirds of an 18-minute
baseline run (109 samples, no faults, default volumes):

| Rate | Measured |
|---|---|
| dag runs created | **65 / hour** |
| task instances created | **420 / hour** |
| archived attempts (`task_instance_history`) | 30 / hour |
| xcom rows | 170 / hour |
| `audit_log` rows | **0 / hour** (nothing in the workload writes audit entries) |
| `pg_database_size()` of the metadatabase | **0.61 MB / hour**, about 1.4 KB per task instance |

Projected: **29 MB at 48 h, 44 MB at 72 h.** The 4 GiB database cap is therefore
roughly 280 days of headroom at this cadence, which means **Postgres is not the
binding disk constraint and the 1.9 GiB fixed floor is.** Three caveats on the
number: Postgres allocates in 8 KiB pages and 1 MiB extents, so a 12-minute window
measures a step function; nothing here runs `VACUUM FULL`, so a genuine multi-day
run is the only way to see whether autovacuum keeps up; and the 0.61 MB/h is a
12-minute slope extrapolated 240-fold, which assumes a linearity that only a long
run can establish. The measurement also covered the metadatabase only, while the
operator leg writes into a second database (`soak_warehouse`) on the same volume;
the monitor now records `db_cluster_bytes` and the cap is applied to the larger of
the two, so the budget covers the disk rather than one database. All of which are
reasons to re-derive the slope from a real long run rather than to trust this
line, which is why the monitor records the inputs on every sample:

```sh
jq -r '[.elapsed_s, .db_bytes, .total_runs, .total_task_instances] | @tsv' \
  .soak/<run>/samples.jsonl | tail -20
```

Take the slope of `db_bytes` against `elapsed_s` over the last third of the run
(the head is dominated by schema and seed data, which is a one-off) and multiply
by 48 h and 72 h.

**What happens when the disk fills.** Not a hope: a cap and a stop condition.
The monitor stops the run **cleanly** and writes `stop_reason` into
`verdict.json` and `summary.md` when any of:

| Condition | Default | Flag |
|---|---|---|
| any database on the soak volume exceeds (summed) | 4 GiB | `--max-db-bytes` |
| the directories the soak writes into exceed (summed: DuckDB scratch, Lite's task logs, the evidence directory) | 4 GiB | `--max-data-bytes` |
| free space on the evidence filesystem drops below | 10 GiB | `--min-free-bytes` |

The stop is checked every sample, before the next sample is taken, and the latch
is sticky. The evidence is safe because it is written continuously: `samples.jsonl`
and `violations.jsonl` are `fsync`ed per record, and `summary.md` / `verdict.json`
are rewritten atomically (temp file plus rename) on every sample. A soak that runs
out of disk mid-write loses at most the record being written, never the report.

The free-space floor is deliberately the largest of the three: filling the
*machine's* disk is a worse outcome than filling the soak's own budget, and 10 GiB
is enough headroom for the operating system to stay comfortable.

### 6.3 Local CPU and memory, and whether the machine stays usable

| Component | Limit | How it is enforced |
|---|---|---|
| Postgres container | **1.0 CPU, 1 GiB RAM, 1 GiB total with swap** | `cpus`, `mem_limit`, `memswap_limit` in `docker-compose.soak.yaml`. Tuned small (`shared_buffers=128MB`, `max_connections=50`) so the limit is not the binding constraint |
| Control plane (`leoflow-server`) | one process, idle between ticks | not capped: capping the thing under test would make the measurement about the cap. Its steady-state cost is one tick per second over an active set under 10 |
| Task subprocesses | **at most 1 long task plus 4 fan-out siblings concurrently** | `max_active_runs=1` on every DAG, `max_active_tasks=4` on `soak_fanout`. That is the concurrency ceiling by construction, not by hope |
| DuckDB memory | a few hundred MiB per task at the default row counts | DuckDB streams; the default 400k-row ingest produces an 8 MiB parquet. `--rows` scales it |
| Monitor | one process, one query burst per 10 s | negligible |

The expected steady state is well under two cores on a laptop, with brief peaks
when the fan-out DAG's four siblings overlap with the ingest DAG. The machine
stays usable. If it does not, `--rows`, `SOAK_FANOUT_ROWS` and `SOAK_SAMPLE_INTERVAL`
are the three knobs, in that order.

### 6.4 Scheduling: local, not CI

The periodic long runs must be **local**. The arithmetic:

A 48 h soak on a GitHub-hosted Linux runner, once a week, is `48 x 60 = 2880`
runner-minutes per occurrence. On a private repository the free allowance is 2000
minutes per month, so **one** weekend soak would exceed the entire monthly budget
by 44%, and a weekly cadence would bill roughly `4 x 2880 - 2000 = 9520`
billable minutes per month. At the published Linux rate of USD 0.008 per minute
that is about **USD 76 per month, for the soak alone**, on a personal card, for a
job whose whole point is that it runs on hardware we already own and pay nothing
for. A weekly 6 h variant is 1440 minutes a month, which fits inside the
allowance only if nothing else in the repository uses it, and the repository uses
it. A hosted runner also gives a
*worse* measurement: the runner is destroyed and recreated per job, so nothing
about long-lived process behavior survives.

So:

* **Long runs: local.** `test/soak/schedule/install.sh` writes a launchd agent
  (macOS) or a systemd user timer (Linux) into the operator's own home directory.
  Nothing about the schedule is committed, and nothing in the repository names an
  account, a hostname, a path outside the repo, or a credential.
* **CI: a short smoke only.** `.github/workflows/soak-smoke.yaml` runs the harness
  for 6 minutes on pull requests that touch `test/soak/**`, plus manual dispatch.
  That is **6 minutes of runner time per touching PR**, an amount that disappears
  into the existing CI budget, and it gates exactly the thing CI is good at: that
  the harness still builds, boots, registers six DAGs, samples, and writes a
  verdict. It does not and cannot gate the long-run behavior.

### 6.5 The kill switch and the maximum

Three independent stops, because an unattended run must not be able to become an
open-ended one:

1. **The monitor's own `--duration`.** Refuses a non-positive value and refuses
   anything above **72 h**. Default **30 min**. The harness parses its own
   `--duration` (which accepts `2d`) and hands the monitor seconds, because Go's
   `time.ParseDuration` has no day unit and would reject `2d` outright.
2. **The harness watchdog.** A background timer that sends `SIGTERM` to the
   harness at `duration + 5 min`, so a hung monitor cannot hold the run open.
3. **`trap cleanup EXIT INT TERM`.** Idempotent teardown on every exit path:
   kills the monitor, the control plane and the fixture, `docker unpause`es the
   Postgres container (a fault injector that died mid-pause would otherwise leave
   it frozen and holding memory), collects the evidence, and stops the container.

The default is conservative on purpose. A weekend run is an explicit
`--duration 48h`, typed by a human who has read this section.

---

## 7. The workload

Six DAG projects in `test/soak/dags/`, all on cron schedules, all with
`max_active_runs=1` so a slow run creates backlog rather than a stampede.

| DAG | Schedule | Shape | What it is in the battery for |
|---|---|---|---|
| `soak_ingest` | `*/5` | write then dependent read-back | generates the data volume with DuckDB; the read-back asserts the bytes survived |
| `soak_fanout` | `*/5` | 1 to 8 to 1, `max_active_tasks=4` | parallelism, and the per-DAG concurrency cap under real pressure (8 ready siblings, 4 slots) |
| `soak_chain` | `*/3` | 10-deep alternating bash/python chain | the most latency-sensitive DAG in the set: its wall-clock is 10 hops of dispatch, so a slowing tick shows here first |
| `soak_operators` | `*/5` | provider operators plus a reschedule sensor | the non-native path, and `up_for_reschedule` |
| `soak_flaky` | `*/7` | fails twice then succeeds; plus a task that always fails | the retry budget and the terminal-failure branch. Every run of this DAG ends `failed` on purpose |
| `soak_long` | `*/15` | one 7-minute task plus a tail | a task that must outlive the 90 s agent-lost threshold without being reaped, and must not wedge the tick for everything else |

Measured on an 18-minute baseline run: the battery produces **65 dag runs per
hour** and **420 task instances per hour**, with at most 4 concurrent runs.

Two deliberate authoring choices worth knowing:

* Helper functions are **inlined into every `dag.py`** rather than imported from a
  shared module. Lite materializes a task's work directory from `dag.json.source`,
  which carries `dag.py` verbatim and nothing else, so a `from soak_common import`
  would resolve at parse time and fail at run time.
* No `python_version:` is declared. Pinning one makes the harness refuse to boot
  on a machine without that exact interpreter, which is a portability failure
  rather than a fidelity win.

---

## 8. What this deliberately does not cover

Stated plainly, because a battery that implies coverage it does not have is worse
than one with an honest gap list.

* **The Kubernetes pod path.** The soak runs Lite's subprocess executor. Nothing
  here exercises pod creation, the kubelet, `activeDeadlineSeconds`, the pod
  reconciler, the pod-lost or warm-worker-lost reapers, or the leader-settling
  gate's interaction with an informer cache. `test/e2e/e2e.sh`,
  `execution-timeout-e2e.sh` and `chaos-runtime.sh` own those, and a long k3d run
  on a laptop trades all of the above for a hot fan and a shorter run.
* **Multi-replica leader election.** One process, so `step_downs > 0` is a
  violation rather than a measurement. Real leader churn under contention needs
  two control planes and belongs in its own experiment.
* **Warm pools, inside the soak.** Kubernetes-only; see section 5.
* **dbt (`dbt_group`).** It would add a fourth task type, and it is genuinely
  local (dbt-duckdb needs nothing external). It is excluded because its
  soak-relevant behavior is fan-out breadth, which `soak_fanout` already produces
  natively, while its cost is a `dbt` binary on `PATH` at compile time. A harness
  that fails to provision on a clean box is a worse trade than a missing task
  type. Three e2e gates already cover dbt compilation and execution.
* **Redis.** Lite is Redis-free by construction (ADR 0026). XCom sits on Postgres
  here, which is the Lite path, not the Pro path.
* **External secret backends.** ADR 0060 resolution is gated by
  `test/e2e/secrets-localstack.sh` and needs LocalStack.
* **At-most-once, as an assertion.** C3 is in the battery and holds, but it
  cannot fire on anything this workload does (see section 1). Until a per-attempt
  execution ledger exists, the suite does not prove at-most-once and must not be
  described as proving it. `test/e2e/chaos-runtime.sh` is what owns that property
  today.
* **Any claim about a weekend that was not run.** The harness reports what it
  measured. Numbers in a PR body that were not produced by a `samples.jsonl` in
  the tree are not results.

---

## 9. Files

| Path | What it is |
|---|---|
| `soak.sh` | the harness: provision, run, assert, collect, tear down. One command |
| `docker-compose.soak.yaml` | the dedicated Postgres, resource-capped |
| `dags/` | the six DAG projects |
| `fixture/` | the loopback HTTP server the operator leg points at |
| `monitor/` | the continuous invariant checker (`package main`, `test/load` convention) |
| `warmpool-ab.sh` | the k3d warm-pool A/B (written, not yet executed) |
| `schedule/install.sh` | installs a local launchd agent or systemd user timer |

### Evidence layout

Every run writes into `.soak/<timestamp>-<label>/` (git-ignored):

| File | Contents |
|---|---|
| `samples.jsonl` | one JSON object per sample. The machine-readable record |
| `violations.jsonl` | one object per invariant violation, with the evidence attached |
| `summary.md` | the human summary, rewritten atomically every sample |
| `verdict.json` | the bottom line, also rewritten every sample |
| `faults.jsonl` | the fault windows the harness declared |
| `lite.log`, `fixture.log`, `monitor.log` | process logs |
| `metrics-final.prom`, `pg-table-sizes.txt`, `disk-usage.txt`, `environment.txt` | end-of-run snapshots |

## 10. First runs

Three bounded runs on an arm64 macOS laptop, all short. **No long run has been
executed. Nothing below is a weekend result**, and the numbers that matter most
(whether tick cost drifts as the history grows past a few hundred runs, whether
autovacuum keeps up) need hours the harness has not yet been given.

| Run | Duration | Faults | Verdict | What it establishes |
|---|---|---|---|---|
| baseline | 18 min, 109 samples | none | **PASS**, 0 violations | the harness boots, all six DAGs register and run, every task type executes, and none of the invariants fires under a healthy control plane. Read with the review caveat below: three of those invariants could not have fired in that run |
| selftest-red | 8 min, 34 samples | `selftest-red` | **FAIL**, 8 violations, exit 1 | the assertions fire: a 300 s Postgres outage declared as a 45 s window produced 4 `scheduler_unhealthy` and 4 `cadence_starved` violations, the first landing 160 s after the declared window closed, which is the 120 s recovery budget plus sampling lag: with the database paused, every query waited out its timeout and the sampling interval stretched from 10 s to about 22 s |
| verify | 7 min, 43 samples | none | **PASS**, 0 violations | the same result after the code was refactored for lint, so the PASS is reproducible and not a one-off |

Measured on the baseline run:

| Signal | Value |
|---|---|
| dag runs completed | 28 (24 success, 4 failed; the 4 are `soak_flaky`, which fails on purpose) |
| task instances | 175 |
| dispatch latency p50 / p95 / p99 | **20 / 47 / 54 ms** |
| dispatch latency by type (p50) | `bash` 16 ms, `python` 22 ms, `airflow_operator` 25 ms |
| tick probe p50 across the whole run | **2.03 ms**, max 10.8 ms |
| tick probe against active runs | 3.41 ms at 3 active, 1.56 ms at 1 active |
| tick probe against total runs | **flat** while total runs went 8 to 27 |
| database growth | 0.61 MB/h |

The tick-probe columns are the interesting pair. At this scale the probe tracks
the **active** set and shows no sign of tracking the history, which is the shape
`test/load/scheduler_ceiling` predicts from `ActiveRuns` being `1 + 2N` in active
runs. But 27 runs is not a test of the history hypothesis: it takes a multi-hour
run to move that column by an order of magnitude, and until one has been done the
honest statement is that the soak found no drift in the range it observed, not
that there is none.

### What those tick-probe numbers can and cannot support

Four limits, all of them structural rather than fixable by running longer:

* **It is a proxy, not the loop.** The probe times `SchedulerStore.ActiveRuns`
  from a second process on its own pool. It excludes the per-run advance, the
  dispatch enqueue, the write half of a tick and any lock wait the scheduler's
  own transaction takes, and it runs on a connection nothing is contending for.
  It is the read half of a tick measured under better conditions than a tick has.
* **The two conclusions come from one correlated sample.** "Falls with fewer
  active runs" and "flat against total runs" were read off the same 109 samples,
  in which the active-run count and the total-run count both co-vary with elapsed
  time. A single run cannot separate those axes; only holding one constant while
  moving the other can, and this workload holds neither.
* **The range is 8 to 27 runs, a factor of 3.4.** Every table involved fits in
  `shared_buffers` throughout, so an index or scan cost that appears at a working
  set larger than memory cannot appear here at all.
* **Nothing controlled for the machine.** The first samples of a run pay pgx's
  statement preparation, autovacuum can land inside any sample, and a laptop
  doing other work is not excluded, because CPU and RSS were deliberately not
  collected. The statement-cache effect in particular makes early samples slower,
  which biases a run *towards* looking flat. The monitor now also times a trivial
  `SELECT 1` in the same sample and prints it beside the probe, so a reader can
  tell "ActiveRuns got more expensive" from "everything did"; the runs above
  predate that column and have no such control.

The defensible reading of the baseline is: at an active set under 4 and a history
under 30 runs, `ActiveRuns` cost about 2 ms, moved with the active set in a way
consistent with `1 + 2N`, and showed no trend against history over a range too
small for one to show. Nothing beyond that.

### Review caveat on the three runs above

Those runs were made with the monitor scraping `/metrics` from the API port,
which does not serve it. The scrape returned 404 on every sample and was read as
three zeros, so `leader_churn` and `undispatchable_task` could not have fired in
any of them, and `dispatch_at_capacity_total` was recorded as 0 rather than as
unread. `upstream_failed_task_ran` keyed on a state `never_runs` cannot reach, so
it could not have fired either. All three are fixed in the current tree (the
monitor refuses to start unless the metrics listener answers), but the PASS
verdicts above were produced before the fixes and cover fewer invariants than
their "0 violations" suggests. They should be re-run before anyone cites them.

One observation worth following up, from the selftest-red evidence: the
`scheduler_unhealthy` streak reset to 0 s mid-outage (violations at 348 s, 370 s,
392 s report streaks of 0 s, 22 s, 44 s, then 0 s again at 436 s), which means
`/api/v2/monitor/health` reported the scheduler healthy for at least one sample
while the metadatabase was paused. That is a health-endpoint question, not a
harness question, and it is the kind of thing this battery exists to surface.

## 11. Relationship to the other test directories

| Directory | Question it answers |
|---|---|
| `test/e2e/` | does this path work, once, end to end |
| `test/load/` | what does one operation cost at one instant, at a given width |
| `test/soak/` | is the system still behaving after hours or days, and does the cost of a tick change as the database ages |

The monitor follows the `test/load` convention on purpose: a small `package main`
driving the real control-plane code against a real Postgres, printing a table. It
diverges in one way, and the divergence is the point: a load experiment seeds and
measures, while the monitor only reads. Every query it issues is a `SELECT`, and
the one product call it makes (`SchedulerStore.ActiveRuns`) mutates nothing. A
monitor that could perturb the system it measures would be worse than no monitor.
