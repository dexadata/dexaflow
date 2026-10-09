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
  machine with Postgres 16 and Redis 7), `k3d` (a local cluster, `make dev-up`),
  `cluster` (a real cloud cluster, see [rc-cluster-validation.md](rc-cluster-validation.md)).

Numbers are compared with the previous release on the same machine. Absolute
targets come from [test/load/BASELINE.md](../load/BASELINE.md); when a target and
a relative rule disagree, the stricter one decides.

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
| B1 | Scheduler, planner, projection and static asset benchmarks | Check out `vPREV` and `HEAD` side by side, run the command in BASELINE.md "Go benchmarks" with `-count 10` on both, compare with `benchstat` | No benchmark slower by more than 10% with p < 0.05; allocations per op not up by more than 10% | GA, patch* (`internal/scheduler`, `internal/storage`, `internal/dispatch`) | local |
| B2 | Absolute targets | Same run | `BenchmarkStepSteadyState` 1,000 runs x 20 tasks under 2 ms; `BenchmarkPlanRunFanOut` 5,000 tasks under 2 ms; static bundle with gzip under 5 ms | GA | local |
| B3 | BASELINE.md is current | Re-record the tables when B1 changes a row by more than 10% | The file names the release and commit it was recorded on | GA | local |

## C. Database at scale

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| C1 | Hot query plans | `go run ./test/load/query_plans` on the BASELINE dataset | Every query on the scheduler tick, the reapers and the dashboard under 20 ms median; no new sequential scan on `task_instances`, `dag_runs` or `audit_log`; `DeleteDag` (51 versions) under 100 ms | GA, patch* (`migrations/`, `internal/storage/queries/`) | local |
| C2 | Tick read at scale | `go run ./test/load/scheduler_ceiling --n 50,200,500,1000,5000 --tasks 20` | Tick p99 under half the loop interval (500 ms at the 1 s default) at 1,000 runs x 20 tasks; record the N where p99 crosses the interval | GA, patch* (scheduler or storage) | local |
| C3 | Reaper candidate queries | EXPLAIN (ANALYZE) of the stale-queued, pod-lost, agent-lost and warm-bound lists with 150k active task instances | Each under 50 ms | GA | local |
| C4 | Migrations on a realistic database | Upgrade from `vPREV` over the BASELINE dataset, then roll back to `vPREV`'s schema (AGENTS.md review 7) | Time of each migration recorded; no `INVALID` index left (`SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid` returns 0 rows); the upgrade guide's recovery table lists every new migration | rc, GA, patch* (`migrations/`) | local |
| C5 | Connection budget | Count the pools the release opens per replica (main, scheduler, leader, health) and compare with the chart's documented formula | The formula in the HA guide matches the code | GA | local |

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
| E2 | Leader DB session black-holed (no RST) | Freezing TCP proxy in front of the leader pool | Leader steps down within 3 check intervals (15 s) | GA | local |
| E3 | Apiserver 503 for 3 minutes | k3d with an admission webhook that fails closed, or the fake apiserver harness | No task ends `dispatch_failed`; dispatch resumes when the apiserver does | GA | k3d |
| E4 | Agent cannot reach the control plane for 2 minutes | NetworkPolicy dropping agent egress to the gRPC port | Heartbeats fail fast and reconnect; tasks that finish during the window are not re-run | GA | k3d |
| E5 | Task pod deleted | `make chaos-runtime` scenario B | Task leaves `running` within 2 minutes as infra, re-placed, no orphan pod | rc, GA | k3d |
| E6 | Node drained (eviction) | `kubectl drain` the node running a task | The task is re-placed as infra; user retries are not consumed | GA | cluster |
| E7 | Pod unschedulable | A task with a `node_selector` no node satisfies | The task leaves `queued` within the documented pending bound with a reason naming scheduling | GA | k3d |
| E8 | Control plane killed mid-report | `make chaos-runtime` scenarios C and D | Outcomes recovered from the durable record inside the settling grace | rc, GA | k3d |
| E9 | 100 long running tasks plus one lost attempt | Integration test that seeds 100 healthy running rows and one row with no pod | The lost row is reaped within one reaper sweep after its threshold | GA | local |

## F. Warm pools (when the release touches them or ships for GA)

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| F1 | A/B against dedicated pods | `make soak-warmpool-ab` | Warm arm median start latency at least 3x better; no task failure in either arm | GA, patch* (`warmpool*`, `internal/agent/warm.go`, `internal/agentrpc`) | k3d |
| F2 | Deploy rollover | Push a new DAG version while warm workers run tasks of the old one | No busy worker deleted; old version's idle workers drained; warm anchor ConfigMaps equal the number of versions with pods once the drain ends | GA, patch* | k3d |
| F3 | Leader change | Restart the leader replica with 2 replicas and warm pools on | Warm placements resume within 60 s (not after `worker_idle_ttl`) | GA, patch* | k3d |
| F4 | Placement honoured | A task with `node_selector` or `resource_claims` and warm pools on | It runs on a dedicated pod with its placement | rc, GA | k3d |

## G. Soak and leaks

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| G1 | Short soak | `make soak` (30 min) | Zero recorded violations | rc, patch* (scheduler, dispatch, storage) | local |
| G2 | Long soak with faults | `bash test/soak/soak.sh --duration 12h --faults standard` | Zero violations; tick duration p99 at the end within 20% of the first hour; RSS growth under 10% after the first hour | GA | local |
| G3 | Soak self test | `make soak-selftest` | Exits 1 with recorded violations (the assertions can still fail) | GA | local |
| G4 | Nothing leaks after a run | After G2 or the k3d legs: count task pods older than the GC age, warm anchor ConfigMaps without pods, staging PVCs without a row, task instances non-terminal in finished runs, goroutines (`/debug/pprof/goroutine` delta) | All zero, goroutines back to the idle count within 10% | GA | local, k3d |

## H. Observability needed to judge the above

| # | Item | How | Decides | When | Where |
|---|---|---|---|---|---|
| H1 | Scheduler SLIs are exported | Scrape `/metrics` on a running leader | `dexaflow_scheduler_loop_duration_seconds` has samples, `dexaflow_dispatch_queue_depth` moves under load | GA | local |
| H2 | Every new metric or alert in the release is documented | Compare `internal/observability` with the metrics reference page | No undocumented metric | rc, GA | local |
