# parser/

Rules for changing the DAG parser (`leoflow_parser`), which compiles a `dag.py`
into `dag.json` without running task code. The root [AGENTS.md](../AGENTS.md)
applies too, and [`runtime/AGENTS.md`](../runtime/AGENTS.md) covers the Python
that runs inside task images.

- **Python stays out of the control plane.** The parser runs as a subprocess of
  `dexaflow compile` and of Lite (`python3 -m leoflow_parser`), never inside
  the server process (root principle 5,
  [ADR 0048](https://dexaflow.dexadata.ai/project/adrs/0048-no-user-code-in-control-plane/)).
- **The parser imports a structural shim, not Airflow.** `leoflow_parser/_shim/` provides the
  `airflow` names the compiler reads, using the standard library only
  ([ADR 0024](https://dexaflow.dexadata.ai/project/adrs/0024-dag-parsing-structural-shim/)).
  Unsupported operators are absent from the shim on purpose, so importing one
  fails early with a clear "not supported" error.
- **No third-party runtime dependencies.** `pyproject.toml` declares
  `dependencies = []`, and the project configuration reaches the parser as JSON
  in `LEOFLOW_PROJECT_CONFIG_JSON`, parsed by the Go CLI.
  `test_parser_package_has_no_vendored_yaml` fails if a YAML parser is vendored
  back in.
- **Fidelity is locked by golden files.** `tests/test_golden.py` compiles
  each example under `examples/` that has a file in `tests/golden/` and
  compares the result with the `dag.json` real Airflow produced for it. Its
  docstring has the command to regenerate a golden with
  `LEOFLOW_PARSER_BACKEND=airflow`; a golden changes only with the behavior
  change that explains it.
- **The output contract is a schema.** `dag.json` follows
  `docs/api/dag-schema.json`; the copy embedded in the binary must stay byte
  equal to it (`TestEmbeddedSchemasMatchDocs` in `internal/domain`).
- **Tested on the host Pythons we claim.** The `python-parser` job in
  `ci.yaml` runs the suite on each released interpreter that the host detector
  (`pythonCandidates` in `internal/setup/detect.go`) supports; the comment
  above the job says to add a minor there when it is added to the detector.
  `scripts/check-python-host-interpreters.sh` checks how the detector's list
  relates to the published task images.
- **Before pushing:** `ruff check .` and `pytest --cov=leoflow_parser` in this
  directory; CI fails under 70% coverage.
