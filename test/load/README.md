# Load tests

Load/scale harnesses for Dexaflow's control plane. The README calls load tests
the remaining Phase 6 gap; this directory is where they land, one experiment at
a time.

Each experiment is a small `package main` that drives the **real** control-plane
code (not a mock) against a **real** Postgres and prints a table. They boot no
cluster and no executor, so they run on a laptop with just Postgres up.

## Experiments

| # | Name | Dir | Status |
|---|------|-----|--------|
| 1 | Scheduler ceiling — per-tick cost vs. active runs `N` | [`scheduler_ceiling/`](scheduler_ceiling/) | ✅ implemented |
| 2 | Dispatch throughput — queue depth vs. arrival rate | — | not yet (see _Adding an experiment_) |
| 3 | Reaper cost — orphan/heartbeat sweep vs. run count | — | not yet |
| 4 | API read fan-out — dashboard queries under many DAGs | — | not yet |
| 5 | Log tailer — SSE fan-out under many concurrent viewers | — | not yet |
| 6 | Query plans at scale: hot queries on 1M runs and 5M task instances | [`query_plans/`](query_plans/) | ✅ implemented |

Experiments 1 and 6 are built. The others are documented extension points.

## Prerequisites

A migrated Postgres with the `default` tenant (migration `001` seeds it).

```sh
# 1. Postgres (leoflow/leoflow on :5432, database `leoflow`)
docker compose -f docker-compose.dev.yaml up -d postgres

# 2. Apply migrations (installs the migrate CLI first if you don't have it:
#    make install-migrate)
export DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable'
migrate -path migrations -database "$DATABASE_URL" up
```

Any migrated database works — point `DATABASE_URL` at it. `make test-db` (the
`leoflow_test` database) is a fine target too.

## Experiment 1: scheduler ceiling

The leader scheduler runs `Scheduler.Step` once per tick (~1s). `Step`'s dominant
read is `Store.ActiveRuns`, which issues **1 + 2N** DB queries (one list, then
`GetDagVersionByID` + `ListTaskInstancesByRun` per run) and **N** spec unmarshals,
then advances every run **serially**. Experiment 1 measures how one tick's
wall-time grows with the number of active runs `N`, so we can find where a tick
approaches the loop interval (the ceiling).

The harness seeds `N` synthetic DAGs, each with one run pinned `running` and a
`running` task instance. Such a run stays in the active set every tick while
`advance` stays a no-op for it (nothing to dispatch, nothing to finalize) — so
what is timed is the steady-state read + per-run advance cost. `N` values are
seeded **incrementally** (ascending), so measuring `50,200,500,1000` seeds 1000
DAGs total, not 1750.

```sh
DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable' \
  go run ./test/load/scheduler_ceiling --n 50,200,500,1000 --window 5s
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--db` | `$DATABASE_URL`, then `$LEOFLOW_DATABASE_URL` | Postgres URL |
| `--n` | `50,200,500,1000` | active-run counts to measure (sorted ascending) |
| `--window` | `5s` | per-`N` window; `Step` is called back-to-back until it elapses |
| `--tasks` | `1` | task instances per synthetic run |
| `--warmup` | `3` | warm-up ticks discarded before timing each `N` |
| `--keep` | `false` | keep the synthetic DAGs/runs instead of deleting them on exit |

Output is a table of `N → loop-duration p50/p95/p99/max/mean` plus the step-down
delta observed during each window. The harness cleans up every DAG it created on
exit (best-effort); a fresh `migrate up` on an empty database is always the clean
slate.

### Known limitation: `dexaflow_scheduler_loop_duration_seconds` is not wired

The declared SLI `dexaflow_scheduler_loop_duration_seconds` is **defined** in
`internal/observability/metrics.go` but is **never `Observe`d inside the scheduler
loop** (grep confirms: only `metrics_test.go` observes it). A `/metrics` scrape
today would therefore report an empty histogram for it. Until that observation is
wired into `Scheduler.tick`/`Step`, this harness times `Step` **directly** (the
faithful equivalent) and also feeds each measured duration into the real
histogram object, so a future scrape — or a one-line follow-up that moves the
`Observe` into the loop — sees the same distribution. `dexaflow_scheduler_step_downs_total`
**is** scraped from the live registry (it stays 0 here: leadership never churns in
the harness). `dexaflow_dispatch_queue_depth` is a dispatcher gauge and is out of
scope for Experiment 1 (no dispatcher is wired).

## Experiment 6: query plans at scale

Most read paths look fine on a laptop with a few hundred runs and degrade only
once `dag_runs` and `task_instances` hold millions of rows. Experiment 6 seeds a
dedicated tenant (`load-query-plans`) sized like a busy installation and runs
`EXPLAIN (ANALYZE, BUFFERS)` on the hot queries the UI, the API and the
maintenance loops issue.

The SQL comes from `internal/storage/queries/*.sql` at run time, so it always
measures what ships. Every `EXPLAIN` runs in a transaction that is rolled back,
so the mutating case (`DeleteDag`) leaves the dataset as it was. The first run of
each case warms the cache and is discarded.

Seeding the default dataset takes a few minutes and adds about 2 GB, so use a
throwaway database. The dataset is kept between runs; `--reseed` rebuilds it and
`--drop` removes it.

```sh
DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable' \
  go run ./test/load/query_plans
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--db` | `$DATABASE_URL`, then `$DEXAFLOW_DATABASE_URL`, then `$LEOFLOW_DATABASE_URL` | Postgres URL |
| `--dags` | `1000` | DAGs; every 20th is inactive and every 10th paused |
| `--runs-per-dag` | `1000` | hourly runs per DAG; every 17th failed |
| `--tasks-per-run` | `5` | task instances per run |
| `--audit` | `1000000` | `audit_log` rows |
| `--versions` | `51` | versions of the DAG `DeleteDag` removes |
| `--staging` | `200` | active staging volumes (capped by the running runs) |
| `--running-every` | `7` | every Nth DAG has a running newest run |
| `--repeat` | `5` | `EXPLAIN ANALYZE` runs per case, including the warm up |
| `--only` | all | comma-separated case names |
| `--plans` | `false` | print each case's text plan after the table |
| `--reseed` | `false` | drop and rebuild the dataset |
| `--drop` | `false` | drop the dataset and exit |

Output is a Markdown table per case: median, best and worst execution time, the
time spent in triggers (FK checks and cascades, already part of execution time)
and the tables read with a sequential scan. Recorded results live in
[`BASELINE.md`](BASELINE.md).

## Adding an experiment

1. Create `test/load/<name>/main.go` as its own `package main`.
2. Reuse the storage/scheduler/observability packages the way
   `scheduler_ceiling` does — connect with `storage.NewPostgres`, seed via
   `storage.Repository` / `storage.SchedulerStore`, and read metrics from a
   `prometheus.NewRegistry()` you own. Do **not** modify product code.
3. Keep it cluster-free: prefer seeding rows directly over booting executors.
   If a signal genuinely needs the full server (e.g. the SSE tailer for
   Experiment 5), boot `dexaflow lite` the way `test/e2e/lite-*.sh` does and
   scrape its `/metrics`, and say so in this table.
4. Add a row to the _Experiments_ table above and a section here.
5. It must `go build`, `go vet`, and `gofmt` clean, and actually run.
