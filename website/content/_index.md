---
title: Leoflow
linkTitle: Home
description: A Go control plane with an Airflow-compatible UI and REST API — container-native orchestration with native map-reduce, and none of the Python pain.
---

{{% blocks/cover title="Leoflow" subtitle="The orchestrator that ate Airflow's lunch" image_anchor="top" height="med" color="primary" %}}
A **Go control plane** with an Airflow-compatible UI and REST API — zero of the
Python pain. Native map-reduce for ML/AI: fan-out + reduce as a list comprehension.

<a class="btn btn-lg btn-primary me-3 mb-4" href="/get-started/quickstart/">
  Get started <i class="fas fa-arrow-alt-circle-right ms-2"></i>
</a>
<a class="btn btn-lg btn-secondary me-3 mb-4" href="/why-leoflow/">
  Why Leoflow <i class="fas fa-heart ms-2"></i>
</a>
<a class="btn btn-lg btn-secondary me-3 mb-4" href="https://github.com/dexadata/leoflow">
  GitHub <i class="fab fa-github ms-2"></i>
</a>
{{% /blocks/cover %}}

{{% blocks/lead color="dark" %}}
A DAG is a `dexaflow.yaml` plus a `dag.py` (the real Airflow SDK) that compile to
**one immutable artifact** — a `dag.json` and a container image. Parsed once, at
compile time. No shared `/dags` filesystem, no dependency hell, no re-parsing on
every tick.
{{% /blocks/lead %}}

{{% blocks/section color="white" type="row" %}}

{{% blocks/feature icon="fa-solid fa-pen-ruler" title="Author" url="/author-dags/dag-authoring/" url_text="Author a DAG" %}}
Write a `dag.py` on the Airflow Task SDK, declare packaging in `dexaflow.yaml`, and
compile it to an immutable image. Native **map-reduce** for ML/AI as a Python list
comprehension.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-solid fa-cubes-stacked" title="dbt in your DAGs" url="/author-dags/dbt/" url_text="Put dbt in a DAG" %}}
Drop `dbt_group()` into your `dag.py` and your **dbt** project's models become tasks
in the same graph — pod-per-model against your warehouse, no Airflow and no Cosmos
at runtime. Your `ref()` graph is the shape; you never write the dependencies.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-solid fa-plug" title="Connect" url="/connections/" url_text="Browse connectors" %}}
54 documented connectors — Postgres, Snowflake, AWS, GCP, Kafka, Slack and more.
Declare a provider, wire a Connection, and the control plane delivers it to your
task pod.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-solid fa-robot" title="Automate with MCP" url="/mcp/" url_text="The MCP server" %}}
Point an AI agent at Leoflow over the **Model Context Protocol**: *"diagnose the
latest failed run of `sales`."* Read-only, scoped to the caller's token — its blast
radius is your own API rights.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-solid fa-gears" title="Operate" url="/operate/first-pro-dag/" url_text="Deploy & operate" %}}
Go from `leoflow lite` on one host to a Kubernetes control plane. CI/CD deploy,
Helm, upgrades, backup/restore, scheduler resilience, and warm worker pools.
{{% /blocks/feature %}}

{{% blocks/feature icon="fa-brands fa-github" title="Contribute" url="/contribute/contributing/" url_text="Start contributing" %}}
GitOps-first and TDD-strict. A real from-source dev loop with `leoflow lite` —
isolated cluster, hot reload — plus the `make lite-redeploy` inner loop for Go
changes.
{{% /blocks/feature %}}

{{% /blocks/section %}}

{{% blocks/section color="primary" %}}
### Start where you are

**New here?** [Quickstart](/get-started/quickstart/) gets Leoflow Lite running in
two commands. **Evaluating?** [Why Leoflow](/why-leoflow/) and
[Editions & modes](/concepts/editions/) lay out the model and the Lite/Pro split.
**Building?** The [Reference](/reference/) has the HTTP API, CLI, Go packages, and
every `DEXAFLOW_*` config key.
{{% /blocks/section %}}
