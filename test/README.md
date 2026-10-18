# The test suites, and when each one runs

Dexaflow has several test trees and they answer different questions. This page
says which is which, what each one proves, and when CI runs it, so a
contributor can tell whether the suite that stayed silent was meant to speak.

## The suites

| suite | what it proves | what it does NOT prove | when it runs |
| --- | --- | --- | --- |
| Go unit tests (`internal/`, `pkg/`) | logic, in isolation, with a coverage floor per package | that the pieces work together, or against a real database | every pull request |
| Integration (`//go:build integration`) | the SQL and the storage layer against a real Postgres | anything about Kubernetes | every pull request |
| `test/e2e/lite-*.sh` | Lite boots, serves, and runs a DAG on the subprocess executor | the Pro or Kubernetes paths | every pull request |
| `test/e2e/e2e.sh`, `split-two-process.sh`, `dbt-e2e.sh`, `deploy-e2e.sh` | the Kubernetes paths on k3d: pod-per-task, the two-process split, dbt, and build-then-deploy against a registry | NetworkPolicy enforcement, ReadWriteMany, QoS, or anything k3d's CNI cannot show | every pull request, **unless the gate below skips them** |
| `test/e2e/perf-gates-on.sh`, run by `lite-login.sh` and `e2e.sh` under `e2e-gates.yaml` | that every opt-in performance gate (`scheduler.dispatch.buffer_size`, `database.scheduler_max_conns` and `statement_timeout_ms`, `executor.collect_settled_run_pods`, `observability.metrics.drop_legacy_names`, `ui.etag_revalidation`, `logs.tail.publish=on_demand`, `logs.sink.layout=segmented`) takes effect in the running control plane, on the Lite and the k3d legs | the gates' behavior under load (that is `test/load/`), and warm pools, which the same workflow's own `E2E warm pools (k3d)` job covers (`test/e2e/warm-pool-e2e.sh`), as does the warm pools leg of `deploy-e2e`, below, on pull requests, merge queue runs and pushes that touch their path | pushes to `main` and `release-*`, and on demand; not on pull requests, to spare the runner queue |
| `test/e2e/daemonless-deploy-e2e.sh`, under `e2e-daemonless-deploy.yaml` | the daemonless deploy journey (#1888): `dexaflow deploy examples/taskflow_sales` from the `dexaflow-cli` image with no container daemon reachable takes the dependency layer from wheels with uv, then the code layer, and the version runs green on k3d | a dependency that uv must resolve and install (the example has none), the remote builder, or a daemon build | rc tags and on demand (`make e2e-daemonless-deploy`, and `make rc-smoke` on Linux); not on pull requests, to spare the runner queue |
| `test/e2e/pro-netpol-rwx.sh` | real NetworkPolicy enforcement and a real RWX volume, on kind with Calico and NFS | cloud-specific behavior | on demand |
| `test/soak/` | that a control plane dispatching for hours stays healthy, and that the cost of a tick does not track table growth | anything about a single request, and it is not an at-most-once proof (see `test/soak/README.md`) | a short harness smoke in CI; long runs are scheduled locally |
| `test/load/` | one cost at one instant | behavior over time | on demand |
| `test/gcp/` | what only a real cloud cluster can answer: enforcing CNI, real node counts, warm pools | anything that k3d already answers, and it costs real money | on demand, never in CI |
| `test/release/` | that the published artifact installs, and installs over a previous install | anything about the code inside it | on a release tag |
| `test/om-contract/`, `test/ui-contract/` | that our API and UI keep the contracts they claim | that the behavior behind the contract is right | every pull request |

## When the heavy Kubernetes jobs are skipped

The four k3d jobs (`operator-e2e`, `split-e2e`, `e2e-dbt`, `deploy-e2e`) cost
about six to eight minutes each. A pull request that touches **only** the paths
below cannot change what they test, so `scripts/ci-heavy-gate.sh` skips them:

```skip-paths
docs/
spec/
test/load/
website/
\.changes/
.*\.md$
```

| path | what it is | why it cannot affect a cluster run |
| --- | --- | --- |
| `docs/` | generated API and CLI reference | rendered output, never executed |
| `spec/` | specification sources | not built into any image |
| `test/load/` | the load harness | a different suite, run on demand |
| `website/` | the documentation site | built and deployed by its own workflows |
| `.changes/` | changelog fragments (see `.changie.yaml`) | inert data the release cut reads |
| `*.md` | prose anywhere in the tree | prose |

Anything else runs them, including `Makefile`, `scripts/`, `.github/` and the
chart. The rule is deliberately one-sided: a file we have not thought about
runs the jobs rather than skipping them.

**This table is checked, not decorative.** The fenced `skip-paths` block above
is read by `scripts/ci-heavy-gate.sh --self-test`, which fails when it and the
gate disagree. Editing one without the other turns the build red, which is the
point: the same list living in prose and in code with nothing between them is
how `.changes/` came to be missing from the gate for a whole release cycle
(#1234).

## The warm pools leg of the deploy job

`deploy-e2e` runs twice, as a matrix: once at the shipped default and once with
warm pools on (`DEXAFLOW_E2E_WARM_POOLS=on`, #1707). The default leg keeps the
gate above. The warm pools leg costs as much again, so it runs only when a file
on the warm placement or deploy path changed (`--scope warm-pools`), and on
every event decides by that path: a pull request against its base branch, a
merge queue run against the queue's base and a push against the commit before
it. Prose under these paths does not count, as in the gate above.

```warm-paths
internal/dispatch/
internal/executor/
internal/agentrpc/
internal/agent/
internal/kubeexchange/
internal/scheduler/
internal/storage/scheduler_store\.go
internal/storage/agent_store\.go
internal/storage/queries/runs\.sql
internal/domain/resource_unit\.go
internal/domain/default_limits\.go
internal/auth/agent_token\.go
internal/cli/deploy
internal/build/
cmd/dexaflow-agent/
proto/
test/e2e/deploy-e2e\.sh
test/e2e/lib\.sh
scripts/ci-heavy-gate\.sh
```

They are warm placement and the warm worker (dispatch, executor, the agent, its
binary and its RPC side, the agent protocol, the agent token, the token exchange
warm pools require), the scheduler that picks the version a task is dispatched
for, the storage that resolves that version, the warm targets and the warm
binding (`BindWarmAttempt` in `runs.sql`, and its generated `runs.sql.go`), the
warm eligibility rules (`resource_unit.go`, `default_limits.go`), the deploy
command and the image build, and the leg itself. The server wiring
(`cmd/dexaflow-server/`), `internal/config/`, `go.mod` and `ci.yaml` are left
out on purpose: with them the leg ran on 57 of 100 merges; with this list it
runs on 37 of the last 100.
A change confined to those paths that affects warm pools says in its pull
request how warm pools were checked, for example with
`DEXAFLOW_E2E_WARM_POOLS=on bash test/e2e/deploy-e2e.sh` on a local k3d.
The same `--self-test` checks this block against the gate.

```bash
printf '%s\n' internal/api/dags.go | scripts/ci-heavy-gate.sh --decide-warm
# skip

printf '%s\n' internal/dispatch/dispatch.go | scripts/ci-heavy-gate.sh --decide-warm
# run
```

## Asking the gate a question

```bash
printf '%s\n' README.md .changes/unreleased/x.yaml | scripts/ci-heavy-gate.sh --decide
# skip

printf '%s\n' internal/executor/reconcile.go | scripts/ci-heavy-gate.sh --decide
# run
```

## The gate runs inside each job, on purpose

Not as a `paths:` filter and not as a job-level `if:`. A skipped job reports
nothing, so a failure already recorded on that commit stays red forever:
rerunning a `pull_request` run replays the original event payload, so the
condition evaluates the same way every time and no remedy can clear the check.
`.github/workflows/changelog-guard.yaml` carries that lesson, learned from the
v0.4.7 cut (#1176).
