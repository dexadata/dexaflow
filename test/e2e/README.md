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
| `lite-login.sh` | Lite happy path: `dexaflow setup` → control plane → admin login → JWT → web editor | ~15 s | `ci.yaml` job `e2e-lite` on every PR + push |
| `lite-multidag.sh` | The multi-DAG materialization contract: subdir DAG → `dag.json.source` carries `dag.py` verbatim (the property the subprocess executor depends on to materialize per-TI work dirs) | ~5 s | `ci.yaml` job `e2e-lite-multidag` on every PR + push |
| `lite-selfheal.sh` | Lite **boot self-heal** (#404): two `dexaflow lite` sessions share one Postgres; session 2's boot reconcile deregisters a DAG whose files were removed **and** clears its orphan import error, while keeping a valid DAG. Guards the regression where the watcher seeded its set-diff from the workspace instead of the control plane, leaving un-removable ghosts | ~30 s | `ci.yaml` job `e2e-lite-selfheal` on every PR + push |
| `lite-alerts.sh` | Native **on-failure alerting** (#424): a DAG with an `alerts:` block and one `@task` that raises is triggered; the scheduler must fire a real webhook `POST` — resolved from an **encrypted managed connection** and rendered from the message template — to a local receiver. Exercises the whole Go control-plane chain (compile → `dag.json` → scheduler → dispatcher → notifier) against a real DB and real connection encryption, which the unit tests can't | ~1 min | `ci.yaml` job `e2e-lite-alerts` on every PR + push |
| `lite-callback.sh` | Airflow **`on_failure_callback`** (#424): a `@task` that raises must run its native `on_failure_callback` **in the task process**, once, on the terminal failure — resolved off the loaded task object (works for an unbound `with DAG():`) and called even though Airflow normalises it to a list. The callback POSTs the real Airflow context to a local receiver; asserts it fired exactly once. Guards the two integration bugs the unit tests (fake resolver/callback) missed | ~1 min | `ci.yaml` job `e2e-lite-callback` on every PR + push |
| `e2e.sh` | Pod-path E2E on k3d: build images, k3d import, agent-over-gRPC, real pod-per-task. Regression guards for the ADR 0040 features in the real pod path: generic operator/sensor execution, `ti.xcom_pull` chaining, `@task` run-context (`ds`), Admin Variable delivery, **multi-key XCom** (`LEOFLOW_PUSHES_PATH`), **native bash Jinja templating** (`{{ ds }}`, #382), and **cloud connection delivery** — a user-pasted credential (GCP `keyfile_dict`, AWS access keys, Azure client secret) created via the API survives encrypted-at-rest storage (ADR 0019) and is recovered intact inside the pod task, and **reschedule-mode sensors** (#380) — a `DateTimeSensor(mode='reschedule')` releases its pod on each not-ready poke (passes through `up_for_reschedule`) and is re-dispatched to success. Also gates the **`on_failure_callback` pod-path** (#424): a separate failing `@task` runs its callback in the pod on terminal failure (the wiring the Lite `lite-callback.sh` can't cover) | minutes | `ci.yaml` job `e2e-operators` (k3d) on every PR + push |
| `deploy-e2e.sh` | The real `dexaflow deploy` path (ADR 0041): `auth login` → one `dexaflow deploy` that builds for the cluster arch, **pushes to a registry the cluster pulls from**, captures the digest, re-pins `dag.json`, registers — then the cluster pulls the digest-pinned image and runs it | minutes | Manual / separate workflow |
| `dbt-e2e.sh` | dbt support (ADR 0042): `dexaflow compile` renders a dbt project's `manifest.json` into one task per dbt node, then the scheduler dispatches a **pod per node** whose agent runs `dbt seed/run --select <node>` against a shared Postgres warehouse, in dependency order, until every task succeeds and the mart materializes | minutes | Manual (needs `dbt` on PATH) |
| `dbt-connection-e2e.sh` | dbt **managed connections** (ADR 0043): a dbt task generates `profiles.yml` in the pod from a Dexaflow managed connection (not a baked one). The image bakes a **deliberately broken** `profiles.yml`; if tasks still succeed and the mart materializes, the runtime used the managed connection — proving no credential is baked | minutes | Manual (needs `dbt` on PATH) |
| `dbt-mixing-e2e.sh` | dbt **mixed with operators** (ADR 0043): a `dag.py` wires BashOperators around a `dbt_group()`; the compiler merges them into one `dag.json` and the scheduler runs operators **and** per-model dbt pods in order (operator → group roots, group leaves → operator), all succeeding with the mart materialized | minutes | Manual (needs `dbt`, `python3`) |
| `chaos-runtime.sh` | **Runtime fault-injection (#231 Phase 2).** Four scenarios: kills the scheduler process mid-run (asserts `/monitor/health` flips the scheduler unhealthy via the advisory-lock reader, the in-flight run **resumes** to success, and **at-most-once** holds — no duplicate `work` pod); force-deletes a running task pod (asserts the reaper moves the TI off `running` and leaves no orphan); recovers a success from the durable outcome record after the agent dies mid-report (ADR 0052); and replays the production outage — three parallel tasks whose pods terminate while the scheduler is SIGKILLed, asserting the reconciler settles all three **inside** the leader-settling grace, the gate was engaged, the liveness valve never opened, and **no reaper fired**. Subset with `LEOFLOW_CHAOS_ONLY=D`. Destructive; run inside the **Lima Linux VM**. `make chaos-runtime` | ~12 min (D alone ~5) | not in CI (exploratory/destructive) |
| `execution-timeout-e2e.sh` | **`execution_timeout` against a real kubelet (#925 / #930).** The one seam no unit test reaches: only a kubelet stamps `pod.Status.StartTime`, which is where `activeDeadlineSeconds` is counted from. A task declaring `execution_timeout_seconds: 10` whose body sleeps far past it (image **pre-loaded**, so the startup delta is a few seconds) must be interrupted by the **agent's** clock, not the kubelet's — RED before #925, GREEN after. Asserts the TI's `failure_reason` (**served on the API**; the UI does not render it) names `execution_timeout:` **and** that the pod's own `status.reason` is not `DeadlineExceeded`, which names the race winner directly; that the pod's **durable outcome record** carries the same diagnosis (#930) — the reason **bytes on the pod**, which is the channel a lost report would be settled from, not a claim about the reconciler's rendering, since the report does land here; that a pod created through the **real dispatch path** carries `activeDeadlineSeconds == timeout + startup headroom + effective termination grace`; and that an agent **frozen** (SIGSTOP from the k3d node — the pod stays `Running`, so only the reaper can settle it) after `RUNNING` is reaped as `agent_lost` inside 70-150 s (the agent-lost threshold minus a heartbeat, to threshold + sweep + heartbeat + slack) instead of lingering to the pod deadline. `make e2e-timeout` | ~8 min (includes the reaper's own waits) | Manual (needs the `migrate` CLI); candidate for its own k3d job in the heavy tier |
| `sso-login-page.sh` | **The sign-in page offers SSO when SSO is configured (#1160).** Boots two control planes, one under `provider: oidc` against a TLS fake IdP whose discovery document it must fetch at boot, one JWT-only, and drives both with a real browser: the OIDC one has to render a control that reaches the IdP's authorization endpoint carrying `state`, `nonce` and `code_challenge`, and keep the break-glass password form; the JWT-only one must not advertise a route its router never registered. Go handler tests pin the bytes; only a browser proves the user has a way in. **Linux only** (it trusts a throwaway CA through `SSL_CERT_FILE`, which the darwin build of `crypto/x509` never reads). `make e2e-sso` | ~1 min | `e2e-sso-login` job |
| `secrets-localstack.sh` | **External secrets resolver against a real provider backend (ADR 0060).** k3d + **LocalStack** (Secrets Manager): a DAG declares a connection + variable that are **absent from the vault**; registration accepts them (B1 coverage), and the task pod resolves them from LocalStack under the pod-side Python resolver — asserting the exact seeded values in the task logs. Exercises the yaml-declaration → agent → resolver → provider chain a fake backend can't (it surfaced the resolver's stdout-vs-provider-logging bug). Keyless identity (IRSA/Pod-Identity/WI) is the only part LocalStack can't emulate → EKS/GKE. | minutes | `secrets-e2e.yaml` on `internal/agent/**`, `runtime/python/**` + manual dispatch |

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
