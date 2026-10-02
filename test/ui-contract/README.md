# UI contract sweep

A browser-driven contract test: it drives every major view of the embedded
Airflow 3.2.x SPA against a running Dexaflow control plane and **fails if any view
makes a non-2xx `/api` or `/ui` call or logs a console error**.

Its purpose is to catch frontend↔backend contract breaks — especially when the
embedded Airflow SPA is **upgraded to a new version**. A renamed field, a new
endpoint the UI now polls, or a changed `Accept` header surfaces here as a broken
view, instead of as an empty panel a user discovers later.

This is a developer/CI smoke test (not part of `go test`); it needs a browser, so
it runs in a Dockerized Playwright.

## Run

Against the local demo control plane (already running on `:8080`):

```sh
test/ui-contract/run.sh
```

Save screenshots too:

```sh
UICONTRACT_OUT=/tmp/uic test/ui-contract/run.sh
```

Point it elsewhere / override the login:

```sh
LEOFLOW_BASE_URL=http://host.docker.internal:8080 \
LEOFLOW_USER=admin@leoflow.local LEOFLOW_PASSWORD=admin \
test/ui-contract/run.sh
```

The sweep **auto-discovers** a DAG that has a run (and a task instance) so the
DAG- and task-scoped views resolve. Override with `LEOFLOW_DAG_ID`,
`LEOFLOW_RUN_ID`, `LEOFLOW_TASK_ID` if needed. For meaningful task-view coverage,
have at least one DAG with a completed run (e.g. trigger `examples/lifecycle`).

## Output

One line per view (`[ok]` / `[BROKEN]`), then a summary. Exit code is non-zero if
any view is broken, so it gates a release or a UI bump.

`CONSOLE_ALLOWLIST` in `sweep.py` documents the few known-benign console messages
(e.g. the optional SQL-highlight wasm that is not embedded). Keep it short — each
entry is a tracked gap, not a license to ignore errors.
