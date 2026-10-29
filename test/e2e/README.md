# End-to-end tests

This directory holds the E2E suite — scripts that boot real Dexaflow components
and exercise the user's path, asserting behavior end-to-end. They are
developer/CI tools — **not** part of `go test`. They are the gates that catch
the kind of regression unit tests miss (the parser→spec→executor wiring that
shipped broken in `v0.0.1-prealpha.21`, for instance). See
[ADR 0033](../../docs/adr/0033-release-flow-rc-tags-and-e2e-gates.md) for the
gate-and-release policy these tests implement.

## Scripts

| Script | What it gates | Runtime | Where it runs |
|---|---|---|---|
| `lite-login.sh` | Lite happy path: `dexaflow setup` → control plane → admin login → JWT → web editor | ~15 s | `ci.yaml` job `e2e-lite` on every PR + push; `e2e-gates.yaml` job `E2E Lite (all performance gates on)` on pushes to `main` and `release-*` |
| `lite-multidag.sh` | The multi-DAG materialization contract: subdir DAG → `dag.json.source` carries `dag.py` verbatim (the property the subprocess executor depends on to materialize per-TI work dirs) | ~5 s | `ci.yaml` job `e2e-lite-multidag` on every PR + push |
| `lite-selfheal.sh` | Lite **boot self-heal** (#404): two `dexaflow lite` sessions share one Postgres; session 2's boot reconcile deregisters a DAG whose files were removed **and** clears its orphan import error, while keeping a valid DAG. Guards the regression where the watcher seeded its set-diff from the workspace instead of the control plane, leaving un-removable ghosts | ~30 s | `ci.yaml` job `e2e-lite-selfheal` on every PR + push |
| `lite-alerts.sh` | Native **on-failure alerting** (#424): a DAG with an `alerts:` block and one `@task` that raises is triggered; the scheduler must fire a real webhook `POST` — resolved from an **encrypted managed connection** and rendered from the message template — to a local receiver. Exercises the whole Go control-plane chain (compile → `dag.json` → scheduler → dispatcher → notifier) against a real DB and real connection encryption, which the unit tests can't | ~1 min | `ci.yaml` job `e2e-lite-alerts` on every PR + push |
| `lite-callback.sh` | Airflow **`on_failure_callback`** (#424): a `@task` that raises must run its native `on_failure_callback` **in the task process**, once, on the terminal failure — resolved off the loaded task object (works for an unbound `with DAG():`) and called even though Airflow normalises it to a list. The callback POSTs the real Airflow context to a local receiver; asserts it fired exactly once. Guards the two integration bugs the unit tests (fake resolver/callback) missed | ~1 min | `ci.yaml` job `e2e-lite-callback` on every PR + push |
| `lite-admission.sh` | The **dispatch admission webhook** (ADR 0100, #2177): with `DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_URL` pointing at a loopback endpoint, a DAG the endpoint refuses fails at once with `dispatch refused: admission: <reason>` and its task never runs, another DAG is admitted and succeeds, and the captured reviews carry no environment, source or token | ~1 min | `ci.yaml` job `e2e-lite-admission` on every PR + push |
| `e2e.sh` | Pod-path E2E on k3d: build images, k3d import, agent-over-gRPC, real pod-per-task. Regression guards for the ADR 0040 features in the real pod path: generic operator/sensor execution, `ti.xcom_pull` chaining, `@task` run-context (`ds`), Admin Variable delivery, **multi-key XCom** (`LEOFLOW_PUSHES_PATH`), **native bash Jinja templating** (`{{ ds }}`, #382), and **cloud connection delivery**, where a user-pasted credential (GCP `keyfile_dict`, AWS access keys, Azure client secret) created via the API survives encrypted-at-rest storage (ADR 0019) and is recovered intact inside the pod task, and **reschedule-mode sensors** (#380), where a `DateTimeSensor(mode='reschedule')` releases its pod on each not-ready poke (passes through `up_for_reschedule`) and is re-dispatched to success. Also gates the **`on_failure_callback` pod-path** (#424): a separate failing `@task` runs its callback in the pod on terminal failure (the wiring the Lite `lite-callback.sh` can't cover) | minutes | `ci.yaml` job `e2e-operators` (k3d) on every PR + push; `e2e-gates.yaml` job `E2E operators (k3d, all performance gates on)` on pushes to `main` and `release-*` |
| `perf-gates-on.sh` | **The performance gates really apply at runtime** (performance audit, finding M-09: every 0.5.1 gate ships off and the e2e jobs ran the defaults, so their production wiring was executed by nothing). Run by `lite-login.sh` and `e2e.sh` as their extra-checks hook (`LEOFLOW_E2E_EXTRA_CHECKS`) against the **live** control plane: `/metrics` serves no `leoflow_` twin (`observability.metrics.drop_legacy_names`), the grid route answers `ETag` + `private, no-cache` and a `304` on the conditional GET (`ui.etag_revalidation`), the boot echoes of buffered dispatch (`scheduler.dispatch.buffer_size`) and the dedicated scheduler pool (`database.scheduler_max_conns`) are in the server log, and on k3d the s3 sink booted in the segmented layout and a settled run's finished pods are collected by the next sweep (`executor.collect_settled_run_pods`). The workflow then reads `database.statement_timeout_ms` back from Postgres's statement log, the segments from the bucket and the `on_demand` probes from Redis | seconds | `e2e-gates.yaml` on pushes to `main` and `release-*`, and `workflow_dispatch` |
| `source-mode-e2e.sh` | **Pro source mode on a real cluster (ADR 0067, #1539).** The runtime image is pushed to a k3d registry and pinned by its digest; with source mode on, versions registered through `dexaflow compile --image <runtime image>` and `POST /versions` (no image build) run a python task and a bash task from the `dag.py` projected at `/opt/leoflow/source`, each on a dedicated pod carrying the `leoflow.io/dag-source` annotation. A source of exactly 131072 bytes runs and one byte more is refused with `400`; a failing task runs its `on_failure_callback` in the pod; the DAG runs with the read-only task root filesystem off and on. With warm pools on (token exchange and liveness enforcement), a version on its own image gets a warm pod while the source-mode version gets none and runs every attempt on a dedicated pod. `make e2e-source-mode` | minutes | `e2e-gates.yaml` job `E2E operators (k3d, all performance gates on)` on rc tags, nightly and `workflow_dispatch`; `make rc-smoke` |
| `warm-pool-e2e.sh` | **Warm pools on a real cluster (ADR 0058, #1611).** With warm pools on (token exchange, liveness enforcement, one idle worker and a pool of one per version, the read-only warm root filesystem) in a namespace that enforces Pod Security `restricted`, one DAG built through the generated Dockerfile on the base of this checkout runs: at least one probe task is served by a warm worker (its `warm_worker_id` names a `leoflow-warm-*` pod), a task that declares its own pod labels runs on a dedicated pod carrying them (#1543), the second probe finds none of the marker files the first wrote to `$HOME`, `$TMPDIR`, `/tmp`, `/dev/shm`, its working directory or `/var/tmp` when both ran on the same worker (reported as not judged when they did not), and once the run settles no task instance is open and the warm pods and `leoflow.io/warm-anchor` ConfigMaps of the version are gone (it has no active run and declares no `min_idle_workers`, so it is not prewarmed, ADR 0082). A leader restart and a worker killed mid-attempt are not covered yet; the drain on deploy is the warm pools leg of `deploy-e2e.sh`. `make e2e-warm-pools` | ~10 min | `e2e-gates.yaml` job `E2E warm pools (k3d)` on rc tags, nightly and `workflow_dispatch`; `make rc-smoke` |
| `deploy-e2e.sh` | The real `dexaflow deploy` path (ADR 0041): `auth login` → one `dexaflow deploy` that builds for the cluster arch, **pushes to a registry the cluster pulls from**, captures the digest, re-pins `dag.json`, registers, then the cluster pulls the digest-pinned image and runs it. Then a v2 deploy lands while a v1 run is in flight: the run finishes on v1 (its task logs and `dag_version`) and the next run runs v2, with warm pools off and on (`DEXAFLOW_E2E_WARM_POOLS`); with them on, every warm-served task runs on a warm worker of its own version and image, run 2 has at least one, and the superseded v1 pool drains once run 1 ends (#1707, ADR 0082) | minutes | `ci.yaml` job `deploy-e2e`, a matrix: the default leg on every PR + push past the heavy gate, the warm pools leg only when the warm placement or deploy path changed (`test/README.md`) |
| `daemonless-deploy-e2e.sh` | **The daemonless deploy journey (#1888, ADR 0073, ADR 0078).** `dexaflow deploy examples/taskflow_sales`, the example as committed (its `dexaflow.yaml`, no Dockerfile), from the `dexaflow-cli` image (`deploy/Dockerfile.cli.release`) built with this commit's CLI and run with no Docker socket mounted. The container first proves no daemon is reachable (no container tool on `PATH`, no daemon socket, no `DOCKER_HOST`); the registry, the platform and the runtime base built from this commit come from the cluster deploy defaults. The deploy must print exactly one `builder:` line naming `dependency-layer (uv ...)`, then `code-layer`, built from wheels without a daemon; the registry must hold the `deps-*` dependency image and `dag.json` must be pinned by digest. A run then succeeds on k3d and the task logs carry the example's own output. `--self-test` checks the builder line and no-daemon assertions offline. `make e2e-daemonless-deploy` (Linux only) | minutes | `e2e-daemonless-deploy.yaml` on rc tags and `workflow_dispatch`; `make rc-smoke` on Linux |
| `dbt-e2e.sh` | dbt support (ADR 0042): `dexaflow compile` renders a dbt project's `manifest.json` into one task per dbt node, then the scheduler dispatches a **pod per node** whose agent runs `dbt seed/run --select <node>` against a shared Postgres warehouse, in dependency order, until every task succeeds and the mart materializes | minutes | Manual (needs `dbt` on PATH) |
| `dbt-connection-e2e.sh` | dbt **managed connections** (ADR 0043): a dbt task generates `profiles.yml` in the pod from a Dexaflow managed connection (not a baked one). The image bakes a **deliberately broken** `profiles.yml`; if tasks still succeed and the mart materializes, the runtime used the managed connection — proving no credential is baked | minutes | Manual (needs `dbt` on PATH) |
| `dbt-mixing-e2e.sh` | dbt **mixed with operators** (ADR 0043): a `dag.py` wires BashOperators around a `dbt_group()`; the compiler merges them into one `dag.json` and the scheduler runs operators **and** per-model dbt pods in order (operator → group roots, group leaves → operator), all succeeding with the mart materialized | minutes | Manual (needs `dbt`, `python3`) |
| `chaos-runtime.sh` | **Runtime fault-injection (#231 Phase 2).** Four scenarios: kills the scheduler process mid-run (asserts `/monitor/health` flips the scheduler unhealthy via the advisory-lock reader, the in-flight run **resumes** to success, and **at-most-once** holds — no duplicate `work` pod); force-deletes a running task pod (asserts the reaper moves the TI off `running` and leaves no orphan); recovers a success from the durable outcome record after the agent dies mid-report (ADR 0052); and replays the production outage — three parallel tasks whose pods terminate while the scheduler is SIGKILLed, asserting the reconciler settles all three **inside** the leader-settling grace, the gate was engaged, the liveness valve never opened, and **no reaper fired**. Subset with `LEOFLOW_CHAOS_ONLY=D`. Destructive; run inside the **Lima Linux VM**. `make chaos-runtime` | ~12 min (D alone ~5) | not in CI (exploratory/destructive) |
| `execution-timeout-e2e.sh` | **`execution_timeout` against a real kubelet (#925 / #930).** The one seam no unit test reaches: only a kubelet stamps `pod.Status.StartTime`, which is where `activeDeadlineSeconds` is counted from. A task declaring `execution_timeout_seconds: 10` whose body sleeps far past it (image **pre-loaded**, so the startup delta is a few seconds) must be interrupted by the **agent's** clock, not the kubelet's — RED before #925, GREEN after. Asserts the TI's `failure_reason` (**served on the API**; the UI does not render it) names `execution_timeout:` **and** that the pod's own `status.reason` is not `DeadlineExceeded`, which names the race winner directly; that the pod's **durable outcome record** carries the same diagnosis (#930) — the reason **bytes on the pod**, which is the channel a lost report would be settled from, not a claim about the reconciler's rendering, since the report does land here; that a pod created through the **real dispatch path** carries `activeDeadlineSeconds == timeout + startup headroom + effective termination grace`; and that an agent **frozen** (SIGSTOP from the k3d node — the pod stays `Running`, so only the reaper can settle it) after `RUNNING` is reaped as `agent_lost` inside 70-150 s (the agent-lost threshold minus a heartbeat, to threshold + sweep + heartbeat + slack) instead of lingering to the pod deadline. `make e2e-timeout` | ~8 min (includes the reaper's own waits) | Manual (needs the `migrate` CLI); candidate for its own k3d job in the heavy tier |
| `sso-login-page.sh` | **The sign-in page offers SSO when SSO is configured (#1160).** Boots two control planes, one under `provider: oidc` against a TLS fake IdP whose discovery document it must fetch at boot, one JWT-only, and drives both with a real browser: the OIDC one has to render a control that reaches the IdP's authorization endpoint carrying `state`, `nonce` and `code_challenge`, and keep the break-glass password form; the JWT-only one must not advertise a route its router never registered. Go handler tests pin the bytes; only a browser proves the user has a way in. **Linux only** (it trusts a throwaway CA through `SSL_CERT_FILE`, which the darwin build of `crypto/x509` never reads). `make e2e-sso` | ~1 min | `e2e-sso-login` job |
| `secrets-localstack.sh` | **External secrets resolver against a real provider backend (ADR 0060).** k3d + **LocalStack** (Secrets Manager): a DAG declares a connection + variable that are **absent from the vault**; registration accepts them (B1 coverage), and the task pod resolves them from LocalStack under the pod-side Python resolver — asserting the exact seeded values in the task logs. Exercises the yaml-declaration → agent → resolver → provider chain a fake backend can't (it surfaced the resolver's stdout-vs-provider-logging bug). Keyless identity (IRSA/Pod-Identity/WI) is the only part LocalStack can't emulate → EKS/GKE. | minutes | `secrets-e2e.yaml` on `internal/agent/**`, `runtime/python/**` + manual dispatch |
| `replaced-database-e2e.sh` | **A database emptied under a running chart install (#1996, ADR 0090).** kind + the chart with its migration Job: a DAG is registered and a run triggered, then the database is dropped and recreated. The serving pod must exit with code 3 and restart into the wait (Running, not ready, one restart, no crash loop), `/readyz` must answer 503 naming `database replaced`, `dexaflow_schema_ready{reason="database replaced"}` must read 0, and nothing may migrate the empty database. A `helm upgrade` without the acknowledgement must fail with the Job exiting 8; `--set installation.id=<new id>` must then migrate it, write the id into the database and the ConfigMap, bring the release back ready, and accept a new DAG version and run while the old DAG is gone. Task pods are not run (no task image is built). | ~15 min | `e2e-replaced-database.yaml` nightly, manual dispatch, and pull requests that change it |

The `lite-*` scripts gate Lite behavior; `e2e.sh` gates the pod-path that
Lite cluster mode and Pro both rely on; `deploy-e2e.sh` gates the pipeline-less
`dexaflow deploy` promotion (the build→push→digest→register glue that unit tests
leave to e2e).

## Running locally

### Lite tests (Postgres + Redis required for `lite-login.sh`)

```bash
make dev-up                                  # Postgres + Redis on the host
bash test/e2e/lite-login.sh                  # ~15 s
bash test/e2e/lite-multidag.sh               # ~5 s — no DB needed
PYTHONPATH=parser bash test/e2e/lite-alerts.sh   # ~1 min — needs Postgres (leoflow_dev)
```

`lite-login.sh` is destructive — it resets `leoflow_dev`.
`lite-multidag.sh` runs entirely in a tmpdir and needs nothing but Go +
Python. `lite-alerts.sh` also resets `leoflow_dev` and needs the parser on
`PYTHONPATH` (it boots `dexaflow lite`, which compiles the DAG).

### Pod-path test (k3d + Docker)

```bash
make dev-up      # Postgres + Redis on the host
make build       # bin/leoflow, bin/leoflow-server, bin/leoflow-agent
make e2e         # or: bash test/e2e/e2e.sh [cluster-name]
```

The pod-path test builds the base + DAG images, imports them into k3d, runs
the control plane on the host, pushes and triggers a DAG, and asserts every
task instance reaches `success` — i.e. each task ran in a real pod whose
agent reported state over gRPC.

## Network layout for `e2e.sh`

The control plane runs on the host and listens for agents on `:9091`. Task
pods run inside k3d and dial back via `host.k3d.internal:9091`, which the
script sets through `LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` — the host
listen address (`0.0.0.0:9091`) is not reachable from inside a pod. The DAG
image is imported into the cluster with `k3d image import` so no registry
is needed.

### Deploy test (`deploy-e2e.sh`) — k3d + a registry the cluster pulls from

```bash
make dev-up      # Postgres + Redis on the host
make build       # bin/leoflow, bin/leoflow-server
bash test/e2e/deploy-e2e.sh
```

Unlike `e2e.sh` (which `k3d image import`s the image), this uses a **k3d-managed
registry** so it exercises the real push→pull path: a single `dexaflow deploy`
builds for the cluster arch (auto-detected — `linux/arm64` on an arm64 Mac,
`linux/amd64` on amd64 CI), pushes to `k3d-registry.localhost:5111`, captures the
image digest, re-pins `dag.json`, and registers; the cluster then pulls the
digest-pinned image and runs it. It binds the API on `:18080` by default
(`LEOFLOW_E2E_HTTP_PORT`) to avoid clashing with a Lima/other forward on 8080.

To run the warm pools leg, set `DEXAFLOW_E2E_WARM_POOLS=on` (it needs `psql`,
and `DEXAFLOW_E2E_DATABASE_URL` when the server does not use the default
database). `DEXAFLOW_E2E_HOLD_SECONDS` (default 30, and 60 with warm pools on) is how long the first task
of each run holds it open, so the redeploy lands in flight and the warm pool
reconciler starts a worker before the second task; with warm pools on, the
script fails early when no warm worker of the run's version is Running 10s
before the hold ends.

!!! note "Docker Desktop on macOS"
    The host `docker push` to the k3d registry can be intermittently routed
    through Docker Desktop's HTTP proxy. If a push times out
    (`proxyconnect … i/o timeout`), add `k3d-registry.localhost:5111` to Docker
    Desktop → Settings → Resources → Proxies bypass, or run this gate on Linux/CI
    (no Docker Desktop proxy), where it is clean.

## Adding a new gate

Per ADR 0033, every gate added here should be:

- **Reality-anchored** — no mocks at the boundary it gates.
- **Fast enough for PR-time CI** — target < 60 s for `lite-*`; longer is OK
  for k3d-heavy paths if they run on a separate workflow.
- **Named after the bug it catches** — e.g. `lite-multidag.sh` exists because
  shipping `v0.0.1-prealpha.21` taught us subdir DAGs need their own gate.
  Name reveals intent.

Wire the new script into a CI job in `.github/workflows/ci.yaml` following
the `e2e-lite-multidag` pattern (no DB dep) or the `e2e-lite` pattern
(Postgres + Redis services).
