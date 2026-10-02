---
title: "Python authoring & Airflow compatibility"
linkTitle: "Airflow compatibility"
weight: 15
description: "How Dexaflow relates to the Airflow Python API: you write standard Apache Airflow Task SDK code, and Dexaflow adds a thin runtime plus a packaging file — it never re-implements Airflow's Python surface."
---

Dexaflow does **not** invent a new Python API. You author DAGs in **standard Apache
Airflow Task SDK** code — the same `DAG`, `@task`, operators, sensors, and hooks
you already know — and Dexaflow adds only a thin runtime and a packaging file on
top. That is the whole compatibility story, and it is why Dexaflow's *own* Python
surface is deliberately tiny.

{{% alert title="The one thing to remember" color="primary" %}}
The Python you write is **Airflow's**, not Dexaflow's. Operators, sensors, hooks,
the `DAG`/`@task` API — all of that is the Apache Airflow Task SDK, documented on
[airflow.apache.org](https://airflow.apache.org/docs/). Dexaflow contributes a Go
control plane (no Python), a `dexaflow.yaml` packaging file, and a ~2-function
runtime shim. Nothing more.
{{% /alert %}}

## What you import is Airflow

A `dag.py` is real Apache Airflow SDK **3.2.x** code:

```python
from airflow.sdk import DAG, task
```

Everything reachable from there is the upstream Airflow authoring surface, and
Dexaflow re-implements **none** of it:

- **Operators & sensors** — `SnowflakeOperator`, `S3KeySensor`, the ~1,500
  provider classes — are the Airflow provider ecosystem. Dexaflow executes the
  real class inside your task pod rather than shipping its own copy. See
  [Operators & sensors](/author-dags/operators-sensors/) for the execution model
  and the current support surface, and [ADR 0040](/project/adrs/0040-airflow-operator-support/)
  for the rollout.
- **Hooks & Connections** — provider hooks read the standard `AIRFLOW_CONN_*`
  wire format; the control plane delivers the secret to the pod
  ([Variables & Connections](/author-dags/variables-connections/),
  [ADR 0021](/project/adrs/0021-exposing-variables-connections-to-pods/)).
- **The run context** — `{{ ds }}`, `params`, the data interval, templating —
  behaves as Airflow authors expect.

For the "big" reference a developer reaches for — the operator catalog, hook
arguments, templating fields — the source of truth is **Airflow's own docs**, not
this site. Dexaflow does not duplicate them.

## What Dexaflow adds

The Dexaflow-specific surface is small and worth learning once:

| Piece | What it is | Where it's documented |
| --- | --- | --- |
| `dexaflow.yaml` | Packaging & deploy config that pairs with `dag.py`. **Not** an Airflow file. | [DAG authoring](/author-dags/dag-authoring/) |
| Compile-to-artifact | `dag.py` + `dexaflow.yaml` → an immutable `dag.json` + image (ADR 0003) — parsed once, at compile time. | [DAG authoring](/author-dags/dag-authoring/) |
| `leoflow_runtime` | The in-pod shim that runs your task callable and bridges its return value to XCom — public API is just `run` and `xcom_pull`. | [Python runtime API](/reference/python-api/) |
| Go control plane | Speaks the Airflow-compatible `/api/v2/` and orchestrates pods. **Never imports Airflow.** | [Architecture](/concepts/architecture/) |

## Why the Python surface is small — by design

If you come from Airflow, its Python docs are enormous: operators, hooks,
executors, the scheduler, the metadatabase. Dexaflow's equivalent looks like
almost nothing — and that is the point, not a gap.

Dexaflow's bet is to **reuse** Airflow's authoring surface rather than reimplement
it. Building a hand-written runtime shim was prototyped and **deliberately shelved**
([ADR 0036](/project/adrs/0036-airflow-runtime-compat-shim/)): growing it
provider-by-provider is unbounded maintenance, and the real Airflow Task SDK
already makes provider hooks work today. So Dexaflow keeps the real SDK at the two
edges (a compile-time structural shim in [ADR 0024](/project/adrs/0024-dag-parsing-structural-shim/),
real execution in the pod) and adds only the glue.

The consequence: **there is no large Dexaflow Python API to learn.** You learn
Airflow (which you may already know) plus `dexaflow.yaml` and two runtime helpers.

## Where to go next

- **[DAG authoring](/author-dags/dag-authoring/)** — the two-file model and the
  compile lifecycle. Start here.
- **[Operators & sensors](/author-dags/operators-sensors/)** — how any provider
  operator runs on Dexaflow.
- **[Python runtime API](/reference/python-api/)** — the `run` / `xcom_pull`
  reference (the Dexaflow delta).
- **[Apache Airflow docs](https://airflow.apache.org/docs/)** — the operator,
  hook, and Task SDK reference Dexaflow relies on.
