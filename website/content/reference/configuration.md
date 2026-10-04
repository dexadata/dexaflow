---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /configuration.html
# --- end AUTO redirect aliases ---
title: Configuration
weight: 60
description: "The DEXAFLOW_* environment variables and config keys for the server."
---

Two surfaces: **`dexaflow.yaml`** (per-DAG, authoring) and **server environment**
(`DEXAFLOW_*`, the control plane). The canonical `dexaflow.yaml` schema is
[`docs/api/leoflow-yaml-schema.json`](https://github.com/dexadata/dexaflow/blob/main/docs/api/leoflow-yaml-schema.json).

{{% alert title="Deploying Pro on Kubernetes?" color="info" %}}
These `DEXAFLOW_*` variables are what the Helm chart sets under the hood. For the
chart's own values (image, replicas, ingress, Postgres/Redis wiring), see the
[Helm chart](/operate/helm-chart/) page and its full values reference.
{{% /alert %}}

## dexaflow.yaml

| Key | Type | Notes |
|---|---|---|
| `dag_id` *(required)* | string | Unique DAG id (`^[A-Za-z0-9_][A-Za-z0-9_-]{0,199}$`). |
| `description`, `owner`, `tags` | string / string / list | Metadata. |
| `python_version` | `3.10`\|`3.11`\|`3.12`\|`3.13` | Base image Python (default 3.11). `3.10` is **deprecated** — see [Python version support](#python-version-support). |
| `base_image` | string | Override the runtime base image. |
| `dependencies` | list | pip specifiers baked into the image. Version floors (`"setuptools>=80.9.0"`) and [PEP 508](https://peps.python.org/pep-0508/) environment markers both work — see the `dependencies` row under [Defaults](#defaults) for what the build does with them. |
| `connectors` | list | Short connector names (`postgres`, `http`, …) expanded at compile to their `apache-airflow-providers-*` packages. Sugar over `dependencies` — see [Installing a connector's provider](/connections/#installing-a-connectors-provider). |
| `system_packages` | list | apt packages, installed into the DAG image at compile. Resolved against the task base image's Debian suite, now **Debian 13 (trixie)** — it was Debian 12 (bookworm) through v0.4.5, so a package name or version pin that only existed in bookworm has to be re-pinned. |
| `dag_source` | string | DAG file (default `dag.py`). |
| `build`, `registry` | object | Image build + push settings. |
| `defaults` | object | DAG-level `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `resources`. |
| `staging` | object | Opt-in per-run RWX volume: `enabled`, `size`, `storage_class` (ADR 0022). |
| `tasks.<task_id>` | object | Per-task overrides (ADR 0023): `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `env`, `resources`, `execution`. |

See [DAG authoring](/author-dags/dag-authoring/) for the override layers.

### Python version support

Every value the schema accepts has a published, multi-arch, cosign-signed base
image at `ghcr.io/dexadata/dexaflow-runtime:py<version>`. Nothing else does —
if a version is not in the table above, no base image exists for it and the
build fails on the pull.

| Line | Status | Upstream EOL | Published until |
|---|---|---|---|
| `3.10` | **Deprecated** | 2026-10-31 | 2026-10-31 |
| `3.11` | Supported *(default)* | 2027-10-31 | — |
| `3.12` | Supported | 2028-10-31 | — |
| `3.13` | Supported | 2029-10-31 | — |

**Published until** is the last date on which a release publishes that leg; a
release cut after it ships no `py<line>` image. A `—` means the line is
supported with no removal date set. The Status and Published-until cells are
generated from nothing — they are written by hand — but
`scripts/check-python-runtime-matrix.sh` reconciles them against the
`x-leoflow-python-deprecations` block in the authoring schema, so a deprecation
that moves in the schema and not here fails the build rather than leaving this
table quietly telling you the opposite.

`3.13` is the current ceiling, and it is set by the dbt adapters rather than by
Airflow: `dbt-core` and `dbt-postgres` publish for 3.14, but `dbt-snowflake`,
`dbt-bigquery`, `dbt-databricks` and `dbt-duckdb` stop at 3.13. A `py3.14` base
would give you an image where `dbt-postgres` installs and `dbt-snowflake` does
not — discovered inside your build, not ours — so it is not published.

**`3.10` is deprecated.** Python 3.10 reaches upstream end-of-life on
2026-10-31, and `docker-library/python` stops rebuilding an EOL line the day
after (`python:3.9-slim` was last rebuilt 2025-11-01, one day after 3.9 went
EOL). From that point `python:3.10-slim` — and so
`dexaflow-runtime:py3.10` — receives no further OS security updates and
accumulates unfixed CVEs indefinitely. The `py3.10` leg keeps being published
until 2026-10-31, so nothing breaks today; `dexaflow validate`, `dexaflow
compile` and `dexaflow deploy` warn when your project resolves to it.

There are two ways to resolve to it, and they have different fixes:

- **You set `python_version: "3.10"`.** Set `python_version: "3.11"` (or later)
  and rebuild.
- **You pinned `base_image` to a published `py3.10` tag** (`…:py3.10`,
  `…:py3.10-v0.4.5`). Repoint `base_image` to the matching `py3.11` tag and
  rebuild. Changing `python_version` here does nothing: when `base_image` is
  set it is used verbatim and `python_version` is not consulted for the `FROM`.

Existing images keep running either way; the rebuild is what moves you.

This matters more than for most images because it is inherited: a DAG image is
built `FROM` this base, so pinning `base_image` freezes your DAG on whatever the
base was on the day you pinned it.

#### Which base you get when you do not pin one

When `base_image` is unset, `dexaflow compile --build` writes the `FROM` itself,
and it chooses between two tag shapes based on the CLI you are running:

- a **released** `dexaflow` pins `dexaflow-runtime:py<ver>-v<X.Y.Z>`, which is
  immutable, so a compile from that release reproduces byte for byte (ADR 0003)
- a **development** build, from source or a dirty tree, falls back to
  `dexaflow-runtime:py<ver>`, a line every release republishes

So two people compiling the same project can end up on different bases if one
runs a released CLI and the other runs one built from source. Setting
`base_image` explicitly overrides both rules and is used verbatim. The full tag
scheme for every published image is in
[Published images](/reference/published-images/).

#### Which interpreter reads your DAG

`python_version` is a statement about the interpreter your DAG runs on, and the
cluster honours it through the task base image. Every local tool that reads your
`dag.py` honours it too, because a tool that judges your code with a different
minor gives the wrong answer in the most confusing direction: `type Alias[T]` is
valid from 3.12 and a `SyntaxError` on 3.11, so a 3.11 checker rejects a DAG the
cluster runs correctly, and phrases it as a mistake in your code.

| Tool | What it does with the declared version |
|---|---|
| `dexaflow validate` | Lints `dag.py` under that minor. If it is not installed, it falls back to any interpreter **at least as new**, because a newer one accepts everything the declared minor accepts. If all that is installed is older, the lint is **skipped with a warning** naming the version rather than run under it. |
| `dexaflow lite` | Builds the project's venv on it, and stops rather than substituting a different minor. |

Three things follow from this that are worth knowing:

- **Only an older interpreter is refused, not every different one.** Python's
  grammar grows, so a 3.11 checker rejects valid 3.13 code while a 3.13 checker
  accepts valid 3.11 code. Refusing every mismatch would have been the larger
  bug: `dexaflow init` writes `python_version` explicitly, so every scaffolded
  project takes this path, and most hosts carry a newer `python3` than the
  `3.11` it writes.
- **A skipped check is reported, never silent.** When only an older interpreter
  is around, `validate` would rather tell you it could not check than hand you
  an answer it does not trust. Install the named minor, or run `dexaflow setup`,
  to turn the check back on. Your `dexaflow.yaml` is validated either way.
- **The fallback is not as strict as the declared minor.** Checked under a newer
  interpreter, syntax that only the newer one accepts passes here and then fails
  on the task image. Installing the minor you declare is what makes the check
  exact; the fallback only guarantees that what it rejects is genuinely wrong.
- **`dexaflow compile` does not honour it yet.** The parser *executes* your
  `dag.py`, so its own interpreter decides which syntax is legal, and today that
  is whichever interpreter `dexaflow setup` baked into `parser_cmd`. A project
  declaring a newer minor can still see a `SyntaxError` from `compile` for code
  the cluster runs
  ([#1095](https://github.com/dexadata/dexaflow/issues/1095)). Running
  `dexaflow setup` under the minor you declare is the workaround.
- **The three exemptions are the same everywhere.** A version you never wrote is
  not a statement (the default applies), a declared `base_image` makes the field
  inert because you chose the `FROM` by hand, and a deprecated version warns
  rather than demanding you install an interpreter we are asking you to leave.

#### Values that reach the generated Dockerfile

Every value the generated Dockerfile interpolates is checked, because the
Dockerfile format and Docker's own operand lexer give some characters a meaning
no quoting can take away. The refusal always names the field and the value, since
a stray control character in YAML is invisible in the source.

**Refused everywhere: a line break, a vertical tab or a form feed.** These end a
Dockerfile instruction or split it into new words, so a value carrying one closes
the instruction it sits in and whatever follows becomes an instruction of its own.
Docker splits a line on `[\t\v\f\r ]+`, which is why the vertical tab and form
feed count alongside the newline. This covers `base_image` in the `FROM`;
`dbt.project`, `dbt_groups.*.project`, `dag_source` and `include_paths` in their
`COPY` lines; `exclude_paths` in the generated `.dockerignore`; and the
`dependencies` and `system_packages` entries that already had the guard.

**Refused in a `COPY` path: `'`, `"`, `\`, `$` and `<`.** After a Dockerfile is
parsed, every `COPY` operand goes through a second pass that strips quotes, eats
backslashes and expands `$VAR`. That pass runs whatever quoting the line used, so
`COPY ["d'a't.py", "..."]` copies `dat.py`, not the file you named. None of these can be quoted into safety, so they are refused
rather than silently copying the wrong path.

**Refused in a `COPY` path: `<`.** A different mechanism, not the operand lexer:
`COPY` is heredoc-capable, so `COPY <<EOF` opens a heredoc that swallows the rest
of the generated Dockerfile and then fails on the missing terminator.

**This is a breaking change if one of those characters is already in your
`dag_source`, `dbt.project`, `dbt_groups.*.project` or `include_paths`.** An
apostrophe in a directory name is not exotic. Such a project used to build, but
it was copying the wrong path into the image the whole time: `raw/$schema`
expanded to whatever the base image set, and `sql\queries` copied `sqlqueries`.
The build fails now and names the field, which is the point.

**Refused in `base_image`: any whitespace.** An image reference cannot contain
one, `FROM` has no quoting, and the rest of the line would be read as the
`FROM <image> AS <stage>` form.

**Refused in a `COPY` path: a leading `--`,** which Docker reads as one of
`COPY`'s own flags rather than as a path.

A path containing a space or a tab, or starting with `[`, is legal and is
**quoted** rather than refused. Paths without any of those keep rendering
exactly as before, so a project's generated Dockerfile does not change because
this guard exists.

`exclude_paths` is checked on the patterns that are actually emitted, not on the
field alone: a dbt project path reaches the same `.dockerignore` through the
build-artifact exclusions Dexaflow adds for it, so checking only the field left
the class reachable through `dbt.project` and `dbt_groups`.

The same guards apply to the Dockerfile `dexaflow lite --executor=k8s` generates
when a project ships none. That one writes `<project>/Dockerfile` and leaves it
there, and a project-supplied Dockerfile is afterwards used verbatim, so a
value that slipped through there would outlive the command that wrote it.

### Rotating the encryption key

`DEXAFLOW_SECRET_KEY` takes a comma-separated list. **The first entry encrypts
and decrypts; every later entry only decrypts**, and nothing is ever written
under one. It is the same rule as Airflow's `fernet_key`.

```bash
DEXAFLOW_SECRET_KEY="<new key>,<old key>"
```

The control plane re-encrypts the stored connection secrets onto the first key
at startup, logs how many it moved, and then the old key is no longer needed:

```
secret key rotation complete for the stored connections re_encrypted=7
```

Remove the old entry once every replica has started with the list. Until then
it is still required, because a replica that has not restarted is still reading
rows only the old key opens.

A row that **no** configured key can open is left untouched and reported at
`ERROR`. Its ciphertext is the only copy of that credential, so the rotation
never overwrites or deletes it; put the missing key in the list and restart.

Trying keys in order is safe because AES-256-GCM is authenticated: a wrong key
fails to open rather than returning plausible garbage.

{{% alert title="A raw key can no longer contain a comma" color="warning" %}}
The value is split on commas and each entry is trimmed, so a **raw 32-character
passphrase** containing a comma, or with a leading or trailing space, no longer
parses. The server then refuses connection writes and cannot read existing rows.

Hex and base64 keys are unaffected, as is any raw key without those characters.
If yours has one, re-key with `openssl rand -hex 32` and rotate using the list
above, which is the safe way to change it.
{{% /alert %}}

{{% alert title="Dexaflow Lite: new installs only, for now" color="warning" %}}
`dexaflow setup` generates a per-install key and keeps it in
`~/.dexaflow/config.yaml`.

**An install created before per-install keys existed is not migrated.** Its
connection secrets stay encrypted with the key that used to be compiled into
this repository, which every Lite install shares, so anyone who obtains that
datastore file can read them. Moving an existing install means re-encrypting
every stored secret, and that migration is tracked separately.

**`config.yaml` holds the only copy of the key that decrypts your stored
connections.** `dexaflow lite backup` includes it, which also means the backup
archive holds the key and the ciphertext together. If you roll your own backup
of the datastore, back up `config.yaml` with it, and `dexaflow uninstall` warns
before it removes the only copy.
{{% /alert %}}

### Defaults

Every field in `dexaflow.yaml` is optional. Zero-valued fields are filled by
`LeoflowConfig.ApplyDefaults()` (`internal/domain/config.go`) from the values
declared in [`leoflow-yaml-schema.json`](https://github.com/dexadata/dexaflow/blob/main/docs/api/leoflow-yaml-schema.json).
Defaults are hardcoded for v1; making them workspace-configurable is a v2
roadmap item.

| Field | Default | Notes |
|---|---|---|
| `schema_version` | `"1.0"` | Stamps every artifact for forward-compat. |
| `dag_id` | *subdir basename* | If `dexaflow.yaml` is absent, the parent directory name is used. Two subdirs resolving to the same `dag_id` is a hard error — see [Discovery rules](/author-dags/dag-authoring/#discovery-rules). |
| `python_version` | `"3.11"` | Pick `3.10`, `3.11`, `3.12`, or `3.13`. It selects the task base image **and**, when you declare it explicitly, the interpreter every local tool judges the project with; see [Which interpreter reads your DAG](#which-interpreter-reads-your-dag). `dexaflow lite` builds that project's venv on it, so the dev loop and the cluster run the same minor. If no interpreter on the host reports that version, `dexaflow lite` stops and says so rather than substituting a different one; a venv already built on another minor is rebuilt, which reinstalls the runtime and your dependencies. Leaving the field out keeps the previous behaviour (any host Python 3.11+, managed build preferred), because then the image is `3.11` by the same default and the two already agree. A declared `build.base_image` makes this field inert on both sides, and a deprecated version warns and falls back instead of blocking. |
| `dag_source` | `"dag.py"` | DAG file relative to the project. |
| `dependencies` | `[]` | pip specifiers baked into the image. Any [PEP 508](https://peps.python.org/pep-0508/) form works, including version floors (`"setuptools>=80.9.0"`) and environment markers (`'requests; python_version < "3.12"'`) — each entry is passed to pip as one literal argument, so shell characters in a specifier are never interpreted. A line break inside an entry is refused, since it would end the generated `RUN` instruction, and every entry is passed after a `--` so an entry beginning with a dash is treated as a package name rather than as an option to pip. |
| `connectors` | `[]` | Short connector names expanded to provider packages at compile (ADR 0038). |
| `system_packages` | `[]` | apt packages. `apt-get install`ed into the DAG image at compile, resolving against the task base image's Debian suite — see the `system_packages` row under [dexaflow.yaml](#dexaflowyaml) for which suite that is and what moved. |
| `include_paths` | `["."]` | Extra paths copied into the image **alongside** `dag_source` — a helper module, a config file, a fixtures directory. Entries are relative to the project directory; an absolute one, or one escaping the context (`../x`), is refused at compile with the entry named, because Docker cannot `COPY` it and failing at build time would name a Docker error instead. The default `["."]` means *no extra paths*, not "everything": it is what every existing project carries, so it must not change what their images contain. Entries already copied (the DAG source, a dbt group directory) are skipped rather than duplicated. Only the **generated** Dockerfile honours it — a project-supplied Dockerfile copies whatever its own `COPY` lines say. Included paths are scanned by the credential warning like everything else that ships. |
| `exclude_paths` | `[".git", "__pycache__", "*.pyc", ".venv", "venv"]` | Kept out of the image. On `--build` these become a `.dockerignore` in the build context for the duration of the build — merged with yours if you have one, and removed afterwards. Each entry is expanded to the forms Docker actually honours, because a bare name in a `.dockerignore` matches only at the context root: a plain directory name becomes four patterns (`p`, `**/p`, `p/**`, `**/p/**`) so that both the directory and its contents are pruned at any depth; an entry whose last segment contains a glob becomes `p` and `**/p` only, since a glob names files rather than a directory to descend into; and an entry containing a `/` is already anchored, so it becomes `p` and `p/**`. An entry starting with `!` or `#` contributes nothing: it is dropped rather than expanded, so a negation belongs in your own `.dockerignore` (which is merged, never rewritten) and not here. A dropped `!` is **reported by name** at build time — leoflow's block is appended after your own lines, so a negation emitted there could resurrect a path one of your earlier lines excluded. Add anything holding credentials: the image is pushed to a registry and pulled by every pod that runs the DAG. **Not** used by workspace discovery, which has its own hardcoded skip list. |
| `build.context` | `"."` | **Not implemented.** Declared and defaulted, but the build always uses the DAG directory. Tracked in [#1062](https://github.com/dexadata/dexaflow/issues/1062). |
| `build.platforms` | `["linux/amd64"]` | Multi-arch via `["linux/amd64","linux/arm64"]`. |
| `registry.auth_method` | `"docker_config"` | Credential source for `compile --push`. |
| `registry.tag_strategy` | `"version"` | How `dag_version` is mapped to image tag. |
| `staging.enabled` | `false` | Opt-in per-run RWX volume — ADR 0022. |
| `defaults.*` | *unset* | DAG-level task defaults; layered under task overrides — ADR 0023. |
| `tasks.<id>` | *unset* | Per-task overrides; must reference a `task_id` present in the compiled DAG. |

## Server environment (`DEXAFLOW_*`)

This page is hand-maintained against the server's configuration struct and
default map in
[`internal/config/server.go`](https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go)
— treat that source as the final authority. Every `DEXAFLOW_*` variable maps to a
config key by upper-casing it and replacing `.` (and `-`) with `_`: e.g.
`auth.oidc.client_id` → `DEXAFLOW_AUTH_OIDC_CLIENT_ID`. The same keys can be set in
a YAML config file. The Helm chart models many of them as values, but not all: a
key with no chart value has to go through `extraEnv`. The two OIDC maps below are
the exception in both directions: no env var can carry them, so `extraEnv` is not
a route, and the chart delivers them by writing a partial config file into a
mounted ConfigMap (`auth.oidc.tenantClaims`, `auth.oidc.roleMappings`).

Values resolve in increasing order of precedence — a later source overrides an
earlier one:

```mermaid
flowchart LR
  D["Built-in defaults<br/>(serverDefaults)"] --> C["Config file<br/>(YAML)"]
  C --> E["DEXAFLOW_* env vars"]
  E --> F["CLI flags"]
```

The **Edition** column reads `both` (Lite and Pro), `Pro` (Pro / Kubernetes
topologies only), or `dev-only`. `dexaflow lite` sets the dev-appropriate values
automatically (isolated DB, port 8088, admin login on, no Redis).

**List**-valued keys (CORS origins, OIDC scopes, allowed email domains,
break-glass emails, trusted proxies) DO come from a single env var: viper's
decode hook splits a comma-separated value into a list, so
`DEXAFLOW_AUTH_OIDC_SCOPES=openid,email` works. That is how the Helm chart sets
them, since it ships no server config file. In a config file they are ordinary
YAML lists.

**Map**-valued keys do not. `auth.oidc.role_mappings` and `auth.oidc.tenant_claims`
are read only from a YAML config file, because their keys may contain dots (an IdP
group name, a Google Workspace domain) and a dotted key is ambiguous in both env
and viper's own key space. The chart sets them through `auth.oidc.tenantClaims`
and `auth.oidc.roleMappings`, which it renders into a ConfigMap mounted as the
server's `DEXAFLOW_CONFIG` file, with the keys quoted so a dotted domain survives
([#1143](https://github.com/dexadata/dexaflow/issues/1143)). That file is
deliberately partial: it carries only these two keys, so it can never override a
setting the chart delivers as an env var.

In the tables below, the row name tells you which of these two groups a key is
in: a row named after its `DEXAFLOW_*` env var binds from that env var (and so
from `extraEnv` or a chart value that sets it); a row named after its dotted
config key (e.g. `auth.oidc.role_mappings`) is config-file-only.

### Server (`server.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_SERVER_ROLE` | `all` | Pro | Which components this process runs ([ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/)): `all` (default — the Lite monolith; every component in one process), `api` (HTTP + UI only, restricted identity), or `scheduler` (reconciler + dispatch + agent gRPC, privileged). Empty defaults to `all`, which is behavior-identical to the pre-split monolith; splitting is a Pro-only topology. |
| `DEXAFLOW_SERVER_HTTP_ADDR` | `0.0.0.0:8080` | both | HTTP/UI listener. |
| `DEXAFLOW_SERVER_GRPC_ADDR` | `0.0.0.0:9091` | both | Agent gRPC listener. |
| `DEXAFLOW_SERVER_METRICS_ADDR` | `0.0.0.0:9090` | both | Prometheus metrics. |
| `DEXAFLOW_SERVER_GRPC_TLS_CERT` | _(empty)_ | Pro | PEM cert enabling TLS on the agent gRPC listener (#58). Set with `_KEY`; empty means plaintext (dev). The Pro Helm chart requires both (see [Pro TLS](/operate/pro-tls/)). |
| `DEXAFLOW_SERVER_GRPC_TLS_KEY` | _(empty)_ | Pro | PEM private key paired with `DEXAFLOW_SERVER_GRPC_TLS_CERT`. Both must be set together to encrypt the agent channel. |
| `DEXAFLOW_SERVER_CORS_ALLOWED_ORIGINS` | `http://localhost:8080` | both | Browser origins allowed to call the API cross-origin (`server.cors.allowed_origins`, a list). The UI is served same-origin with the API, so most deployments need no entry and should leave the server default alone. Comma-separated via the env var; in the chart set `config.cors.allowedOrigins` (a YAML list) and it is rendered comma-joined for you. The chart rejects `"*"` at render time (#1144). |
| `DEXAFLOW_SERVER_TRUSTED_PROXIES` | *(empty — trust none)* | both | Proxy IPs/CIDRs whose `X-Forwarded-For` is honored for the client IP (`server.trusted_proxies`, a list). See note below. |
| `DEXAFLOW_SERVER_MAX_PAGE_LIMIT` | `0` | both | Largest `limit` a list endpoint (`/api/v2/*`, `/ui/*`) serves, and the largest `dag_runs_limit` of `/ui/dags`; a larger request is served this many rows, like Airflow's `[api] maximum_page_limit`. `0` means no cap. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_GZIP_RESPONSES` | `false` | both | Gzip JSON and NDJSON responses of 1 KB or more on `/api/v2/*` and `/ui/*` for clients that send `Accept-Encoding: gzip`. Log routes and any response that streams (flushes before 1 KB, such as a live log tail) are never compressed. Clients that list `gzip;q=0` get identity. Routes that return secrets or tokens (`/api/v2/variables`, `/api/v2/connections`, XCom routes, `/api/v2/auth/`) are never compressed, because compressing a secret next to attacker-reflected input lets the response length leak it (BREACH). Other JSON may still reflect request input next to private data, so weigh the bandwidth saved against that before turning it on behind a TLS terminator that does not already compress. Off sends every body uncompressed. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_READ_TIMEOUT` | `0s` | both | Longest time the API and metrics listeners spend reading one request, headers and body (a Go duration such as `60s`). Protects against clients that trickle a body to hold connections. Set it above your slowest legitimate upload. It never limits a response, so live log tails keep streaming. `0s` means no limit. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_IDLE_TIMEOUT` | `0s` | both | Closes a keep-alive connection idle this long (a Go duration such as `120s`). `0s` keeps idle connections open, also when `DEXAFLOW_SERVER_READ_TIMEOUT` is set (the read timeout is never used as an idle timeout). Helm: set it through `extraEnv`. |

### Database (`database.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_DATABASE_URL` | `postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable` | both | Postgres DSN. |
| `DEXAFLOW_DATABASE_MAX_OPEN_CONNS` | `25` | both | Max open connections in the Postgres pool. |
| `DEXAFLOW_DATABASE_MAX_IDLE_CONNS` | `5` | both | Max idle connections retained in the pool. |
| `DEXAFLOW_DATABASE_SCHEDULER_MAX_CONNS` | `0` | both | Size of a dedicated pool for the scheduler loop, its reapers and its janitors (`database.scheduler_max_conns`; Helm `database.schedulerMaxConns`), so API traffic that holds every main pool connection cannot stall a scheduler tick. It is opened in addition to the main pool, only by a process that runs the scheduler (`DEXAFLOW_SCHEDULER_ENABLED`). `0` keeps them on the main pool. |
| `DEXAFLOW_DATABASE_STATEMENT_TIMEOUT_MS` | `0` | both | `statement_timeout`, in milliseconds, for every connection of the main pool, which serves the API (`database.statement_timeout_ms`; Helm `database.statementTimeoutMs`). About `30000` bounds a runaway API query without cutting legitimate ones. Never applied to the leader election connection (its session holds the scheduler advisory lock), the health checks, the scheduler pool or migrations. Deleting a DAG, clearing its history and the XCom janitor lift it with `SET LOCAL statement_timeout = 0` for their own transaction, since their cost grows with the data they cascade over. Without `DEXAFLOW_DATABASE_SCHEDULER_MAX_CONNS` the scheduler shares the main pool, and with it this timeout, so set both together. The server applies it with `SET` as each connection opens, not as a startup parameter, so it also works through PgBouncer in session mode (no `ignore_startup_parameters` entry needed); in transaction mode a session `SET` does not stay on one server connection, so set it on the role instead (`ALTER ROLE ... SET statement_timeout`) and leave this at `0`. `0` sets none. |
| `DEXAFLOW_DATABASE_CONN_MAX_LIFETIME_JITTER_MS` | `0` | both | Up to this many milliseconds of random extra lifetime per pooled connection (`database.conn_max_lifetime_jitter_ms`; Helm `database.connMaxLifetimeJitterMs`), so replicas started together do not all reconnect at the same moment. Applies to the main, scheduler and health pools, never to the leader election connection. `0` adds none. |

### Redis (`redis.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_REDIS_URL` | _(empty)_ | **Pro only** | Redis URL (XCom + locks). Empty selects the embedded Lite edition — XCom on Postgres, in-process log tailer ([ADR 0026](/project/adrs/0026-lite-datastore-no-redis/)). Pro sets it via the Helm chart. |
| `DEXAFLOW_REDIS_CA_FILE` | _(empty)_ | Pro | Absolute path to a PEM CA bundle trusted when negotiating TLS to a `rediss://` URL (#312). Needed for managed Redis (Memorystore, ElastiCache in-transit encryption, Azure Cache) whose server cert is signed by a provider/per-instance CA not in the container's system roots. Empty falls back to system roots only. The Helm chart sets it when `redis.caConfigMap` is configured. |

### Auth (`auth.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_AUTH_PROVIDER` | `jwt` | both | Credential authenticator: `jwt` (default — username/password issues an HS256 token) or `oidc` (adds the OIDC/SSO login flow on top; the JWT authenticator stays the request-path verifier in both modes). `oidc` is Pro-gated and fails boot closed unless its prerequisites are met (see [OIDC / SSO](#oidc--sso-authoidc)). |
| `DEXAFLOW_AUTH_JWT_SECRET` | — *(required)* | both | Signs API/agent tokens. Required for both `jwt` and `oidc` (both mint the app's own HS256 token). |
| `DEXAFLOW_AUTH_JWT_TOKEN_TTL_SECONDS` | `3600` | both | Lifetime, in seconds, of an issued API token. |
| `DEXAFLOW_AUTH_JWT_MAX_LIFETIME_SECONDS` | `86400` | both | Ceiling, in seconds, on the **total** age of a transparently renewed session, measured from first login and preserved across every renewal. Past it, `POST /api/v2/auth/token/renew` refuses and the user must log in again; the short `TOKEN_TTL_SECONDS` is what bounds a stolen token, this only caps how long a live session may keep refreshing. A non-positive value disables the ceiling. Renewal also re-checks that the account is still active, so a deactivated user stops being issued tokens as well as being refused on use. The chart has no value for this yet — set it through `extraEnv`. |
| `DEXAFLOW_AUTH_LOGIN_RATE_LIMIT_PER_MINUTE` | `5` | both | Cap on **failed** `/auth/token` attempts per client IP per minute (anti-brute-force). A successful login consumes no budget. `dexaflow lite` raises this well above the default (local single-user tool). |
| `DEXAFLOW_SECRET_KEY` | — | both | Key encrypting connection secrets at rest ([ADR 0019](/project/adrs/0019-secret-encryption-at-rest/)). Raw 32 chars, 64-char hex, or base64. Empty disables connection writes. Accepts a **comma-separated list to rotate**: the first entry encrypts and decrypts, later entries only decrypt, and nothing is ever written under them. Same rule as Airflow's `fernet_key`. See [Rotating the encryption key](#rotating-the-encryption-key). |
| `DEXAFLOW_AUTH_SECRET_SCOPING` | `permissive` | both | Scope-by-declaration policy ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `permissive` (delivers the whole tenant vault; warns when a DAG declares a narrower set), `enforce` (delivers only the declared subset — empty declaration ⇒ nothing), or `off` (no scoping). Operator-scoped, never author-settable. Helm: `auth.secretScoping`. |
| `DEXAFLOW_AUTH_SECRET_LIVENESS_MODE` | `observe` | both | Gates secret delivery on task-instance liveness ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `observe` (logs + audits a would-have-denied when the caller's task instance is not live, but still delivers) or `enforce` (denies). Liveness renewal is always on regardless of mode; this only chooses whether a not-live token is refused. Required to be `enforce` when warm pools are on. Helm: `auth.secretLivenessMode`. |
| `DEXAFLOW_AUTH_AGENT_TOKEN_TRANSPORT` | `envvar` | Pro (K8s) | How the in-pod agent obtains its control-plane bearer credential ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `envvar` (plaintext `DEXAFLOW_AGENT_TOKEN` on the pod spec — today's behavior, byte-identical) or `exchange` (projected ServiceAccount token exchanged once via a control-plane `TokenReview` for a task-scoped JWT — nothing secret on the pod object; requires cluster-scoped `create` on `authentication.k8s.io/tokenreviews`). Operator-scoped. Prerequisite for warm pools. Ignored by the subprocess (Lite) executor. See [Agent credential transport](/operate/agent-credential-transport/). Helm: `auth.agentTokenTransport`. |
| `DEXAFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME` | `24h` | both | Duration ceiling on how long one attempt's agent credential may be kept alive by heartbeat renewal ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)). A runaway-task backstop — the short per-attempt TTL is what bounds a stolen token. A non-positive value disables the ceiling. No Helm value yet — `extraEnv` only ([#955](https://github.com/dexadata/dexaflow/issues/955)). |
| `DEXAFLOW_AUTH_SERVICE_TOKEN` | _(empty)_ | both | Turns on the [operator service API](#operator-service-api) under `/api/v2/service/` and is its bearer credential. At least 32 characters; boot fails on a shorter one. Keep it in a Secret. Empty leaves the API off and its routes absent. Helm: `auth.serviceToken`, or `auth.serviceTokenExistingSecret` naming a Secret with key `serviceToken`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_ISSUER` | _(empty)_ | both | Turns on the [trusted-issuer handoff](#trusted-issuer-handoff): a platform that already authenticates its users opens a UI session for them by posting a token its own issuer signed. The exact `iss` of those tokens. Empty disables it and the endpoint does not exist. Helm: `auth.trustedIssuer.issuer`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_NAME` | _(empty)_ | both | Name of the trusted issuer, 1-40 lowercase letters, digits or `-`. Users the issuer may sign in are linked under `issuer:<name>`, so keep it stable once users exist. Helm: `auth.trustedIssuer.name`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_JWKS_URL` | _(empty)_ | both | Where the issuer publishes its public signing keys (RS256, ES256 or PS256). `https`, or `http` on a loopback host. Fetched on first use and refreshed when a token names an unknown key, so key rotation needs no restart and an outage of the issuer does not block boot. Helm: `auth.trustedIssuer.jwksUrl`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_AUDIENCE` | _(empty)_ | both | The `aud` the issuer's tokens must carry for this Dexaflow. Helm: `auth.trustedIssuer.audience`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_TENANT_CLAIM` | `tenant_id` | both | The string claim that names the Dexaflow tenant. Helm: `auth.trustedIssuer.tenantClaim`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_ALLOWED_TENANTS` | _(empty)_ | both | Comma-separated tenants the issuer may sign in to; `*` allows every tenant, for an operator that serves many. Required when the issuer is set. Helm: `auth.trustedIssuer.allowedTenants`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_MAX_LIFETIME_SECONDS` | `0` | both | Longest `exp - iat` a handoff token may have, its replay window. `0` uses 120 seconds; at most 600. Helm: `auth.trustedIssuer.maxLifetimeSeconds`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_ALLOWED_ORIGINS` | _(empty)_ | both | Comma-separated origins (`scheme://host[:port]`, no path) whose pages may post a handoff, typically your portal. A post with any other `Origin`, or none, is refused with `403`, so another site cannot sign a visitor in as someone else. Required when the issuer is set. Helm: `auth.trustedIssuer.allowedOrigins`. |
| `DEXAFLOW_AUTH_EXTERNAL_SIGNIN_URL` | _(empty)_ | both | Sends UI visitors without a session to your own sign-in instead of Dexaflow's page, for a Dexaflow served from a larger platform. The page they asked for travels in a `next` query parameter (a same-origin path, `/` when the request carried anything else), added to whatever query your URL already has; your flow is expected to return them with a Dexaflow session. API calls without a session still get `401`. `/api/v2/auth/login?local=1` and a refused single sign-on still render Dexaflow's page, so break-glass access survives an outage of your sign-in. Absolute `http(s)` URL; boot fails otherwise. Helm: `auth.externalSigninUrl`. |
| `DEXAFLOW_AUTH_EXTERNAL_SIGNOUT_URL` | _(empty)_ | both | Where `/api/v2/auth/logout` lands after clearing the session cookie, so your platform can end its own session too. Empty returns to Dexaflow's sign-in page. Absolute `http(s)` URL; boot fails otherwise. Helm: `auth.externalSignoutUrl`. |
| `DEXAFLOW_AUTH_SESSION_COOKIE_INSECURE` | `false` | both | Drops the `Secure` attribute from the browser session cookie (`_token`) and the OIDC state cookie. Leave it off. Both login paths set the session cookie server-side, `HttpOnly`, `SameSite=Lax`, `Secure`, so the session token is never readable by a script. There is one reason to turn it on: a deployment served over **plain http to something that is not a loopback address**, where the browser refuses a `Secure` cookie outright and the sign-in page would post valid credentials, get a `200`, and land back on itself with no error anywhere. A loopback deployment (`localhost`, `127.0.0.1`) needs nothing: browsers treat it as trustworthy and accept the cookie over http. It cannot be derived from the request (behind a TLS-terminating ingress the server sees plain http while the browser sees https), so it is a setting, and boot logs a `WARN` while it is on. Operator-scoped. No Helm value on purpose: a chart install terminates TLS at the ingress, where this must stay off. `extraEnv` if a deployment genuinely needs it. **Set this before upgrading a plain-http deployment on a non-loopback name.** The browser refuses a `Secure` cookie there and refuses the `Secure` deletion too, so a new login is discarded and sign-out cannot clear the session the previous build left behind until it expires on its own. |
| `DEXAFLOW_AUTH_DEV_NO_AUTH` | `false` | dev-only | Legacy escape hatch — bypasses auth entirely, treating every request as admin. Permitted only on a loopback `http_addr` (boot fails otherwise). Modern Lite uses a real admin login generated by `dexaflow setup`; set this only for ephemeral test scaffolds. |

### OIDC / SSO (`auth.oidc.*`)

Read only when `DEXAFLOW_AUTH_PROVIDER=oidc`, which is Pro-gated (`ui.edition:
pro`) and fails boot closed unless `issuer`, `client_id`, `redirect_url` **and
the tenant pin (`tenant_claim` + `tenant_claims`)** are all set. The pin is in
that set because every login resolves a tenant from it and an absent or unmapped
claim value fails the login closed, never falling back to `default`, so a
deployment without it boots green and rejects 100% of logins ([#1143](https://github.com/dexadata/dexaflow/issues/1143)).
`tenant_claims` is a map, so it loads only from the YAML config file named by
`DEXAFLOW_CONFIG`; no env var can carry it. A blank name on either side of an
entry in `tenant_claims` or `role_mappings` fails boot: `corp.example:` with
nothing after it is valid YAML that binds to the empty string, and it would deny
every login it governs while looking like a key you had filled in. Verification is keyless (the ID
token is validated against the issuer's public JWKS), so no secret is stored for
the verify path.

Every key in this section has a modeled Helm value under `auth.oidc.*`, off by
default (`auth.oidc.enabled: false`, which renders nothing at all). The chart
sends the scalars and lists as `DEXAFLOW_AUTH_OIDC_*` env vars, the two maps as a
mounted config file, and the client secret through the chart-managed Secret or
`auth.oidc.existingSecret`. It refuses to render an `enabled: true` block that
lacks the tenant pin. See the chart README's SSO section and
`helm/dexaflow/examples/values-oidc-google.yaml`.

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_AUTH_OIDC_ISSUER` | _(empty)_ | Pro | The org's single-tenant issuer URL (must be `https://`). Pinned: any ID token whose `iss` differs is rejected. |
| `DEXAFLOW_AUTH_OIDC_CLIENT_ID` | _(empty)_ | Pro | Registered application (client) id; the expected audience of every ID token. |
| `DEXAFLOW_AUTH_OIDC_CLIENT_SECRET` | _(empty)_ | Pro | Used only for the authorization-code exchange. Inject via env; never persist it in a config file, never logged. |
| `DEXAFLOW_AUTH_OIDC_REDIRECT_URL` | _(empty)_ | Pro | This server's callback URL registered with the IdP (`…/api/v2/auth/oidc/callback`). Must be `https://` (http allowed only for loopback hosts). |
| `DEXAFLOW_AUTH_OIDC_SCOPES` | `openid, email, profile` | Pro | OAuth scopes requested. A list, set as a comma-separated env var. Add the IdP's groups scope when group→role mapping is used. |
| `DEXAFLOW_AUTH_OIDC_GROUPS_CLAIM` | `groups` | Pro | The ID-token claim carrying the user's IdP groups; its values drive `role_mappings`. |
| `auth.oidc.role_mappings` | _(empty map)_ | Pro | Maps an IdP group value → an existing Dexaflow role name. **Default-DENY**: an unmapped group grants no role. YAML config file only (a map does not bind from an env var). Helm: `auth.oidc.roleMappings`, rendered into the mounted config file. Reconciliation is IdP-authoritative, so an EMPTY resolved set CLEARS the user's existing grants on every login: configure this or `default_role`. |
| `DEXAFLOW_AUTH_OIDC_DEFAULT_ROLE` | _(empty)_ | Pro | When an authenticated user resolves to zero mapped roles and this is set, grants this single role (advised: a read-only role such as `viewer`). Empty keeps strict default-deny. Must name an existing DB role for the resolved tenant. |
| `DEXAFLOW_AUTH_OIDC_TENANT_CLAIM` | _(empty)_ | Pro | **Required with `provider: oidc`** (boot fails otherwise). Which IdP claim identifies the tenant: `tid` (Entra) or `hd` (Google Workspace). Set it to `hd` with exactly one entry in `tenant_claims` and the login redirect also carries Google's `hd` parameter, so the account chooser offers only accounts in that domain. That is a convenience: the pin is still the verified claim on the returned token. |
| `auth.oidc.tenant_claims` | _(empty map)_ | Pro | **Required with `provider: oidc`, with at least one entry** (boot fails otherwise). Maps a `tenant_claim` value → a Dexaflow tenant name. A value not present is rejected and the login never falls back to `default`. The claim may be a string or an array of strings (some IdPs emit `aud` as an array); an array naming two accepted tenants is rejected as ambiguous rather than resolved to either, and a claim that is neither shape is rejected with its own audit reason. Config file only (a map does not bind from an env var), read from the path in `DEXAFLOW_CONFIG`. Helm: `auth.oidc.tenantClaim` + `auth.oidc.tenantClaims`, which the chart requires together before it will render an SSO install. **The value must name a tenant that already exists**: the only tenant anything in Dexaflow creates is `default`, from the first migration, so map to `default` unless you created one yourself. The server checks this at boot and warns. |
| `DEXAFLOW_AUTH_OIDC_ALLOWED_EMAIL_DOMAINS` | _(empty)_ | Pro | Login-level allowlist layered on TOP of the `tid`/`hd` tenant pin (not the pin itself). Empty imposes no domain restriction. Non-empty admits a login only when the verified email's domain is in the list. A list, set as a comma-separated env var. |
| `DEXAFLOW_AUTH_OIDC_BREAK_GLASS_EMAILS` | _(empty)_ | Pro | Allowlist of local password logins permitted while provider is `oidc`; every other password login is rejected (SSO-only). A list, set as a comma-separated env var. **Empty means an IdP outage or a wrong tenant pin locks everyone out**, including whoever has to fix it; the server warns at boot. It also warns when none of the listed addresses has a **local password account**, which is the worse case: the allowlist admits the address and the credential store then rejects it exactly like a wrong password, so the hatch does not open while you believe it will. A user provisioned through SSO does not count, it has no password. Create the local account before you need it, while an admin session still exists. |
| `DEXAFLOW_AUTH_OIDC_JIT_PROVISIONING` | `false` | Pro | Create a user row on first OIDC login when none matches; the new row is granted the roles from `role_mappings` (or `default_role`). **Off means no SSO login can succeed**: a login is matched by `(oidc_provider, oidc_subject)` and JIT is the only path that ever writes those columns, so there is no supported way to pre-provision a matching account and every first login is denied (audited `no_user_jit_off`). The Helm chart therefore defaults `auth.oidc.jitProvisioning` to `true`. An address that already has a local password account in the same tenant cannot be provisioned either way (unique `(tenant, email)`; audited `jit_failed`). The server logs a WARN at boot when it is off, so the cause is visible before the first login is attempted. |
| `DEXAFLOW_AUTH_OIDC_AUTO_REDIRECT` | `false` | Pro | Start the login flow on the sign-in page instead of rendering it, for a deployment where that page is a screen to acknowledge for nothing (an edge proxy already authenticated the session, or SSO is the only way in). **Suppressed on a refused sign-on**, so a denial lands on the page that explains it rather than bouncing back to the IdP forever, and suppressed by `?local=1`, so a break-glass account can always reach the password form without a values edit. Helm: `auth.oidc.autoRedirect`. |
| `DEXAFLOW_AUTH_OIDC_CLOCK_SKEW_SECONDS` | `60` | Pro | Tolerance (seconds) on the ID token's `exp`/`iat`/`nbf` checks to absorb clock differences between the IdP and this server. |

{{% alert title="Set `default_role`, not only `role_mappings`" color="warning" %}}
Roles are IdP-authoritative: each login resolves a role set and the user's grants
are reconciled to **exactly** that set, so a login that resolves to zero roles
**clears every grant the user already had**. That happens in two configurations,
and the server logs a WARN at boot for both:

- **neither key set** - every login resolves to zero roles;
- **`role_mappings` set, `default_role` empty** - a login whose group claim
  matches no entry resolves to zero roles. Google Workspace emits no `groups`
  claim at all unless Directory API group sync is configured, so on that IdP
  every login takes this path.

Setting `default_role` to a read-only role such as `viewer` gives resolution a
floor and makes the clear impossible. It is not free: that role is granted to
every login the tenant pin admits, so on an IdP that does emit the groups claim,
`role_mappings` alone is the stricter posture and the WARN is one to dismiss
deliberately rather than configure away.
{{% /alert %}}

{{% alert title="`client_secret` is required by every confidential client" color="warning" %}}
`client_secret` is optional at boot because a public client (PKCE only, no
secret) is a valid registration. It is not optional for Google Workspace or
Entra, which always register a server-side application as confidential, nor for
Okta or Keycloak unless the client is explicitly public. Without it the
authorization-code exchange is rejected with `invalid_client`, and the callback
answers the same generic 403 it answers for every other failure. The server logs
a WARN at boot when the secret is empty.
{{% /alert %}}

### Scheduler (`scheduler.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_SCHEDULER_ENABLED` | `true` | both | Whether this process runs the scheduler loop. |
| `DEXAFLOW_SCHEDULER_LOOP_INTERVAL_MS` | `1000` | both | Scheduler tick interval, in milliseconds. |
| `DEXAFLOW_SCHEDULER_DISPATCH_BUFFER_SIZE` | `0` | both | Depth of the queued-dispatches channel ([ADR 0031](/project/adrs/0031-scheduler-architecture/), #127). `0` keeps dispatch synchronous with the tick (right for Lite); `>0` enables the worker pool (right for Pro, where K8s API calls add latency): the tick only enqueues, and a full buffer leaves the task scheduled for the next tick. Recommended for a busy cluster: `512` with 16 workers. Helm: `config.scheduler.dispatch.bufferSize`. |
| `DEXAFLOW_SCHEDULER_DISPATCH_WORKERS` | `0` | both | Goroutines draining the dispatch queue. Ignored when buffer size ≤ 0; otherwise floored to 1. Helm: `config.scheduler.dispatch.workers`. |

### Executor (`executor.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_EXECUTOR_TYPE` | `kubernetes` | both | Pod-path executor: `kubernetes` (default, pod-per-task) or `subprocess` (dev only — runs the agent on the host without isolation; `dexaflow lite` sets it). |
| `DEXAFLOW_EXECUTOR_TASK_NAMESPACE` | `leoflow` | Pro | Kubernetes namespace the server creates task pods and per-run staging PVCs in. MUST match the namespace the Helm chart grants the executor Role in (chart `taskNamespace`); a mismatch 403s every dispatch (#480). |
| `DEXAFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` | _(empty → `server.grpc_addr`)_ | both | gRPC address task pods dial back to. In a local k3d/kind cluster set it to a host-reachable address such as `host.k3d.internal:9091`. |
| `DEXAFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP` | _(empty)_ | Pro | Names a ConfigMap (key `ca.crt`) mounted into task pods so the agent verifies the control plane's gRPC TLS cert (#58). Empty = agents use the insecure channel (dev). |
| `DEXAFLOW_EXECUTOR_TASK_SECRET_NAME` | _(empty)_ | Pro | Names a Kubernetes Secret mounted read-only into every task pod, so a task can read a cluster-stored credential (e.g. a GCP SA key) referenced by a connection's `key_path` ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). Empty = no secret mounted. |
| `DEXAFLOW_EXECUTOR_TASK_SECRET_MOUNT_PATH` | `/etc/leoflow/secrets` | Pro | Where `DEXAFLOW_EXECUTOR_TASK_SECRET_NAME` is mounted in the task pod. |
| `DEXAFLOW_EXECUTOR_TASK_SERVICE_ACCOUNT` | _(empty)_ | Pro | ServiceAccount task pods run as when a DAG does not set `execution.service_account`. The Helm chart wires this from `taskServiceAccount.name` when `taskServiceAccount.create: true`, so creating the task SA is enough for keyless secret access — no per-DAG opt-in. An explicit per-task `execution.service_account` still wins; empty leaves pods on the namespace default SA. |
| `DEXAFLOW_EXECUTOR_AGENT_PATH` | `leoflow-agent` | dev-only | The agent binary the subprocess executor runs (`leoflow-agent`, a link to `dexaflow-agent`, so agents from before the rename are found too). |
| `DEXAFLOW_EXECUTOR_SUBPROCESS_WORKDIR` | _(empty)_ | dev-only | Working directory the subprocess executor runs the agent in (so it can import the project's `dag.py`). Empty keeps the server's working directory. |
| `DEXAFLOW_EXECUTOR_HTTP_USER_AGENT` | `leoflow/0.1` | both | Default `User-Agent` header for HTTP requests a task image may make on the platform's behalf. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_QPS` | `5` | Pro | Client-side request rate (queries per second) of the Kubernetes client that creates task pods. The agent token exchange builds its own client with the same limits. `5` is client-go's default; a 1,000-task fan out at 5 QPS takes over three minutes just to create pods, so a large deployment raises it (for example `50`). Non-positive falls back to `5`. Helm: `executor.kubeClient.qps`. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_BURST` | `10` | Pro | Burst of the same client's token bucket. Non-positive falls back to `10`. Helm: `executor.kubeClient.burst`. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_MAINTENANCE_QPS` | `0` | Pro | When above `0`, maintenance work (pod informer, reconciler, reapers, staging GC, warm pool reconciler) gets its own Kubernetes client and rate limiter at this QPS, so a maintenance burst cannot starve pod creation. `0` keeps maintenance on the dispatch client, one shared budget. Set it whenever you raise `KUBE_CLIENT_QPS`. Helm: `executor.kubeClient.maintenanceQps`. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_MAINTENANCE_BURST` | `0` | Pro | Burst of the separate maintenance client. Ignored while `KUBE_CLIENT_MAINTENANCE_QPS` is `0`; non-positive falls back to `10`. Helm: `executor.kubeClient.maintenanceBurst`. |

### Executor task defaults (`executor.defaults.*`)

Lowest-precedence (L0) per-cluster task defaults, applied at dispatch to fill
gaps the DAG artifact left empty ([ADR 0023](/project/adrs/0023-dag-authoring-config-binding/)).
They never override a value baked into `dag.json`, keeping the artifact portable
across clusters.

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_EXECUTOR_DEFAULTS_RUN_TASKS_AS_NON_ROOT` | `true` | Pro | **Refuses to start a task container whose image resolves to UID 0**, completing Pod Security Admission's `restricted` set. On by default — the images this repo ships carry a numeric non-root UID (`USER 65532:65532`) and the executor pairs it with a pod-level `fsGroup` so the staging PVC stays writable. Turn it off only for a cluster whose task images legitimately run as root. Operator-scoped (never a DAG field). |
| `DEXAFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM` | `false` | Pro | Mounts every task container's root filesystem read-only. Off by default (`restricted` does not require it and it breaks ordinary Python tasks — pip cache, `/tmp`, matplotlib config). Turn on for a fleet of tasks known not to write outside their volumes. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_STAGING_ACCESS_MODE` | `ReadWriteMany` | Pro | PVC access mode for the per-run staging volume. Default `ReadWriteMany` (multi-node prod); single-node dev (k3d local-path, no RWX) sets `ReadWriteOnce`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_STAGING_SIZE` | _(empty)_ | Pro | Default size of the per-run staging volume when the DAG enabled staging without pinning it (a Kubernetes quantity, e.g. `10Gi`). Empty leaves the size unset. Helm: `executor.defaults.staging.size`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_STAGING_STORAGE_CLASS` | _(empty)_ | Pro | Default StorageClass for the staging volume (e.g. the cluster's RWX class). Empty falls back to the cluster's default StorageClass. Helm: `executor.defaults.staging.storageClass`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU` | _(empty)_ | Pro | Default CPU for a task that declares none of its own (a Kubernetes quantity, e.g. `250m`). Applied as **both request and limit**. Guaranteed QoS needs the **memory** default set too — cpu alone leaves the task Burstable with no memory bound at all, and the control plane WARNs at boot naming the missing key; empty leaves it BestEffort unless the DAG sets its own. Helm: `executor.defaults.resources.cpu`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_RESOURCES_MEMORY` | _(empty)_ | Pro | Default memory for a task that declares none of its own (e.g. `256Mi`). Applied as **both request and limit**. Set it together with the CPU default — either one alone is Burstable, not Guaranteed. Helm: `executor.defaults.resources.memory`. |

### Warm worker pools (`execution.*`)

Pro-gated N:1 pod reuse ([ADR 0058](/project/adrs/0058-warm-worker-pools/)). Every field
is operator-set. The default is a byte-for-byte no-op — warm pools OFF means a
dedicated pod per task attempt.

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_EXECUTION_WARM_POOLS_ENABLED` | `false` | Pro | Reuse one task pod across many attempts of the same DAG version. Off = dedicated pod-per-task. Validated fail-closed at boot: requires `agent_token_transport=exchange` **and** `secret_liveness_mode=enforce`. Helm: `execution.warmPoolsEnabled`. |
| `DEXAFLOW_EXECUTION_MIN_IDLE_WORKERS` | `0` | Pro | Warm workers kept ready per DAG version when the DAG declares no warmth of its own (D6). `0` is scale-to-zero. Read only while warm pools are on. |
| `DEXAFLOW_EXECUTION_MAX_POOL_SIZE` | `8` | Pro | Cap on the warm workers one DAG version may hold, and the ceiling a DAG author's warmth request is clamped to (D6). |
| `DEXAFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER` | `50` | Pro | Attempts a warm worker serves before it drains and recycles (D9). |
| `DEXAFLOW_EXECUTION_MAX_WORKER_LIFETIME` | `1h` | Pro | Wall-clock lifetime of a warm worker before it drains and recycles, independent of the attempt count (D9). A duration string. |
| `DEXAFLOW_EXECUTION_WORKER_IDLE_TTL` | `5m` | Pro | How long an idle warm worker is kept before it is recycled (D6). A duration string. |
| `DEXAFLOW_EXECUTION_MAX_WARM_PODS_PER_TENANT` | `100` | Pro | Cap on the total warm pods one tenant may hold across all its DAG versions (M4), so one team cannot pin idle pods and starve neighbours on a shared cluster. |
| `DEXAFLOW_EXECUTION_WARM_READ_ONLY_ROOT_FILESYSTEM` | `false` | Pro | Mount every warm worker's root filesystem read only, give each attempt its own `HOME` and XDG dirs inside the scratch the worker wipes between attempts, and empty the `/tmp` emptyDir and `/dev/shm` before each attempt and again as soon as it ends, so nothing one attempt writes reaches the next one on the same worker. A task that writes outside `$HOME`, `$TMPDIR`, `/tmp` and `/dev/shm` fails with it on. Takes effect on warm pods created after it is turned on; running warm pods keep their spec until they recycle. Dedicated task pods are not affected. Helm: `execution.warmReadOnlyRootFilesystem`. |
| `DEXAFLOW_EXECUTION_WARM_POD_RESOURCES_CPU` | _(empty)_ | Pro | CPU request and limit of every warm worker pod. Empty inherits `executor.defaults.resources_cpu`, so a task without resources of its own gets on a warm worker what its dedicated pod would. A task that declares more, or a limit the warm pod would undercut, runs on a dedicated pod. Helm: `execution.warmPodResources.cpu`. |
| `DEXAFLOW_EXECUTION_WARM_POD_RESOURCES_MEMORY` | _(empty)_ | Pro | Memory request and limit of every warm worker pod; same rules as the CPU value. Helm: `execution.warmPodResources.memory`. |
| `DEXAFLOW_EXECUTION_WARM_POOL_EVENT_REFILL` | `false` | Pro | Refill warm pools on events instead of every 30s: the reconciler reads the warm fleet from a dedicated pod informer instead of a LIST per tick, reacts at once when a warm worker is deleted, fails or is claimed by an attempt, and creates replacements concurrently (up to 4 at a time). The periodic tick stays as a backstop. Helm: `execution.warmPoolEventRefill`. |

### Logs (`logs.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_LOGS_DIR` | `/var/log/leoflow` | both | Task-log sink directory (used by the default `disk` backend). |
| `DEXAFLOW_LOGS_BACKEND` | `disk` | Pro | Durable task-log store: `disk` (default — the on-disk sink, unchanged; the only backend Lite uses), `s3` (AWS S3, MinIO, Ceph RGW), or `gcs` (Google Cloud Storage, native SDK). See [ADR 0056](/project/adrs/0056-task-log-object-sink/). |
| `DEXAFLOW_LOGS_SINK_BUCKET` | _(empty)_ | Pro | Target bucket. Required when the backend is `s3` or `gcs` (boot fails otherwise). |
| `DEXAFLOW_LOGS_SINK_PREFIX` | _(empty)_ | Pro | Optional key prefix; objects are laid out at `{prefix}/{tenant}/{dag}/{run}/{task}/{try}.log`. |
| `DEXAFLOW_LOGS_SINK_REGION` | _(empty)_ | Pro | **s3-only.** Store region (e.g. `us-east-1`). Required by AWS S3; ignored by some S3-compatible stores. |
| `DEXAFLOW_LOGS_SINK_ENDPOINT` | _(empty)_ | Pro | **s3-only.** Endpoint override for S3-compatible stores (MinIO, Ceph RGW). Empty uses the AWS default. Not a path to GCS — use `gcs`. |
| `DEXAFLOW_LOGS_SINK_FORCE_PATH_STYLE` | `false` | Pro | **s3-only.** Use path-style addressing (bucket in the path, not the host). Required by MinIO and some S3-compatible stores. |
| `DEXAFLOW_LOGS_SINK_ACCESS_KEY_ID` / `DEXAFLOW_LOGS_SINK_SECRET_ACCESS_KEY` | _(empty)_ | Pro | **s3-only.** Static credentials — **discouraged**. Leave empty (recommended) to use the keyless chain (IRSA / instance profile), per [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/). |
| `DEXAFLOW_LOGS_SINK_CREDENTIALS_FILE` | _(empty)_ | Pro | **gcs-only.** Path to a service-account JSON key — **discouraged**. Leave empty (recommended) to use Application Default Credentials (GKE Workload Identity). |
| `DEXAFLOW_LOGS_SINK_LAYOUT` | `single` | Pro | How new attempts are written. `single` keeps one object per attempt at `{try}.log`, rewritten on every flush. `segmented` writes numbered segments under `{try}.log.d/` so each flush uploads only the open segment (up to 4 MiB) and the control plane holds one segment per attempt instead of the whole log. Both layouts are always readable, but a server older than this setting reads only `{try}.log`: enable `segmented` once every replica is upgraded. Before downgrading to an older version, switch back to `single`; attempts already written as segments stay unreadable by older versions. On S3, `segmented` needs `s3:ListBucket` on the bucket so a missing segment answers not-found. |

### External secrets (`secrets.*`)

Operator-only ([ADR 0060](/project/adrs/0060-external-secrets-resolution/)):
delivered to the task pod as `DEXAFLOW_SECRETS_*`, which an author's task env can
never set. Empty `backend` keeps the Dexaflow vault as the only source —
byte-identical to having no external secrets at all. See
[External secrets](/operate/external-secrets/) and run the
[cluster validation runbook](/operate/external-secrets-cluster-validation/)
before enabling it in production.

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_SECRETS_BACKEND` | _(empty — disabled)_ | Pro (K8s) | Provider secrets-backend class the in-pod resolver drives (e.g. `airflow.providers.amazon.aws.secrets.secrets_manager.SecretsManagerBackend`). When set, a Connection/Variable a DAG declares can be resolved pod-side from the provider store under the pod's keyless identity. Helm: `secrets.backend`. |
| `DEXAFLOW_SECRETS_BACKEND_KWARGS` | _(empty — treated as `{}`)_ | Pro (K8s) | Provider kwargs as a JSON **object string** (`connections_prefix`, `variables_prefix`, `region_name`, …), delivered to the pod verbatim. A kind is served only if its `*_prefix` kwarg is present. A JSON string rather than a map so a single env var sets it, matching the env-only control-plane chart. Keyless auth (IRSA / Workload Identity) uses the task pod's ServiceAccount — set `executor.task_service_account` accordingly. Helm: `secrets.backendKwargs`. |

### Observability (`observability.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_OBSERVABILITY_LOG_LEVEL` | `info` | both | Control-plane log verbosity: `debug`, `info`, `warn` (alias `warning`), or `error`. Unknown values fall back to `info`. |
| `DEXAFLOW_OBSERVABILITY_LOG_FORMAT` | `json` | both | Control-plane log format: `json` (default) or `text`. |
| `DEXAFLOW_OBSERVABILITY_OTEL_ENABLED` | `true` | both | Enable OpenTelemetry trace export. |
| `DEXAFLOW_OBSERVABILITY_OTEL_ENDPOINT` | `localhost:4317` | both | OTLP collector endpoint (when OTel is enabled). |
| `DEXAFLOW_OBSERVABILITY_METRICS_DROP_LEGACY_NAMES` | `false` | both | Stop publishing every `dexaflow_*` metric a second time under its pre-rename `leoflow_*` name. The default keeps both, so dashboards and alerts on either name work. An opt-in for operators who do not need the `leoflow_*` names; it halves the scrape. Helm: set it through `extraEnv`. |
| `DEXAFLOW_OBSERVABILITY_OTEL_SAMPLE_RATIO` | `1` | both | Share of request traces kept, from `0` to `1`. An incoming `traceparent` header is not propagated, so every request starts its own trace and is sampled at this ratio; spans within a request follow its decision. `1` traces every request; other values outside the range fail boot. |
| `DEXAFLOW_OBSERVABILITY_OTEL_SKIP_PROBE_SPANS` | `false` | both | When `true`, no spans are recorded for `/healthz`, `/readyz` and `/static/*`. Their HTTP metrics are still recorded. |

### UI (`ui.*`)

| Variable | Default | Edition | Purpose |
|---|---|---|---|
| `DEXAFLOW_UI_INSTANCE_NAME` | `Dexaflow` | both | UI navbar label (`dexaflow lite` sets it to mark the environment). |
| `DEXAFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS` | `0` | both | SPA polling cadence for DAG / DagRun / task-instance state. `0` falls back to the production-safe 30s default; `dexaflow lite` sets 1s for a snappy inner loop. Helm: `ui.autoRefreshIntervalSeconds`, which the chart omits entirely when unset so the server default decides. |
| `DEXAFLOW_UI_EDITION` | _(empty)_ | both | Edition badge in the UI shell: `lite` shows the silver LITE badge, `pro` the gold PRO badge (independent of the auth mode; also gates `auth.provider: oidc`). Empty/other shows no badge. |
| `DEXAFLOW_UI_WORKSPACE` | _(empty)_ | both | DAG project directory the Lite web editor edits ([ADR 0025](/project/adrs/0025-lite-embedded-web-editor/)). Empty disables the editor. |
| `DEXAFLOW_UI_MONACO_DIR` | _(empty)_ | both | Where the pinned Monaco bundle was fetched by `dexaflow setup`; the editor page is served Monaco from it. Empty shows a setup hint. |
| `DEXAFLOW_UI_HOME_LINK_LABEL` | _(empty)_ | both | Text of an optional link from the UI back to the platform you serve it from, shown on every page at the bottom-left and opened in the same tab. Set it together with `DEXAFLOW_UI_HOME_LINK_URL`. Helm: `ui.homeLink.label`. |
| `DEXAFLOW_UI_HOME_LINK_URL` | _(empty)_ | both | Absolute `http://` or `https://` URL of the home link. Empty shows no link. Boot fails on another scheme, a missing host, or a URL without a label. Helm: `ui.homeLink.url`. |
| `DEXAFLOW_UI_THEME` | _(empty)_ | both | Theme for the UI as a JSON object, the same shape as Airflow's `[api] theme`: `tokens` (Chakra design tokens such as `colors.brand` and `fonts`), `globalCss`, `icon`, `icon_dark_mode`. Served in `/ui/config`, so the UI applies it through its own theming. Boot fails on invalid JSON, an unknown top-level key, or an icon that is not http(s) or root-relative. Helm: `ui.theme` (YAML, rendered as JSON). See [Branding the UI](#branding-the-ui). |
| `DEXAFLOW_UI_FAVICON_URL` | _(empty)_ | both | Favicon for the UI, http(s) or root-relative. Empty keeps the stock icon. Helm: `ui.faviconUrl`. |
| `DEXAFLOW_UI_STYLESHEET_URLS` | _(empty)_ | both | Comma-separated stylesheets every UI page loads in `<head>`, typically the web fonts a theme names. Each must be http(s) or root-relative and contain no comma. Helm: `ui.stylesheetUrls`. |
| `DEXAFLOW_UI_ETAG_REVALIDATION` | `false` | both | Lets the browser revalidate the grid's task summaries (`/ui/grid/ti_summaries/*`), the one UI route that computes an `ETag`: that route answers `Cache-Control: private, no-cache` with `Vary: Authorization, Cookie` instead of `no-store`, so an unchanged poll gets `304 Not Modified` and no body. Every revalidation still runs authentication and authorization. With it on, the browser keeps the last grid body in its private cache after logout (on a shared machine it stays on disk until evicted); it is never shown without a revalidation, so a signed-out user gets `401`, not the cached grid. Off keeps `no-store` on every UI route. Helm: set it through `extraEnv`. |

### Branding the UI

The bundled UI reads its look from `theme` in `/ui/config`, so a theme changes
colors, fonts and the navigation icon without touching the bundle. This example
uses a blue brand palette and the Outfit and JetBrains Mono fonts, and loads the
fonts from Google Fonts:

```yaml
ui:
  theme:
    tokens:
      colors:
        brand:
          "50":  { value: "#eff6ff" }
          "100": { value: "#dbeafe" }
          "200": { value: "#bfdbfe" }
          "300": { value: "#93c5fd" }
          "400": { value: "#60a5fa" }
          "500": { value: "#3b82f6" }
          "600": { value: "#2563eb" }
          "700": { value: "#1d4ed8" }
          "800": { value: "#1e40af" }
          "900": { value: "#1e3a8a" }
          "950": { value: "#172554" }
      fonts:
        heading: { value: "Outfit, system-ui, sans-serif" }
        body:    { value: "Outfit, system-ui, sans-serif" }
        mono:    { value: "'JetBrains Mono', ui-monospace, monospace" }
  stylesheetUrls:
    - "https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap"
```

Set all eleven `brand` shades: the UI uses different shades for buttons,
selections and the navigation highlight, on the light and the dark theme. The UI only
exposes part of its styling through the theme; anything else is a `globalCss`
rule, and holds only as long as the bundle keeps the selector it targets.

### Trusted-issuer handoff

When Dexaflow is part of a larger platform that already signs its users in,
the platform can open a Dexaflow UI session for them without Dexaflow storing a
password and without the platform holding Dexaflow's signing secret:

1. The platform's issuer signs a short-lived JWT with its own key, carrying
   `iss`, `aud`, `sub`, `iat`, `exp`, a unique `jti`, the tenant claim and,
   optionally, `email`. It publishes the public key as a JWKS.
2. The browser posts that token to `POST /api/v2/auth/session` as the form
   field `token`, with the page to open as `next` (a same-origin path), from a
   page on one of `allowed_origins`. An auto-submitting form is the usual way,
   because a token in a URL ends up in logs and history.
3. Dexaflow verifies the token, finds the active user linked to
   (`issuer:<name>`, `sub`) in the token's tenant, sets the same session cookie
   a password or SSO login sets, and redirects to `next` with `303`.

The token never creates a user and never grants roles: the user must already
exist and be linked to the issuer, and its roles are the ones Dexaflow holds.
Refusals set no cookie and answer `400` (no token), `401` (token rejected),
`403` (origin not allowed, or no active linked user in that tenant) or `500`, with the reason in the
server log and the audit trail (`issuer.login.success` /
`issuer.login.failure`), never in the response.

Only pages on `allowed_origins` can post a handoff: browsers send `Origin` on
every cross-site form post, and Dexaflow refuses any other, so a page elsewhere
cannot sign a visitor in as someone else. A token must also have been issued
no later than a minute from now and live no longer than
`max_lifetime_seconds` (120 seconds unless set, at most 600). Each token opens
one session: a second post of the same `jti` is refused until the token
expires. That memory is per server process, so with several replicas a token
could be accepted once by each; the short lifetime is what bounds that
window. Mint each token right before posting it, and never put one in a URL.

### Operator service API

An operator that serves several organizations from one Dexaflow (a hosting
provider, an internal platform team) creates tenants and their users from its
own automation instead of writing to the database. With `auth.service_token`
set, two idempotent endpoints accept `Authorization: Bearer <service token>`;
a user session never reaches them.

`PUT /api/v2/service/tenants/{tenant}` with an optional
`{"display_name": "Acme Corp"}` creates the tenant (1-63 lowercase letters,
digits or `-`) with the same built-in roles, role permissions and default pool
as the `default` tenant, copied from it so every tenant's ladder stays equal.
It answers `201` when the tenant is new and `200` when it already existed; a
second call fills in anything missing and changes nothing else.

`PUT /api/v2/service/tenants/{tenant}/users/{subject}` with
`{"email": "ana@acme.com", "roles": ["operator"]}` makes sure a user with no
password exists in the tenant, linked to the [trusted
issuer](#trusted-issuer-handoff) under that subject, with exactly those roles.
It answers `201` for a new user and `200` for an existing one, whose roles it
sets to the list given. It needs `auth.trusted_issuer` (`409` otherwise),
answers `404` for an unknown tenant, `422` for a role the tenant does not have,
and `409` for a subject already linked in another tenant or an email already
used by another user of the tenant, such as a password account. A tenant the
trusted issuer may not sign in to (`auth.trusted_issuer.allowed_tenants`) is
`403`. The user signs in only through the trusted issuer's handoff.

Every call that reaches the database is recorded in the audit trail, as
`service.tenant.ensure` (with whether the tenant was created) and
`service.user.ensure` (with the subject, email, roles and whether the user was
created), each with outcome `success` or `failure`.

The service token is a root-level credential: whoever holds it can create
tenants and grant any role, `admin` included, in every tenant the trusted
issuer covers. Keep it in a Secret, give it only to the automation that
provisions tenants, and rotate it by changing the Secret and restarting.

### Trusted proxies and the client IP

By default Dexaflow trusts **no** proxy: `X-Forwarded-For` is ignored and the
client IP (used by the login rate-limiter and the audit log) is the direct peer.
This is the safe default — it stops a spoofed `X-Forwarded-For` from forging the
client IP — and is correct for Lite (exposed directly) and for any deployment
reached without a reverse proxy.

When the API runs **behind a reverse proxy or ingress**, set
`server.trusted_proxies` (env `DEXAFLOW_SERVER_TRUSTED_PROXIES`) to the proxy's
IP or CIDR — e.g. your ingress controller's pod CIDR. Only then is the left-most
`X-Forwarded-For` entry honored, so rate-limiting and audit see the real client
instead of the proxy. **Do not** set this to a broad private range (e.g. all of
`10.0.0.0/8`) in a cluster where task pods run: a task pod inside that range
could then spoof the client IP. Scope it to the ingress. An invalid value fails
secure (trust none) with a logged error.

The value is a list. Via the env var (the Helm chart's only override path — it
ships no server config file) set it **comma-separated**, e.g.
`DEXAFLOW_SERVER_TRUSTED_PROXIES=10.0.0.0/8,192.168.1.1`; viper splits it back
into a list. In the chart set the `config.trustedProxies` value (a YAML list) and
it is rendered comma-joined for you. In a config file it is an ordinary YAML list.

## Names from before the rename

Dexaflow was called Leoflow. Every name from that time keeps working, and none
is scheduled for removal. When both the current and the old name are present,
the current one wins.

| Current | Also accepted | When both exist |
|---|---|---|
| `dexaflow.yaml` | `leoflow.yaml` | `dexaflow.yaml` is used and a note is printed; `leoflow.yaml` is ignored. |
| `DEXAFLOW_*` variables | `LEOFLOW_*` variables | The `DEXAFLOW_*` value is used; a conflict is logged. Every binary mirrors one prefix onto the other at startup, so processes it starts see both. |
| `~/.dexaflow` | `~/.leoflow` | An existing `~/.leoflow` is kept in place and `~/.dexaflow` becomes a link to it, so nothing is moved. |
| `~/dexaflow` (default workspace) | `~/leoflow` | `~/dexaflow` is used. With only `~/leoflow`, that stays the default, so its DAG projects are found. A workspace recorded by `dexaflow setup` is always used as is. |
| `dexaflow`, `dexaflow-server`, `dexaflow-agent`, `dexaflow-mcp` | `leoflow`, `leoflow-server`, `leoflow-agent`, `leoflow-mcp` | The installer and `make build` add the old names as links to the new binaries. |
| `from dexaflow import ...` in a `dag.py` | `from leoflow import ...` | `leoflow` is a re-export of `dexaflow`; both names refer to the same objects. |
| `dexaflow_*` metrics | `leoflow_*` metrics | The `/metrics` endpoint publishes every family under both names with the same values, so existing dashboards, alerts and recording rules keep working. Each family therefore appears twice in a scrape. |

A few internal names keep the old spelling on purpose, because renaming them
would break running installations: the Postgres database names (`leoflow`,
`leoflow_dev`), the Lite cluster (`leoflow-dev`), the `leoflow.io/*` labels and
annotations on task pods, the `LEOFLOW_*` variables the control plane passes
to task pods (agents built before the rename only read those), and the
OpenTelemetry service name `leoflow-server` (traces stay continuous across the
upgrade), and the internal Python modules `leoflow_runtime` and `leoflow_parser`
(task images built before the rename run the former, and existing `config.yaml`
files name the latter in `parser_cmd`).
