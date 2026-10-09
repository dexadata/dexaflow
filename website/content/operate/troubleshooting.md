---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /troubleshooting.html
# --- end AUTO redirect aliases ---
title: "Troubleshooting & observability"
linkTitle: Troubleshooting
weight: 60
description: "Diagnose DAG, scheduler and executor problems; where the logs and signals live."
---

Symptoms grouped by where they surface. Start with the diagnostics — most
issues are one `dexaflow doctor` away from a clear cause. New here? The
[Quickstart](/get-started/quickstart/) and [Installation](/get-started/installation/)
guides cover a clean first run; this page is where you land when one goes wrong.

## First things to run

```bash
dexaflow doctor                          # host check (OS, python, docker, k3d, kubectl, recommended tier)
dexaflow version                         # version + commit + build date
tail -f /tmp/leoflow-lite.log           # the live boot log when you ran `dexaflow lite` via lite-redeploy
journalctl -u dexaflow-server -f         # Pro / systemd hosts
```

{{% alert title="Every binary reports its version" color="success" %}}
When filing a bug, include the exact build. The root CLI takes both
`dexaflow version` (with commit + build date) and `dexaflow --version`, and each
companion binary answers `--version`:

```bash
dexaflow --version
dexaflow-server --version
dexaflow-agent --version
dexaflow-mcp --version
```
{{% /alert %}}

## Install & setup

| Symptom | Cause / fix |
|---|---|
| `command not found: dexaflow` | The binary is not on `PATH` — re-run `curl … \| sh`, or open a fresh shell to pick up the install-script's PATH line. Building from source? `go install .../cmd/dexaflow@latest` and add `$(go env GOPATH)/bin` to `PATH`. |
| `dexaflow setup` says "python: none on PATH" but you have `python3.12` | Older Dexaflow versions only matched literal `python3.11`. Update to the latest release — `setup` now accepts any `python3.11`+ that's on `PATH`. |
| Install on Alpine / musl fails fetching CPython | The musl-libc relocatable CPython build can be missing system libs. `dexaflow lite --postgres docker` falls back to the Docker Postgres path instead of the embedded managed one. |
| Control plane never becomes ready; logs show `remaining connection slots are reserved` or `too many connections` (SQLSTATE 53300) | Every control-plane pod opens `database.maxIdleConns` + 3 connections at boot and can grow to `database.maxOpenConns` + 3 under load, plus 1 for the migration Job. On a small managed Postgres (Cloud SQL `db-f1-micro`, the smallest RDS or Azure tiers, about 25 slots) that can exceed `max_connections`, or a per-role or per-database `CONNECTION LIMIT`. The boot error names the setting; lower `database.maxOpenConns` / `database.maxIdleConns` in the chart values, or raise the server's limit. `helm install` prints the estimated boot and peak demand in its notes. |

## `dexaflow lite` boot

| Symptom | Cause / fix |
|---|---|
| `error: duplicate dag_id in workspace — rename one of the colliding projects` | The workspace has two project directories declaring the same `dag_id` — the most common cause is clicking the IDE's "Download examples" while a same-named project already exists at the workspace root. Delete or rename one of the two copies, then re-run `dexaflow lite`. Recent builds skip the example when a collision is detected (#298). |
| `provision incomplete: dev database` | The managed Postgres did not start. End-users run `dexaflow setup` to bootstrap the managed runtime. Contributors on a source checkout use `dexaflow lite provision`. If Docker is the chosen backend, confirm the daemon is up. |
| Pro refuses to boot with `DEXAFLOW_AGENT_ALLOW_INSECURE_SECRETS=true` set | The Pro edition rejects this flag at boot (it would expose plaintext secrets). Unset it for Pro deployments; it stays valid for Lite where the agent talks loopback gRPC without TLS by design. |
| `jwt_secret is empty; falling back to the dev-only constant` | First boot before `dexaflow setup` has run, or `DEXAFLOW_SECRET_KEY` not set. Run `dexaflow setup` — it provisions a per-install secret. Not fatal on Lite (the constant works), but rotate before sharing the install. |
| Permission denied on `/tmp/leoflow-*` | Older Lite versions shared `/tmp/leoflow*` paths across users on multi-user hosts. Update to the latest release — paths are now per-user. |

## Running a DAG

| Symptom | Cause / fix |
|---|---|
| `declares unknown connection(s)` on the first `dexaflow lite` of a project | Lite seeds a declared connection from `AIRFLOW_CONN_<ID>` when your environment already carries it, so this usually means the variable is absent or misspelled — the lookup upper-cases the id, so `my_db` reads `AIRFLOW_CONN_MY_DB`. Only connections the DAG **declares** are considered, a connection already in the vault is never replaced by the environment (use `dexaflow connections set` to change one), and a URI that cannot be parsed is reported by name and not stored. |
| `dexaflow compile` dumps a Python traceback with internal parser paths first | Recent builds lead the failure with the user-facing line (e.g. `SyntaxError: ...`) and put the parser paths in the bounded tail. If you still see the internal-first dump, you are on an older release — update. |
| `dexaflow validate` reports a `SyntaxError` in code that runs fine on the cluster | The local tool judged your `dag.py` with a different Python minor than the one the project declares. Typical tell: `type Alias[T]`, or any other 3.12+ syntax, flagged on a project declaring `python_version: "3.12"` or later. Recent builds honour the declared version and warn instead of guessing: update, then install that minor (or run `dexaflow setup`). `dexaflow compile` is not fixed yet ([#1095](https://github.com/dexadata/dexaflow/issues/1095)); run `dexaflow setup` under the minor you declare. See [Which interpreter reads your DAG](/reference/configuration/#which-interpreter-reads-your-dag). |
| `warning: skipping dag.py syntax check` on `dexaflow validate` | Working as intended: the project declares a `python_version` newer than any interpreter installed here, so the lint is skipped rather than run under an older one and reject valid code. A newer interpreter than the declared one is used without complaint. Install the named interpreter, or run `dexaflow setup`, to turn the check back on. Your `dexaflow.yaml` is still validated. |
| `dexaflow lite` reinstalls every dependency after you change `python_version` | Expected. The venv is rebuilt on the new interpreter, and the "already installed" markers are discarded with it, because a marker records what one interpreter has, so it cannot outlive that interpreter. |
| `dexaflow compile` rejects a sensor / Jinja template / branching operator | This is **intentional** — Dexaflow accepts a closed set of task types (`python`, `bash`, `airflow_operator`). See [DAG authoring → Not supported](/author-dags/dag-authoring/#not-supported--dexaflow-compile-rejects-these) for the full list and workarounds (`@task` + poll loop for sensors; build values from `airflow.sdk` context for Jinja). |
| `Compiled .../dag.py -> dag.json (image , version dev)` (dangling comma) | Older build — update. Recent versions render `(no image, version dev)` when `--image` is unset. |
| Task pod `ErrImagePull` (cluster mode) | The DAG's image is not in the cluster — rebuild + import. Cluster-mode rebuilds on save; for a manual push, `dexaflow compile --build --push`. |
| Run stuck at `queued` (subprocess) | The agent must reach the control plane — Lite uses `127.0.0.1:<grpc>`. The executor launches async and the agent reports state back. Look for the agent process in `ps`; if it exited, check `/tmp/leoflow-lite.log` for the launch error. |
| Run stuck at `running` long after the task finished | The agent's heartbeat reaper picks these up after the configured window. Check `DEXAFLOW_TI_HEARTBEAT_TIMEOUT_SECONDS` and look for a `reaped` log line. |
| A task's outbound TLS call to one endpoint hangs or resets, right after a green build, from inside your network | The task base image moved to Debian 13 (trixie)'s OpenSSL 3.5, which puts a post-quantum key share in the first TLS ClientHello — growing it from ~517 to ~1525 bytes. Some middleboxes and TLS-inspecting proxies mishandle a ClientHello that no longer fits one segment. Restore the classical group list by pointing `OPENSSL_CONF` at a file setting `[system_default_sect]` / `Groups = x25519:secp256r1:x448:secp521r1:secp384r1` (confirmed: puts the ClientHello back to 517 bytes). |
| Task pod `CreateContainerConfigError: container has runAsNonRoot and image will run as root` | Your task image runs as UID 0; the executor's `taskPodSecurity.runAsNonRoot` default refuses it. Fix: numeric `USER 65532:65532` in your Dockerfile, or an operator sets `taskPodSecurity.runAsNonRoot: false`. See [Deploy prerequisites](/operate/deploy-prerequisites/#4-non-root-task-image). |
| `CreateContainerConfigError: secret "<release>-secrets" not found` on the control plane | Chart-managed credentials are missing. On charts before 0.4.7 that Secret was a Helm **hook**, and under Argo CD a hook is deleted and recreated in separate passes of one sync, is absent from the compared state, and is not restored by self-heal. An interrupted sync therefore removes it for good while the Application still reports `Synced`. Run an explicit sync (`argocd app sync`) and the pods recover with no restart, because the kubelet retries container creation once the Secret returns. From 0.4.7 the Deployment reads `<release>-credentials`, an ordinary tracked resource, so deleting it makes the app OutOfSync and self-heal restores it. |
| `dexaflow deploy`/`push` fails on auth, registry, or a version conflict | One of the deploy-time gates. [Deploy prerequisites & why shortcuts fail](/operate/deploy-prerequisites/) covers every gate with the exact error and fix. |

## UI / browser

| Symptom | Cause / fix |
|---|---|
| `Invalid credentials` on the login page even with the right password | Disable autofill or type the password manually — some browsers append a trailing space. Usernames are trimmed, passwords are not (per security best practice). |
| Login rate-limits you out after a few typos | Older builds counted *every* attempt against a 5/min cap; the fix splits successful and failed attempts so a typo does not block recovery. Update to the latest release. |
| A DAG you deleted weeks ago still runs and fails | Lite's state lives in `~/.dexaflow/dev` and outlives sessions, so a DAG registered during an old spike stays registered and keeps being scheduled. The ready banner now reports how many DAGs earlier sessions left (`state: N DAGs registered by earlier sessions`); dexaflow lite --fresh starts from an empty local database. Deleting the project directory stops it being re-registered, but does not deregister what is already there. |
| No **Lite** badge on `http://localhost:8088` | You are likely on the **Demo** (production-shaped reference, port `8080`) — Lite runs on `8088` with a silver `Dexaflow Lite` badge. See [operating modes](/concepts/editions/). |
| Copy-logs button silently fails over `http://<lan-ip>:8088` | The Clipboard API requires a secure context, so plain HTTP origins (LAN access from another machine) used to break copy. Recent builds inject a polyfill (`document.execCommand('copy')` fallback) — update. |
| `server returned 401` from `push`, `deploy`, `dags`, `runs`, `connections` or `variables` | Usually the saved token belongs to a **different** control plane. `~/.dexaflow/config.yaml` holds one `server_url` and one `token`, written together by `dexaflow auth login`; passing `--server` points the command elsewhere while still sending that token. The error now names both servers and prints `dexaflow auth login --server <the one being called>` — run it, or unset `DEXAFLOW_TOKEN` if an env token is shadowing the file. Holding several servers' tokens at once is [#1102](https://github.com/dexadata/dexaflow/issues/1102). |
| Task state badge does not refresh after "Mark as failed/success" | Known upstream Airflow bug — see [apache/airflow#67883](https://github.com/apache/airflow/issues/67883). The server-side mutation persists correctly; the SPA cache update is the gap. Hard-refresh the page (Cmd+Shift+R) to see the new state. |
| Browser tab title shows "Airflow" not "Dexaflow Lite" | Old build; the SPA shell rewrites the `<title>` to the configured instance name at request time. Update to the latest release. |

## Single sign-on (OIDC)

| Symptom | Cause / fix |
|---|---|
| Sign-in page still shows only username/password | No OIDC flow was discovered at boot (`auth.provider` is not `oidc`, or discovery failed). The SSO control only renders when the server registered the route. Check the server log for `oidc setup:` at boot. |
| A configured SSO deployment refuses every login, and nothing explains why | The callback answers a generic 403 by design (a browser must not learn why a login was refused). Read the audit log for `oidc.%` actions and their `metadata->>'reason'`, and the server's WARN-level `oidc: login denied` log line, which also carries the reason and, for the token_invalid and group_claim_overage arms, the underlying error. The full reason vocabulary (`tenant_not_allowed`, `no_user_jit_off`, `jit_failed`, `unknown_role`, `inactive`, `tenant_mismatch`, `issuer_mismatch`, `token_expired`, and the rest) is documented in [SSO with Google Workspace: when a login is denied](/operate/sso-google-workspace/#when-a-login-is-denied); the mechanism and every reason there is provider-agnostic. |
| SSO looks dead before the IdP redirect even happens | Check the boot log first. Four configurations are named at boot rather than at login time: an empty `auth.oidc.client_secret`, `auth.oidc.jit_provisioning` off, `auth.oidc.role_mappings` set with no `default_role`, and IdP discovery timing out (bounded at 15s). See [When no login is even attempted](/operate/sso-google-workspace/#when-no-login-is-even-attempted). |
| Not on Google Workspace | [SSO with Microsoft Entra ID, Okta, or another OIDC provider](/operate/sso-other-providers/) covers the two settings that differ per IdP. |

## Reset paths (when in doubt)

```bash
dexaflow lite reset-password --user admin@leoflow.local # fresh admin password, signs out old sessions (no sudo)
dexaflow lite --fresh                                     # start a session with nothing registered (DESTRUCTIVE)
dexaflow db reset --yes                                  # drop + recreate the Lite database (DESTRUCTIVE)
dexaflow uninstall                                       # remove ~/.dexaflow (binaries, managed Python, config)
dexaflow uninstall --purge                               # also remove the workspace (your DAGs!)
```

## Logs

Task logs stream from the agent over gRPC to the control plane's log sink and
are served at
`/api/v2/dags/<dag>/dagRuns/<run>/taskInstances/<task>/logs/<try>` (the UI's
drill-down), or from the CLI: `dexaflow runs logs <dag_id> <run_id> <task_id>
[--try N] [-f]` (landing in v0.4.1). The sink directory is `DEXAFLOW_LOGS_DIR`
(must be writable; `dexaflow lite` points it at a temp dir).

{{% alert title="Not kubectl logs" color="warning" %}}
On Pro, `kubectl logs <pod>` shows only the **agent wrapper's** own stderr —
never the task's stdout/stderr, which the agent ships to the control plane's
log sink instead. Use the UI, the API route above, or `dexaflow runs logs`.
{{% /alert %}}

Control-plane logs are structured `slog` (JSON by default), one line per HTTP
request with a request id — `grep <request_id>` correlates a UI click to its
backend trace.

### "the request could not be completed; see the server logs"

API error bodies never carry the underlying failure. A storage error carries
the database's own text — for Postgres, `severity: message (SQLSTATE code)`,
with the constraint, table and column names of the schema inside the message —
and Dexaflow is multi-tenant, so that text stays server-side (CWE-209). The
response says only which *kind* of failure it was:

| Status | Body detail | Means |
|---|---|---|
| 400 | `the request was rejected by a validation rule` | Input a caller can fix. Rules Dexaflow states itself (an unknown role, an undeclared variable or connection) name the offending value instead. |
| 404 | `the requested resource does not exist` | No such DAG, run, task instance, variable, connection or pool for this tenant. |
| 409 | `the request conflicts with the current state of the resource` | A duplicate write, or a rule such as the `max_active_runs` cap, which names itself. |
| 499 | `the client closed the request before it completed` | The caller went away (the UI supersedes in-flight grid requests routinely). Not a server fault. |
| 500 | `the request could not be completed; see the server logs` | A server-side failure. The cause is in the log line for that request. |

The cause is on the control-plane's request log line, under `cause`, alongside
the `request_id` the response header `X-Request-Id` carries:

```bash
journalctl -u dexaflow-server -o cat | grep '"request_id":"<id>"' | jq '.cause'
# "upserting dag: ERROR: ... violates foreign key constraint \"dag_versions_dag_id_fkey\" (SQLSTATE 23503)"
```

`kubectl logs deploy/<release> -c leoflow-server | jq 'select(.cause) | {path, status, cause}'`
lists every request that failed with a server-side cause.

### "…: the request could not be completed; see the control-plane logs" in a task log

The agent inside a task pod talks to the control plane over gRPC, and the same
rule applies there for the same reason: the pod runs *your* image and
entrypoint, so it is not a place to put the database's text. A failed agent RPC
therefore reads as the step that failed plus a fixed phrase, for example:

```
fetching task spec: rpc error: code = Internal desc = loading task spec: the request could not be completed; see the control-plane logs
```

The step name is the diagnostic half and is always there. All sixteen:
`loading task spec`, `loading task spec for scope enforcement`, `recording
state`, `recording reschedule`, `storing xcom`, `reading xcom`, `fetching
variables`, `fetching connections`, `resolving pod to agent identity`,
`minting agent token`, `opening log sink for task; logs will not be shipped`,
`writing log line`, `flushing logs`, `receiving log line`, `receiving
assignment request`, `receiving assignment ack`. The cause is on the
control-plane log line of the same name, carrying the attempt identity:

```bash
kubectl logs deploy/<release> -c leoflow-server \
  | jq 'select(.cause) | select(.run == "<run_id>" and .task == "<task_id>") | {msg, try, cause}'
# {"msg":"loading task spec","try":1,"cause":"loading run: ERROR: … (SQLSTATE 42P01)"}
```

On Lite the same line comes from the service journal rather than a pod — worth
knowing because `opening log sink` against a filesystem sink is a
characteristically Lite failure:

```bash
journalctl -u dexaflow -o cat \
  | jq 'select(.cause) | select(.run == "<run_id>" and .task == "<task_id>") | {msg, try, cause}'
```

Three messages are *not* redacted, because they are Dexaflow's own words about
your DAG rather than an infrastructure failure, and you can act on them:

| What the pod sees | Means |
|---|---|
| `loading task spec: task "x" not found in run "y"` | The pod is running a task its `dag_version` does not declare — usually a stale image, or a run created against a different version. |
| `task "a" may not read xcom from "b" (not a declared input or dependency)` | The task pulled an XCom it never declared as an input or a dependency. |
| `no xcom for task "x"` | Nothing was pushed under that key. The agent handles this one by status code rather than by text, so you will normally see its effect rather than the sentence. |

## Observability

- **Metrics:** Prometheus at `:9090/metrics` (scheduler, dispatch, inline
  runner, undispatchable counters; ADR 0007 has the catalogue).
- **Tracing:** OpenTelemetry — set `DEXAFLOW_OBSERVABILITY_OTEL_ENABLED=true`
  and `…_OTEL_ENDPOINT`.
- **Logs:** structured `slog` (JSON by default), one line per HTTP request
  with a request id.

Observability ships from the first commit (it is not optional).
