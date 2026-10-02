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
  -bench 'PlanRunFanOut|StepSteadyState|StepQueueFanOut|ActiveRunProjection|TopoSortFanOut|StaticHandlerBundle' \
  ./internal/scheduler/ ./internal/storage/ ./internal/api/ ./internal/ui/
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
