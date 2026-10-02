# gcp_dataform_trigger — chained Google operators (Dataform)

Compiles a [Dataform](https://cloud.google.com/dataform) repository and runs its
workflow using the **real Google provider operators**, executed through Dexaflow's
generic operator path (ADR 0040). It is the reference for **chained operators**:
`invoke` consumes `compile`'s output the Airflow-idiomatic way —
`{{ ti.xcom_pull('compile')['name'] }}` — and Dexaflow resolves the upstream's
`return_value` just like Airflow, so the two operators pass data.

```
compile  (DataformCreateCompilationResultOperator)
   │  return_value = the CompilationResult (carries .name)
   ▼
invoke   (DataformCreateWorkflowInvocationOperator)
         workflow_invocation.compilation_result = {{ ti.xcom_pull('compile')['name'] }}
```

## Set up

1. Edit the constants at the top of `dag.py`:
   - `PROJECT`, `REGION`, `REPOSITORY` — your Dataform repository.
   - `GIT_COMMITISH` — the branch/tag/commit to compile (default `main`).
   - `SERVICE_ACCOUNT` — set **only** if your project enforces strict act-as on
     Dataform invocations; otherwise leave empty.
2. Credentials come from the `google_cloud_platform` Connection / ADC — see
   [`examples/gcp_gcs_load`](../gcp_gcs_load/) for the auth modes (keyless /
   Workload Identity is recommended). `connectors: [gcp]` installs the provider.

## Run

```bash
# Lite (local): host ADC via `gcloud auth application-default login`
dexaflow lite --executor=subprocess examples/gcp_dataform_trigger

# Pro: dexaflow compile examples/gcp_dataform_trigger --build --push -o dag.json
#      dexaflow push dag.json && dexaflow runs trigger gcp_dataform_trigger
```

`compile` logs the created compilation result; `invoke` runs the workflow and waits
for it to finish.

## Notes

- **Chaining idiom:** Dexaflow resolves `ti.xcom_pull('<task>')` for a declared
  dependency (`compile >> invoke`). The `.output` / XComArg idiom is not captured yet
  — use the `{{ ti.xcom_pull(...) }}` template, as here.
- Operators run standalone (no live Airflow metastore): templating and XCom chaining
  via `return_value` work; deferrable mode is not supported (the operator runs
  synchronously in the pod).
