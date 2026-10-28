---
title: "Review checklist: Airflow compatibility"
linkTitle: Airflow compatibility
weight: 10
description: Check an Airflow compatibility claim against the parser, the authoring schema and the domain types, not against what Airflow does.
---

Airflow compatibility is a hard target (root `AGENTS.md`, principle 8), and
the most common review mistake is to answer from knowledge of Airflow. Airflow
supporting something says nothing about Dexaflow supporting it. Each question
below names the file that settles it.

## Authoring: does Dexaflow accept it?

- [ ] **The parser can see it.** The parser executes `dag.py` against a
  structural shim, not Airflow
  ([ADR 0024](/project/adrs/0024-dag-parsing-structural-shim/)). A class or
  argument that is not in `parser/leoflow_parser/_shim/` does not exist at
  compile time, and `parser/leoflow_parser/compiler.py` decides what it reads
  from what is there.
- [ ] **The authoring schema allows it.** `dexaflow.yaml` is validated against
  `docs/api/leoflow-yaml-schema.json` (embedded in the binary from
  `internal/domain/schemas/`, kept equal by `TestEmbeddedSchemasMatchDocs`).
- [ ] **The compiled DAG carries it.** `dag.json` follows
  `docs/api/dag-schema.json`, and the control plane reads it into
  `DAGSpec` and `TaskSpec` (`internal/domain/dag.go`). A field the compiler
  emits but `TaskSpec` drops never reaches the scheduler or the pod.
- [ ] **Something acts on it.** A setting that is parsed and stored but read by
  nothing is a silent no-op for the author. Find the code that reads it (the
  planner in `internal/scheduler`, dispatch, the agent or the runtime) and the
  test that drives it.
- [ ] **Unsupported means a clear error.** What Dexaflow does not support fails
  at compile with a message that says so (`not supported by Dexaflow` in
  `compiler.py`), never at run time in the pod.
- [ ] **Semantics match.** Where Dexaflow implements an Airflow concept, it
  keeps Airflow's meaning: trigger rules (`EvaluateTriggerRule` and its table
  test in `internal/scheduler`), `pool_slots`
  ([ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/)),
  retries and state names. A deliberate difference is written down on the page
  that describes the feature.
- [ ] **The golden files still hold.** `parser/tests/test_golden.py` compares
  the shim's output with real Airflow's for the examples; a changed golden is
  explained by the change under review.

## Runtime: does it run in the pod?

- [ ] **The import resolves in the task image.** User task code runs on the
  Airflow Task SDK that `runtime/Dockerfile` pins
  (`AIRFLOW_TASK_SDK_VERSION`); the `runtime-authoring-contract` CI job imports
  the authoring names from the installed wheel. A name the shim accepts but the
  image lacks compiles and then fails in the pod.

## API and UI: does the Airflow UI still work?

- [ ] **`/api/v2/` keeps Airflow's shape.** Paths, parameters and fields match
  Airflow 3.2.x; Dexaflow-only data is an added field or a separate route
  (`internal/api/AGENTS.md`). Compare with Airflow's own OpenAPI document for
  that version, not with memory.
- [ ] **The UI bundle version is the one in `internal/ui/assets/VERSION`**
  (`scripts/check-airflow-ui-pin.sh` keeps the other mentions in line). An
  endpoint that bundle calls and Dexaflow does not serve degrades instead of
  failing (`internal/api/airflow33_unserved.go`,
  [ADR 0018](/project/adrs/0018-ui-custom-north-star/)), and is listed as a
  known gap in [UI compatibility](/concepts/ui-compatibility/).
- [ ] **A browser saw it.** A change the UI reads ran the Playwright flows
  (`test/ui-contract/`, `test/e2e/ui-smoke.js`), with screenshots of the
  changed screens (release review 5).

## Docs

- [ ] [Airflow compatibility](/author-dags/airflow-compatibility/),
  [Operators and sensors](/author-dags/operators-sensors/) and
  [UI compatibility](/concepts/ui-compatibility/) still say what is true after
  the change.
