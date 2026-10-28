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
| 4 | UI refresh cycle: what one auto-refresh of a page costs, and the viewers an interval allows | [`ui_refresh/`](ui_refresh/) | ✅ implemented |
| 5 | Log tailer — SSE fan-out under many concurrent viewers | — | not yet |
| 6 | Query plans at scale: hot queries on 1M runs and 5M task instances | [`query_plans/`](query_plans/) | ✅ implemented |
| 7 | Code-only deploy: p50 of `dexaflow deploy` after a DAG code change, and the warm-up of the new version's pool | [`deploy_p50/`](deploy_p50/) | ✅ implemented |
| 9 | Primary key order: insert rate, WAL and index size of a random UUIDv4 key against a time-ordered UUIDv7 | [`uuid_keys/`](uuid_keys/) | ✅ implemented |

Experiments 1, 4, 6, 7 and 9 are built. The others are documented extension points.
Experiments 4 and 7 drive a running control plane instead of the packages:
Experiment 4 needs only an api role process and its Postgres, Experiment 7
the CLI, and its warm-up half a cluster with warm pools on.

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

### Loop duration: the harness times `Step` itself

Since 0.5.4 the product observes `dexaflow_scheduler_loop_duration_seconds` around
every leader tick (`Scheduler.tick`, #1186), so a `/metrics` scrape of a running
leader shows the real distribution. This harness calls `Step` directly instead of
going through the tick loop, so it times each `Step` itself and feeds the measured
duration into the same histogram object; the two measure the same work. `dexaflow_scheduler_step_downs_total`
**is** scraped from the live registry (it stays 0 here: leadership never churns in
the harness). `dexaflow_dispatch_queue_depth` is a dispatcher gauge and is out of
scope for Experiment 1 (no dispatcher is wired).

## Experiment 4: UI refresh cycle

The embedded UI polls. On a page whose data holds a run in progress it
refetches a fixed set of queries every `ui.autoRefreshIntervalSeconds` (30 s
on Pro, 1 s on Lite), so the load the UI puts on the control plane is that set
times the viewers divided by the interval (#1196). Experiment 4 measures the
set: it replays one page's refresh, a _profile_, against a running control
plane as V viewers that each issue cycles back to back, and reports the cycles
per second the control plane sustains, the latency of a cycle and, with
`--db`, the database transactions one cycle costs. A viewer at interval I
issues one cycle every I seconds, so the sustained rate times I is the number
of viewers the control plane serves on that page at I.

The profiles hold what the SPA repeats every interval in steady state. They
were recorded from the Airflow 3.3.2 UI in headless Chromium against an api
role process: the page load requests and the queries that refresh on a longer
period are not in them.

| Profile | Page | Requests per cycle |
|---|---|---:|
| `dag` | a DAG's grid while a run is in progress | 9 |
| `run` | one run in progress, with its task instance table | 9 |
| `idle` | a DAG's grid with no run in progress (only the latest run is polled) | 1 |

The home dashboard and the DAG list did not repeat any request within 15 s at
a 3 s interval in that recording: the dashboard only refreshes while runs are
pending, and the DAG list refreshes every ten intervals while none of its DAGs
has a run in progress.

Start an api role process against a seeded database (any installation with a
DAG that has a run in progress works), then:

```sh
DEXAFLOW_PASS=admin go run ./test/load/ui_refresh \
  --url http://localhost:8080 --dag my_dag --profile dag --viewers 1,10,50 \
  --db "$DATABASE_URL"
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--url` | `$DEXAFLOW_URL`, then `$LEOFLOW_URL` | control plane URL |
| `--user` | `admin@dexaflow.local` | user to sign in as |
| `--pass` | `$DEXAFLOW_PASS`, then `$LEOFLOW_PASS` | password |
| `--dag` | required | DAG whose pages are replayed |
| `--run` | the DAG's newest run | run of the `run` profile |
| `--profile` | `dag` | `dag`, `run` or `idle` |
| `--viewers` | `1,10,50` | concurrent viewer counts, one row each |
| `--window` | `10s` | measured window per row |
| `--parallel` | `6` | requests one viewer has in flight, as a browser's 6 connections per host |
| `--intervals` | `1,5,10,30` | intervals of the capacity table |
| `--db` | `$DATABASE_URL`, then `$DEXAFLOW_DATABASE_URL`, then `$LEOFLOW_DATABASE_URL` | the control plane's database, to count transactions per cycle; optional |

Output is a Markdown table per viewer count (cycles and requests per second,
cycle p50, p95 and p99, database transactions per cycle, error rate) and the
capacity table. The transaction count reads `pg_stat_database`, so it includes
whatever else uses the database during the window: measure on a database the
experiment has to itself. The harness, the control plane and Postgres share the
host when run on a laptop, so the rate is a floor of what a dedicated control
plane sustains. Recorded results live in [`BASELINE.md`](BASELINE.md).

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

## Experiment 7: code-only deploy and warm-up

A deploy that changed only DAG code should not pay for a build: the registry
already holds the dependency image, so the CLI appends the code as one layer
through the registry API and registers the new digest (ADR 0077). The target
is a p50 under 30 seconds (#1706). With warm pools on, the new version's pool
starts when it becomes current (ADR 0082), and its pods pull only the code
layer, since the node has the dependency layers already (#1707).

`deploy_p50` drives the real `dexaflow deploy` binary. It scaffolds a project
with `dexaflow init`, gives it pinned dependencies and a registry, and deploys
it once cold; that deploy builds the dependency image and is not counted. It
then changes a comment line of `dag.py` before each of `--n` deploys and times
each one from the start of the command to its exit (compile, content label,
pre-check, code layer push, digest read and register). Every measured deploy
must report `builder: code-layer`; one that took another path did not measure
a code-only deploy, and the run fails.

Prerequisites: a built CLI (`make build`), the parser on `PYTHONPATH`, `uv` on
`PATH` (the cold deploy builds the dependency image from wheels without a
daemon), a running control plane and a CLI config logged in to it.

```sh
make build
bin/dexaflow auth login --server http://localhost:8080 --username admin@dexaflow.local \
  --password admin --config /tmp/p50-cli.yaml
PYTHONPATH=parser go run ./test/load/deploy_p50 --cli bin/dexaflow \
  --config /tmp/p50-cli.yaml --local-registry --n 20
```

`--local-registry` serves an in-memory registry on the loopback inside the
harness and builds the scaffold on a stand-in runtime base pushed there (it
holds only what the dependency layer reads from a base, and runs nothing). It
needs no container daemon, no cluster and no access to a public registry, and
it gives the lower bound: the CLI and the register, with no network between
the CLI and the registry. It also counts the registry requests of each deploy;
on a remote registry each of them costs a round trip. A control plane with
`executor.type=subprocess` is enough for it. Measure against a real registry
with `--registry <prefix>` instead, with the registry login a deploy to it
needs. `--warm` refuses `--local-registry`: a cluster cannot pull from the
harness's loopback.

The warm-up half runs on a cluster with warm pools on, such as the k3d setup of
`test/e2e/deploy-e2e.sh` with `DEXAFLOW_E2E_WARM_POOLS=on`:

```sh
PYTHONPATH=parser go run ./test/load/deploy_p50 --cli bin/dexaflow \
  --config /tmp/p50-cli.yaml --registry k3d-registry.localhost:5111 \
  --platform linux/amd64 --warm --n 10
```

With `--warm` the project declares `min_idle_workers: 1` and deploys with
`--active`, since only an unpaused DAG that declares a warm floor is
prewarmed. After each deploy the harness polls the warm worker pods
(`leoflow.io/warm-worker=true` in `--namespace`) for one whose image is the
digest the deploy registered, and records when it appeared and when it became
Ready, from the end of the deploy, with the pull time the kubelet reported in
its `Pulled` event. The project builds on the published runtime base, which
the pods run.

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--cli` | `bin/dexaflow` | the CLI binary |
| `--config`, `--server`, `--token` | none | passed to `deploy` when set |
| `--registry` | none | registry prefix the image is pushed to |
| `--local-registry` | `false` | push to an in-memory loopback registry instead |
| `--project` | none | deploy this project instead of a scaffold |
| `--workdir` | a new temporary directory | where the scaffold is written |
| `--dag-id` | `deploy_p50` | `dag_id` of the scaffold; use a new one per run against a shared server |
| `--platform` | `linux/<host arch>` | the platform built |
| `--python` | `3.12` | `python_version` of the scaffold |
| `--base-image` | the published runtime base (the stand-in with `--local-registry`) | `base_image` of the scaffold |
| `--dependency` | `requests==2.32.3` | a pinned dependency of the scaffold; repeat for more |
| `--n` | `10` | measured deploys after the cold one |
| `--target` | `30s` | the p50 of the measured deploys must be under it |
| `--warm` | `false` | also measure the warm-up |
| `--kubeconfig`, `--namespace` | default rules, `leoflow` | the cluster and namespace of the warm workers |
| `--warm-target` | `1m` | the p50 to a Ready warm worker must be under it |
| `--warm-timeout` | `3m` | how long to wait for each version's warm worker |
| `--deploy-arg` | none | an extra `deploy` flag; repeat for more |

Output is a Markdown table with one row per deploy (time, builder path,
registry requests, and with `--warm` the warm worker's appearance, Ready time
and image pull), the p50, p95 and maximum, and a `PASS` or `FAIL` line; the
exit code is 1 on `FAIL`. Recorded results live in [`BASELINE.md`](BASELINE.md).

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

## Experiment 9: primary key order

Every table is keyed by `gen_random_uuid()`, a random UUIDv4, so each insert
lands on a random leaf of the primary key index (#525). This experiment
measures what that costs against a time-ordered UUIDv7 on a table of the
width of a task instance row, without touching the schema:

```sh
DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable' \
  go run ./test/load/uuid_keys [--rows 2000000] [--batch 1000] [--lookups 20000]
```

It needs only a database it may create two scratch tables in
(`load_uuid_v4`, `load_uuid_v7`, dropped at the end). Both keys are generated
in the client and the two tables are filled in turns, `--batch` rows per
transaction through `COPY`, so they see the same checkpoints and cache
pressure. It prints the insert rate overall and over the last tenth of the
rows, the WAL per row, the sizes of the key index and of the table, and the
rate of point lookups by key, as a table for
[BASELINE.md](BASELINE.md#primary-key-order).

