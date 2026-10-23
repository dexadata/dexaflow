---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0023-dag-authoring-config-binding.html
# --- end AUTO redirect aliases ---
title: "ADR 0023: DAG Authoring — Config Binding and Override Layers"
linkTitle: 0023 · DAG Authoring — Config Binding and Override Layers
weight: 230
description: "ADR 0023: DAG Authoring — Config Binding and Override Layers"
---

**Status:** Accepted
**Date:** 2026-05-24
**Deciders:** Project founder

## Context

A DAG author writes two files: a `dag.py` (real Apache Airflow SDK 3.2.x code,
imported by the Python parser via `DagBag`) and a `leoflow.yaml` (Leoflow's
deployment config). `leoflow compile` runs the parser to produce an immutable
`dag.json`, then overlays Leoflow-specific config onto it, builds the image, and
`leoflow push` registers the artifact (ADR 0003: one image per DAG; DAGs are
immutable artifacts).

Two questions arose while designing the authoring experience:

1. **Where do Leoflow-specific knobs live, and how are they bound to a DAG and
   to individual tasks?** Things like per-task `resources`, `retries`, `env`, and
   the staging volume (ADR 0022) are *not* Airflow operator attributes. You cannot
   invent kwargs on an Airflow operator (`PythonOperator(my_kwarg=...)` raises
   `TypeError` because the parser imports the real Airflow), so these knobs need a
   home outside the operator call.

2. **How do defaults and overrides layer?** An author wants a default for the
   whole DAG and the ability to override per task; an operator wants per-cluster
   defaults (e.g. the RWX `storage_class`, which differs between GKE / EKS / k3d)
   without re-baking the portable artifact.

### The Airflow dialect we accept (and its limits)

The parser imports the real Airflow SDK, so any `dag.py` Airflow can import,
imports. The constraint is in the *translation* to `dag.json`, not the parse:

- **Task types** are detected by operator class name: `Python` (incl. TaskFlow
  `@task`), `Bash`, `Http`. Any other operator → compile error.
- **Trigger rules**: `all_success`, `all_failed`, `all_done`, `one_success`,
  `one_failed`. Others → compile error.
- **XCom**: TaskFlow data-flow (`transform(extract())`) is resolved into
  `xcom_input`; manual `xcom_pull` is not detected.
- **Schedule**: string/cron/preset only.
- Not translated: branching (`BranchPythonOperator` is silently treated as plain
  `python` today — see Consequences), dynamic task mapping (`.expand`), sensors,
  KubernetesPodOperator, datasets/assets, Jinja templating, per-task
  `default_args` from `dag.py` (only `leoflow.yaml` defaults are honored).

## Decision

### 1. Binding

- **YAML ↔ DAG** binds by `dag_id` (already the case: `leoflow.yaml` declares
  `dag_id`, the parser loads that DAG).
- **YAML ↔ task** binds by `task_id`, via a `tasks:` map keyed by `task_id`:

  ```yaml
  dag_id: my_etl
  staging:
    enabled: true          # DAG-level
  tasks:
    transform:             # binds to task_id "transform"
      retries: 5
      env: { TZ: "UTC" }
      resources:
        requests: { cpu: "2", memory: 4Gi }
  ```

- **YAML-only for now.** Inline Python (`executor_config={"leoflow": {...}}`, a
  native Airflow per-task escape hatch) is a viable future evolution but is **not**
  implemented, to keep a single source of truth. If added later it would be the
  most-specific layer (see precedence) and is the only sanctioned way to attach
  Leoflow config inside `dag.py`.

### 2. Override layers and precedence

Three layers, **most specific wins**:

```
L2 task override (leoflow.yaml tasks.<id>)
  > L1 DAG default (leoflow.yaml defaults / default_args)
    > L0 platform default (server config, applied at dispatch)
```

- **L1 + L2 are merged at compile time** and baked into `dag.json`. The artifact
  carries the author's explicit intent and stays self-describing.
- **L0 is applied at dispatch time** by the control plane, filling only the gaps
  the artifact left empty. It is **never** baked into `dag.json`, because
  per-cluster values (storage class, default resources) would make the artifact
  non-portable (violating ADR 0003 immutability/portability). L0 lives in
  `executor.defaults` server config.

Precedence falls out naturally: L2/L1 set explicit fields on the `TaskSpec`;
`applyDefaultRetries` and the dispatcher's gap-fill only act when a field is
unset.

### 3. Overridable-per-level matrix

| Knob | L2 task | L1 DAG | L0 platform | Notes |
|---|---|---|---|---|
| `retries`, `retry_delay_seconds`, `execution_timeout_seconds` | ✅ | ✅ (`defaults`) | — | |
| `env` | ✅ (merged) | — | — | merged over compiled env |
| `resources` | ✅ | ✅ (`defaults`; both `cpu` and `memory` required) | ✅ | L0 fills per dimension: each of cpu, memory and ephemeral storage the task leaves empty (neither request nor limit) gets the platform request and limit; a dimension the task sets at all stays the task's (#802) |
| `execution` (node selector, tolerations, SA, pull policy) | ✅ | — | — | |
| `staging` (size, storage_class) | ❌ | ✅ | ✅ | **DAG-level only**: one RWX volume is shared atomically by the whole run (ADR 0022); per-task staging would break that semantic. L0 defaults size/class per cluster. |

### 4. Guardrails (fail closed, never silently drop)

- A `tasks:` entry naming a `task_id` absent from the compiled DAG → **compile
  error** naming the unknown id and listing the DAG's task ids.
- A duplicate `task_id` key in the YAML → **parse error** (yaml.v3 rejects
  duplicate mapping keys).
- A duplicate `dag_id` across projects in a monorepo is a **CI-gate concern**, not
  a single-project compile error: within one compile the `dag_id` is unique by
  construction, and re-pushing the same `dag_id` is the intended re-deploy path
  (`dags` is `UNIQUE (tenant_id, dag_id)` with `ON CONFLICT DO UPDATE`). The CI
  pattern (below) must detect cross-project collisions before push.

### 5. Dev loop vs CI artifact

- **Dev**: best local experience is a fast edit→run loop that skips the slow image
  build (subprocess executor, hot reload), running the same parser + overlay +
  guardrails in memory. (A `leoflow dev` watch command is planned separately.)
- **CI** (the authoritative path): on push, `leoflow compile` runs the parser +
  overlay + **guardrails as a gate**, builds + pushes the image (tag = git sha),
  and `leoflow push` registers the immutable artifact. The same guardrails that
  warn the dev locally fail the CI build — a duplicate `dag_id`/`task_id` or
  unknown task binding never reaches production.

## Consequences

- Authors get a clean separation: `dag.py` is pure Airflow logic; `leoflow.yaml`
  carries deployment knobs, bound explicitly by `dag_id`/`task_id`.
- The artifact stays portable: only the author's explicit config is baked;
  per-cluster defaults are layered in at dispatch.
- Typos fail loudly at compile, not silently in production.
- **Known sharp edge to address later:** `BranchPythonOperator` / `@task.branch`
  match the `Python` name check and are translated as plain `python`, losing
  branch semantics with no error. A follow-up should make unsupported control-flow
  operators a hard compile error rather than a silent mistranslation.
- The full authoring guide (`docs/dag-authoring.md`) and its documentation format
  are tracked separately; this ADR records the decisions, not the end-user guide.
