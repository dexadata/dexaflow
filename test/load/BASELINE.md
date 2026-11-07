# Performance baseline

Numbers every performance change is measured against. A PR that changes one of
these paths quotes the matching row before and after its change, run on the same
machine; when a batch of the performance plan closes, its rows are updated here.

Each section names the commit, release and host it was recorded on. The Go
benchmarks, the scheduler tick read and the query plans were re-recorded
2026-11-06 on `main` at `3c8c1ede` (the v0.5.9 candidate: v0.5.9-rc.2 plus
#2373) with the v0.5.10 benchmark changes of #1617 and #2324, on a 4 vCPU Intel
Xeon at 2.10 GHz with 15 GB of RAM (the host of the v0.5.0 recording), Go
1.26.10 and Postgres 16.15 with default settings. They describe v0.5.9, after
the v0.5.1 performance changes. Absolute times move with the machine; compare
ratios, and re-record both sides on the same host. The v0.5.0 numbers they
replace are in the history of this file (`fc4b56c`).

## Go benchmarks

```sh
go test -run '^$' -benchmem -count 5 \
  -bench 'PlanRunFanOut|StepSteadyState|StepQueueFanOut|ActiveRunProjection|TopoSortFanOut|StaticHandlerBundle|WarmPoolTick' \
  ./internal/scheduler/ ./internal/storage/ ./internal/api/ ./internal/ui/ ./internal/executor/
```

Median of 5 runs.

| Benchmark | Case | Time | Memory | Allocations |
|---|---|---:|---:|---:|
| `BenchmarkStepSteadyState` | 100 runs x 20 tasks | 0.455 ms | 406 KiB | 1,423 |
| `BenchmarkStepSteadyState` | 1,000 runs x 20 tasks | 4.73 ms | 4.07 MiB | 14,029 |
| `BenchmarkStepSteadyState` | 200 runs x 500 tasks | 20.9 ms | 17.9 MiB | 3,023 |
| `BenchmarkStepQueueFanOut` | 1,000 tasks | 0.583 ms | 479 KiB | 89 |
| `BenchmarkStepQueueFanOut` | 5,000 tasks | 3.28 ms | 2.62 MiB | 140 |
| `BenchmarkPlanRunFanOut` | 100 tasks | 19.2 µs | 17.4 KiB | 12 |
| `BenchmarkPlanRunFanOut` | 1,000 tasks | 182 µs | 182 KiB | 14 |
| `BenchmarkPlanRunFanOut` | 5,000 tasks | 1.01 ms | 859 KiB | 26 |
| `BenchmarkActiveRunProjection` | 20 tasks | 10.2 µs | 17.0 KiB | 52 |
| `BenchmarkActiveRunProjection` | 500 tasks | 178 µs | 455 KiB | 212 |
| `BenchmarkTopoSortFanOut` | 1,000 tasks | 0.745 ms | 839 KiB | 1,031 |
| `BenchmarkTopoSortFanOut` | 5,000 tasks | 3.83 ms | 3.93 MiB | 5,071 |
| `BenchmarkStaticHandlerBundle` | 5 MB bundle, gzip | 0.574 ms | 2.01 MiB | 36 |
| `BenchmarkStaticHandlerBundle` | 5 MB bundle, identity | 6.78 ms | 16.0 MiB | 42 |
| `BenchmarkWarmPoolTick` | 60 versions, run | 50.2 µs | 52.8 KiB | 51 |
| `BenchmarkWarmPoolTick` | 60 versions, rationed prewarm | 72.6 µs | 64.4 KiB | 88 |

`BenchmarkActiveRunProjection` now calls `projectRunState`, the function
`ActiveRuns` runs for each run (#1617); before, it timed a copy of that code, so
its rows are not comparable with the v0.5.0 ones. `BenchmarkWarmPoolTick`
(#1906) measures one steady warm pool reconcile tick over 60 versions of 3
tenants under a per-tenant cap, with the Kubernetes and database reads faked
and nothing to create or delete; the second case adds 30 prewarm-only versions
the cap rations (ADR 0082).

What stands out:

- Against v0.5.0, the v0.5.1 changes show where they were aimed: queueing a
  5,000-task fan out went from 118 ms to 3.3 ms, the topological sort of 5,000
  tasks from 68.6 ms to 3.8 ms, and serving the UI bundle with gzip from 417 ms
  to 0.57 ms (the compressed bundle is cached).
- `BenchmarkStepSteadyState` at 1,000 runs x 20 tasks is level with v0.5.0
  (4.73 ms against 4.76 ms): the tick's planning cost did not move, and its
  database read, which dominates the real tick, is below.

## Scheduler tick read

```sh
make bench-integration BENCH_DATABASE_URL='postgres://...'   # a migrated database of its own
```

`BenchmarkActiveRunsIntegration` (#2324) reads every active run the way the
tick does, through `SchedulerStore.ActiveRuns` on a real Postgres. It seeds 200
running runs of one 500-task DAG, 100,000 task instances spread over the states
of a busy run (40% success, 20% running, 20% none, 10% queued, 10% failed) with
the pod, node, host, log and error columns a real attempt fills. Median of 5
runs, spec cache warm.

| Benchmark | Case | Time | Memory | Allocations |
|---|---|---:|---:|---:|
| `BenchmarkActiveRunsIntegration`, v0.5.10 | 200 runs x 500 task instances | 555 ms | 555 MiB | 1,326,364 |
| `BenchmarkActiveRunsIntegration`, #2325 | 200 runs x 500 task instances | 140 ms | 90 MiB | 363,600 |

This is the read before planning, every tick. Before #2325 it was 27 times the
planning cost of the same 200 x 500 case above (`BenchmarkStepSteadyState`,
20.9 ms on a fake store). #2325 reads only the twelve columns the projection
uses, drops the server-side sort (it spilled to disk at this size), sizes the
result from the previous tick and stops allocating a scan target per row:
benchstat over 6 runs on the same host shows -74% time and -84% memory. The
query now runs in about 48 ms on the server (`EXPLAIN ANALYZE`, from 174 ms);
most of what is left is decoding 100,000 rows that are mostly settled, which
#2332 (read settled rows only when a run changed) removes. It is the bar of B4
in the release checklist.

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
| ListDagsFiltered (state) | /ui/dags state chip | 4.3 | 3.5 | 4.5 | 0.0 | none |
| ListDagsFiltered | /ui/dags | 0.2 | 0.2 | 0.3 | 0.0 | none |
| CountDagsFiltered (state) | /ui/dags state chip | 5.0 | 3.3 | 5.8 | 0.0 | dags |
| CountDagsByLatestRunState | dashboard | 3.6 | 3.4 | 3.7 | 0.0 | dags |
| CountDagRunStatesInWindow | dashboard history | 66.0 | 63.2 | 67.7 | 0.0 | dag_runs, dags |
| CountTaskInstanceStatesInWindow | dashboard history | 811.5 | 793.4 | 816.5 | 0.0 | dag_runs, dags, task_instances |
| ListActiveDagRuns | scheduler tick | 0.6 | 0.5 | 0.6 | 0.0 | none |
| ListRunningTasks | pod-lost reaper | 1.8 | 1.6 | 1.9 | 0.0 | dags |
| ListActiveStagingVolumes | staging GC | 0.7 | 0.7 | 0.7 | 0.0 | staging_volumes |
| PoolSlotUsage | /api/v2/pools | 0.3 | 0.3 | 0.3 | 0.0 | none |
| CountAuditLogs (dag) | audit page | 0.7 | 0.6 | 0.9 | 0.0 | none |
| ListAuditLogs (dag) | audit page | 0.1 | 0.1 | 0.1 | 0.0 | none |
| ListAuditLogsAfter (dag) | audit page by cursor | 0.1 | 0.1 | 0.1 | 0.0 | none |
| ListDagRunsByDagAfter | /api/v2 dag runs by cursor | 0.1 | 0.1 | 0.2 | 0.0 | none |
| DeleteDag | DAG deletion | 76.0 | 55.0 | 76.1 | 75.1 | none |

What stands out:

- Every query the v0.5.1 changes targeted is now a few milliseconds: the latest
  run state queries went from 320 to 450 ms to 3.6 to 5.0 ms,
  `ListActiveStagingVolumes` from 404 ms to 0.7 ms, `PoolSlotUsage` from
  296 ms to 0.3 ms, and `DeleteDag` with 51 versions from 4.8 s to 76 ms.
- The dashboard history counts still count every row of their window:
  `CountTaskInstanceStatesInWindow` takes 812 ms here (710 ms at v0.5.0, inside
  the noise of a seed that differs run to run) and `CountDagRunStatesInWindow`
  66 ms. They are the only rows above 20 ms; the release checklist compares
  them with the previous release.

## Code-only deploy

```sh
make build
PYTHONPATH=parser go run ./test/load/deploy_p50 --cli bin/dexaflow \
  --config <cli config> --local-registry --n 20 [--dependency pandas==2.2.3]
```

Recorded 2026-10-17 on `main` at `8d50751b` with the harness of #1892, on a 4
vCPU Intel Xeon at 2.80 GHz with 15 GB of RAM (not the host of the tables
above), Go 1.26.9, uv 0.11.32, the parser on the host's Python 3.13 and a
control plane with `executor.type=subprocess` on Postgres 16.15 on the same
host. The registry is the harness's in-memory loopback registry, so the times
are the CLI and the register with no network to the registry. Each run is one
cold deploy, then 20 deploys that each change one comment line of `dag.py`.
See [Experiment 7](README.md#experiment-7-code-only-deploy-and-warm-up).

| Dependencies | Cold deploy | Code-only p50 | p95 | Max | Builder of every measured deploy | Registry requests per code-only deploy |
|---|---:|---:|---:|---:|---|---:|
| `requests==2.32.3` | 0.51 s | 0.33 s | 0.35 s | 0.35 s | `code-layer` | 34 |
| `pandas==2.2.3` | 4.66 s | 0.32 s | 0.35 s | 0.37 s | `code-layer` | 34 |

What stands out:

- A code-only deploy does not depend on the size of the dependencies: the
  pandas image costs 4.7 s to build once, and its code-only deploys cost what
  the requests ones do. The cold deploys reused the uv cache of earlier runs,
  so they are not the time of a first build on a clean machine.
- With the registry on the loopback, a code-only deploy is about a third of a
  second, 90 times under the 30 s target. On a remote registry the 34 requests
  are round trips: at 50 ms each they add about 1.7 s, plus the code layer
  upload. Nine of the 34 are the `GET /v2/` ping each new registry client
  sends first; the others look for an image of this content label, read the
  base and the dependency image, push the code layer and the image config,
  push the image and its index, and read the pushed tag back for its digest.
- The warm-up after a code-only deploy (checklist row I2) needs a cluster
  with warm pools on and has not been recorded yet: record it with the next k3d
  run of `deploy_p50 --warm`.

## UI refresh cycle

Experiment 4 (`test/load/ui_refresh`, #1196), recorded 2026-10-23 on `main` at
`5bb7a7b` plus this experiment, on a 4 vCPU Intel Xeon at 2.80 GHz with 15 GB
of RAM, Go 1.26.10 and Postgres 16.15 with default settings. The harness, one
api role process (`ui.autoRefreshIntervalSeconds` irrelevant here: the harness
paces itself) and Postgres shared the host. The database held 200 DAGs of 500
hourly runs and 2 tasks each (100,000 runs, 200,000 task instances); the DAG
replayed has a run in progress. 10 s per row.

| Profile | Viewers | Cycles/s | Cycle p50 ms | Cycle p95 ms | DB transactions per cycle |
|---|---:|---:|---:|---:|---:|
| `dag` (9 requests) | 1 | 191.7 | 4.9 | 7.3 | 27.8 |
| `dag` | 10 | 277.2 | 34.8 | 54.8 | 25.0 |
| `dag` | 50 | 278.0 | 179.7 | 218.7 | 26.7 |
| `run` (9 requests) | 1 | 185.5 | 5.0 | 7.6 | 28.4 |
| `run` | 10 | 286.5 | 33.5 | 52.0 | 29.9 |
| `run` | 50 | 301.9 | 163.4 | 205.2 | 33.1 |
| `idle` (1 request) | 1 | 628.9 | 1.5 | 2.1 | 3.2 |
| `idle` | 10 | 2,802.3 | 3.2 | 6.5 | 2.7 |
| `idle` | 50 | 2,750.2 | 17.0 | 33.0 | 3.0 |

Read as capacity: about 280 cycles/s on a page with a run in progress is about
280 viewers of such a page at a 1 s interval, 1,400 at 5 s and 8,400 at 30 s,
with the whole host shared. One such viewer costs about 27 transactions per
interval, so 50 of them at 5 s are about 270 transactions per second.

## Usage event trigger

Experiment 8 (#2174), measured when migration 055 landed on a 4 vCPU
container with Postgres 16 on the same host, `--tasks 1000 --samples 20`,
the range of the medians over several runs on an idle database (a run right
after the integration suite read 10 times higher, so measure on a quiet one):

| Case | Switch off | Switch on |
|---|---:|---:|
| Single settle, median | 0.7 to 1.1 ms | 0.8 to 1.1 ms |
| Cascade of 1,000 tasks, median | 21 to 24 ms | 41 to 42 ms |

With the switch off (the default) the trigger's `WHEN` clause keeps it from
being called for any other write, and for these it returns after reading the
switch row. The harness compares the switch off and on; it does not measure a
schema without the trigger, which differs from the off column by that `WHEN`
check and one row read per finished attempt. With the switch on, a single
settle costs about the same within the noise, and a cascade about 20 µs per
task for the revision upsert and the insert. ADR 0096 names a
statement-level trigger as the fallback, but its transition tables would be
built for every update of `task_instances`, with the switch off too, so it
would move a cost onto installs that do not record; the row trigger stays.

## Primary key order

Experiment 9 (`test/load/uuid_keys`, #525), recorded 2026-10-28 on `main` at
`2a0bf40a` plus this experiment, on a 4 vCPU Intel Xeon at 2.80 GHz with
15 GB of RAM, Go 1.26.10 and Postgres 16.15 with default settings
(`shared_buffers` 128 MB), the harness and Postgres on the same host, with
other test runs sharing it (both keys were measured in turns, so they shared
the same load). 5,000,000 rows per key, 1,000 per transaction.

| Key | Rows | Insert rows/s | Last tenth rows/s | WAL per row | PK index | Table | PK lookups/s |
|---|---:|---:|---:|---:|---:|---:|---:|
| UUIDv4 | 5000000 | 61069 | 54915 | 244 B | 191 MiB | 640 MiB | 9221 |
| UUIDv7 | 5000000 | 82694 | 84803 | 183 B | 150 MiB | 640 MiB | 11061 |

What it says:

- Once the key index outgrows the cache, a random key costs a third of the
  insert rate (55,000 against 85,000 rows per second over the last tenth),
  a third more WAL per row (full page images of the leaves it touches after
  each checkpoint) and a key index 27% larger (leaves split half full). A
  time-ordered key appends to the right edge and keeps its rate flat.
- Point lookups of random rows by key are about a sixth slower with the
  random key, whose larger index fits the cache less well.
- The table itself is the same size: the heap is append-only whatever the key.
- The control plane inserts task instances at a small fraction of these
  rates, so the key order is not a throughput limit today. It shows up as
  WAL volume, index bloat and cache pressure on a large install, which is
  where #525 asked to look.

A change of key would be one `ALTER TABLE ... ALTER COLUMN id SET DEFAULT`
per hot table, with a UUIDv7 generator (Postgres 18 has `uuidv7()`; on 16 and
17 it is a SQL function or the application). Existing ids keep working, since
both are `uuid`. A UUIDv7 carries its creation time in its first 48 bits, and
these ids are served by the API, so the change needs its own decision.

## Targets

The gates of the performance plan, set against the v0.5.0 numbers, and where
the v0.5.9 rows above stand. Two were never met and are no longer absolute bars
of the release checklist; its section "Amended bars and known failures" says
why.

| Gate | v0.5.0 | Target | v0.5.9 (rows above) |
|---|---|---|---|
| Scheduler tick, 1,000 active runs x 20 tasks (batch 3) | 4.76 ms | under 2 ms | Not met: 4.73 ms. The checklist compares it with the previous release (B1) instead |
| UI bundle served with gzip (batch 4) | 417 ms | under 5 ms | Met: 0.57 ms. Still an absolute bar (B2) |
| Hot queries above, at this dataset size (batch 2) | up to 710 ms | each under 20 ms | Met except the dashboard history counts (66 ms and 812 ms), which count every row of their window; the checklist compares them with the previous release (C1) |
| DeleteDag with 51 versions (batch 2) | 4,768 ms | under 100 ms | Met: 76 ms. Still an absolute bar (C1) |

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
