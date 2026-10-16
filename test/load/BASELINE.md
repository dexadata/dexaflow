# Performance baseline

Numbers every performance change is measured against. A PR that changes one of
these paths quotes the matching row before and after its change, run on the same
machine; when a batch of the performance plan closes, its rows are updated here.

Recorded 2026-10-02 on `main` at `fc4b56c` (the benchmarks come from PR 1306), on a 4 vCPU Intel Xeon at 2.10 GHz
with 15 GB of RAM, Go 1.26.6 and Postgres 16.14 with default settings
(`shared_buffers` 160 MB, `work_mem` 4 MB). Absolute times move with the
machine; compare ratios, and re-record both sides on the same host.

## Go benchmarks

```sh
go test -run '^$' -benchmem -count 5 \
  -bench 'PlanRunFanOut|StepSteadyState|StepQueueFanOut|ActiveRunProjection|TopoSortFanOut|StaticHandlerBundle|WarmPoolTick' \
  ./internal/scheduler/ ./internal/storage/ ./internal/api/ ./internal/ui/ ./internal/executor/
```

Median of 5 runs.

| Benchmark | Case | Time | Memory | Allocations |
|---|---|---:|---:|---:|
| `BenchmarkStepSteadyState` | 100 runs x 20 tasks | 0.53 ms | 515 KB | 1,218 |
| `BenchmarkStepSteadyState` | 1,000 runs x 20 tasks | 4.76 ms | 5.2 MB | 12,024 |
| `BenchmarkStepSteadyState` | 200 runs x 500 tasks | 19.1 ms | 30.1 MB | 2,418 |
| `BenchmarkStepQueueFanOut` | 1,000 tasks | 5.14 ms | 494 KB | 62 |
| `BenchmarkStepQueueFanOut` | 5,000 tasks | 118 ms | 2.7 MB | 111 |
| `BenchmarkPlanRunFanOut` | 100 tasks | 22.6 µs | 20.7 KB | 11 |
| `BenchmarkPlanRunFanOut` | 1,000 tasks | 242 µs | 301 KB | 17 |
| `BenchmarkPlanRunFanOut` | 5,000 tasks | 1.34 ms | 1.3 MB | 53 |
| `BenchmarkActiveRunProjection` | 20 tasks | 9.1 µs | 16.4 KB | 47 |
| `BenchmarkActiveRunProjection` | 500 tasks | 153 µs | 430 KB | 207 |
| `BenchmarkTopoSortFanOut` | 1,000 tasks | 3.14 ms | 842 KB | 1,030 |
| `BenchmarkTopoSortFanOut` | 5,000 tasks | 68.6 ms | 4.0 MB | 5,070 |
| `BenchmarkStaticHandlerBundle` | 5 MB bundle, gzip | 417 ms | 9.1 MB | 63 |
| `BenchmarkStaticHandlerBundle` | 5 MB bundle, identity | 3.18 ms | 10.5 MB | 28 |
| `BenchmarkWarmPoolTick` | 60 versions, run | see below | 50.2 KB | 51 |
| `BenchmarkWarmPoolTick` | 60 versions, rationed prewarm | see below | 62.3 KB | 88 |

`BenchmarkWarmPoolTick` (#1906) came after the recording above, and its time
has not been recorded on the baseline host yet: re-record it with the next
update of this table (B3 of the release checklist). Its memory and allocations
do not depend on the machine (they do on the Go version: the Go 1.26.6 of the
header applies). It measures one steady warm pool reconcile tick,
the work the leader repeats every warm pool interval, over 60 versions of 3
tenants under a per-tenant cap, with the Kubernetes and database reads faked
and nothing to create or delete. The second case adds 30 prewarm-only versions
the cap rations (ADR 0082). Before #1906 the first case cost 75.1 KB and 238
allocations per tick and the second 84.7 KB and 286 (v0.5.3 had 57.6 KB and
226 for the first); the v0.5.4-rc.1 recheck measured the first case at
68.8 µs at v0.5.4 against 55.4 µs at v0.5.3 on a quiet 4 vCPU host.

What stands out:

- Queueing a fan out grows faster than linearly: 5x the tasks costs 23x the time
  (5.1 ms to 118 ms), all inside one scheduler tick.
- The topological sort grows the same way: 5x the tasks costs 22x the time.
- Serving the UI bundle with gzip recompresses it on every request: 417 ms of
  CPU per request against 3.2 ms without compression.

## Query plans at scale

```sh
DATABASE_URL=... go run ./test/load/query_plans
```

Dataset: 1,000 DAGs, 1,000,000 DAG runs, 5,000,000 task instances and 1,000,000
audit rows in one tenant, about 2.3 GB. Postgres execution time from
`EXPLAIN (ANALYZE, BUFFERS)`, median of 4 runs after one warm up run. See
[Experiment 6](README.md#experiment-6-query-plans-at-scale).

| Query | Caller | Median ms | Best ms | Worst ms | Trigger ms | Seq scans |
|---|---|---:|---:|---:|---:|---|
| ListDagsFiltered (state) | /ui/dags state chip | 320.0 | 296.6 | 336.3 | 0.0 | none |
| ListDagsFiltered | /ui/dags | 0.0 | 0.0 | 0.0 | 0.0 | none |
| CountDagsFiltered (state) | /ui/dags state chip | 356.6 | 313.8 | 370.1 | 0.0 | dags |
| CountDagsByLatestRunState | dashboard | 452.9 | 422.7 | 461.2 | 0.0 | none |
| CountDagRunStatesInWindow | dashboard history | 71.6 | 70.4 | 74.7 | 0.0 | dag_runs, dags |
| CountTaskInstanceStatesInWindow | dashboard history | 710.2 | 618.8 | 750.2 | 0.0 | dag_runs, dags, task_instances |
| ListActiveDagRuns | scheduler tick | 0.1 | 0.1 | 0.2 | 0.0 | none |
| ListRunningTasks | pod-lost reaper | 2.1 | 1.9 | 2.1 | 0.0 | dags |
| ListActiveStagingVolumes | staging GC | 403.8 | 364.8 | 499.9 | 0.0 | dag_runs, staging_volumes |
| PoolSlotUsage | /api/v2/pools | 296.0 | 259.7 | 305.3 | 0.0 | task_instances |
| CountAuditLogs (dag) | audit page | 66.4 | 57.1 | 69.6 | 0.0 | audit_log |
| ListAuditLogs (dag) | audit page | 11.0 | 10.7 | 11.1 | 0.0 | none |
| DeleteDag | DAG deletion | 4767.7 | 4667.0 | 5193.2 | 4766.4 | none |

What stands out:

- Deleting a DAG with 51 versions takes 4.8 s, all of it in FK triggers:
  `dag_runs.dag_version_id` has no index, so each deleted version scans
  `dag_runs`.
- The latest run state queries (`ListDagsFiltered` with a state filter,
  `CountDagsFiltered`, `CountDagsByLatestRunState`) take 320 to 450 ms because
  their `DISTINCT ON (dag_id)` walks every run.
- `ListActiveStagingVolumes` runs every minute and takes 400 ms: the join casts
  `dag_runs.id` to text, which rules out the primary key index.
- `PoolSlotUsage` scans all 5,000,000 task instances to count a handful of
  active ones.

## Targets

The gates of the performance plan, against the rows above.

| Gate | Baseline | Target |
|---|---|---|
| Scheduler tick, 1,000 active runs x 20 tasks (batch 3) | 4.76 ms | under 2 ms |
| UI bundle served with gzip (batch 4) | 417 ms | under 5 ms |
| Hot queries above, at this dataset size (batch 2) | up to 710 ms | each under 20 ms |
| DeleteDag with 51 versions (batch 2) | 4,768 ms | under 100 ms |

## Accepted changes of the bar

A B1 row of `test/release/perf-reliability-checklist.md` may exceed the 10% bar
only with an entry here: the benchmark, the release, the measured delta, the
lines that cost the time and why the cost is not a regression of production.
Re-check an entry when the code it names changes.

### `BenchmarkStepQueueFanOut`, v0.5.4 (dispatch tick budget, #1728)

- **Delta.** v0.5.3 to v0.5.4-rc.1 (`f69451b1`): +19.7% at 1,000 tasks and
  +15.7% at 5,000 tasks, sec/op, p <= 0.002 (n=8; confirmed with n=10 and
  n=12 interleaved). Memory and allocations within the bar (+0.1% B/op, +4
  allocs/op).
- **Cause.** `holdDispatch` in `internal/scheduler/scheduler.go` reads the
  tick's clock before every dispatch to check `scheduler.dispatch.tick_budget`,
  and `launchQueued` counts each dispatch per tenant for
  `scheduler.dispatch.max_per_tenant_per_tick`. On the 4 vCPU reference host a
  clock read costs 72 ns and the map increment 12 ns. The benchmark dispatches
  every task of a 5,000-task fan-out to an in-memory store and a no-op
  dispatcher, about 700 ns a task, so those reads are 12% to 15% of it. With
  both lines removed the benchmark is level with v0.5.3 (p = 0.16 and 0.71);
  no other change in the range costs anything measurable.
- **Why it stays.** The read is the feature: the budget must be checked before
  each dispatch, because one throttled pod create takes 200 ms under the
  default client limit and the budget exists to stop a fan-out in time (R-03,
  `TestSyncDispatchThrottleDoesNotStallOtherRuns`). Checking every N
  dispatches would overshoot it by N creates. In production each dispatched
  task also writes its queued state to Postgres and creates a pod or enqueues a
  buffered dispatch, hundreds of microseconds at least, so the read adds under
  0.03% to a real tick. The tick benchmarks without dispatch
  (`BenchmarkStepSteadyState`, `BenchmarkTickWithMetrics`) are unchanged, and
  the rc soak tick probe was 1.2 ms with no violations.
- **Measured alternative.** Counting per tenant only while a cap is set,
  passing the run by pointer and reading only the monotonic clock
  (`time.Since`) is 4% to 5% faster and keeps the behavior, but still leaves
  the row 13% to 15% over v0.5.3, and a timer that flips a flag at the deadline
  would change how the budget is enforced to save nanoseconds a task. Neither
  is worth a change in a patch.
- **New reference.** From v0.5.4 on, B1 compares `BenchmarkStepQueueFanOut`
  with v0.5.4, not v0.5.3.
- **Evidence.** release tracking issue for v0.5.4, review 8 line P1; profiles
  and interleaved runs in the rc.1 `perf-p1` evidence folder.
