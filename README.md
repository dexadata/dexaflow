<p align="center">
  <img src="website/static/assets/icon-256.png" alt="Dexaflow" width="140">
</p>

<h1 align="center">Dexaflow</h1>

<p align="center">
  <strong>A fast, container-native workflow orchestrator — compatible with the Apache Airflow UI &amp; REST API.</strong>
</p>

<p align="center">
  <em>The workflow orchestrator that ate Apache Airflow's lunch.<br>
  Same UI. Same vocabulary. Ten times the speed. Zero of the pain.<br>
  <strong>Native map-reduce for ML/AI</strong> — fan-out + reduce as a Python list comprehension, no XCom plumbing, no broker, no special operator.</em>
</p>

<p align="center">
  <sub>Formerly <strong>Leoflow</strong>. Every <code>leoflow</code> command, <code>leoflow.yaml</code>, <code>LEOFLOW_*</code> variable, image and chart name keeps working; see <a href="https://dexaflow.dexadata.ai/reference/configuration/#names-from-before-the-rename">names from before the rename</a>.</sub>
</p>

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Code quality: golangci-lint A+](https://img.shields.io/badge/golangci--lint-A%2B-00ADD8?logo=go&logoColor=white)](https://dexaflow.dexadata.ai/project/adrs/0012-code-quality-standards/)
[![CI](https://github.com/dexadata/leoflow/actions/workflows/ci.yaml/badge.svg)](https://github.com/dexadata/leoflow/actions/workflows/ci.yaml)
[![Security](https://github.com/dexadata/leoflow/actions/workflows/security.yaml/badge.svg)](https://github.com/dexadata/leoflow/actions/workflows/security.yaml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/dexadata/leoflow/badge)](https://securityscorecards.dev/viewer/?uri=github.com/dexadata/leoflow)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13068/badge)](https://www.bestpractices.dev/projects/13068)

[![Edition: Lite](https://img.shields.io/badge/edition-Lite-1F6FEB?labelColor=4a4a4a)](https://dexaflow.dexadata.ai/concepts/editions/#leoflow-lite)
[![Edition: Pro](https://img.shields.io/badge/edition-Pro-8957E5?labelColor=4a4a4a)](https://dexaflow.dexadata.ai/concepts/editions/#leoflow-pro-chart-installable)

---

## 📚 Documentation

**Full docs → <https://dexaflow.dexadata.ai/>** (DAG authoring, deploy, API reference, architecture).

| | |
|---|---|
| [Quickstart](https://dexaflow.dexadata.ai/get-started/quickstart/) · [Installation](https://dexaflow.dexadata.ai/get-started/installation/) | get Dexaflow running locally |
| [Operating modes](https://dexaflow.dexadata.ai/concepts/editions/) · [Editions](https://dexaflow.dexadata.ai/concepts/editions/) | Lite · Pro · Demo — the runtime split and the packaging split |
| [DAG authoring](https://dexaflow.dexadata.ai/author-dags/dag-authoring/) · [Variables & Connections](https://dexaflow.dexadata.ai/author-dags/variables-connections/) | write a DAG; the Lite → deploy lifecycle |
| [**Map-reduce for ML**](https://dexaflow.dexadata.ai/author-dags/map-reduce/) | fan-out + reduce as a Python list comprehension |
| [CI/CD & deploy examples](https://dexaflow.dexadata.ai/operate/cicd-deploy/) | GitHub Actions · GitLab · Cloud Build/Run · generic |
| [Helm chart](helm/dexaflow/README.md) | Pro install: values reference, hardening, PoC recipe |
| [HTTP API (Scalar)](https://dexaflow.dexadata.ai/api-reference.html) · [Go packages](https://dexaflow.dexadata.ai/reference/go/) · [MCP server](https://dexaflow.dexadata.ai/reference/mcp/) | API references & the LLM-agent surface |
| [Concepts & glossary](https://dexaflow.dexadata.ai/concepts/core-concepts/) · [Architecture](https://dexaflow.dexadata.ai/concepts/architecture/) · [ADRs](https://dexaflow.dexadata.ai/project/adrs/) | the model, the *why*, and every major decision |
| [Troubleshooting](https://dexaflow.dexadata.ai/operate/troubleshooting/) · [Security policy](SECURITY.md) | when things break; how to report a vulnerability |

---

<p align="center">
  <a href="https://dexaflow.dexadata.ai/">
    <img src="website/static/assets/screenshots/dev-grid-tasks.png" alt="Dexaflow running the Apache Airflow 3.2 UI — a DAG's grid view, task list, and run-duration overview" width="860">
  </a>
</p>

<p align="center"><em>The unmodified Apache Airflow 3.2 UI, served by a Go control plane.</em></p>

---

## ⚡ Install

Dexaflow ships in two editions. Pick the track that matches your target:

### Lite — the full engine on a single host

The **full Dexaflow control plane on one machine** — the same engine, the same
Airflow-3.2 UI, the same `dag.py` + `dexaflow.yaml` as Pro — in a one-command,
**no-Kubernetes** package. Light to run, powerful where it counts: real
pod-per-task execution, a **durable** Postgres datastore, hot-reload, a real
admin login. It is not a stripped-down demo — it is the whole engine scoped to
one host, carrying real workloads from local development to **light-to-medium
production** on a laptop, a single VM, or an internal server (trusted network).

```bash
curl -fsSL https://raw.githubusercontent.com/dexadata/leoflow/main/install.sh | sh
dexaflow lite                # hot-reload at http://localhost:8088 (LITE badge)
```

Installs the binaries and runs `dexaflow setup` (ensures Python, provisions the
parser, creates your workspace at `~/leoflow/`) — **no sudo, no system Python,
no package manager**. Docker is optional and only unlocks the Kubernetes
executor for higher-fidelity local runs. Linux + macOS, amd64 + arm64
(Windows via WSL2). Each DAG gets its own per-DAG venv under
`~/.dexaflow/dev/venvs/<dag_id>/` so conflicting dependencies between DAGs
coexist out of the box.

### Pro — Kubernetes cluster (Helm)

```bash
kubectl create namespace leoflow
helm install lf oci://ghcr.io/dexadata/charts/leoflow --version 0.4.8 -n leoflow \
  --set image.tag=v0.4.8 \
  --set migrations.image.tag=v0.4.8 \
  --set database.url='postgres://USER:PASS@HOST:5432/leoflow?sslmode=verify-full' \
  --set redis.url='rediss://HOST:6380/0' \
  --set auth.jwtSecret="$(openssl rand -base64 64)" \
  --set secretKey="$(openssl rand -hex 32)" \
  --set bootstrap.password='change-me'
```

Pro deploys the control plane on a real cluster (`leoflow-server` Deployment
+ RBAC for the pod-per-task executor + a pre-install migrations Job). **External**
Postgres 13+ and Redis 6+ are required (the chart fails the install otherwise —
embedded datastores are Lite-only). Managed datastores work out of the box
(Cloud SQL / RDS / Memorystore / ElastiCache / Azure Cache), with optional
`caConfigMap` knobs for verified TLS. See the
**[chart docs](helm/dexaflow/README.md)**.

Full guide for both tracks → **[Installation](https://dexaflow.dexadata.ai/get-started/installation/)**.

---

## The Five Wounds Apache Airflow Will Not Heal

Airflow is the most widely deployed workflow orchestrator on earth. It is also the one that bleeds the most in production. Every data engineer recognizes these wounds:

- **The scheduler that stalls.** Three seconds between tasks. Ten when the cluster is busy. Pipelines that should run in two minutes take twenty.
- **The triggerer that suffocates.** Above five hundred concurrent sensors, the Python asyncio loop chokes. Sensors stop firing. SLAs miss.
- **The DAG file that re-parses itself to death.** Every scheduler loop opens every `.py` file in `/dags`. CPU spikes for nothing. Memory grows. Restarts become a ritual.
- **The worker that leaks until it dies.** Long-running Celery workers accumulate file descriptors, database connections, half-loaded modules. OOMKilled at three in the morning. Always.
- **The dependency hell that has no door.** `pandas==1.0` for the legacy DAG, `pandas==2.0` for the new one. One Airflow image. Pick a side. Cry either way.

Dexaflow was built to close these five wounds, on day one, by construction.

## How It Closes Them

Dexaflow does not invent a new execution model. Pod-per-task is the right pattern, and Airflow's `KubernetesExecutor` proved it years ago. What Dexaflow does is **strip out the Python overhead from every layer of the orchestration stack**:

| Wound | Airflow today | Dexaflow |
|---|---|---|
| Scheduler latency | 3-10 seconds per decision | **<200 ms** — native Go, zero GIL |
| Sensor concurrency | ~500 (asyncio Triggerer) | **100,000+** — each sensor is a 2 KB goroutine |
| DAG parsing cost | Re-parsed every scheduler loop | **Zero** — DAG is pre-compiled to immutable JSON |
| Worker lifecycle | Long-lived, leak-prone | **Ephemeral pod per task** — spawn, run, die |
| Worker image size | 1.5 GB+ Airflow base | **200 MB typical** — each DAG is its own slim image |
| Dependency isolation | Workaround via `KubernetesPodOperator` | **Native** — every DAG is a container |
| Cold start | 15-45 s | **2-5 s target** — agent is a 15 MB static binary |
| Observability | Retrofitted with effort | **Native** — Prometheus + OpenTelemetry + structured logs from commit one |

This is not marketing. This is what falls out of replacing a Python control plane with Go and embracing the container as the unit of isolation.

## What Dexaflow Is

Dexaflow is a **GitOps-first, container-native workflow orchestrator** written in Go. Each phrase carries weight:

- **GitOps-first.** Your DAG is a versioned artifact (`dag.json` + container image), not live source code. CI builds it. The registry stores it. Rollback is a tag change.
- **Container-native.** Each DAG is its own container image, with its own dependencies, its own Python version, its own everything. Built automatically from a one-page `dexaflow.yaml` — you never touch Docker unless you want to.
- **Airflow-UI compatible.** The MVP runs the unmodified Apache Airflow 3.2.x UI. Your team's muscle memory survives the migration. No new tool to learn.
- **Go performance, Go discipline.** Static binary. No GIL. Goroutines for concurrency. Test-driven from the first commit. The full `golangci-lint` A+ stack enforced in CI.

## What It Looks Like to Use

A complete Dexaflow DAG project. No Dockerfile. No `requirements.txt`. No CI plumbing to invent.

```yaml
# dexaflow.yaml
dag_id: etl_vendas
python_version: "3.11"
dependencies:
  - pandas==2.1.0
  - requests==2.31.0
```

```python
# dag.py
from airflow.sdk import DAG, task

@task
def fetch():
    import requests
    return requests.get("https://api.example.com/orders").json()

@task
def transform(orders):
    return [{"id": o["id"], "value": o["amount"] * 1.1} for o in orders]

with DAG("etl_vendas", schedule="0 5 * * *") as dag:
    raw = fetch()
    processed = transform(raw)
```

```bash
dexaflow compile .              # generates Dockerfile, builds image, produces dag.json
dexaflow push ./dag.json        # registers with the control plane
```

That is the entire developer surface. The CLI builds the image on the published Dexaflow task base (`ghcr.io/dexadata/dexaflow-runtime:py3.11`, selected by `python_version`), pushes to your registry, and registers a versioned DAG. The Airflow UI shows it at the next refresh.

## Native map-reduce for ML/AI

Hyperparameter search, k-fold cross-validation, ensemble training, batch
inference, sharded preprocessing, Monte Carlo — **every parallel ML workload
is map-reduce**. Most orchestrators make you build it: an operator per fan-out,
a broker for the intermediate values, shared storage for the artifacts, and a
custom reducer that knows how to find them all. Dexaflow expresses the whole
pattern in **two lines of Python**:

```python
from airflow.sdk import DAG, task

@task
def trial(lr: float) -> dict:
    return train_one(lr)                            # map

@task
def select_best(trials: list[dict]) -> dict:
    return max(trials, key=lambda r: r["score"])    # reduce

with DAG("hparam_search", schedule=None):
    select_best([trial(lr) for lr in [0.001, 0.01, 0.05, 0.1, 0.5]])
```

That `[trial(lr) for lr in …]` is the whole map. `trials: list[dict]` is the
whole reduce. **No XCom plumbing, no broker setup, no shared filesystem, no
special operator** — the parser captures the list shape at compile time; the
runtime assembles the upstream XComs in declaration order and delivers them
as a real Python list. Per-trial isolation (own pod / own process), per-trial
retry, deterministic ordering, and a 256 KB cap per upstream — and a
`null` slot for any upstream that legitimately produced no result.

| ML pattern | Map | Reduce |
|---|---|---|
| Hyperparameter search | one task per `(lr, batch, seed)` triple | pick the best metric |
| K-fold cross-validation | one task per fold | average the metrics |
| Ensemble training | one task per base model | combine predictions / stack |
| Batch inference | one task per partition | collect predictions to a sink |
| Monte Carlo | one task per worker | average / sum results |

Runnable example: `examples/ml_hparam_search/`. Full reference:
**[Map-reduce for ML](https://dexaflow.dexadata.ai/author-dags/map-reduce/)** — guarantees, limits, what
activates fan-in vs what does not, and the on-disk `dag.json` shape.

## Architecture

A DAG is compiled into an **immutable artifact** (a `dag.json` spec plus a
container image) and pushed to the control plane. A Go **control plane**
schedules it and, for each task, dispatches an ephemeral **worker pod** whose
`leoflow-agent` runs the user code and reports back over gRPC. Postgres holds
metadata; Redis holds XCom values and live-log fan-out.

```mermaid
flowchart LR
    subgraph dev["Author / CI"]
        src["dexaflow.yaml · dag.py · Dockerfile"]
    end

    subgraph cp["Control plane — Go"]
        direction TB
        api["HTTP API /api/v2<br/>JWT · RBAC · multi-tenant"]
        sched["Scheduler<br/>state machine · cron<br/>leader election · retries"]
        asvc["Agent gRPC service<br/>task spec · state · XCom · logs"]
        api --- sched --- asvc
    end

    src -->|"dexaflow compile / push"| api
    sched --- pg[("Postgres<br/>metadata")]
    asvc --- redis[("Redis<br/>XCom · log tail")]

    sched -->|"dispatch: one pod per task"| pod
    subgraph k8s["Kubernetes"]
        pod["Worker pod = your DAG image<br/>leoflow-agent ⇄ your Python / Bash"]
    end
    pod -->|"gRPC: register · fetch spec · push XCom · stream logs · report state"| asvc

    classDef store fill:#1f2937,stroke:#4b5563,color:#e5e7eb;
    class pg,redis store;
```

Every task runs pod-per-task; an `HttpOperator` is a provider operator that runs
in its own pod like any other (ADR 0040). Read
[the ADRs](https://dexaflow.dexadata.ai/project/adrs/) for the reasoning behind every decision.

## Status

🧪 **Experimental — pre-1.0.** The HTTP API (`/api/v2`), CLI, and Helm chart
values may change between minor versions until **v1.0.0** locks them. **Lite**
(single host) is the recommended way to run Dexaflow today. **Pro** (Kubernetes)
is Helm-installable and in **active validation** — tested against GKE, not yet
certified for production; pin to a specific tag, read the
[upgrades guide](https://dexaflow.dexadata.ai/operate/upgrades/) before bumping, and exercise
[backup/restore](https://dexaflow.dexadata.ai/operate/backup-restore/) before you need to.

Versioning follows [ADR 0037](https://dexaflow.dexadata.ai/project/adrs/0037-release-version-scheme/):
`vX.Y.Z-rc.N → vX.Y.Z`, no separate alpha/beta.

**Implemented today:**

- **CLI + parser** — `dexaflow init / validate / compile / push / runs trigger / runs status / auth create-token`; the Python DAG parser; `compile --build / --push` builds and pushes the DAG image (out-of-process).
- **Control plane** — Airflow-compatible `/api/v2` API, JWT auth + RBAC + multi-tenant, the scheduler state machine with cron scheduling, Postgres advisory-lock leader election, **task retries**, embedded Scalar API docs, and Prometheus + OpenTelemetry observability.
- **Execution** — real pod-per-task execution via the `leoflow-agent` over gRPC (Kubernetes, ADR 0015); orphaned-pod reconciliation and completed-pod garbage collection.
- **Data flow** — XCom on Redis (256 KB limit, TTL, optional schema validation) passed between tasks; log shipping to disk **or an opt-in S3/GCS object-store sink** (S3-compatible, keyless-first per ADR 0035) with a read API and live tailing over Redis pub/sub.
- **dbt** — a dbt project runs as a DAG (pod-per-model or fused groups); managed warehouse connections generate `profiles.yml` in-pod, with modern service-account auth (Snowflake key-pair, BigQuery keyless / Workload Identity, Databricks OAuth M2M).
- **MCP + typed client** — an experimental [`leoflow-mcp`](https://dexaflow.dexadata.ai/project/adrs/0050-mcp-server/) Model Context Protocol server (read tools + resources over stdio / Streamable HTTP) and a generated, typed Go client for `/api/v2` (`pkg/client`).

**Not yet implemented:** load tests. Tracked refinements live in the [issue tracker](https://github.com/dexadata/leoflow/issues).

## Features

- Python, Bash, and HTTP API operators

- Python, Bash, and HTTP API operators
- DAG-as-Image model with automatic image build via `dexaflow.yaml`
- Hybrid DAG authoring: Python source parsed at compile time, or declarative YAML
- XCom on Redis with 256 KB limit, TTL, and optional schema validation
- Apache Airflow 3.2.x UI compatibility (no fork required)
- JWT authentication, RBAC, multi-tenant schema (OIDC-ready)
- Kubernetes-native execution (no worker pool to manage)
- Local development on Kubernetes (k3d/kind) or a dev-only subprocess executor (ADR 0015)
- Trigger rules: `all_success`, `all_failed`, `all_done`, `one_success`, `one_failed`
- Clear task instance to re-run failed tasks
- Leader election via Postgres advisory locks
- OpenSSF Best Practices compliance, signed releases (cosign), supply chain scanning (govulncheck + Trivy + CodeQL)

**On the post-MVP roadmap:**

- Optimized backfill (parallel execution with throttling)
- UI scaling for 10,000+ DAGs (caching, server-side pagination)
- Dynamic task mapping
- OIDC authentication (Google, Azure AD, Keycloak, Okta)
- Mark success/failed manually
- Custom UI (replacing the Airflow UI)
- Deferrable tasks (efficient dispatch + long-poll pattern, native Go implementation without a separate Triggerer process — see [ADR 0016](https://dexaflow.dexadata.ai/project/adrs/0016-deferrable-tasks/))

## Getting Started

### Try it locally (one command)

After the Lite install above, just run:

```bash
dexaflow lite
```

…then open **http://localhost:8088** (the **LITE** badge confirms you're on the
Lite instance). The first run provisions a managed Postgres + admin login, drops
example DAGs in `~/leoflow/examples/`, and hot-reloads every save. Recover the
admin password any time with `dexaflow lite reset-password`.

> Lite is the primary local path. The legacy Docker-Compose demo profile
> (`docker compose --profile demo up --build`, login `admin@leoflow.local` /
> `admin`) still works for CI / containerized-only environments — see
> [docs/local-deploy.md](https://dexaflow.dexadata.ai/contribute/local-dev-loop/). The pinned Airflow 3.2.x UI is a
> tactical MVP choice; a purpose-built Dexaflow UI is the long-term direction
> (ADR 0018).

### Local development

```bash
git clone https://github.com/dexadata/leoflow
cd leoflow
make setup            # Go tools, Python parser, pre-commit hook
make build            # builds bin/leoflow, bin/leoflow-server, bin/leoflow-agent

# Start Postgres + Redis (Docker) and apply migrations
make dev-up           # docker compose up --wait + migrate-up; `make dev-down` to stop

# Run the control plane (bootstraps a default admin user)
DEXAFLOW_AUTH_JWT_SECRET=dev DEXAFLOW_BOOTSTRAP_PASSWORD=admin123 ./bin/leoflow-server &
# API docs (Scalar) at http://localhost:8080/docs ; metrics at http://localhost:9090/metrics

# Author, compile, and register a DAG
./bin/dexaflow init my-dag
./bin/dexaflow compile my-dag --image my-dag:dev -o my-dag/dag.json
TOKEN=$(./bin/leoflow auth create-token --username admin@leoflow.local --password admin123)
./bin/dexaflow push my-dag/dag.json --token "$TOKEN"
```

> **Two dev environments.** `make dev-up` runs Postgres + Redis as plain Docker containers on the host for a fast inner loop (control plane on the host). For full in-cluster execution (control plane and dependencies on a local Kubernetes cluster, mirroring production and exercising real task pods), the [Helm chart](helm/dexaflow/README.md) is installable on any K8s cluster — chart-test CI gates every change with `helm lint` + `helm-unittest` (41 tests) + kind install/upgrade smoke. Task execution is on Kubernetes only (ADR 0015); the host containers are dev dependencies, not the execution path.

> The Airflow 3.2.1 UI ships embedded in the server and is served at `/` (see the one-command demo above and `docs/ui-compatibility.md`). The Scalar API reference is at `/docs`. Load tests are the remaining Phase 6 work.

## Honest Comparison

We have no patience for marketing fiction. Here is where Dexaflow sits in the landscape:

| | Airflow | Argo Workflows | Prefect | Dagster | **Dexaflow** |
|---|---|---|---|---|---|
| Language of control plane | Python | Go | Python | Python | **Go** |
| Pod-per-task model | Optional (KubernetesExecutor) | Yes | Optional | Optional | **Yes, only mode** |
| Dependency isolation per DAG | Workaround | Manual | Partial | Partial | **Native** |
| UI familiar to Airflow users | Yes | No | No | No | **Yes (Airflow UI)** |
| GitOps-first DAG model | No | Yes | Partial | Partial | **Yes** |
| Scheduler in Go (no GIL) | No | Yes | No | No | **Yes** |
| Native observability | Add-on | Partial | Partial | Yes | **Built-in** |
| Mental model | Celery-era | K8s-native | Python-native | Software-defined assets | **K8s-native + Airflow vocabulary** |

We borrow from Argo Workflows (container-native), from Prefect (modern developer experience), and from Airflow (the UI and vocabulary). We do not pretend we invented any of those. We just put them together in a way nobody had.

## Engineering Discipline

Dexaflow holds itself to a higher bar than most open source projects, because workflow orchestrators must be boring and reliable to be useful:

- **Strict TDD** — every line of production code is preceded by a failing test ([ADR 0011](https://dexaflow.dexadata.ai/project/adrs/0011-tdd-strict/))
- **golangci-lint A+ stack** — the goreportcard checks (gofmt, govet, gocyclo ≤ 15, golint, ineffassign, misspell) enforced in CI from the first commit ([ADR 0012](https://dexaflow.dexadata.ai/project/adrs/0012-code-quality-standards/))
- **GoDocs on every exported identifier** — no exceptions
- **Supply chain security from day one** — govulncheck, gosec, Trivy, CodeQL, Scorecard, signed releases ([ADR 0014](https://dexaflow.dexadata.ai/project/adrs/0014-supply-chain-security/))
- **Per-phase coverage floors** — rising from 70% to 85% across the MVP phases
- **Native observability** — Prometheus, OpenTelemetry, structured logs from commit one ([ADR 0010](https://dexaflow.dexadata.ai/project/adrs/0010-observability/))

If you contribute, read the [CONTRIBUTING guide](CONTRIBUTING.md) first.

## License

Apache License 2.0. See [LICENSE](LICENSE).

## The name 🇧🇷

> A note from Dexaflow's creator.

Dexaflow started in Brazil, every line of it, from the first commit. I'm
Brazilian, and so are this project's roots.

It was born as **Leoflow**. The *flow* part is easy: it orchestrates workflows.
The *Leo* part is personal. Early in my career, **Leonardo**
([@leonardo-jas](https://github.com/leonardo-jas)), an architect with deep roots
in Brazil's tech industry, taught me to think in **state machines**: states,
transitions, invariants, the things that must never happen. Years later, the
core of this project turned out to be exactly that: a scheduler that is,
underneath, a state machine. Naming it after the person who taught me that was
the obvious choice.

In v0.5.0 the project took the name of the company that builds it, DexaData,
and became Dexaflow. The dedication stays: it is where this project came from,
and the `leoflow` command, configuration and images keep working for everyone
who adopted it under that name.

A living tribute, paid while he's still very much building, to Leonardo, and to
the many like him who quietly hold up **Latin America's big-data ecosystem**.

Built in Brazil 🇧🇷, for data teams everywhere.

**Alisson Rosa** ([@neochaotic](https://github.com/neochaotic)), creator of Dexaflow

## Acknowledgements

Dexaflow stands on the shoulders of Apache Airflow. The team behind Airflow defined the vocabulary, proved the architecture, and built the UI that Dexaflow reuses without modification in the MVP. This project would not exist without their work, and we credit them at every layer of our documentation.

We also studied the source of Argo Workflows, Prefect, and Dagster carefully. Each made decisions worth borrowing, and we did.

---

> **Star the repo if you have ever waited five seconds for an Airflow task to start.**
> **Watch the repo if you want a heads-up on every release.**
> **Open an issue if you have a chronic Airflow pain we have not addressed yet — pre-1.0 is the time to shape the API.**

---

<sub>Apache Airflow® is a registered trademark of the Apache Software Foundation. Dexaflow is an independent project, not affiliated with or endorsed by the ASF.</sub>
