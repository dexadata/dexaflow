# Release checklist: performance and reliability

This is the detailed form of review 8 ("Performance and soak") and of the
reliability half of review 3 ("Regression") in [AGENTS.md](../../AGENTS.md#1-release-candidate-reviews).
Every item is a command or a test with a threshold that decides it. Record the
result of each section as one line in the release's tracking issue: the section
id, PASS or FAIL or N/A with the reason, and a link to the evidence (a run, a log,
a pasted table).

Columns:

- **When**: `rc` every release candidate, `GA` the final cut of a minor
  (`X.Y.0`), `patch` every patch candidate (`X.Y.Z-rc.N`, `Z > 0`). A patch runs
  an item marked `patch*` only when the range it ships touches the area named in
  the item (use `git diff --stat vPREV..HEAD -- <paths>`).
- **Where**: `CI` (a workflow run on the commit being tagged), `local` (any
  machine with Postgres 16 and Redis 7), `k3d` (a local cluster, `make dev-up`,
  on a host where k3d can start pods: see [Where the k3d items run](#where-the-k3d-items-run)),
  `cluster` (a real cloud cluster, see [rc-cluster-validation.md](rc-cluster-validation.md)).

Numbers are compared with the previous release on the same machine. Absolute
targets come from [test/load/BASELINE.md](../load/BASELINE.md); when a target and
a relative rule disagree, the stricter one decides. A bar states what the design
guarantees: where a release cannot meet a bar by design, the bar is amended with
the code or ADR that sets the behavior, and where it should be met and is not,
the row is recorded as a known failure with its measured value and issue. Both
are listed in [Amended bars and known failures](#amended-bars-and-known-failures).

## A. Gates that must already be green on the commit

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| A1 | Unit and integration suites with the race detector | `make ci-local`, and CI on the exact commit the cut tags | Green, no `DATA RACE` | rc, GA, patch | CI |
| A2 | Performance gates leg | `.github/workflows/e2e-gates.yaml` run on the commit (push to `main` or `release-*`) | Green, every gate assertion in `test/e2e/perf-gates-on.sh` passed | rc, GA, patch | CI |
| A3 | Concurrency packages are not flaky | `go test -race -count=20 -shuffle=on ./internal/scheduler/ ./internal/dispatch/ ./internal/executor/ ./internal/agentrpc/ ./internal/agent/` | 0 failures in 20 runs | rc, GA, patch* (those paths) | local |
| A4 | Failure-injection integration suite | `go test -tags integration -race -run 'TestChaos' ./internal/storage/` | Green | rc, GA, patch | local |
| A5 | Defaults did not move | `git diff vPREV..HEAD -- internal/config/server.go helm/dexaflow/values.yaml` read against the changelog | Every changed default has a `Changed` changelog fragment; every new performance gate defaults off and binds under both `DEXAFLOW_*` and `LEOFLOW_*` | rc, GA, patch | local |

## B. Micro benchmarks against the previous release

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| B1 | Scheduler, planner, projection, static asset and warm pool tick benchmarks | Check out `vPREV` and `HEAD` side by side, run the command in BASELINE.md "Go benchmarks" with `-count 10` on both, compare with `benchstat`. A benchmark `vPREV` does not have yet (`BenchmarkWarmPoolTick` before v0.5.5) is run from `HEAD`'s test file copied into `vPREV`'s tree when it compiles there, and otherwise recorded for the next release | No benchmark slower by more than 10% with p < 0.05; allocations per op not up by more than 10% | GA, patch* (`internal/scheduler`, `internal/storage`, `internal/dispatch`, `internal/executor/warmpool*`) | local |
| B2 | Absolute targets | Same run | `BenchmarkPlanRunFanOut` 5,000 tasks under 2 ms; static bundle with gzip under 5 ms. `BenchmarkStepSteadyState` 1,000 runs x 20 tasks has no absolute target: B1 decides it against the previous release (5.35 ms at v0.5.4, see the amended bars) | GA | local |
| B3 | BASELINE.md is current | Re-record the tables when B1 changes a row by more than 10%, and at least once per minor | The file names the release and commit its tables were recorded on, and that release is the previous release or this one; otherwise FAIL, naming the release the tables come from | GA | local |

## C. Database at scale

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| C1 | Hot query plans | `go run ./test/load/query_plans` on the BASELINE dataset; record the dataset size when it is smaller | Every query on the scheduler tick, the reapers and the dashboard under 20 ms median, except the dashboard history counts (`CountDagRunStatesInWindow`, `CountTaskInstanceStatesInWindow`), whose median is not slower than the previous release's on the same dataset by more than 10%; no new sequential scan on `task_instances`, `dag_runs` or `audit_log`; `DeleteDag` (51 versions) under 100 ms | GA, patch* (`migrations/`, `internal/storage/queries/`) | local |
| C2 | Tick read at scale | `go run ./test/load/scheduler_ceiling --n 50,200,500,1000,5000 --tasks 20` | Tick p99 under half the loop interval (500 ms at the 1 s default) at 1,000 runs x 20 tasks; record the N where p99 crosses the interval | GA, patch* (scheduler or storage) | local |
| C3 | Reaper candidate queries | EXPLAIN (ANALYZE) of the stale-queued, pod-lost, agent-lost and warm-bound lists with 150k active task instances | Each under 50 ms | GA | local |
| C4 | Migrations on a realistic database | Upgrade from `vPREV` over the BASELINE dataset, then roll back to `vPREV`'s schema (AGENTS.md review 7) | Time of each migration recorded; no `INVALID` index left (`SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid` returns 0 rows); the upgrade guide's recovery table lists every new migration | rc, GA, patch* (`migrations/`) | local |
| C6 | Warm current versions | EXPLAIN (ANALYZE) of `ListWarmCurrentVersions` with 100k DAGs, a few thousand unpaused, and 200 DAG ids of active runs (ADR 0082) | A scan of `dags` with one primary-key probe of `dag_versions` per kept row, never a scan of `dag_versions`; under 100 ms | rc, GA, patch* (`internal/storage/queries/dags.sql`, warm pools) | local |
| C5 | Connection budget | Count the pools the release opens per replica (main, scheduler, leader, health) and compare with the chart's documented formula | The formula in the HA guide matches the code | GA | local |

Watch in C1: `ListActiveDagRuns` reads the pause of each active run's DAG
(#1705). It must stay one primary-key probe of `dags` per active run; a hashed
subplan over the paused DAGs seq-scans `dags` on every tick. Measured on one
machine (EXPLAIN ANALYZE, timing off, median of 10) with 100,000 DAGs (10%
paused), 2,000 active runs and 300,000 settled runs: 1.4 ms without the pause
read, 15.6 ms with the hashed subplan, 10.1 ms with the per-row probe.

## D. Dispatch throughput and backpressure

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| D1 | A wide fan-out does not starve other runs | On k3d with the default client QPS: one DAG with a 2,000 task fan-out plus a second DAG scheduled every minute | The second DAG's runs are created on time (no missed cron slot) and progress while the fan-out drains; no task ends `dispatch_failed` | GA, patch* (scheduler, dispatch) | k3d |
| D2 | Buffered dispatch drains before the dispatch-lost threshold | With `scheduler.dispatch.buffer_size` and the client QPS of the Helm busy profile, compute `buffer_size / effective creates per second` | Under 120 s (two thirds of the 3 min threshold); a 1,000 task burst ends with zero `dispatch_lost` | GA, patch* (dispatch) | k3d |
| D3 | Quota and throttling never fail a task | Namespace `ResourceQuota` of 5 pods, 50 ready tasks | Every task succeeds eventually; `dispatch_attempts` stays 0 for quota 403 and 429 | rc, GA | k3d |
| D4 | Exactly once under dispatch faults | Kill the leader process during a burst (`make chaos-runtime`, scenario A) | Every task ran exactly once; no task stuck `queued` or `scheduled` after recovery | GA | k3d |

## E. Failure injection, expected end states

Each row injects one fault and states the end state the release must reach
without human action. A row the release does not meet yet is recorded as a known
failure with the issue that tracks it; it is never deleted from the list or
recorded as a pass.

| # | Fault | How | Expected | When | Where |
|---|---|---|---|---|---|
| E1 | Postgres unavailable 60 s | `bash test/e2e/lite-db-outage.sh` | Leader steps down and re-acquires; no running task is failed as a user failure; reapers resume after the settling grace | rc, GA | local |
| E2 | Leader DB session black-holed (no RST) | Freezing TCP proxy in front of the leader pool (`TestLeaderStepsDownOnBlackholedSession` in `cmd/dexaflow-server` runs it with a 200 ms interval) | Leader steps down with reason `check_timeout` after 3 consecutive failed checks, each bounded by one check interval: within 4 check intervals of the fault (20 s at the 5 s default), since the first failed check starts at the next tick | GA | local |
| E3 | Apiserver 503 for 3 minutes | k3d with an admission webhook that fails closed, or the fake apiserver harness | No task ends `dispatch_failed`; dispatch resumes when the apiserver does | GA | k3d |
| E4 | Agent cannot reach the control plane for 2 minutes | NetworkPolicy dropping agent egress to the gRPC port | Heartbeats fail fast and reconnect; tasks that finish during the window are not re-run | GA | k3d |
| E5 | Task pod deleted | `make chaos-runtime` scenario B | Task leaves `running` within 2 minutes as infra, re-placed, no orphan pod | rc, GA | k3d |
| E6 | Node drained (eviction) | `kubectl drain` the node running a task | The task is re-placed as infra; user retries are not consumed | GA | cluster |
| E7 | Pod unschedulable | A task with a `node_selector` no node satisfies | The task leaves `queued` within the documented pending bound with a reason naming scheduling | GA | k3d |
| E8 | Control plane killed mid-report | `make chaos-runtime` scenarios C and D | Outcomes recovered from the durable record inside the settling grace | rc, GA | k3d |
| E9 | 100 long running tasks plus one lost attempt | Integration test that seeds 100 healthy running rows and one row with no pod (`TestPodLostReapsBehindAFullPageOfLiveRowsIntegration`, `TestDispatchLostReapsBehindAFullPageOfPendingRowsIntegration` in `internal/storage`) | The lost row is reaped within two reaper sweeps after its threshold: a sweep reads one page of 100 candidates, and the next sweep continues after it (keyset paging, R-07). In general a lost row behind N older candidates is reached within ceil((N + 1) / 100) sweeps | GA | local |

## F. Warm pools (when the release touches them or ships for GA)

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| F1 | A/B against dedicated pods | `make soak-warmpool-ab` (no workflow runs it; `soak-smoke.yaml` only checks its syntax) | Warm arm median start latency at least 3x better; no task failure in either arm | GA, patch* (`warmpool*`, `internal/agent/warm.go`, `internal/agentrpc`) | k3d |
| F2 | Deploy rollover | Push a new DAG version while warm workers run tasks of the old one; repeat with a DAG that declares `min_idle_workers` and once paused | No busy worker deleted; old version's idle workers deleted at the next reconcile, not after `worker_idle_ttl` (ADR 0082); the new version's idle workers Running before its first run when the DAG declares `min_idle_workers` and is unpaused, none while it is paused; warm anchor ConfigMaps equal the number of versions with pods once the drain ends | rc, GA, patch* (warm target computation) | k3d |
| F3 | Leader change | Restart the leader replica with 2 replicas and warm pools on, with a prewarmed DAG and a draining version | Warm placements resume within 60 s (not after `worker_idle_ttl`); the new leader keeps the prewarmed pool and finishes the drain (its `warm_pool_prewarm_started` and `warm_pool_drain_started` counts fire once more, by design) | rc, GA, patch* (warm target computation) | k3d |
| F4 | Placement honoured | A task with `node_selector` or `resource_claims` and warm pools on | It runs on a dedicated pod with its placement | rc, GA | k3d |
| F5 | Warm pool e2e | The `E2E warm pools (k3d)` job of `e2e-gates.yaml` on the candidate's commit, or `make e2e-warm-pools` | Green: warm hit, a task with its own pod labels on a dedicated pod, no file carried between attempts on one worker (or reported as not judged), no open task instance, warm pod or anchor left after the run | rc, GA | k3d |

## G. Soak and leaks

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| G1 | Short soak | `make soak` (30 min) | Zero recorded violations | rc, patch* (scheduler, dispatch, storage) | local |
| G2 | Long soak with faults | `bash test/soak/soak.sh --duration 12h --faults standard` | Zero violations; tick duration p99 at the end within 20% of the first hour; RSS growth under 10% after the first hour | GA | local |
| G3 | Soak self test | `make soak-selftest` | Exits 1 with recorded violations (the assertions can still fail) | GA | local |
| G4 | Nothing leaks after a run | After G2 or the k3d legs: count task pods older than the GC age, warm anchor ConfigMaps without pods, staging PVCs without a row, task instances non-terminal in finished runs, goroutines (`/debug/pprof/goroutine` delta), and the leader's heap (`/debug/pprof/heap` in-use delta: with warm pools on it pins the decoded current spec of each prewarmed DAG, ADR 0082, so compare at the same number of DAGs) | All zero, goroutines back to the idle count within 10%, heap within 10% of the idle reading | GA | local, k3d |

## H. Observability needed to judge the above

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| H1 | Scheduler SLIs are exported | Scrape `/metrics` on a running leader | `dexaflow_scheduler_loop_duration_seconds` has samples, `dexaflow_dispatch_queue_depth` moves under load | GA | local |
| H2 | Every new metric or alert in the release is documented | Compare `internal/observability` with the metrics reference page | No undocumented metric | rc, GA | local |

## I. Deploy

A code-only deploy is one that changed only DAG code: the registry holds the
dependency image, so only the code layer is pushed (ADR 0077). Both rows run
`go run ./test/load/deploy_p50` ([Experiment 7](../load/README.md#experiment-7-code-only-deploy-and-warm-up)),
which exits 1 when a threshold is missed; paste its table as the evidence.

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| I1 | Code-only deploy time | `PYTHONPATH=parser go run ./test/load/deploy_p50 --cli bin/dexaflow --config <cli config> --local-registry --n 20` against a control plane built from the tagged commit, compared with the row in BASELINE.md "Code-only deploy" | `PASS`: p50 under 30 s and every measured deploy took the `code-layer` path; the p50 and the registry requests per deploy within 10% of the previous release on the same machine, or the change explained as in "Accepted changes of the bar" | rc, GA, patch* (`internal/build/`, `internal/cli/deploy*`, `internal/cli/compile*`, `internal/cli/split*`, `parser/`) | local |
| I2 | Warm-up after a code-only deploy | The k3d setup of `test/e2e/deploy-e2e.sh` with `DEXAFLOW_E2E_WARM_POOLS=on`, then `deploy_p50 --registry <the k3d registry> --warm --n 10` | `PASS`: every new version gets a Ready warm worker with no run started (the prewarm of ADR 0082), with a p50 from the end of the deploy under 60 s (one 30 s warm pool interval and the pod start); the image pull of the measured versions is recorded next to the cold deploy's, and stays well under it (only the code layer is pulled) | GA, patch* (`internal/build/`, `internal/executor/warmpool*`, warm target computation) | k3d |

## Where the k3d items run

A `k3d` item needs a host where k3d can start pods: Docker with a kernel that
lets the container runtime write `oom_score_adj`. A developer machine works, and
so does a GitHub-hosted `ubuntu-latest` runner, where the k3d leg of
`.github/workflows/e2e-gates.yaml` runs. The cloud container used for the v0.5.4
release checks cannot: it refuses `oom_score_adj` writes, so no pod starts
(#1922).

D1, D2, F1 and I2 have no workflow: `e2e-gates.yaml` and `make rc-smoke` run
neither the fan-out and burst scenarios, the warm pool A/B nor the warm-up
after a deploy. Run them by hand
on such a host, on the commit to be cut, and link the log. When no such host is
available for a candidate, record the row as not run with that reason; it is
never recorded as a pass or as N/A.

## Amended bars and known failures

Bars changed after the v0.5.4 release checks on `1f1b4cb1` (#1922), with the
reason for each. A later release that meets a stricter bar may tighten it again.

| # | Was | Now | Why |
|---|---|---|---|
| B2 | `BenchmarkStepSteadyState` 1,000 x 20 under 2 ms | No absolute target; B1 compares it with the previous release | The 2 ms figure was the goal of batch 3 of the performance plan (BASELINE.md, Targets). No release met it: 4.76 ms when it was set, 5.35 ms at v0.5.4, unchanged since v0.5.3, and no open issue tracks work toward it (#1610 covers the tick's database read, which this benchmark fakes). A bar that fails on every release whatever it ships says nothing about the release; B1 still catches a regression. A new absolute target needs an issue with the design that reaches it |
| B3 | Names the release and commit | Names them, and they are the previous release or this one | BASELINE.md read "Recorded 2026-10-02 on `main` at `fc4b56c`", a commit of v0.5.0 recorded before the v0.5.1 performance changes, and named no release. It now says so; its tables need a re-record on the reference host, so B3 is a known failure until they are (#1617, #1922) |
| C1 | Every dashboard query under 20 ms | The two dashboard history counts compared with the previous release | `CountDagRunStatesInWindow` and `CountTaskInstanceStatesInWindow` (`internal/storage/queries/runs.sql`) count every run and every task instance whose run falls in the window, a week in the harness, so their cost follows the rows in the window by design; 20 ms would need a pre-aggregated table, which is a design change, not a fix. Measured at one fifth of the BASELINE dataset: 37 ms and 277 ms at v0.5.4, the same as v0.5.3 |
| E2 | Within 3 check intervals (15 s) | Within 4 check intervals (20 s at the 5 s default) | `watchLeadership` in `cmd/dexaflow-server/leader.go` steps down after `maxLeaderCheckFailures` (3) consecutive failed checks, each bounded by `leaderCheckInterval` (5 s), and the first check after the fault starts at the next tick, up to one interval later (#1599, ADR 0009). Measured at about 4 intervals with the 200 ms interval of the integration test |
| E9 | Within one sweep | Within two sweeps for 100 live rows | The reapers page their candidates by keyset, 100 per sweep (`ReapPageSize`, `internal/executor/reap_cursor.go`), and the next sweep continues after the page (R-07, #1597). A lost row behind a full page of live rows is read by the second sweep, which is the guarantee; one sweep would need an unbounded page |
