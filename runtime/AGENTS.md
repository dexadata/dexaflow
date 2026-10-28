# runtime/

Rules for changing the task base image (`Dockerfile`) and the Python runtime
inside it (`python/`: `leoflow_runtime` and the `dexaflow` and `leoflow`
authoring packages). The root [AGENTS.md](../AGENTS.md) applies too, and
[`parser/AGENTS.md`](../parser/AGENTS.md) covers compile time.

- **Python runs here and in the parser, nowhere else.** This is where user task
  code runs, with the real Airflow Task SDK; the control plane runs no Python
  (root principle 5,
  [ADR 0024](https://dexaflow.dexadata.ai/project/adrs/0024-dag-parsing-structural-shim/),
  [ADR 0048](https://dexaflow.dexadata.ai/project/adrs/0048-no-user-code-in-control-plane/)).
- **The published Python versions have one source of truth:** the
  `python_version` enum in `internal/domain/schemas/leoflow-yaml-schema.json`.
  `scripts/check-python-runtime-matrix.sh` fails when the release matrix, the
  Makefile, the comment in `Dockerfile` or the configuration reference
  disagrees with it, so add or drop a version in all of them in one PR.
  `scripts/python-eol-warn.sh` watches upstream end-of-life dates weekly.
- **Pins live in the Dockerfile.** `GO_VERSION` follows `go.mod`
  (`scripts/check-go-toolchain-pin.sh`), and `AIRFLOW_TASK_SDK_VERSION` is the
  Task SDK that Lite also installs: the `runtime-authoring-contract` job in
  `ci.yaml` fails when `internal/cli/dev.go` pins another one.
- **What a `dag.py` imports must resolve in the image.** The
  `runtime-authoring-contract` job installs the runtime as a wheel with that
  Task SDK and imports `dexaflow`, `leoflow` and `airflow.sdk` from it; a name
  the parser shim accepts but the image lacks compiles and then fails in the
  pod.
- **The image is DAG authors' base.** Every DAG image is built `FROM` it
  ([ADR 0003](https://dexaflow.dexadata.ai/project/adrs/0003-dag-as-image/)) and
  keeps the agent built into it, so a change here reaches users only as they
  rebuild; the agent protocol rules in [`proto/AGENTS.md`](../proto/AGENTS.md)
  explain what that means for compatibility.
- **No third-party runtime dependencies.** `python/pyproject.toml` declares
  `dependencies = []` so the base images stay small.
- **The data variant is the one exception, and it is a separate image.**
  `RUNTIME_VARIANT=data` adds the hash-pinned packages of
  `runtime/data/requirements.txt` on top of the base, for Python 3.11 only.
  Never move a package from it into the base; see `data/README.md` to change
  the list.
- **Before pushing:** `ruff check .` and `pytest --cov=leoflow_runtime` in
  `python/`; CI fails under 90% coverage.
