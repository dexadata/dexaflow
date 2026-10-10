# leoflow-runtime

The Python helper baked into every Dexaflow task base image. It runs the user's
task callable inside the container and bridges data flow with the control plane:

- `python -m leoflow_runtime <module:callable>` imports and calls the task,
  writing a non-`None` return value to the path in `DEXAFLOW_RETURN_VALUE_PATH`
  (the `leoflow-agent` sets this to a unique per-task file; it falls back to
  `/tmp/leoflow_return_value.json` only when unset). The agent reads that file
  and pushes it as the task's `return_value` XCom.
- `leoflow_runtime.xcom_pull("name")` returns an upstream XCom the agent injected
  as `DEXAFLOW_XCOM_<NAME>`.

This package contains no third-party dependencies so the base images stay small.
