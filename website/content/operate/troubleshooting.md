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
dexaflow doctor                          # host check (OS, python, docker, k3d, kubectl, recommended tier); with a target, the deploy preflight too
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
| `dexaflow setup` downloads a CPython although you have a `python3.12` or `python3.13` | Older releases used a host interpreter only when it was named `python3.11`. Update: `setup` now runs the parser on the first of `python3.13`, `python3.12`, `python3.11` and `python3` on `PATH` that reports Python 3.11 to 3.13, and downloads a managed CPython 3.11 into `~/.dexaflow/python` (outbound network needed) only when none does. `dexaflow doctor` and `dexaflow setup --dry-run` name the interpreter setup will use before anything is fetched. A host with only Python 3.14 or newer still gets the download, because setup only takes the minors the parser is tested on. |
| Install on Alpine / musl fails fetching CPython | The musl-libc relocatable CPython build can be missing system libs. `dexaflow lite --postgres docker` falls back to the Docker Postgres path instead of the embedded managed one. |
| Control plane never becomes ready; logs show `remaining connection slots are reserved` or `too many connections` (SQLSTATE 53300) | Every control-plane pod opens `database.maxIdleConns` + 3 connections at boot and can grow to `database.maxOpenConns` + 3 under load, plus 1 for the migration Job. On a small managed Postgres (Cloud SQL `db-f1-micro`, the smallest RDS or Azure tiers, about 25 slots) that can exceed `max_connections`, or a per-role or per-database `CONNECTION LIMIT`. The boot error names the setting; lower `database.maxOpenConns` / `database.maxIdleConns` in the chart values, or raise the server's limit. `helm install` prints the estimated boot and peak demand in its notes. |
| Control plane exits at boot with `executor type "kubernetes" requires a Kubernetes client to dispatch task pods, but none is available` | The scheduler runs on the Kubernetes executor (the default) and found neither an in-cluster config nor a kubeconfig. Older versions booted anyway and failed every task as undispatchable; the server now refuses to start instead. In a cluster, check that the pod mounts its ServiceAccount token. Outside one, point `KUBECONFIG` at the cluster, or set `DEXAFLOW_EXECUTOR_TYPE=subprocess` for a dev setup with no cluster. |

## The migration Job

On Pro, `helm install` and `helm upgrade` run the migrations in a
pre-install/pre-upgrade hook Job. When it fails, Helm prints only
`failed pre-install: resource Job/<namespace>/<release>-migrate not ready ...
Job Failed`; the reason is in the pod's log:

```bash
kubectl -n <namespace> logs -l app.kubernetes.io/component=migrate --tail=50
```

The `dexaflow-migrate` image prints two lines: what happened and what to do,
then the driver's own error (`driver error: ...`), which is what older images
printed alone and what the rows below quote. Each failure a retry cannot fix
exits with its own code, and on Kubernetes 1.31 or newer the Job fails on the
first such pod (`podFailurePolicy`) instead of retrying it three times.

| Log line | Exit | Cause / fix |
|---|---|---|
| `database "<name>" does not exist on <host>:<port>`; the driver line ends `database "<name>" does not exist (SQLSTATE 3D000)` (older images: `pq: database "<name>" does not exist`) | 4 | Dexaflow does not create the database. Create it (`CREATE DATABASE <name>;`, as a role allowed to) or point `database.url` at an existing one. The chart README's [Datastore compatibility](https://github.com/dexadata/dexaflow/blob/main/helm/dexaflow/README.md#datastore-compatibility) section has the supported versions. |
| `Postgres at <host>:<port> rejected user "<user>"`, with `password authentication failed` (SQLSTATE 28P01) or `no pg_hba.conf entry` (SQLSTATE 28000) | 5 | The user or password in `database.url` is wrong, or the server does not accept this client. Fix the `databaseUrl` key of the chart Secret (or of `database.existingSecret`), or the server's `pg_hba` rules. Characters such as `@`, `:` or `/` in the password must be percent-encoded in the URL. |
| `database.url is empty`, `database.url has scheme ...`, `database.url has no database name`, `database.url is not a valid postgres:// URL` | 3 | `database.url` must be `postgres://user:password@host:5432/dbname?sslmode=...`. The message never repeats the URL, since it holds the password. |
| `database.url sets sslrootcert=/etc/leoflow/db-ca/ca.crt and that file is not present in this container` (older images: `unable to read CA file`) | 6 | The DSN names a CA file the pod does not have. Set `database.caConfigMap` to a ConfigMap holding the bundle under the key `ca.crt` (the chart mounts it at `/etc/leoflow/db-ca/ca.crt`), or use `sslrootcert=system` for a CA in the system store. A ConfigMap that does not exist keeps the pod in `ContainerCreating` instead: `kubectl -n <namespace> describe pod -l app.kubernetes.io/component=migrate` names it. |
| `schema_migrations is dirty at version N`; the driver line is `Dirty database version N. Fix and force version.` | 7 | A migration was interrupted. Follow [an interrupted migration](/operate/upgrades/#an-interrupted-migration) to see what the file left behind and which version to `force`, then upgrade again. |
| `database replaced: this release's installation id is "<id>", and the database has no schema` (or `carries no installation id`, or `carries installation id "<other>"`) | 8 | The database is not the one this release was installed on, so the Job migrated nothing. See [a replaced database](#a-replaced-database). |
| `installation.id is "<id>", but the database carries installation id "<other>"`, or `installation.id was changed to "<id>", but the database carries installation id "<other>"` | 8 | `installation.id` names neither this database nor a database without an id. Set it to the id the database carries to adopt it, or point `database.url` at the database you meant. |
| `reading the installation ConfigMap <namespace>/<release>-installation failed` (or `writing`) | 1 | The Job's ServiceAccount (`<release>-migrate`) cannot read or write the ConfigMap. The chart's Role grants it when `rbac.create` is true; with your own RBAC, give that account `create` on ConfigMaps in the release namespace and `get` and `update` on `<release>-installation`. |
| `cannot reach Postgres at <host>:<port> yet`, with `connection refused`, `i/o timeout`, `no such host`, or `the database system is starting up` | 75 | Retried. The server is not up yet, or the pod cannot reach it: check the host and port in `database.url`, a NetworkPolicy, a firewall rule, or a managed instance's authorized networks. |
| `another migration holds the schema_migrations lock` | 75 | Retried. A previous Job pod, or a second release on the same database, is migrating. Two releases must not share one database. |
| anything else | 1 | Retried up to `backoffLimit`. The driver line has the detail. |

Exit code 2 is a command line the image cannot run, which only a changed
`migrations.image` or hand-written args produce.

## A control plane that is up but not ready

A Pro control plane that finds its database schema absent or behind does not
exit. It stays up, `/healthz` answers 200 so Kubernetes does not restart it,
and `/readyz` answers 503 with the reason and what to run. It serves nothing
else meanwhile: every other request, the API and the UI included, gets the
same 503, so nothing is written to the database. It checks again on its own,
after 1 second at first, doubling up to every 30 seconds, and starts serving
without a restart once the schema is current. This is what happens after the
database was emptied, restored into a fresh instance or repointed, or when a
rollout runs before its migration Job.

```bash
kubectl -n <namespace> get pods -l app.kubernetes.io/name=dexaflow   # Running, 0/1 ready
kubectl -n <namespace> logs deploy/<release> --tail=20 | grep 'database schema'
```

The log has one line per change of reason, `database schema is not ready;
serving only the probes and metrics and checking again`, with `reason` and
`action` fields, and `database schema is current; starting` when it moves on.
On a chart install the action names this release's own command, with its
release name, namespace and chart version filled in, or, with
`migrations.enabled: false`, your own migration step and the
`dexaflow-migrate` image of the release. Which case you are in, and what to
run for each, is on
[database lost, restored or repointed](/operate/database-lost-restored-repointed/).

| `/readyz` reason | Cause / fix |
|---|---|
| `schema absent` | The database has no `schema_migrations` table: it is empty, or `database.url` points at another database. If the data should be there, restore it first ([backup and restore](/operate/backup-restore/)). To migrate it, run `helm upgrade <release> oci://ghcr.io/dexadata/charts/dexaflow -n <namespace> --version <chart version> --reuse-values`, which runs the migration Job again, or your own migration step when `migrations.enabled` is false. |
| `schema behind vN, requires vM` | The migrations of this release have not all run: a restore of an older backup, a lagging replica, or a rollout whose migration Job failed ([the migration Job](#the-migration-job)). Run the same `helm upgrade`. |
| `schema dirty at vN` | A migration to a version other than the one this binary requires is marked dirty. It may still be running; if no migration Job is, follow [an interrupted migration](/operate/upgrades/#an-interrupted-migration). A version dirty at the one the binary requires stops boot with `schema is dirty at vN, the version this binary requires`. |
| `schema unreadable` | The server reached the database at boot but cannot read the version now. The log line's `error` field has the cause. |
| `database replaced: no schema`, `database replaced: no installation id`, `database replaced: another installation id` | The database is not the one this release was installed on. Migrating does not fix it, and the migration Job refuses it too: see [a replaced database](#a-replaced-database). |

A pod that was serving when the database changed under it stays up and not
ready too, with `postgres schema not current` on `/readyz` and the same
action at the end of its readiness log line, and returns to ready when the
schema is current again. When the database it finds is a
replaced one, it exits instead, with code 3 and `the database was replaced
under this process` in its log, so no cache outlives the old database; the
restarted pod waits as above with a `database replaced` reason.

## A replaced database

Each Pro release records an installation id: the migration Job writes it into
the database (the one-row `installation` table) and into a ConfigMap named
`<release>-installation` that it creates in the release namespace, and the
control plane reads it from the ConfigMap. When the database answers with
another id, with none, or with no schema at all, the release does not treat it
as a fresh install:

- the migration Job migrates nothing and fails with exit code 8 (`database
  replaced: ...`), so `helm upgrade` fails;
- a control plane booting on it waits, not ready, with `database replaced:
  no schema`, `no installation id` or `another installation id` on
  `/readyz`, and `dexaflow_schema_ready{reason="database replaced"}` at 0;
- a control plane that was serving exits with code 3 and restarts into that
  wait.

This is what an emptied database looks like (an `emptyDir` Postgres moved to
another node, a volume lost), as well as a restore from another
installation's backup and a `database.url` changed to another database. A
restore of a backup of this same installation carries the same id and is not
a replacement: it is checked as absent, behind or ahead as usual. The log
line of the server and of the Job names both ids:

```bash
kubectl -n <namespace> get configmap <release>-installation -o jsonpath='{.data.installationId}'
psql "$DATABASE_URL" -c 'SELECT id FROM installation'
```

What to do depends on which database you want:

| You want | Do |
|---|---|
| The database this release had | Restore it or point `database.url` back at it ([backup and restore](/operate/backup-restore/)). The server checks again on its own and starts when the id matches. |
| This database, which belongs to another installation | Adopt it: set `installation.id` to the id it carries and upgrade, `helm upgrade <release> oci://ghcr.io/dexadata/charts/dexaflow -n <namespace> --version <chart version> --reuse-values --set installation.id=<the id it carries>`. The Job migrates it and records that id in the ConfigMap. |
| An empty database: the data is gone and you start over | Accept it with a new id: the same `helm upgrade` with `--set installation.id=$(uuidgen)`. The Job migrates the empty database and writes the new id into it and into the ConfigMap. |

Changing `installation.id` rolls the control plane pods, so they read the new
id. The acceptance applies once: the next replacement is refused again, with
`installation.id` still set, until it is changed again. A new id is written
only into a database that has none, so a database that belongs to an
installation is never re-keyed by mistake.

`helm uninstall` leaves the ConfigMap in place (the chart does not render
it), so reinstalling into the same namespace against an empty database is
reported as replaced too. When that is a fresh start on purpose, accept it
with a new id as above, or delete the ConfigMap before installing. A backup
taken before this installation recorded its id (before 0.5.6) restores
without one and is reported as `no installation id`: accept it with a new id.

On Lite, and on a chart install with `migrations.enabled: false`, no id is
recorded and nothing is compared, unless your own migration step runs the
`dexaflow-migrate` image with `-installation-configmap`,
`-installation-namespace` and, to accept a replacement, `-installation-id`.

## `dexaflow lite` boot

| Symptom | Cause / fix |
|---|---|
| `error: duplicate dag_id in workspace — rename one of the colliding projects` | The workspace has two project directories declaring the same `dag_id` — the most common cause is clicking the IDE's "Download examples" while a same-named project already exists at the workspace root. Delete or rename one of the two copies, then re-run `dexaflow lite`. Recent builds skip the example when a collision is detected (#298). |
| `the managed Postgres can't run on this host: system libraries are missing: ...` | The managed Postgres is a relocatable build that brings its own PostgreSQL libraries but links a few from the host: libxml2, Kerberos (`libgssapi_krb5`), OpenSSL 3, zstd and lz4. A desktop install has them; a bare container or a minimal server may not (a fresh `ubuntu:22.04` lacks libxml2, a fresh `ubuntu:24.04` lacks libxml2 and Kerberos). The message names each missing library and, on Debian, Ubuntu and Alpine, the command that installs them, such as `sudo apt-get install -y libgssapi-krb5-2 libxml2`; run it and start `dexaflow lite` again. The glibc build does not use ICU, so the host's ICU release does not matter, and installing a `libicu` package does not help. Or use `dexaflow lite --postgres docker`. Older builds printed a generic `needs system libraries (ICU, Kerberos)` and suggested `libicu-dev`, which never fixed it. |
| `the Docker Compose v2 plugin is not installed` | The Docker daemon answers, but `docker compose` is missing, and the Docker Postgres is started with it. Install the Docker Compose v2 plugin (Docker Desktop ships it; on Linux, the `docker-compose-plugin` package), or run `dexaflow lite --postgres managed`. With the default `--postgres auto`, Lite uses the managed Postgres on such a host by itself. |
| `the Postgres port 5432 is already in use` | Another Postgres holds the port the Docker Postgres maps. Lite owns its database and does not run on another Postgres, so stop that one, or run `dexaflow lite --postgres managed`, a private Postgres on a Unix socket that needs no port. |
| `refusing to reset, nothing was dropped: DEXAFLOW_DATABASE_URL is set and points at ...` | `dexaflow db reset` only drops Lite's own database, which the message names (host, port, database). Lite does not read `DEXAFLOW_DATABASE_URL` or `LEOFLOW_DATABASE_URL`, so while either names another database the reset refuses rather than drop a database you did not name. Unset the variable and run it again. |
| `provision incomplete: dev database` | The managed Postgres did not start. End-users run `dexaflow setup` to bootstrap the managed runtime. Contributors on a source checkout use `dexaflow lite provision`. If Docker is the chosen backend, confirm the daemon is up. |
| Pro refuses to boot with `DEXAFLOW_AGENT_ALLOW_INSECURE_SECRETS=true` set | The Pro edition rejects this flag (config key `agent.allow_insecure_secrets`) at boot (it would expose plaintext secrets). Unset it for Pro deployments; it stays valid for Lite where the agent talks loopback gRPC without TLS by design. |
| `jwt_secret is empty; falling back to the dev-only constant` | First boot before `dexaflow setup` has run, or `DEXAFLOW_SECRET_KEY` not set. Run `dexaflow setup` — it provisions a per-install secret. Not fatal on Lite (the constant works), but rotate before sharing the install. |
| Permission denied on `/tmp/leoflow-*` | Older Lite versions shared `/tmp/leoflow*` paths across users on multi-user hosts. Update to the latest release — paths are now per-user. |

## Running a DAG

| Symptom | Cause / fix |
|---|---|
| `declares unknown connection(s)` on the first `dexaflow lite` of a project | Lite seeds a declared connection from `AIRFLOW_CONN_<ID>` when your environment already carries it, so this usually means the variable is absent or misspelled — the lookup upper-cases the id, so `my_db` reads `AIRFLOW_CONN_MY_DB`. Only connections the DAG **declares** are considered, a connection already in the vault is never replaced by the environment (use `dexaflow connections set` to change one), and a URI that cannot be parsed is reported by name and not stored. |
| `dexaflow compile` dumps a Python traceback with internal parser paths first | Recent builds lead the failure with the user-facing line (e.g. `SyntaxError: ...`) and put the parser paths in the bounded tail. If you still see the internal-first dump, you are on an older release — update. |
| `dexaflow validate` reports a `SyntaxError` in code that runs fine on the cluster | The local tool judged your `dag.py` with a different Python minor than the one the project declares. Typical tell: `type Alias[T]`, or any other 3.12+ syntax, flagged on a project declaring `python_version: "3.12"` or later. Recent builds honour the declared version and warn instead of guessing: update, then install that minor (or run `dexaflow setup`). `dexaflow compile` does the same since [#1095](https://github.com/dexadata/dexaflow/issues/1095): it parses on the declared minor when the parser from `dexaflow setup` is older and that minor is installed. See [Which interpreter reads your DAG](/reference/configuration/#which-interpreter-reads-your-dag). |
| `warning: skipping dag.py syntax check` on `dexaflow validate` | Working as intended: the project declares a `python_version` newer than any interpreter installed here, so the lint is skipped rather than run under an older one and reject valid code. A newer interpreter than the declared one is used without complaint. Install the named interpreter, or run `dexaflow setup`, to turn the check back on. Your `dexaflow.yaml` is still validated. |
| `dexaflow validate` says the project is valid, then `dexaflow compile` fails with an `AttributeError`, `NameError` or `ModuleNotFoundError` | By default `validate` checks `dexaflow.yaml` and the syntax of `dag.py` without importing it, so errors that only appear when the code runs are left to `compile`. Run `dexaflow validate --parse` to import the DAG the way `compile` does. It needs the DAG's dependencies on the host, which is why it is opt-in. |
| `dexaflow lite` fails with `fetching the managed CPython 3.11 failed` | The project declares `python_version: "3.11"` (the default) and no interpreter on this host reports 3.11, so `dexaflow lite` downloads the managed CPython 3.11 the first time it builds that venv. `dexaflow setup` no longer fetches it up front when you have a Python 3.12 or 3.13. Give the machine outbound network once (or set `HTTPS_PROXY`), install `python3.11`, or declare the minor you have in `dexaflow.yaml`. |
| `dexaflow lite` prints `cannot create a venv ... python3-venv package`, then downloads a CPython | The host interpreter has no `venv` or `ensurepip` module (Debian and Ubuntu split them into `python3-venv`), so `dexaflow lite` builds the venv of a project declaring `python_version: "3.11"`, or declaring none, on the managed CPython 3.11 instead. Install `python3-venv` to use the host interpreter. For a project declaring another minor, `dexaflow lite` stops with `creating dev venv` and the same hint, because it never substitutes a different minor. |
| `dexaflow lite` reinstalls every dependency after you change `python_version` | Expected. The venv is rebuilt on the new interpreter, and the "already installed" markers are discarded with it, because a marker records what one interpreter has, so it cannot outlive that interpreter. |
| `dexaflow compile` rejects a sensor / Jinja template / branching operator | This is **intentional** — Dexaflow accepts a closed set of task types (`python`, `bash`, `airflow_operator`). See [DAG authoring → Not supported](/author-dags/dag-authoring/#not-supported--dexaflow-compile-rejects-these) for the full list and workarounds (`@task` + poll loop for sensors; build values from `airflow.sdk` context for Jinja). |
| `Compiled .../dag.py -> dag.json (image , version dev)` (dangling comma) | Older build — update. Recent versions render `(no image, version dev)` when `--image` is unset. |
| Task pod `ErrImagePull` (cluster mode) | The DAG's image is not in the cluster — rebuild + import. Cluster-mode rebuilds on save; for a manual push, `dexaflow compile --build --push`. |
| Run stuck at `queued` (subprocess) | The agent must reach the control plane — Lite uses `127.0.0.1:<grpc>`. The executor launches async and the agent reports state back. Look for the agent process in `ps`; if it exited, check `/tmp/leoflow-lite.log` for the launch error. |
| Run stuck at `running` long after the task finished | The agent's heartbeat reaper picks these up after the configured window. Check `DEXAFLOW_TI_HEARTBEAT_TIMEOUT_SECONDS` and look for a `reaped` log line. |
| A task's outbound TLS call to one endpoint hangs or resets, right after a green build, from inside your network | The task base image moved to Debian 13 (trixie)'s OpenSSL 3.5, which puts a post-quantum key share in the first TLS ClientHello — growing it from ~517 to ~1525 bytes. Some middleboxes and TLS-inspecting proxies mishandle a ClientHello that no longer fits one segment. Restore the classical group list by pointing `OPENSSL_CONF` at a file setting `[system_default_sect]` / `Groups = x25519:secp256r1:x448:secp521r1:secp384r1` (confirmed: puts the ClientHello back to 517 bytes). |
| Task pod `CreateContainerConfigError: container has runAsNonRoot and image will run as root` | Your task image runs as UID 0; the executor's `taskPodSecurity.runAsNonRoot` default refuses it. Fix: numeric `USER 65532:65532` in your Dockerfile, or an operator sets `taskPodSecurity.runAsNonRoot: false`. See [Deploy prerequisites](/operate/deploy-prerequisites/#4-non-root-task-image). |
| `deploy` stops with `preflight: Dockerfile runs as root` or `runs as a name` | The server runs task pods as non-root and the final `USER` of your Dockerfile is root or a name, so the pods would not start. End it with a numeric non-root id, such as `USER 65532:65532`. |
| `deploy` stops with `preflight: task ServiceAccount "<name>" ... does not exist` | A task pod would run as a ServiceAccount the task namespace does not have. Set `execution.service_account` to one that exists, or have the operator create it (`taskServiceAccount.create` in the Helm values). When the line adds `not a valid ServiceAccount name`, the name cannot exist at all: use lower case letters, digits, `-` and `.`. A missing `default` ServiceAccount is only a note, since Kubernetes creates it a moment after the namespace. |
| `CreateContainerConfigError: secret "<release>-secrets" not found` on the control plane | Chart-managed credentials are missing. On charts before 0.4.7 that Secret was a Helm **hook**, and under Argo CD a hook is deleted and recreated in separate passes of one sync, is absent from the compared state, and is not restored by self-heal. An interrupted sync therefore removes it for good while the Application still reports `Synced`. Run an explicit sync (`argocd app sync`) and the pods recover with no restart, because the kubelet retries container creation once the Secret returns. From 0.4.7 the Deployment reads `<release>-credentials`, an ordinary tracked resource, so deleting it makes the app OutOfSync and self-heal restores it. |
| `dexaflow deploy`/`push` fails on auth, registry, or a version conflict | One of the deploy-time gates. [Deploy prerequisites & why shortcuts fail](/operate/deploy-prerequisites/) covers every gate with the exact error and fix. |

## UI / browser

| Symptom | Cause / fix |
|---|---|
| `Invalid credentials` on the login page even with the right password | Disable autofill or type the password manually — some browsers append a trailing space. Usernames are trimmed, passwords are not (per security best practice). |
| A page saying **Dexaflow is temporarily unavailable** instead of the UI | The control plane could not reach its database to check your session, so it neither opens the UI nor sends you to sign in (the sign-in would fail for the same reason). The page answers `503` and keeps your session. Check the database and the control-plane log; reload once it is back. See [when the database disappears](/operate/control-plane-ha/#when-the-database-disappears-without-a-reset). |
| **Sign-in is temporarily unavailable. Try again shortly.** on the login page | `POST /auth/token` answered `503`: the control plane could not reach its database to check the password, so nothing is known about it. Do not reset the password; check the database and the control-plane log, then sign in again once it is back. |
| Login rate-limits you out after a few typos | Older builds counted *every* attempt against a 5/min cap; the fix splits successful and failed attempts so a typo does not block recovery. Update to the latest release. |
| A DAG you deleted weeks ago still runs and fails | Lite's state lives in `~/.dexaflow/dev` and outlives sessions, so a DAG registered during an old spike stays registered and keeps being scheduled. The ready banner now reports how many DAGs earlier sessions left (`state: N DAGs registered by earlier sessions`); dexaflow lite --fresh starts from an empty local database. Deleting the project directory stops it being re-registered, but does not deregister what is already there. |
| No **Lite** badge on `http://localhost:8088` | You are likely on the **Demo** (production-shaped reference, port `8080`) — Lite runs on `8088` with a silver `Dexaflow Lite` badge. See [operating modes](/concepts/editions/). |
| Copy-logs button silently fails over `http://<lan-ip>:8088` | The Clipboard API requires a secure context, so plain HTTP origins (LAN access from another machine) used to break copy. Recent builds inject a polyfill (`document.execCommand('copy')` fallback) — update. |
| `server returned 401` from `push`, `deploy`, `dags`, `runs`, `connections` or `variables` | Usually the saved token belongs to a **different** control plane. `~/.dexaflow/config.yaml` holds one `server_url` and one `token`, written together by `dexaflow auth login`; passing `--server` points the command elsewhere while still sending that token. The error now names both servers and prints `dexaflow auth login --server <the one being called>`. Run it, or unset `DEXAFLOW_TOKEN` if an env token is shadowing the file. Holding several servers' tokens at once is [#1102](https://github.com/dexadata/dexaflow/issues/1102). When the saved session itself expired (unused past `auth.jwt.token_ttl_seconds`, or renewed up to `auth.jwt.max_lifetime_seconds`), the error says `hint: the saved session expired at <time>` and prints the same login command. |
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
dexaflow lite reset-password                             # fresh admin password, signs out old sessions (no sudo)
dexaflow lite --fresh                                     # start a session with nothing registered (DESTRUCTIVE)
dexaflow db reset --yes                                  # drop + recreate the Lite database (DESTRUCTIVE)
dexaflow uninstall                                       # remove ~/.dexaflow (binaries, managed Python, config)
dexaflow uninstall --purge                               # also remove the workspace (your DAGs!)
```

## Logs

Task logs stream from the agent over gRPC to the control plane's log sink and
are served at
`/api/v2/dags/<dag>/dagRuns/<run>/taskInstances/<task>/logs/<try>` (the UI's
drill-down), or from the CLI: `dexaflow runs logs <dag_id>` prints every task
of the latest run, `dexaflow runs logs <dag_id> <run_id>` every task of that
run, and `dexaflow runs logs <dag_id> <run_id> <task_id> [--try N] [-f]` one
attempt of one task. Without a task, `-f` follows the run to its end. The sink directory is `DEXAFLOW_LOGS_DIR`
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

### `agent_lost` that is not a network fault: a refused agent credential

An agent whose bearer the control plane refuses gets `Unauthenticated` on every
RPC, so its heartbeats stop counting and the attempt is eventually reaped as
`agent_lost`, which looks exactly like a network partition. The control plane
writes a WARN line for every refusal, `rejected agent bearer token`, with a
`reason` that tells the cases apart:

| `reason` | Means | Look at |
|---|---|---|
| `expired` | The credential lapsed. A running attempt renews it on every heartbeat, so the agent could not reach the control plane for a whole token TTL (10 minutes), or its pod started after the credential minted at dispatch had already run out. | Control-plane and database availability around `expired_at`. |
| `wrong_audience` | A token minted for another surface (a user token) reached the agent service. | A wiring bug: report it. |
| `bad_signature` | The token was signed with another key: the JWT secret was rotated, or the token comes from another install. | `auth.jwt.secret` across replicas. |
| `wrong_issuer`, `not_yet_valid`, `malformed`, `invalid` | The bearer is not a token this control plane minted as it stands. | The caller at `peer`. |
| `missing` | The RPC carried no bearer at all. | The caller at `peer`. |

For `expired` (and `not_yet_valid`) the line also names the attempt the
credential belonged to (`ti`, `run`, `dag`, `task`, `try`, `epoch`) and when it
lapsed (`expired_at`, `expired_for`), because the token's signature verified.
For every other reason the token's claims are not trusted, so they are not
logged. The token itself is never logged. The line is rate limited to ten per
reason per minute, and the first line after a quiet minute reports how many
were left out in `suppressed`; the metric
`dexaflow_agent_token_rejections_total{reason}` counts every refusal.

```bash
kubectl logs deploy/<release> -c leoflow-server \
  | jq 'select(.msg == "rejected agent bearer token") | {reason, run, task, try, expired_at, peer}'
```

## Ingress errors while no server pod is ready (502, 503, 504)

When no control plane pod is ready (while it boots, or while it waits for its
schema), the Service has no endpoints and the ingress controller answers with
its own 502 or 503 page. Once a pod answers again, the server's own outage page
and API errors take over
([#1099](https://github.com/dexadata/dexaflow/issues/1099)).

### Maintenance page

The chart can deploy a small static maintenance page (a `<release>-maintenance`
Deployment and Service running an unprivileged nginx) that answers every path
with `503`, a `Retry-After` header and a page that reloads by itself:

```yaml
ingress:
  enabled: true
  className: nginx
  maintenancePage:
    enabled: true
    operatorText: "Scheduled maintenance in progress"
    statusUrl: "https://status.example.com"
```

- **ingress-nginx:** the chart sets `nginx.ingress.kubernetes.io/default-backend`
  to the maintenance Service, which ingress-nginx uses while the server has no
  ready endpoint, and `custom-http-errors: "502,504"` for a pod lost in the
  middle of a request. `503` is not intercepted, so the server's own outage
  responses still reach the browser and API clients. Annotations you set under
  `ingress.annotations` win over these.
- **Gateway API and other controllers:** the annotations do nothing there.
  Route the controller's fallback or error backend to the `<release>-maintenance`
  Service on port 80, if the controller supports one.

The page text is generic on purpose: it names no cause. Operators find the
reason in the server logs and metrics. The image is
`nginxinc/nginx-unprivileged` by default; set
`ingress.maintenancePage.image` to use a mirror or pin a digest.

## Observability

- **Metrics:** Prometheus at `:9090/metrics` (scheduler, dispatch, inline
  runner, undispatchable counters; ADR 0007 has the catalogue).
- **Schema readiness:** `dexaflow_schema_ready` is 1 when the database schema
  is one the binary can serve, and 0 while it is not, with a `reason` label
  (`schema absent`, `schema behind`, `schema dirty`, `schema unreadable`,
  `database replaced`; the
  versions are on `/readyz`). `dexaflow_schema_wait_seconds` is how long the
  process has been waiting, 0 when it is ready. Every role exports both. With
  the Prometheus Operator, `metrics.prometheusRule.enabled` renders the alert
  below; without it, add the same rule to your Prometheus:

  ```yaml
  - alert: DexaflowSchemaNotReady
    expr: dexaflow_schema_ready == 0
    for: 5m
    labels:
      severity: warning
  ```
- **Tracing:** OpenTelemetry — set `DEXAFLOW_OBSERVABILITY_OTEL_ENABLED=true`
  and `…_OTEL_ENDPOINT`.
- **Logs:** structured `slog` (JSON by default), one line per HTTP request
  with a request id.

Observability ships from the first commit (it is not optional).
