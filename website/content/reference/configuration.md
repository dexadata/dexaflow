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

{{% alert title="Deploying Cluster on Kubernetes?" color="info" %}}
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
| `defaults` | object | DAG-level `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `size`, `resources`. |
| `staging` | object | Opt-in per-run RWX volume: `enabled`, `size`, `storage_class` (ADR 0022). |
| `min_idle_workers` | integer, 0 to 100 | Idle warm workers kept for the DAG's current version, so its tasks skip the cold pod start. With warm pools on, an unpaused DAG that declares it is prewarmed as soon as a version becomes current ([Prewarm and drain on deploy](/operate/warm-pools/#prewarm-and-drain-on-deploy)). The server clamps it to `execution.max_pool_size`; with warm pools off it does nothing. Refused with `staging.enabled`. |
| `tasks.<task_id>` | object | Per-task overrides (ADR 0023): `retries`, `retry_delay_seconds`, `execution_timeout_seconds`, `size`, `env`, `resources`, `execution`. `size` is the task's pool slots, 1 to 1024 (ADR 0066, see [Task size](/author-dags/dag-authoring/#task-size-size-adr-0066)). |

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
  `…:py3.10-v0.4.5`<!-- stale-version-ok: py3.10 runtimes stopped at 0.4.x -->). Repoint `base_image` to the matching `py3.11` tag and
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
| `dexaflow lite` | Builds the project's venv on it, and stops rather than substituting a different minor. The one minor it can supply itself is `3.11`: when no host interpreter reports it, `dexaflow lite` downloads the pinned, checksum-verified managed CPython 3.11 into `~/.dexaflow/python` the first time it builds the venv (this needs outbound network once). It does the same when the host 3.11 cannot build a venv (on Debian and Ubuntu, a system Python without the `python3-venv` package), and says so. For any other declared minor, install `python3-venv` (or your distribution's equivalent) for that interpreter. |

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
- **`dexaflow compile` and `validate --parse` parse on it when the parser is
  older.** The parser *executes* your `dag.py`, so its own interpreter decides
  which syntax is legal, and that is whichever interpreter `dexaflow setup`
  baked into `parser_cmd`. When that interpreter is older than the minor you
  declare and the declared minor is installed, the parse runs on the declared
  minor instead
  ([#1095](https://github.com/dexadata/dexaflow/issues/1095)). Only a
  `parser_cmd` of the shape setup writes (`[env VAR=value ...] python3[.N] -m
  leoflow_parser`) is changed; any other runs as configured. Because the
  parser imports `dag.py`, a module installed only for the configured
  interpreter is missing on the declared one: the parse then runs once more on
  the configured parser and, when that succeeds, keeps its result with a
  warning naming the module and both interpreters. If the declared minor is not
  installed, the configured parser runs with a warning that newer syntax is
  reported as an error.
- **The three exemptions are the same everywhere.** A version you never wrote is
  not a statement (the default applies), a declared `base_image` makes the field
  inert because you chose the `FROM` by hand, and a deprecated version warns
  rather than demanding you install an interpreter we are asking you to leave.

#### Values that reach the generated Dockerfile

Every value the generated Dockerfile interpolates is checked, because the
Dockerfile format and Docker's own operand lexer give some characters a meaning
no quoting can take away. The refusal always names the field and the value, since
a stray control character in YAML is invisible in the source.

The check is part of validating `dexaflow.yaml`, so every command that reads the
file runs it: `dexaflow validate`, `dexaflow compile` with or without `--build`,
`dexaflow deploy` and `dexaflow lite`. It used to run only while `--build` was
rendering the Dockerfile, so `validate` called such a project valid and the
refusal first appeared on the machine about to build the image.

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
`dexaflow validate` and `dexaflow compile` now fail and name the field, which is
the point.

**Refused in `base_image`: any whitespace, and `'`, `"`, `\` and `$`.** An image
reference cannot contain any of them. `FROM` has no quoting, so whitespace makes
the rest of the line read as the `FROM <image> AS <stage>` form. And `FROM`'s
operand goes through the same lexer as a `COPY` path, so the other four rewrite
the reference: `runtime:v1$SUFFIX` pulls `runtime:v1` (with only a warning
about an undeclared build argument), `runtime:v'1'` and `runtime:v\1` pull
`runtime:v1` with no warning at all, and a stray `"` fails the build. The build
would run your tasks on a different image than the one `base_image` names.
Everything a reference can legally hold (a registry host and port, a path, a
tag, a `@sha256:` digest) is accepted unchanged.

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
at startup and logs how many it moved. Only when that pass moved every row it
found, with none changed underneath it by a concurrent write and none that no
key opens, does it log the rotation as complete:

```
secret key rotation complete for the stored connections re_encrypted=7
```

Remove the old entry once every replica has started with the list. Until then
it is still required, because a replica that has not restarted is still reading
rows only the old key opens.

A row that **no** configured key can open is left untouched and reported at
`ERROR`. Its ciphertext is the only copy of that credential, so the rotation
never overwrites or deletes it; put the missing key in the list and restart.
Until then the server logs a warning naming the connection, and:

- the connections list (`GET /api/v2/connections`) still returns every
  connection of the tenant, and shows this one with `extra` set to `null` and
  `extra_unreadable: true`, so the Admin Connections page still opens it;
- reading it alone (`GET /api/v2/connections/<connection_id>`), or updating
  it (saving it from the Admin page included), answers `500` with a message
  that names the connection, and its stored `extra` is left as it is;
- delivery to a task pod skips it with a warning, so only the tasks that use
  it fail.

If the old key is lost, delete the connection and set it again.

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

{{% alert title="Dexaflow Lite: moving an existing install to its own key" color="warning" %}}
`dexaflow setup` generates a per-install key and keeps it in
`~/.dexaflow/config.yaml`.

An install created before per-install keys existed has no `secret_key`. Its
connection secrets are encrypted with the key that used to be compiled into
this repository, which every Lite install shares, so anyone who obtains that
datastore can read them, and `dexaflow lite` warns on every start. Stop Lite
and run:

```bash
dexaflow lite migrate-key --dry-run   # what it found and what it would do; writes nothing
dexaflow lite migrate-key             # asks before it changes anything
```

It records a new key next to the old one in `config.yaml` before it touches a
row, re-encrypts every stored secret in one verified transaction per
datastore (the managed Postgres and the Docker one, when the install has
both), re-checks them under the new key alone, and only then drops the old key
from the file. If it is interrupted at any point, run the same command again;
until it finishes, `dexaflow lite` starts normally with both keys and warns
that a key migration has not finished.

The Lite server never re-encrypts at startup (it runs with
`DEXAFLOW_SECRET_KEY_REENCRYPT_ON_BOOT=false`), and Lite takes its keys from
`config.yaml` only: a `DEXAFLOW_SECRET_KEY` or `LEOFLOW_SECRET_KEY` exported in
your shell is ignored, and `dexaflow lite` says so when it differs.

Downgrading after a migration: v0.5.0 is the oldest release that reads
`secret_key`, so its `dexaflow` and `dexaflow-server` still open every
migrated secret (downgrade both binaries together: this `dexaflow lite` refuses
a v0.5.0 `dexaflow-server`). v0.5.0 does not take the key-migration lock, takes
an exported `DEXAFLOW_SECRET_KEY` or `LEOFLOW_SECRET_KEY` over the file, and,
on an install whose migration has not finished, re-encrypts at startup onto
`secret_key`, which is safe because both keys are recorded. A release older than
v0.5.0 ignores `secret_key` and cannot read migrated secrets.

**`config.yaml` holds the only copy of the key that decrypts your stored
connections.** `dexaflow lite backup` includes it, which also means the backup
archive holds the key and the ciphertext together. If you roll your own backup
of the datastore, back up `config.yaml` with it, and `dexaflow uninstall` warns
before it removes the only copy.
{{% /alert %}}

### Encrypting Variables

Connection secrets are always encrypted at rest. Variables are stored in
plaintext by default, and masked in API responses only when the key name looks
secret (`password`, `token`, `api_key`, ...). A Variable that holds a DSN, a
webhook URL or an API key under another name is therefore readable in the
database and in every backup of it.

Set `DEXAFLOW_VARIABLE_ENCRYPTION=on` (or `LEOFLOW_VARIABLE_ENCRYPTION`, or
`variable_encryption: on` in `dexaflow.yaml`/`leoflow.yaml`) to encrypt them
with the same key and cipher as connections:

- every write stores the value encrypted;
- at startup the server encrypts the Variables already stored in plaintext and
  logs how many it changed;
- reads, the API and delivery to task pods return the plaintext, so DAGs see no
  difference. The API reports `is_encrypted: true` for an encrypted Variable;
  masking still follows the key name;
- a key rotation and `dexaflow lite migrate-key` move encrypted Variables onto
  the new key together with the connection secrets.

`on` needs a usable `DEXAFLOW_SECRET_KEY`: without one the server refuses to
start rather than keep storing plaintext. With the default `off`, an install
without a key works as before. A Variable that no configured key opens (after a
rotation that dropped the old key, for example) is left as it is, and the
server logs a warning naming it:

- the Variables list (`GET /api/v2/variables`) still returns the tenant's other
  Variables, and shows this one with its value masked (`***`) and
  `value_unreadable: true`;
- reading it alone (`GET /api/v2/variables/<key>`) answers `500` with a message
  that names the Variable;
- delivery to a task pod skips it with a warning, as it does for a connection.

To recover, add the key it was encrypted with back as a later
`DEXAFLOW_SECRET_KEY` entry, or delete the Variable and set it again.

A value cannot start with `dexaflow:enc:v1:`, the tag that marks an encrypted
value, in any mode.

{{% alert title="Turn it on after every replica runs 0.5.4" color="warning" %}}
A release older than 0.5.4 reads an encrypted Variable as its ciphertext and
hands that to the task. Turn encryption on only once no older replica is
running, and see [Upgrading to 0.5.4](/operate/upgrades/#upgrading-to-054)
before rolling back.
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
| `python_version` | `"3.11"` | Pick `3.10`, `3.11`, `3.12`, or `3.13`. It selects the task base image **and**, when you declare it explicitly, the interpreter every local tool judges the project with; see [Which interpreter reads your DAG](#which-interpreter-reads-your-dag). `dexaflow lite` builds that project's venv on it, so the dev loop and the cluster run the same minor. If no interpreter on the host reports that version, `dexaflow lite` stops and says so rather than substituting a different one, except for `3.11`, which it fetches as the managed CPython on first use; a venv already built on another minor is rebuilt, which reinstalls the runtime and your dependencies. Leaving the field out keeps the previous behaviour (any host Python 3.11+, managed build preferred), because then the image is `3.11` by the same default and the two already agree. A declared `build.base_image` makes this field inert on both sides, and a deprecated version warns and falls back instead of blocking. |
| `dag_source` | `"dag.py"` | DAG file relative to the project. |
| `dependencies` | `[]` | pip specifiers baked into the image. Any [PEP 508](https://peps.python.org/pep-0508/) form works, including version floors (`"setuptools>=80.9.0"`) and environment markers (`'requests; python_version < "3.12"'`) — each entry is passed to pip as one literal argument, so shell characters in a specifier are never interpreted. A line break inside an entry is refused, since it would end the generated `RUN` instruction, and every entry is passed after a `--` so an entry beginning with a dash is treated as a package name rather than as an option to pip. |
| `connectors` | `[]` | Short connector names expanded to provider packages at compile (ADR 0038). |
| `system_packages` | `[]` | apt packages. `apt-get install`ed into the DAG image at compile, resolving against the task base image's Debian suite — see the `system_packages` row under [dexaflow.yaml](#dexaflowyaml) for which suite that is and what moved. |
| `include_paths` | `["."]` | Extra paths copied into the image **alongside** `dag_source` — a helper module, a config file, a fixtures directory. Entries are relative to the project directory; an absolute one, or one escaping the context (`../x`), is refused at compile with the entry named, because Docker cannot `COPY` it and failing at build time would name a Docker error instead. The default `["."]` means *no extra paths*, not "everything": it is what every existing project carries, so it must not change what their images contain. Entries already copied (the DAG source, a dbt group directory) are skipped rather than duplicated. Only the **generated** Dockerfile honours it — a project-supplied Dockerfile copies whatever its own `COPY` lines say. Included paths are scanned by the credential warning like everything else that ships. |
| `exclude_paths` | `[".git", "__pycache__", "*.pyc", ".venv", "venv"]` | Kept out of the image. On `--build` these become a `.dockerignore` in the build context for the duration of the build — merged with yours if you have one, and removed afterwards. Each entry is expanded to the forms Docker actually honours, because a bare name in a `.dockerignore` matches only at the context root: a plain directory name becomes four patterns (`p`, `**/p`, `p/**`, `**/p/**`) so that both the directory and its contents are pruned at any depth; an entry whose last segment contains a glob becomes `p` and `**/p` only, since a glob names files rather than a directory to descend into; and an entry containing a `/` is already anchored, so it becomes `p` and `p/**`. An entry starting with `!` or `#` contributes nothing: it is dropped rather than expanded, so a negation belongs in your own `.dockerignore` (which is merged, never rewritten) and not here. A dropped `!` is **reported by name** at build time — leoflow's block is appended after your own lines, so a negation emitted there could resurrect a path one of your earlier lines excluded. Add anything holding credentials: the image is pushed to a registry and pulled by every pod that runs the DAG. **Not** used by workspace discovery, which has its own hardcoded skip list. |
| `build.context` | `"."` | **Ignored.** Declared and defaulted, but the build always uses the DAG directory; `validate`, `compile` and `deploy` warn when it is set to anything other than `.`. Tracked in [#1062](https://github.com/dexadata/dexaflow/issues/1062). |
| `build.dockerfile` | *unset* | A Dockerfile the project ships, used as-is instead of the generated one when the file exists (a missing file falls back to the generated Dockerfile). It is relative to the directory holding `dexaflow.yaml` and must stay inside it: an absolute path, or one escaping the project (`../x`), is refused by `dexaflow validate` and `compile`, and `compile --build` refuses one that a symlink leads out of the project. A Dockerfile outside the project is not reviewed with it, and it skips every check described in [Values that reach the generated Dockerfile](#values-that-reach-the-generated-dockerfile). A project Dockerfile (this one, or a `Dockerfile` in the project) replaces the generated one, so `base_image`, `dependencies`, `connectors`, `system_packages` and `include_paths` do not reach the image; `compile --build` and `deploy` print a warning naming those the project's own file sets (a `base_image` filled from the workspace, a target or the cluster is not named) ([ADR 0003](/project/adrs/0003-dag-as-image/)). The `--dockerfile` flag is not confined: it is the operator's own choice on the command line, and it wins over this field. |
| `build.platforms` | `["linux/amd64"]` | The platforms the image is built for, `os/arch[/variant]`. `--platform` on `compile`, `build` and `deploy` overrides it, and a target or the workspace defaults fill it when the project sets none. A malformed value is refused before the build, with its source named. Several platforms need a tool that pushes one image per platform under one manifest list, such as docker with buildx or the containerd image store, or podman; the manifest the registry holds for the push, read at the digest the tool reported, is checked to list every platform, and when the tool cannot build it or the check cannot confirm it, the build fails with a hint (see [Builders](/operate/builders/#choosing-the-platform)). |
| `build.labels` | *unset* | **Ignored.** Accepted, with a warning from `validate`, `compile` and `deploy`: no label is set on the image, whichever builder runs. The map is still part of the `dexaflow.yaml` the content label hashes, so changing it gives the DAG a new version label. |
| `build.args` | *unset* | Build arguments (`--build-arg`) for the `ARG` lines of your own Dockerfile; the generated Dockerfile declares none. Plain values that stay in the image history and are part of the content label. Every builder passes them. See [Builders](/operate/builders/#build-arguments-secrets-and-a-private-package-index). |
| `build.secrets` | *unset* | Build secrets, each `{id, env}`: a `RUN --mount=type=secret,id=<id>` line of your own Dockerfile reads the value of the environment variable `env` of the machine that builds. The value never reaches the command line, the build log, `dag.json` or a layer. A variable that is not set fails the build. The build runs on your machine, never on a remote builder. |
| `build.package_index.url` | *unset* | A Python package index that replaces PyPI, such as `https://pypi.example.com/simple`: `https` (plain `http` on the loopback), with no credentials, query or fragment. uv installs from it on the daemonless path, and the generated Dockerfile's `pip install` reads it from the build secret `dexaflow_index_url`. Part of the content label. The build runs on your machine, never on a remote builder. |
| `build.package_index.username_env`, `build.package_index.password_env` | *unset* | The environment variables holding the index user name and password or token. Set both or neither. |
| `build.remote_builder.cloud_build.project` | *unset* | Builds no daemonless path covers (a package without wheels, `system_packages`, a project `Dockerfile`, the dependency image of a split build the wheels cannot build) run on Google Cloud Build in this project, through the `gcloud` CLI signed in on your machine, instead of a local container daemon. `linux/amd64` only. A target's or the workspace's `remote_builder` fills it when the project sets none. Not part of the content label. See [Builders](/operate/builders/#building-on-cloud-build). |
| `build.remote_builder.cloud_build.region` | *unset* | Cloud Build region, such as `us-central1`; gcloud's `builds/region`, else global, when unset. |
| `build.remote_builder.cloud_build.worker_pool` | *unset* | A private pool, `projects/<project>/locations/<region>/workerPools/<name>`. |
| `build.remote_builder.cloud_build.service_account` | *unset* | The account the build runs as, `projects/<project>/serviceAccounts/<email>`; the build then logs to Cloud Logging only. The project's Cloud Build service account when unset. |
| `build.remote_builder.buildkit.namespace` | *unset* | The same builds run on the [in-cluster builder](/operate/builders/#building-on-the-in-cluster-builder) the Helm chart runs in this namespace (`builder.namespace`), with `buildctl` over `kubectl exec` as you: you need both on `PATH` and a binding in the chart's `builder.subjects`. One platform, the builder node's. Set this or `cloud_build`, not both. Over the target's and the workspace's `remote_builder`, and the one the cluster advertises. |
| `build.remote_builder.buildkit.pod_selector` | `app.kubernetes.io/component=builder` | Label selector of the builder pod, `key=value` pairs separated by commas. |
| `build.remote_builder.buildkit.context` | *unset* | The kubeconfig context that reaches the builder's cluster; kubectl's current context when unset. |
| `registry.auth_method` | `"auto"` | Where the CLI reads the registry credential for `deploy`, `compile --push` and every registry read. `auto`: the docker config and its credential helpers, then, for an Artifact Registry, GCR, ECR or ACR host, the session of the `gcloud`, `aws` or `az` CLI, so no separate `docker login` is needed. `docker_config`: the docker config and its credential helpers only. `cloud`: the cloud CLI's session only for a registry a cloud owns (for a docker config entry that has expired), the docker config for any other registry. `env` and `iam` are deprecated and still load, with a warning, and read as `auto`. Changing the value among `auto`, `docker_config` and `cloud` does not change the content label, and a project that sets `env` or `iam` keeps the label it had. See [Registry credentials](/operate/builders/#registry-credentials). |
| `registry.tag_strategy` | `"version"` | How the image tag is picked. `version` and `custom`: the DAG version label, the `--dag-version` you pass or else the default `<git describe>-<content hash>` (see [the version label](/operate/deploy-prerequisites/#8-the-version-label-and-its-409)). `git_sha`: the short commit of the project directory. `git_tag`: the git tag on the project's `HEAD`. `git_sha` and `git_tag` fall back to the version label, with a warning, when there is no commit or no tag on `HEAD`. Characters an image tag cannot carry become `-`. |
| `staging.enabled` | `false` | Opt-in per-run RWX volume — ADR 0022. |
| `min_idle_workers` | `0` | No warm workers are kept for the DAG beyond the operator's `execution.min_idle_workers` floor, and the DAG is not prewarmed. |
| `defaults.*` | *unset* | DAG-level task defaults; layered under task overrides — ADR 0023. |
| `tasks.<id>` | *unset* | Per-task overrides; must reference a `task_id` present in the compiled DAG. |

## Server environment (`DEXAFLOW_*`)

<!-- The tables between BEGIN/END GENERATED markers are written by
`make gen-config-reference` (internal/tools/configref) from internal/config:
edit settings.yaml there or the Go doc comments, not the rows. The prose around
them is hand-written. A test fails when the tables are stale. -->

The tables in this section are generated from the server's configuration
struct and default map in
[`internal/config/server.go`](https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go),
so they list every key the server reads, with its real default. Every `DEXAFLOW_*` variable maps to a
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

The **Edition** column reads `both` (Lite and Cluster), `Cluster` (Cluster / Kubernetes
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

In the tables below, the Variable column tells you which of these two groups a
key is in: a row with a `DEXAFLOW_*` variable binds from that variable (and so
from `extraEnv` or a chart value that sets it); a row that reads _(config file
only)_ (e.g. `auth.oidc.role_mappings`) can only be set in the config file. The
Key column is the same setting's path in that file.

### Server (`server.*`)

<!-- BEGIN GENERATED configref settings server. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_SERVER_ROLE` | `server.role` | string | _(empty)_, means `all` | Cluster | Which components this process runs ([ADR 0049](/project/adrs/0049-split-api-and-scheduler-roles/)): `all` (default: the Lite monolith; every component in one process), `api` (HTTP + UI only, restricted identity), or `scheduler` (reconciler + dispatch + agent gRPC, privileged). Empty defaults to `all`, which is behavior-identical to the pre-split monolith; splitting is a Cluster-only topology. |
| `DEXAFLOW_SERVER_HTTP_ADDR` | `server.http_addr` | string | `0.0.0.0:8080` | both | HTTP/UI listener. |
| `DEXAFLOW_SERVER_METRICS_ADDR` | `server.metrics_addr` | string | `0.0.0.0:9090` | both | Prometheus metrics. |
| `DEXAFLOW_SERVER_GRPC_ADDR` | `server.grpc_addr` | string | `0.0.0.0:9091` | both | Agent gRPC listener. |
| `DEXAFLOW_SERVER_CORS_ALLOWED_ORIGINS` | `server.cors.allowed_origins` | list | `http://localhost:8080` | both | Browser origins allowed to call the API cross-origin (`server.cors.allowed_origins`, a list). The UI is served same-origin with the API, so most deployments need no entry and should leave the server default alone. Comma-separated via the env var; in the chart set `config.cors.allowedOrigins` (a YAML list) and it is rendered comma-joined for you. The chart rejects `"*"` at render time (#1144). |
| `DEXAFLOW_SERVER_TRUSTED_PROXIES` | `server.trusted_proxies` | list | _(empty)_, trusts no proxy | both | Proxy IPs/CIDRs whose `X-Forwarded-For` is honored for the client IP (`server.trusted_proxies`, a list). See note below. |
| `DEXAFLOW_SERVER_GRPC_TLS_CERT` | `server.grpc_tls_cert` | string | _(empty)_ | Cluster | PEM cert enabling TLS on the agent gRPC listener (#58). Set with `_KEY`; empty means plaintext (dev). The Helm chart requires both (see [Cluster TLS](/operate/cluster-tls/)). |
| `DEXAFLOW_SERVER_GRPC_TLS_KEY` | `server.grpc_tls_key` | string | _(empty)_ | Cluster | PEM private key paired with `DEXAFLOW_SERVER_GRPC_TLS_CERT`. Both must be set together to encrypt the agent channel. |
| `DEXAFLOW_SERVER_GZIP_RESPONSES` | `server.gzip_responses` | bool | `false` | both | Gzip JSON and NDJSON responses of 1 KB or more on `/api/v2/*` and `/ui/*` for clients that send `Accept-Encoding: gzip`. Log routes and any response that streams (flushes before 1 KB, such as a live log tail) are never compressed. Clients that list `gzip;q=0` get identity. Only full `200` and `201` bodies are compressed, and every response of an eligible route carries `Vary: Accept-Encoding`. Routes that return secrets, tokens or code (`/api/v2/variables`, `/api/v2/connections`, XCom routes, `/api/v2/auth/`, `/ui/auth/`, `/api/v2/ide/`, `/api/v2/dagSources`, a DAG's `/spec` and a single task's detail, and a single task instance with its rendered fields) are never compressed, because compressing a secret next to attacker-reflected input lets the response length leak it (BREACH). Other JSON may still reflect request input next to private data, so weigh the bandwidth saved against that before turning it on behind a TLS terminator that does not already compress. Off sends every body uncompressed. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_MAX_PAGE_LIMIT` | `server.max_page_limit` | int | `0` | both | Largest `limit` a list endpoint (`/api/v2/*`, `/ui/*`) serves, and the largest `dag_runs_limit` of `/ui/dags`; a larger request is served this many rows, like Airflow's `[api] maximum_page_limit`. `0` means no cap; a negative value fails boot. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_MAX_REQUEST_BODY_BYTES` | `server.max_request_body_bytes` | int | `0` | both | Largest request body, in bytes, the API and UI listener accepts; a larger one is refused with `413` and an RFC 7807 problem. The routes that take a DAG's code or spec (registering a version, its dry run, writing an IDE file) use `server.max_upload_body_bytes` instead. `0` means no limit; a negative value fails boot. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_MAX_UPLOAD_BODY_BYTES` | `server.max_upload_body_bytes` | int | `0` | both | Largest request body, in bytes, of the routes that take a DAG's code or spec (`POST /api/v2/dags/{dag_id}/versions`, its `dry_run`, and `PUT` or `POST /api/v2/ide/file`); a larger one is refused with `413`. `0` leaves those routes unlimited even when `server.max_request_body_bytes` is set, so a limit sized for JSON calls never refuses a deploy; a negative value fails boot. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_READ_TIMEOUT` | `server.read_timeout` | duration | `0s` | both | Longest time the API and metrics listeners spend reading one request, headers and body (a Go duration such as `60s`). Protects against clients that trickle a body to hold connections. Set it above your slowest legitimate upload. It never limits a response, so live log tails keep streaming. Headers must always arrive within 10 seconds, or within this timeout when it is shorter. `0s` means no limit. Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_IDLE_TIMEOUT` | `server.idle_timeout` | duration | `0s` | both | Closes a keep-alive connection idle this long (a Go duration such as `120s`). `0s` keeps idle connections open, also when `DEXAFLOW_SERVER_READ_TIMEOUT` is set (the read timeout is never used as an idle timeout). Helm: set it through `extraEnv`. |
| `DEXAFLOW_SERVER_PROBE_BUDGET` | `server.probe_budget` | duration | `2s` | both | How long `/readyz` may spend checking its dependencies, all checks sharing one deadline; past it the probe answers `503` naming the slow dependency. It must stay under the kubelet's readiness timeout, so the Helm chart sets it to `probes.readiness.timeoutSeconds` minus one second: raise that value to give a slow metadatabase more room. `0` means `2s`; a negative value fails boot. |
| `DEXAFLOW_SERVER_HTTP_TLS_CERT` | `server.http_tls_cert` | string | _(empty)_ | both | Path to a PEM certificate (chain) that makes the API and UI listener (`server.http_tls_cert`, on `DEXAFLOW_SERVER_HTTP_ADDR`) serve HTTPS instead of plain HTTP, so a load balancer can re-encrypt to the pod (#638). Set with `_KEY`; setting only one of the two fails boot. Empty (the default) keeps plain HTTP. The files are re-read when they change on disk, so a rotated certificate is served without a restart. TLS 1.2 is the minimum. The metrics listener stays plain HTTP. In the chart set `httpTLS.enabled` and `httpTLS.secretName`. See [In-transit encryption](/operate/in-transit-encryption/). |
| `DEXAFLOW_SERVER_HTTP_TLS_KEY` | `server.http_tls_key` | string | _(empty)_ | both | Path to the PEM private key paired with `DEXAFLOW_SERVER_HTTP_TLS_CERT` (`server.http_tls_key`). Both must be set together. |
| `DEXAFLOW_SERVER_POOLS_READ_ONLY` | `server.pools_read_only` | bool | `false` | Cluster | Makes the tenant-facing pool API (`/api/v2/pools`) read-only (`server.pools_read_only`). Create, resize and delete answer `403` with the detail `pools are read-only on this server: their slots are managed by the platform operator` for every role, tenant `admin` included, while list and get keep working. Turn it on when one engine serves many tenants and the platform operator sizes each tenant's pools: a tenant `operator` holds `write:pool`, and a tenant `admin` can grant itself anything, so a lock that spared admins would not hold a slot budget. The platform then changes pools out of band, not through this API. With the option on, a task that names a pool its tenant has not defined is also admitted against that tenant's `default_pool` instead of running unlimited, and the Pools screen counts it there; existing DAGs using such pool names start sharing `default_pool`. A tenant with no `default_pool` row stays unlimited (the gate never deadlocks). Set it on every role: with split API and scheduler roles, the API enforces the lock and the scheduler the `default_pool` fallback (the chart sets both). The platform sizes `default_pool` through the service API (`default_pool_slots`); other named pools have no platform API yet. Default `false` keeps pools writable under `write:pool`. In the chart set `config.poolsReadOnly`. |

<!-- END GENERATED configref -->

### Database (`database.*`)

<!-- BEGIN GENERATED configref settings database. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_DATABASE_URL` | `database.url` | string | `postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable` | both | Postgres DSN. On Lite, `dexaflow lite` sets it for the server to Lite's own database and ignores one exported in the shell: Lite does not run on another database, and `dexaflow db reset` refuses while it names one. |
| `DEXAFLOW_DATABASE_MAX_OPEN_CONNS` | `database.max_open_conns` | int | `25` | both | Max open connections in the Postgres pool. |
| `DEXAFLOW_DATABASE_MAX_IDLE_CONNS` | `database.max_idle_conns` | int | `5` | both | Max idle connections retained in the pool. |
| `DEXAFLOW_DATABASE_SCHEDULER_MAX_CONNS` | `database.scheduler_max_conns` | int | `0` | both | Size of a dedicated pool for the scheduler loop, its reapers and its janitors (`database.scheduler_max_conns`; Helm `database.schedulerMaxConns`), so API traffic that holds every main pool connection cannot stall a scheduler tick. It is opened in addition to the main pool, only by a process that runs the scheduler (`DEXAFLOW_SCHEDULER_ENABLED`). `0` keeps them on the main pool. |
| `DEXAFLOW_DATABASE_STATEMENT_TIMEOUT_MS` | `database.statement_timeout_ms` | int | `0` | both | `statement_timeout`, in milliseconds, for every connection of the main pool, which serves the API (`database.statement_timeout_ms`; Helm `database.statementTimeoutMs`). About `30000` bounds a runaway API query without cutting legitimate ones. Never applied to the leader election connection (its session holds the scheduler advisory lock), the health checks, the scheduler pool or migrations. Deleting a DAG, clearing its history and the XCom janitor lift it with `SET LOCAL statement_timeout = 0` for their own transaction, since their cost grows with the data they cascade over. Without `DEXAFLOW_DATABASE_SCHEDULER_MAX_CONNS` the scheduler shares the main pool, and with it this timeout, so set both together. The server applies it with `SET` as each connection opens, not as a startup parameter, so it also works through PgBouncer in session mode (no `ignore_startup_parameters` entry needed); in transaction mode a session `SET` does not stay on one server connection, so set it on the role instead (`ALTER ROLE ... SET statement_timeout`) and leave this at `0`. `0` sets none. |
| `DEXAFLOW_DATABASE_CONN_MAX_LIFETIME_JITTER_MS` | `database.conn_max_lifetime_jitter_ms` | int | `0` | both | Up to this many milliseconds of random extra lifetime per pooled connection (`database.conn_max_lifetime_jitter_ms`; Helm `database.connMaxLifetimeJitterMs`), so replicas started together do not all reconnect at the same moment. Applies to the main, scheduler and health pools, never to the leader election connection. `0` adds none. |
| `DEXAFLOW_DATABASE_INSTALLATION_ID` | `database.installation_id` | string | _(empty)_ | **Cluster only** | The installation id of the database this release was installed on ([ADR 0090](/project/adrs/0090-database-replaced-under-a-running-release/), decision 3). The Helm chart sets it from the `<fullname>-installation` ConfigMap that the migration Job keeps; do not set it by hand. When set, the server serves only a database that carries this id: one with another id, none, or no schema is reported as `database replaced`, boot waits not ready on it, and a running server exits with code 3 when its database changes under it. Empty checks nothing. |
| `DEXAFLOW_DATABASE_MIGRATE_ON_BOOT` | `database.migrate_on_boot` | bool | `false` | **Cluster only** | Apply the embedded migrations at boot when the schema is absent or behind (Helm `migrations.onBoot`, [ADR 0090](/project/adrs/0090-database-replaced-under-a-running-release/), decision 4), while the probes already answer. Never when the schema is ahead or dirty, or when the database is reported as `database replaced`. It needs a `database.url` that may run DDL; golang-migrate's advisory lock serializes it with the migration Job and other replicas. Refused on the `api` role, which never migrates. `false` leaves migrations to the chart's Job. |

<!-- END GENERATED configref -->

### Helm release (`helm.*`)

The chart sets these on every control plane pod. They only fill in the
recovery command a pod waiting for its schema names; see
[a control plane that is up but not ready](/operate/troubleshooting/#a-control-plane-that-is-up-but-not-ready).

<!-- BEGIN GENERATED configref settings helm. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_HELM_RELEASE` | `helm.release` | string | _(empty)_ | **Cluster only** | The Helm release name. The chart sets it, with the four settings below, so that a control plane waiting for its schema names the exact command that migrates this release on `/readyz` and in its log ([ADR 0090](/project/adrs/0090-database-replaced-under-a-running-release/), decision 2). Nothing else reads it; do not set it by hand. Empty keeps the hint with `<release>`, `<namespace>` and `<chart version>` placeholders. |
| `DEXAFLOW_HELM_NAMESPACE` | `helm.namespace` | string | _(empty)_ | **Cluster only** | The release namespace, set by the chart for the recovery hint. |
| `DEXAFLOW_HELM_CHART_VERSION` | `helm.chart_version` | string | _(empty)_ | **Cluster only** | The version of the chart that rendered the pod, set by the chart, so the hint's `helm upgrade --version` reruns the same chart. |
| `DEXAFLOW_HELM_MIGRATIONS_ENABLED` | `helm.migrations_enabled` | bool | `true` | **Cluster only** | The chart's `migrations.enabled`, set by the chart. When `false` there is no migration Job to run again, and the hint names your own migration step and the migrate image instead of `helm upgrade`. |
| `DEXAFLOW_HELM_MIGRATE_IMAGE` | `helm.migrate_image` | string | _(empty)_ | **Cluster only** | The `dexaflow-migrate` image and tag of this release (`migrations.image`), set by the chart and named by the hint when `migrations.enabled` is false. |

<!-- END GENERATED configref -->

### Redis (`redis.*`)

<!-- BEGIN GENERATED configref settings redis. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_REDIS_URL` | `redis.url` | string | _(empty)_ | **Cluster only** | Redis URL (XCom + locks). Empty selects the embedded Lite edition: XCom on Postgres, in-process log tailer ([ADR 0026](/project/adrs/0026-lite-datastore-no-redis/)). Cluster sets it via the Helm chart. |
| `DEXAFLOW_REDIS_CA_FILE` | `redis.ca_file` | string | _(empty)_ | Cluster | Absolute path to a PEM CA bundle trusted when negotiating TLS to a `rediss://` URL (#312). Needed for managed Redis (Memorystore, ElastiCache in-transit encryption, Azure Cache) whose server cert is signed by a provider/per-instance CA not in the container's system roots. Empty falls back to system roots only. The Helm chart sets it when `redis.caConfigMap` is configured. |

<!-- END GENERATED configref -->

### Auth (`auth.*`)

<!-- BEGIN GENERATED configref settings auth. !auth.oidc. secret_key secret_key_reencrypt_on_boot secret_key_migration_lock variable_encryption -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_AUTH_PROVIDER` | `auth.provider` | string | `jwt` | both | Credential authenticator: `jwt` (default: username/password issues an HS256 token) or `oidc` (adds the OIDC/SSO login flow on top; the JWT authenticator stays the request-path verifier in both modes). `oidc` is Cluster-gated and fails boot closed unless its prerequisites are met (see [OIDC / SSO](#oidc--sso-authoidc)). |
| `DEXAFLOW_AUTH_JWT_SECRET` | `auth.jwt.secret` | string | _(empty)_, required | both | Signs API/agent tokens. Required for both `jwt` and `oidc` (both mint the app's own HS256 token). |
| `DEXAFLOW_AUTH_JWT_TOKEN_TTL_SECONDS` | `auth.jwt.token_ttl_seconds` | int | `3600` | both | Lifetime, in seconds, of an issued API token. The web UI renews its session every 15 minutes and when its tab is shown again, so a browser session with the UI open lasts up to `auth.jwt.max_lifetime_seconds`. |
| `DEXAFLOW_AUTH_JWT_MAX_LIFETIME_SECONDS` | `auth.jwt.max_lifetime_seconds` | int | `86400` | both | Ceiling, in seconds, on the **total** age of a transparently renewed session, measured from first login and preserved across every renewal. Past it, `POST /api/v2/auth/token/renew` refuses and the user must log in again; the short `TOKEN_TTL_SECONDS` is what bounds a stolen token, this only caps how long a live session may keep refreshing. A non-positive value disables the ceiling. Renewal also re-checks that the account is still active, so a deactivated user stops being issued tokens as well as being refused on use. The renew endpoint is rate-limited to 60 requests a minute per client IP, on a budget separate from login's. Helm: `auth.sessionMaxLifetimeSeconds`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_NAME` | `auth.trusted_issuer.name` | string | _(empty)_ | both | Name of the trusted issuer, 1-40 lowercase letters, digits or `-`. Users the issuer may sign in are linked under `issuer:<name>`, so keep it stable once users exist. Helm: `auth.trustedIssuer.name`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_ISSUER` | `auth.trusted_issuer.issuer` | string | _(empty)_ | both | Turns on the [trusted-issuer handoff](#trusted-issuer-handoff): a platform that already authenticates its users opens a UI session for them by posting a token its own issuer signed. The exact `iss` of those tokens. Empty disables it and the endpoint does not exist. Helm: `auth.trustedIssuer.issuer`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_JWKS_URL` | `auth.trusted_issuer.jwks_url` | string | _(empty)_ | both | Where the issuer publishes its public signing keys (RS256, ES256 or PS256). `https`, or `http` on a loopback host. Fetched on first use and refreshed when a token names an unknown key (at most once every 30 seconds), so key rotation needs no restart and an outage of the issuer does not block boot. Helm: `auth.trustedIssuer.jwksUrl`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_AUDIENCE` | `auth.trusted_issuer.audience` | string | _(empty)_ | both | The `aud` the issuer's tokens must carry for this Dexaflow. Helm: `auth.trustedIssuer.audience`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_TENANT_CLAIM` | `auth.trusted_issuer.tenant_claim` | string | `tenant_id` | both | The string claim that names the Dexaflow tenant. Helm: `auth.trustedIssuer.tenantClaim`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_ALLOWED_TENANTS` | `auth.trusted_issuer.allowed_tenants` | list | _(empty)_ | both | Comma-separated tenants the issuer may sign in to; `*` allows every tenant, for an operator that serves many. Required when the issuer is set. Helm: `auth.trustedIssuer.allowedTenants`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_MAX_LIFETIME_SECONDS` | `auth.trusted_issuer.max_lifetime_seconds` | int | `0` | both | Longest `exp - iat` a handoff token may have, its replay window. `0` uses 120 seconds; at most 600. Helm: `auth.trustedIssuer.maxLifetimeSeconds`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_ALLOWED_ORIGINS` | `auth.trusted_issuer.allowed_origins` | list | _(empty)_ | both | Comma-separated origins (`scheme://host[:port]`, no path) whose pages may post a handoff, typically your portal. A post with any other `Origin`, or none, is refused with `403`, so another site cannot sign a visitor in as someone else. Required when the issuer is set. Helm: `auth.trustedIssuer.allowedOrigins`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_BEARER_AUDIENCES` | `auth.trusted_issuer.bearer_audiences` | list | _(empty)_ | both | Comma-separated audiences whose tokens from the trusted issuer are accepted as the bearer of any `/api/v2` request, reused until they expire: the [bearer mode](#trusted-issuer-bearer-tokens), for remote MCP clients behind your platform (`leoflow-mcp` is the conventional audience). Each must differ from `DEXAFLOW_AUTH_TRUSTED_ISSUER_AUDIENCE`. Empty leaves the mode off. Helm: `auth.trustedIssuer.bearerAudiences`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_BEARER_MAX_LIFETIME_SECONDS` | `auth.trusted_issuer.bearer_max_lifetime_seconds` | int | `0` | both | Longest `exp - iat` a bearer token may have. `0` uses 900 seconds; at most 3600. Helm: `auth.trustedIssuer.bearerMaxLifetimeSeconds`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_DEPLOY_AUDIENCE` | `auth.trusted_issuer.deploy_audience` | string | _(empty)_ | both | Makes the trusted issuer the only way to deploy: registering a DAG version, its dry run, setting the current version and deleting a DAG then take only a bearer the issuer minted for this audience alone with the `dexaflow:deploy` scope. See [Deploys through the issuer only](#deploys-through-the-issuer-only). Must differ from `DEXAFLOW_AUTH_TRUSTED_ISSUER_AUDIENCE` and every bearer audience; boot fails otherwise, or when the issuer is not set. Empty changes nothing. Helm: `auth.trustedIssuer.deployAudience`. |
| `DEXAFLOW_AUTH_TRUSTED_ISSUER_HOST_PATTERN` | `auth.trusted_issuer.host_pattern` | string | _(empty)_ | both | Binds every browser session to its tenant's host, for an operator that serves each tenant on its own host in front of one Dexaflow, for example `{tenant}.example.com`. A handoff for a tenant is accepted only on that tenant's host, and a session cookie only on the host of the session's tenant. See [Binding sessions to tenant hosts](#binding-sessions-to-tenant-hosts). A host and optional port with exactly one `{tenant}`; boot fails otherwise, or when the issuer is not set. Empty binds nothing. Helm: `auth.trustedIssuer.hostPattern`. |
| `DEXAFLOW_AUTH_CI_OIDC_SESSION_TTL_SECONDS` | `auth.ci_oidc.session_ttl_seconds` | int | `3600` | both | How long the session a CI token is exchanged for lives, 300 to 43200 seconds. It is not renewed: a later job exchanges again. Read only when an issuer is configured. Helm: `auth.ciOidc.sessionTtlSeconds`. |
| `DEXAFLOW_AUTH_CI_OIDC_ISSUERS` | `auth.ci_oidc.issuers` | list | _(empty)_ | both | The CI OIDC issuers whose job tokens a pipeline exchanges for a deploy session, so it stores no deploy token ([ADR 0092](/project/adrs/0092-ci-deploys-with-oidc-tokens/)). Each entry has a `name`, a `provider` (`github`, `gitlab`, `cloudbuild` or `other`), the exact `issuer`, an optional `jwks_url` (discovered otherwise), the `audience` its tokens carry, the one `tenant` it deploys to, an optional `max_lifetime_seconds` (0 means 3600, at most 86400) and ordered `rules`, each mapping `claims` patterns to a `role` and optional `scopes`. The tenant never comes from a claim. In the configuration file, or as a JSON array of the same objects in this variable, which wins over the file. Empty (the default) leaves the exchange off. See [CI OIDC](/operate/cicd-deploy/#deploying-without-a-stored-token-ci-oidc). Helm: `auth.ciOidc.issuers`. |
| `DEXAFLOW_AUTH_API_TOKENS_DISABLED` | `auth.api_tokens.disabled` | bool | `false` | both | `true` stops serving the [API tokens](/operate/cicd-deploy/#credentials-for-the-control-plane) a signed-in user mints for CI and scripts: `/api/v2/apiTokens` and the tokens screen are not served, and every API token is refused as an unknown bearer, including one minted before (it works again if they are served again before it expires). Turn it on when a platform in front of Dexaflow decides at each sign-in who may reach it, so no Dexaflow credential outlives that decision. `false` (the default) serves them. Helm: `auth.apiTokens.disabled`. |
| `DEXAFLOW_AUTH_SERVICE_TOKEN` | `auth.service_token` | string | _(empty)_ | both | Turns on the [operator service API](#operator-service-api) under `/api/v2/service/` and is its bearer credential. At least 32 characters; boot fails on a shorter one. Keep it in a Secret. Empty leaves the API off and its routes absent. Helm: `auth.serviceToken`, or `auth.serviceTokenExistingSecret` naming a Secret with key `serviceToken`. |
| `DEXAFLOW_AUTH_EXTERNAL_SIGNIN_URL` | `auth.external_signin_url` | string | _(empty)_ | both | Sends UI visitors without a session to your own sign-in instead of Dexaflow's page, for a Dexaflow served from a larger platform. The page they asked for travels in a `next` query parameter (a same-origin path, `/` when the request carried anything else), added to whatever query your URL already has; your flow is expected to return them with a Dexaflow session. A refused [trusted-issuer handoff](#trusted-issuer-handoff) is also sent back here, with an `error` code. API calls without a session still get `401`. `/api/v2/auth/login?local=1` and a refused single sign-on still render Dexaflow's page, so break-glass access survives an outage of your sign-in. Absolute `http(s)` URL; boot fails otherwise. Helm: `auth.externalSigninUrl`. |
| `DEXAFLOW_AUTH_EXTERNAL_SIGNOUT_URL` | `auth.external_signout_url` | string | _(empty)_ | both | Where `/api/v2/auth/logout` lands after clearing the session cookie, so your platform can end its own session too. Empty returns to Dexaflow's sign-in page. Absolute `http(s)` URL; boot fails otherwise. Helm: `auth.externalSignoutUrl`. |
| `DEXAFLOW_AUTH_DEV_NO_AUTH` | `auth.dev_no_auth` | bool | `false` | dev-only | Legacy escape hatch: bypasses auth entirely, treating every request as admin. Permitted only on a loopback `http_addr` (boot fails otherwise). Modern Lite uses a real admin login generated by `dexaflow setup`; set this only for ephemeral test scaffolds. |
| `DEXAFLOW_AUTH_LOGIN_RATE_LIMIT_PER_MINUTE` | `auth.login_rate_limit_per_minute` | int | `5` | both | Cap on **failed** `/auth/token` attempts per client IP per minute (anti-brute-force). A successful login consumes no budget. `dexaflow lite` raises this well above the default (local single-user tool). |
| `DEXAFLOW_AUTH_SECRET_SCOPING` | `auth.secret_scoping` | string | `permissive` | both | Scope-by-declaration policy ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `permissive` (delivers the whole tenant vault; warns when a DAG declares a narrower set, an empty one included), `enforce` (delivers only the declared subset: empty declaration ⇒ nothing), or `off` (no scoping). Operator-scoped, never author-settable. Helm: `auth.secretScoping`. |
| `DEXAFLOW_AUTH_SECRET_LIVENESS_MODE` | `auth.secret_liveness_mode` | string | `observe` | both | Gates secret delivery on task-instance liveness ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `observe` (logs + audits a would-have-denied when the caller's task instance is not live, but still delivers) or `enforce` (denies). Liveness renewal is always on regardless of mode; this only chooses whether a not-live token is refused. Required to be `enforce` when warm pools are on. Helm: `auth.secretLivenessMode`. |
| `DEXAFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME` | `auth.max_attempt_credential_lifetime` | duration | `24h` | both | Duration ceiling on how long one attempt's agent credential may be kept alive by heartbeat renewal ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)). A runaway-task backstop; the short per-attempt TTL is what bounds a stolen token. An attempt still running past it fails as a task failure with `credential_ceiling` (subject to its retries) instead of being re-placed as `agent_lost` ([#1461](https://github.com/dexadata/dexaflow/issues/1461)); in Lite the reaper also stops its task at the ceiling ([#1511](https://github.com/dexadata/dexaflow/issues/1511)). A non-positive value disables the ceiling. No Helm value yet; `extraEnv` only ([#955](https://github.com/dexadata/dexaflow/issues/955)). |
| `DEXAFLOW_AUTH_AGENT_TOKEN_TRANSPORT` | `auth.agent_token_transport` | string | `envvar` | Cluster (K8s) | How the in-pod agent obtains its control-plane bearer credential ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)): `envvar` (plaintext `DEXAFLOW_AGENT_TOKEN` on the pod spec: today's behavior, byte-identical) or `exchange` (projected ServiceAccount token exchanged once via a control-plane `TokenReview` for a task-scoped JWT: nothing secret on the pod object; requires cluster-scoped `create` on `authentication.k8s.io/tokenreviews`). Operator-scoped. Prerequisite for warm pools. Ignored by the subprocess (Lite) executor. See [Agent credential transport](/operate/agent-credential-transport/). Helm: `auth.agentTokenTransport`. |
| `DEXAFLOW_AUTH_SESSION_COOKIE_INSECURE` | `auth.session_cookie_insecure` | bool | `false` | both | Drops the `Secure` attribute from the browser session cookie (`_token`) and the OIDC state cookie. Leave it off. Both login paths set the session cookie server-side, `HttpOnly`, `SameSite=Lax`, `Secure`, so the session token is never readable by a script. There is one reason to turn it on: a deployment served over **plain http to something that is not a loopback address**, where the browser refuses a `Secure` cookie outright and the sign-in page would post valid credentials, get a `200`, and land back on itself with no error anywhere. A loopback deployment (`localhost`, `127.0.0.1`) needs nothing: browsers treat it as trustworthy and accept the cookie over http. It cannot be derived from the request (behind a TLS-terminating ingress the server sees plain http while the browser sees https), so it is a setting, and boot logs a `WARN` while it is on. Operator-scoped. No Helm value on purpose: a chart install terminates TLS at the ingress, where this must stay off. `extraEnv` if a deployment genuinely needs it. **Set this before upgrading a plain-http deployment on a non-loopback name.** The browser refuses a `Secure` cookie there and refuses the `Secure` deletion too, so a new login is discarded and sign-out cannot clear the session the previous build left behind until it expires on its own. |
| `DEXAFLOW_AUTH_SESSION_COOKIE_NAME` | `auth.session_cookie_name` | string | `_token` | both | Name of the browser session cookie. Set it to a name starting with `__Host-` (for example `__Host-dexaflow`) on any deployment that serves several hosts under one parent domain, such as one host per tenant (`<tenant>.example.com`): a browser then accepts the cookie only when it is `Secure`, has `Path=/` and no `Domain`, so a page on a sibling host can no longer plant a session on this one (session fixation). The session cookie is always set that way, so the prefix needs nothing else. Boot fails on a name a browser would not accept, and on a `__Host-` or `__Secure-` name together with `DEXAFLOW_AUTH_SESSION_COOKIE_INSECURE`. Changing the name signs every browser out once: sessions under the old name are no longer read, and that cookie expires on its own. The bundled UI never reads the cookie from a script, so it works under any name. Helm: `auth.sessionCookieName`. |
| `DEXAFLOW_SECRET_KEY` | `secret_key` | string | _(empty)_ | both | Key encrypting connection secrets at rest ([ADR 0019](/project/adrs/0019-secret-encryption-at-rest/)), and Variable values when `DEXAFLOW_VARIABLE_ENCRYPTION=on`. Raw 32 chars, 64-char hex, or base64. Empty disables connection writes. Accepts a **comma-separated list to rotate**: the first entry encrypts and decrypts, later entries only decrypt, and nothing is ever written under them. Same rule as Airflow's `fernet_key`. See [Rotating the encryption key](#rotating-the-encryption-key). |
| `DEXAFLOW_SECRET_KEY_REENCRYPT_ON_BOOT` | `secret_key_reencrypt_on_boot` | bool | `true` | both | Re-encrypt stored connection secrets onto the first `DEXAFLOW_SECRET_KEY` entry at startup ([Rotating the encryption key](#rotating-the-encryption-key)). `dexaflow lite` sets it to `false`: Lite moves keys only through [`dexaflow lite migrate-key`](/reference/cli/dexaflow_lite_migrate-key/). |
| `DEXAFLOW_SECRET_KEY_MIGRATION_LOCK` | `secret_key_migration_lock` | bool | `false` | both | Hold the key-migration advisory lock for the server's lifetime: refuse to start while `dexaflow lite migrate-key` runs, and exit if the lock's database session is lost. `dexaflow lite` sets it to `true`; there is no reason to set it elsewhere. |
| `DEXAFLOW_VARIABLE_ENCRYPTION` | `variable_encryption` | string | `off` | both | How Variable values are stored. `off` writes plaintext, as every earlier release did. `on` encrypts every Variable value with `DEXAFLOW_SECRET_KEY` (AES-256-GCM, the cipher connections use) and encrypts the stored plaintext rows at startup. `decrypt` writes plaintext and turns every encrypted row back into plaintext at startup, the step before rolling back to a release older than 0.5.4. `on` and `decrypt` refuse to start without a usable key. Every mode reads both forms. Config key `variable_encryption`. See [Encrypting Variables](#encrypting-variables). |

<!-- END GENERATED configref -->

### OIDC / SSO (`auth.oidc.*`)

Read only when `DEXAFLOW_AUTH_PROVIDER=oidc`, which is Cluster-gated (`ui.edition:
cluster`) and fails boot closed unless `issuer`, `client_id`, `redirect_url` **and
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

<!-- BEGIN GENERATED configref settings auth.oidc. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_AUTH_OIDC_ISSUER` | `auth.oidc.issuer` | string | _(empty)_ | Cluster | The org's single-tenant issuer URL (must be `https://`). Pinned: any ID token whose `iss` differs is rejected. |
| `DEXAFLOW_AUTH_OIDC_CLIENT_ID` | `auth.oidc.client_id` | string | _(empty)_ | Cluster | Registered application (client) id; the expected audience of every ID token. |
| `DEXAFLOW_AUTH_OIDC_CLIENT_SECRET` | `auth.oidc.client_secret` | string | _(empty)_ | Cluster | Used only for the authorization-code exchange. Inject via env; never persist it in a config file, never logged. |
| `DEXAFLOW_AUTH_OIDC_REDIRECT_URL` | `auth.oidc.redirect_url` | string | _(empty)_ | Cluster | This server's callback URL registered with the IdP (`…/api/v2/auth/oidc/callback`). Must be `https://` (http allowed only for loopback hosts). |
| `DEXAFLOW_AUTH_OIDC_SCOPES` | `auth.oidc.scopes` | list | `openid,email,profile` | Cluster | OAuth scopes requested. A list, set as a comma-separated env var. Add the IdP's groups scope when group→role mapping is used. |
| `DEXAFLOW_AUTH_OIDC_GROUPS_CLAIM` | `auth.oidc.groups_claim` | string | `groups` | Cluster | The ID-token claim carrying the user's IdP groups; its values drive `role_mappings`. |
| _(config file only)_ | `auth.oidc.role_mappings` | map | _(empty)_ | Cluster | Maps an IdP group value → an existing Dexaflow role name. **Default-DENY**: an unmapped group grants no role. YAML config file only (a map does not bind from an env var). Helm: `auth.oidc.roleMappings`, rendered into the mounted config file. Reconciliation is IdP-authoritative, so an EMPTY resolved set CLEARS the user's existing grants on every login: configure this or `default_role`. |
| `DEXAFLOW_AUTH_OIDC_DEFAULT_ROLE` | `auth.oidc.default_role` | string | _(empty)_ | Cluster | When an authenticated user resolves to zero mapped roles and this is set, grants this single role (advised: a read-only role such as `viewer`). Empty keeps strict default-deny. Must name an existing DB role for the resolved tenant. |
| `DEXAFLOW_AUTH_OIDC_TENANT_CLAIM` | `auth.oidc.tenant_claim` | string | _(empty)_ | Cluster | **Required with `provider: oidc`** (boot fails otherwise). Which IdP claim identifies the tenant: `tid` (Entra) or `hd` (Google Workspace). Set it to `hd` with exactly one entry in `tenant_claims` and the login redirect also carries Google's `hd` parameter, so the account chooser offers only accounts in that domain. That is a convenience: the pin is still the verified claim on the returned token. |
| _(config file only)_ | `auth.oidc.tenant_claims` | map | _(empty)_ | Cluster | **Required with `provider: oidc`, with at least one entry** (boot fails otherwise). Maps a `tenant_claim` value → a Dexaflow tenant name. A value not present is rejected and the login never falls back to `default`. The claim may be a string or an array of strings (some IdPs emit `aud` as an array); an array naming two accepted tenants is rejected as ambiguous rather than resolved to either, and a claim that is neither shape is rejected with its own audit reason. Config file only (a map does not bind from an env var), read from the path in `DEXAFLOW_CONFIG`. Helm: `auth.oidc.tenantClaim` + `auth.oidc.tenantClaims`, which the chart requires together before it will render an SSO install. **The value must name a tenant that already exists**: the only tenant anything in Dexaflow creates is `default`, from the first migration, so map to `default` unless you created one yourself. The server checks this at boot and warns. |
| `DEXAFLOW_AUTH_OIDC_ALLOWED_EMAIL_DOMAINS` | `auth.oidc.allowed_email_domains` | list | _(empty)_ | Cluster | Login-level allowlist layered on TOP of the `tid`/`hd` tenant pin (not the pin itself). Empty imposes no domain restriction. Non-empty admits a login only when the verified email's domain is in the list. A list, set as a comma-separated env var. |
| `DEXAFLOW_AUTH_OIDC_BREAK_GLASS_EMAILS` | `auth.oidc.break_glass_emails` | list | _(empty)_ | Cluster | Allowlist of local password logins permitted while provider is `oidc`; every other password login is rejected (SSO-only). A list, set as a comma-separated env var. **Empty means an IdP outage or a wrong tenant pin locks everyone out**, including whoever has to fix it; the server warns at boot. It also warns when none of the listed addresses has a **local password account**, which is the worse case: the allowlist admits the address and the credential store then rejects it exactly like a wrong password, so the hatch does not open while you believe it will. A user provisioned through SSO does not count, it has no password. Create the local account before you need it, while an admin session still exists. |
| `DEXAFLOW_AUTH_OIDC_JIT_PROVISIONING` | `auth.oidc.jit_provisioning` | bool | `false` | Cluster | Create a user row on first OIDC login when none matches; the new row is granted the roles from `role_mappings` (or `default_role`). **Off means no SSO login can succeed**: a login is matched by `(oidc_provider, oidc_subject)` and JIT is the only path that ever writes those columns, so there is no supported way to pre-provision a matching account and every first login is denied (audited `no_user_jit_off`). The Helm chart therefore defaults `auth.oidc.jitProvisioning` to `true`. An address that already has a local password account in the same tenant cannot be provisioned either way (unique `(tenant, email)`; audited `jit_failed`). The server logs a WARN at boot when it is off, so the cause is visible before the first login is attempted. |
| `DEXAFLOW_AUTH_OIDC_AUTO_REDIRECT` | `auth.oidc.auto_redirect` | bool | `false` | Cluster | Start the login flow on the sign-in page instead of rendering it, for a deployment where that page is a screen to acknowledge for nothing (an edge proxy already authenticated the session, or SSO is the only way in). **Suppressed on a refused sign-on**, so a denial lands on the page that explains it rather than bouncing back to the IdP forever, and suppressed by `?local=1`, so a break-glass account can always reach the password form without a values edit. Helm: `auth.oidc.autoRedirect`. |
| `DEXAFLOW_AUTH_OIDC_CLOCK_SKEW_SECONDS` | `auth.oidc.clock_skew_seconds` | int | `60` | Cluster | Tolerance (seconds) on the ID token's `exp`/`iat`/`nbf` checks to absorb clock differences between the IdP and this server. |

<!-- END GENERATED configref -->

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

### Bootstrap admin (`bootstrap.*`)

The admin user the server creates in tenant `default` at boot, or reconciles
when a password hash is set. Nothing happens while both credentials are
empty. The credentials are never logged.

<!-- BEGIN GENERATED configref settings bootstrap. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_BOOTSTRAP_EMAIL` | `bootstrap.email` | string | _(empty)_, means `admin@dexaflow.local` | both | Email of the admin user created at boot from `DEXAFLOW_BOOTSTRAP_PASSWORD` or `DEXAFLOW_BOOTSTRAP_PASSWORD_HASH`, in tenant `default`. Left empty, an install whose admin is the pre-rename `admin@leoflow.local` keeps that address. With a credential set, boot fails on an address with a space or a control character. |
| `DEXAFLOW_BOOTSTRAP_PASSWORD` | `bootstrap.password` | string | _(empty)_ | both | Plaintext password of the bootstrap admin, used when `DEXAFLOW_BOOTSTRAP_PASSWORD_HASH` is empty. Unset, no admin is created. Prefer the hash. Boot fails on a password longer than the 72 bytes bcrypt accepts. Never logged. Helm: `bootstrap.password` or `bootstrap.existingSecret`. |
| `DEXAFLOW_BOOTSTRAP_PASSWORD_HASH` | `bootstrap.password_hash` | string | _(empty)_ | both | Bcrypt hash of the bootstrap admin's password, so the plaintext never reaches the server. Takes precedence over `DEXAFLOW_BOOTSTRAP_PASSWORD` and reconciles: an existing admin with that email gets this password on every boot. Boot fails on a value that is not a bcrypt hash. Never logged. `dexaflow lite` sets it from its config file. |

<!-- END GENERATED configref -->

### Agent channel (`agent.*`)

<!-- BEGIN GENERATED configref settings agent. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_AGENT_ALLOW_INSECURE_SECRETS` | `agent.allow_insecure_secrets` | bool | `false` | dev-only | `true` lets the server deliver secrets to agents over a plaintext gRPC channel. Dev only: `dexaflow lite` sets it for its loopback agents, and the cluster edition refuses to boot with it. As in earlier releases, only the exact value `true` turns it on: any other value from the environment (`1`, `TRUE`, `yes`) reads as `false`, with a warning at boot. No Helm value on purpose. |

<!-- END GENERATED configref -->

### DAG registration

<!-- BEGIN GENERATED configref settings dags_are_paused_at_creation deploy.require_digest -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_DEPLOY_REQUIRE_DIGEST` | `deploy.require_digest` | string | `off` | both | What the control plane does with an image that is not pinned by a full sha256 digest (`<repository>@sha256:<64 hex>`), when a version is registered and when one is made current again (`dexaflow rollback`). `off` accepts any image, as before. `warn` accepts it with a warning in the answer (printed by `dexaflow push`, `dexaflow deploy` and `dexaflow rollback`), logs it, counts it in `dexaflow_unpinned_image_total{stage,outcome="warned"}` and records `require_digest` and `image_pinned` in the `dag.version.register` or `dag.version.set_current` audit entry. `enforce` refuses it with a 400 that names this setting and the way out, counted with `outcome="refused"`. The dry run of a register gives the same answer and records nothing. `dexaflow deploy`, and `dexaflow compile --build --push` followed by `dexaflow push`, register the image by digest (#1703); a CLI from before that registers a tag on the second path, so start with `warn` to find those pipelines. Any other value fails boot; in a config file a YAML `false` means `off`. Config key `deploy.require_digest`. Helm: `deploy.requireDigest`. See [Require images pinned by digest](/operate/cicd-deploy/#require-images-pinned-by-digest). |
| `DEXAFLOW_DAGS_ARE_PAUSED_AT_CREATION` | `dags_are_paused_at_creation` | bool | `true` | both | Start a DAG paused when it is registered for the first time, so its schedule creates no run until someone unpauses it. Airflow's setting of the same name. A DAG's own `is_paused_upon_creation` wins over it. Applies only on creation: a redeploy never pauses or unpauses an existing DAG. `false` registers new DAGs active, as releases before 0.5.4 did. `dexaflow lite` sets it to `false`. Config key `dags_are_paused_at_creation`. Helm: set the chart value `config.dagsArePausedAtCreation` (not `extraEnv`); the chart always renders this variable. A DAG deleted and deployed again comes back paused. See [New DAGs start paused](/concepts/core-concepts/#new-dags-start-paused). |

<!-- END GENERATED configref -->

### Cluster deploy defaults (`deploy.defaults.*`)

What `dexaflow deploy` uses on this cluster for whatever the project, its
workspace, its target and the person's CLI config leave unset. The server
serves them on `GET /api/v2/deployDefaults`; unset, it advertises nothing and
every deploy resolves as before. They are not secrets: never put registry
credentials here. The task service account and the warm pools are the
server's own settings, advertised when their switch is on. The server refuses to start with a value no deploy could use.
See [Cluster deploy defaults](/operate/workspaces-and-targets/#cluster-deploy-defaults).

<!-- BEGIN GENERATED configref settings deploy.defaults. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_DEPLOY_DEFAULTS_REGISTRY` | `deploy.defaults.registry` | string | _(empty)_ | Cluster | Image prefix a deploy pushes to, such as `ghcr.io/acme`: a host, an optional port and a path, lowercase, with no scheme, credentials, tag or trailing `/`. The image name defaults to the `dag_id`. Helm: `deploy.defaults.registry`. |
| `DEXAFLOW_DEPLOY_DEFAULTS_PLATFORMS` | `deploy.defaults.platforms` | list | _(empty)_ | Cluster | Platforms a deploy builds for, comma separated (`linux/amd64,linux/arm64`), each `os/arch` or `os/arch/variant`. Unset, the built-in `linux/amd64` applies. Helm: `deploy.defaults.platforms` (a list). |
| `DEXAFLOW_DEPLOY_DEFAULTS_BASE_IMAGE` | `deploy.defaults.base_image` | string | _(empty)_ | Cluster | Base image of the generated Dockerfile for projects that set no `base_image`. Helm: `deploy.defaults.baseImage`. |
| `DEXAFLOW_DEPLOY_DEFAULTS_SOURCE_MODE` | `deploy.defaults.source_mode` | bool | `false` | Cluster | Advertise the source mode runtime image (`execution.source_mode.image`) as `source_mode_image`, so a deploy that names no runtime image of its own registers the DAG's `dag.py` on it with no build or push, and refuses a DAG that needs a build. The server serves its own runtime image, so nothing is copied by hand. Requires `execution.source_mode.enabled`; the server refuses to start otherwise. There is no `deploy.defaults.source_mode_image` key: the server refuses to start when one is set (in the file or as `DEXAFLOW_DEPLOY_DEFAULTS_SOURCE_MODE_IMAGE`). Helm: `deploy.defaults.sourceMode`. See [Deploying in source mode](/operate/source-mode/#deploying-in-source-mode). |
| `DEXAFLOW_DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_PROJECT` | `deploy.defaults.remote_builder.cloud_build.project` | string | _(empty)_ | Cluster | Google Cloud project of the remote builder (Cloud Build, ADR 0083) a deploy uses for builds no daemonless path covers, when the project's `build.remote_builder`, its target and its workspace name none. Required when any other `remote_builder` value is set. Served as `remote_builder.cloud_build`. Helm: `deploy.defaults.remoteBuilder.cloudBuild.project`. See [Builders](/operate/builders/). |
| `DEXAFLOW_DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_REGION` | `deploy.defaults.remote_builder.cloud_build.region` | string | _(empty)_ | Cluster | Cloud Build region of that remote builder, such as `us-central1`; required with a worker pool. Helm: `deploy.defaults.remoteBuilder.cloudBuild.region`. |
| `DEXAFLOW_DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_WORKER_POOL` | `deploy.defaults.remote_builder.cloud_build.worker_pool` | string | _(empty)_ | Cluster | Private pool of that remote builder, `projects/<project>/locations/<region>/workerPools/<name>`, in its region. Helm: `deploy.defaults.remoteBuilder.cloudBuild.workerPool`. |
| `DEXAFLOW_DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_SERVICE_ACCOUNT` | `deploy.defaults.remote_builder.cloud_build.service_account` | string | _(empty)_ | Cluster | Account the remote build runs as, `projects/<project>/serviceAccounts/<email>`. Helm: `deploy.defaults.remoteBuilder.cloudBuild.serviceAccount`. |
| `DEXAFLOW_DEPLOY_DEFAULTS_REMOTE_BUILDER_BUILDKIT_NAMESPACE` | `deploy.defaults.remote_builder.buildkit.namespace` | string | _(empty)_ | Cluster | Namespace of the in-cluster builder (rootless BuildKit, #1883) a deploy uses for builds no daemonless path covers, when the project's `build.remote_builder`, its target and its workspace name none. The person deploying needs `buildctl` and `kubectl`, and the builder Role through `builder.subjects`; one who has not is passed over for the local daemon. Required when `pod_selector` is set; refused with any `cloud_build` value. Served as `remote_builder.buildkit`. The chart sets it to `builder.namespace` when `builder.enabled` and `builder.advertise` are on. See [Builders](/operate/builders/#building-on-the-in-cluster-builder). |
| `DEXAFLOW_DEPLOY_DEFAULTS_REMOTE_BUILDER_BUILDKIT_POD_SELECTOR` | `deploy.defaults.remote_builder.buildkit.pod_selector` | string | _(empty)_ | Cluster | Label selector of the builder pod, `key=value` pairs separated by commas; `app.kubernetes.io/component=builder` when unset. The chart sets it to the builder's instance and component labels. |
| `DEXAFLOW_DEPLOY_DEFAULTS_TASK_SERVICE_ACCOUNT` | `deploy.defaults.task_service_account` | bool | `false` | Cluster | Advertise `executor.task_service_account` as `task_service_account`, so a deploy says which ServiceAccount its tasks run as when they set no `execution.service_account`. The deploy only reports it: nothing is written into `dag.json`. Requires `executor.task_service_account`; the server refuses to start otherwise. Helm: `deploy.defaults.taskServiceAccount`. |
| `DEXAFLOW_DEPLOY_DEFAULTS_WARM_POOLS` | `deploy.defaults.warm_pools` | bool | `false` | Cluster | Advertise `execution.warm_pools_enabled`, `execution.min_idle_workers` and `execution.max_pool_size` as `warm_pools`, so a deploy says whether warm pools are on and how many idle workers its DAG gets: its own `min_idle_workers`, else the cluster's, capped at the pool size. The deploy only reports it. Helm: `deploy.defaults.warmPools`. |

<!-- END GENERATED configref -->

### Scheduler (`scheduler.*`)

<!-- BEGIN GENERATED configref settings scheduler. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_SCHEDULER_LOOP_INTERVAL_MS` | `scheduler.loop_interval_ms` | int | `1000` | both | Scheduler tick interval, in milliseconds. |
| `DEXAFLOW_SCHEDULER_ENABLED` | `scheduler.enabled` | bool | `true` | both | Whether this process runs the scheduler loop. |
| `DEXAFLOW_SCHEDULER_EAGER_PROMOTION` | `scheduler.eager_promotion` | bool | `false` | both | Dispatches downstream tasks sooner after their upstreams settle. Within one tick, a task that becomes ready is queued at once instead of on the next tick, and skips or upstream failures cascade down the DAG (bounded passes per run, the rest continues next tick). A task's final state report also triggers an early tick on the replica that receives it, started at least 100 ms, and at least twice the previous tick's duration, after the previous tick began; the loop interval stays the upper bound. `dexaflow_scheduler_woken_ticks_total` (also exported as `leoflow_scheduler_woken_ticks_total`) counts those early ticks. Max active tasks and pool limits apply across the extra passes. Off by default. |
| `DEXAFLOW_SCHEDULER_DISPATCH_BUFFER_SIZE` | `scheduler.dispatch.buffer_size` | int | `0` | both | Depth of the queued-dispatches channel ([ADR 0031](/project/adrs/0031-scheduler-architecture/), #127). `0` keeps dispatch synchronous with the tick (right for Lite); `>0` enables the worker pool (right for Cluster, where K8s API calls add latency): the tick only enqueues, and a full buffer leaves the task scheduled for the next tick. The task is recorded `queued` when the buffer accepts it, so time in the buffer counts toward the 3 minute dispatch-lost threshold: a request that waited more than half of it (90 s) is not dispatched, and its task is offered again on a later tick with no dispatch attempt charged and a fresh `queued_at`. Size the buffer to what the Kubernetes client creates in well under 90 s (`executor.kube_client.qps`, shared with maintenance unless `executor.kube_client.maintenance_qps` is set): at the default QPS of 5, 512 requests take over 100 s. Recommended for a busy cluster: `512` with 16 workers, with the client QPS raised to match (for example `50`). Helm: `config.scheduler.dispatch.bufferSize`. |
| `DEXAFLOW_SCHEDULER_DISPATCH_WORKERS` | `scheduler.dispatch.workers` | int | `0` | both | Goroutines draining the dispatch queue. Ignored when buffer size ≤ 0; otherwise floored to 1. Helm: `config.scheduler.dispatch.workers`. |
| `DEXAFLOW_SCHEDULER_DISPATCH_TICK_BUDGET` | `scheduler.dispatch.tick_budget` | duration | `5s` | both | Wall time one scheduler tick may spend dispatching. Once it is spent the tick stops dispatching: the remaining ready tasks stay `scheduled` with no dispatch attempt charged, and the next tick starts at the run after the one that was cut, so one wide fan-out (for example under the Kubernetes client limit of `EXECUTOR_KUBE_CLIENT_QPS`) cannot hold every other run of every tenant for minutes. Due cron runs are created before the tick dispatches. `0s` dispatches every ready task in the tick however long it takes (the behavior before 0.5.4); negative fails boot. |
| `DEXAFLOW_SCHEDULER_DISPATCH_MAX_PER_TENANT_PER_TICK` | `scheduler.dispatch.max_per_tenant_per_tick` | int | `0` | both | Most tasks one tenant may dispatch in one tick, so a tenant with a wide fan-out or many runs leaves dispatch room for the others; the rest wait for the next tick with no attempt charged. `0` means no cap; negative fails boot. |
| `DEXAFLOW_SCHEDULER_ALERTS_BLOCK_PRIVATE_DESTINATIONS` | `scheduler.alerts.block_private_destinations` | bool | `false` | both | Refuse on-failure alert requests to loopback, private, link-local (including the `169.254.169.254` metadata address), shared, unspecified, multicast and broadcast addresses. See [Alert destinations](#alert-destinations). |
| `DEXAFLOW_SCHEDULER_ALERTS_ALLOWED_CIDRS` | `scheduler.alerts.allowed_cidrs` | list | _(empty)_ | both | CIDRs or single addresses exempted from that block (`scheduler.alerts.allowed_cidrs`, a list; comma-separated via the env var). Applied only while the block is on, but validated at startup either way: an invalid entry fails startup. |
| `DEXAFLOW_SCHEDULER_POOL_STARVATION_THRESHOLD` | `scheduler.pool_starvation_threshold` | duration | `60s` | Cluster | How long a scheduled task of more than one slot that does not fit its pool (its dispatch backoff elapsed, its DAG under `max_active_tasks`) waits before the pool is reserved for it ([ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/)). While reserved the pool admits no other task, so tasks of fewer slots cannot keep a larger one out forever; the oldest waiter wins, one per pool; a task of size 1 never reserves, so an install that sets no `size` admits exactly as before; a tick in which something other than the pool holds the waiter keeps its waiting time; and a task larger than its whole pool is never reserved for (it is logged once instead). The state lives in the leader's memory. `0s` disables it; negative fails boot. Helm: `config.scheduler.poolStarvationThreshold`. |

<!-- END GENERATED configref -->

### Executor (`executor.*`)

<!-- BEGIN GENERATED configref settings executor. !executor.defaults. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_EXECUTOR_HTTP_USER_AGENT` | `executor.http.user_agent` | string | `leoflow/0.1` | both | Default `User-Agent` header for HTTP requests a task image may make on the platform's behalf. |
| `DEXAFLOW_EXECUTOR_TASK_NAMESPACE` | `executor.task_namespace` | string | `leoflow` | Cluster | Kubernetes namespace the server creates task pods and per-run staging PVCs in. MUST match the namespace the Helm chart grants the executor Role in (chart `taskNamespace`); a mismatch 403s every dispatch (#480). The Helm chart always sets it (to the release namespace by default, #1682), so the `leoflow` default applies only to installs that do not use the chart. |
| `DEXAFLOW_EXECUTOR_TYPE` | `executor.type` | string | `kubernetes` | both | Pod-path executor: `kubernetes` (default, pod-per-task) or `subprocess` (dev only, runs the agent on the host without isolation; `dexaflow lite` sets it). With `kubernetes`, a scheduler that finds no in-cluster config or kubeconfig refuses to start. |
| `DEXAFLOW_EXECUTOR_AGENT_PATH` | `executor.agent_path` | string | `leoflow-agent` | dev-only | The agent binary the subprocess executor runs (`leoflow-agent`, a link to `dexaflow-agent`, so agents from before the rename are found too). |
| `DEXAFLOW_EXECUTOR_SUBPROCESS_WORKDIR` | `executor.subprocess_workdir` | string | _(empty)_ | dev-only | Working directory the subprocess executor runs the agent in (so it can import the project's `dag.py`). Empty keeps the server's working directory. |
| `DEXAFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR` | `executor.agent_control_plane_addr` | string | _(empty)_, uses `server.grpc_addr` | both | gRPC address task pods dial back to. In a local k3d/kind cluster set it to a host-reachable address such as `host.k3d.internal:9091`. |
| `DEXAFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP` | `executor.agent_tls_ca_configmap` | string | _(empty)_ | Cluster | Names a ConfigMap (key `ca.crt`) mounted into task pods so the agent verifies the control plane's gRPC TLS cert (#58). Empty = agents use the insecure channel (dev). |
| `DEXAFLOW_EXECUTOR_TASK_SERVICE_ACCOUNT` | `executor.task_service_account` | string | _(empty)_ | Cluster | ServiceAccount task pods run as when a DAG does not set `execution.service_account`. The Helm chart wires this from `taskServiceAccount.name` when `taskServiceAccount.create: true`, so creating the task SA is enough for keyless secret access: no per-DAG opt-in. An explicit per-task `execution.service_account` still wins; empty leaves pods on the namespace default SA. |
| `DEXAFLOW_EXECUTOR_TASK_SECRET_NAME` | `executor.task_secret_name` | string | _(empty)_ | Cluster | Names a Kubernetes Secret mounted read-only into every task pod, so a task can read a cluster-stored credential (e.g. a GCP SA key) referenced by a connection's `key_path` ([ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/)). Empty = no secret mounted. |
| `DEXAFLOW_EXECUTOR_TASK_SECRET_MOUNT_PATH` | `executor.task_secret_mount_path` | string | `/etc/leoflow/secrets` | Cluster | Where `DEXAFLOW_EXECUTOR_TASK_SECRET_NAME` is mounted in the task pod. |
| `DEXAFLOW_EXECUTOR_COLLECT_SETTLED_RUN_PODS` | `executor.collect_settled_run_pods` | bool | `false` | Cluster | Collect a settled run's finished task pods as soon as the reconciler has recorded every outcome, in one `DeleteCollection` by the run's and tenant's labels limited to finished phases and served from the apiserver's watch cache, instead of one delete per pod after the 10 minute grace period. A run is settled when it is `success` or `failed` and none of its task instances is outside `success`, `failed`, `skipped` and `upstream_failed`; a sweep collects at most 50 runs. Off keeps finished pods inspectable with `kubectl` for the grace period. Needs the `deletecollection` verb on pods, which the chart's executor Role grants only when `executor.collectSettledRunPods` is on; without it the server falls back to per-pod deletes. Helm: `executor.collectSettledRunPods`. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_QPS` | `executor.kube_client.qps` | float | `5` | Cluster | Client-side request rate (queries per second) of the Kubernetes client that creates task pods. The agent token exchange builds its own client with the same limits. `5` is client-go's default; a 1,000-task fan out at 5 QPS takes over three minutes just to create pods, so a large deployment raises it (for example `50`). Non-positive falls back to `5`. Helm: `executor.kubeClient.qps`. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_BURST` | `executor.kube_client.burst` | int | `10` | Cluster | Burst of the same client's token bucket. Non-positive falls back to `10`. Helm: `executor.kubeClient.burst`. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_MAINTENANCE_QPS` | `executor.kube_client.maintenance_qps` | float | `0` | Cluster | When above `0`, maintenance work (pod informer, reconciler, reapers, staging GC, warm pool reconciler) gets its own Kubernetes client and rate limiter at this QPS, so a maintenance burst cannot starve pod creation. `0` keeps maintenance on the dispatch client, one shared budget. Set it whenever you raise `KUBE_CLIENT_QPS`. Helm: `executor.kubeClient.maintenanceQps`. |
| `DEXAFLOW_EXECUTOR_KUBE_CLIENT_MAINTENANCE_BURST` | `executor.kube_client.maintenance_burst` | int | `0` | Cluster | Burst of the separate maintenance client. Ignored while `KUBE_CLIENT_MAINTENANCE_QPS` is `0`; non-positive falls back to `10`. Helm: `executor.kubeClient.maintenanceBurst`. |
| `DEXAFLOW_EXECUTOR_UNIT_CPU` | `executor.unit.cpu` | string | _(empty)_ | Cluster | CPU of one pool slot, the resource unit ([ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/)), as a Kubernetes quantity (e.g. `250m`). Set together with `DEXAFLOW_EXECUTOR_UNIT_MEMORY`; one without the other fails boot. With a unit, a task that declares no cpu or memory gets `pool_slots x unit` as requests and limits (this replaces `executor.defaults.resources`); a task that declares some keeps its values, a missing request follows the declared limit, and a missing limit is `pool_slots x unit` (or the request, when that is larger), so requests are never above limits, and a task that declares more than `pool_slots x unit` is refused at registration (400 naming the `size` it needs) and at dispatch (the task fails once, spending neither dispatch retries nor its own `retries`). With warm pools on, a warm pod is one unit and only takes tasks of size 1 that declare no resources; the rest get a dedicated pod. Empty (default) changes nothing. Helm: `executor.unit.cpu`. |
| `DEXAFLOW_EXECUTOR_UNIT_MEMORY` | `executor.unit.memory` | string | _(empty)_ | Cluster | Memory of one pool slot (e.g. `512Mi`). See `DEXAFLOW_EXECUTOR_UNIT_CPU`. Helm: `executor.unit.memory`. |
| `DEXAFLOW_EXECUTOR_UNIT_ENFORCE` | `executor.unit.enforce` | string | `refuse` | Cluster | What happens to a task larger than its size while a unit is set. `refuse` fails it at registration and at dispatch; `warn` accepts it, runs it with its own resources, logs it and counts it in `dexaflow_unit_misfit_total{stage}`. Turn a unit on with `warn`, fix the DAGs the log names, then switch to `refuse`. Any other value fails boot. Helm: `executor.unit.enforce`. |
| `DEXAFLOW_EXECUTOR_UNIT_MAX_SIZE` | `executor.unit.max_size` | int | `64` | Cluster | Largest `pool_slots` a task may have while a unit is set, whatever its pool: a larger task is refused at registration and at dispatch, under `warn` too, since a pool without a budget would otherwise let one task ask for any multiple of the unit. Must be positive. Helm: `executor.unit.maxSize`. |

<!-- END GENERATED configref -->

### Executor task defaults (`executor.defaults.*`)

Lowest-precedence (L0) per-cluster task defaults, applied at dispatch to fill
gaps the DAG artifact left empty ([ADR 0023](/project/adrs/0023-dag-authoring-config-binding/)).
They never override a value baked into `dag.json`, keeping the artifact portable
across clusters.

<!-- BEGIN GENERATED configref settings executor.defaults. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_EXECUTOR_DEFAULTS_STAGING_SIZE` | `executor.defaults.staging_size` | string | _(empty)_ | Cluster | Default size of the per-run staging volume when the DAG enabled staging without pinning it (a Kubernetes quantity, e.g. `10Gi`). Empty leaves the size unset. Helm: `executor.defaults.staging.size`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_STAGING_STORAGE_CLASS` | `executor.defaults.staging_storage_class` | string | _(empty)_ | Cluster | Default StorageClass for the staging volume (e.g. the cluster's RWX class). Empty falls back to the cluster's default StorageClass. Helm: `executor.defaults.staging.storageClass`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_STAGING_ACCESS_MODE` | `executor.defaults.staging_access_mode` | string | `ReadWriteMany` | Cluster | PVC access mode for the per-run staging volume. Default `ReadWriteMany` (multi-node prod); single-node dev (k3d local-path, no RWX) sets `ReadWriteOnce`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU` | `executor.defaults.resources_cpu` | string | _(empty)_ | Cluster | Default CPU for a task that sets no CPU request or limit of its own (a Kubernetes quantity, e.g. `250m`). Applied as **both request and limit**. Guaranteed QoS needs the **memory** default set too: cpu alone leaves the task Burstable with no memory bound at all, and the control plane WARNs at boot naming the missing key; empty leaves it BestEffort unless the DAG sets its own. Helm: `executor.defaults.resources.cpu`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_RESOURCES_MEMORY` | `executor.defaults.resources_memory` | string | _(empty)_ | Cluster | Default memory for a task that sets no memory request or limit of its own (e.g. `256Mi`). Applied as **both request and limit**. Set it together with the CPU default: either one alone is Burstable, not Guaranteed. Helm: `executor.defaults.resources.memory`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_LIMITS_CPU` | `executor.defaults.limits_cpu` | string | _(empty)_ | Cluster | Default CPU **limit** for every task pod whose resources leave the CPU limit empty, including a task that declares requests only (a Kubernetes quantity, e.g. `2`). A declared limit is never changed, and a request above this value raises the filled limit to the request. Where the CPU request is also empty, Kubernetes sets the request to this limit, as it does for a `LimitRange` default, so a task declaring only a memory request also requests this CPU. Without `executor.unit`, the default limits also size every warm worker pod, and a task whose own limit would be larger skips warm placement and takes a dedicated pod. Empty (the default) sets no limit. Not applied with `executor.unit`, which always sets both limits. Helm: `executor.defaults.limits.cpu`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_LIMITS_MEMORY` | `executor.defaults.limits_memory` | string | _(empty)_ | Cluster | Default memory **limit** for every task pod whose resources leave the memory limit empty (e.g. `4Gi`). Same rules as the CPU limit above (a task declaring only a CPU request also requests this memory, and warm worker pods carry it too); either may be set alone. Empty (the default) sets no limit. Helm: `executor.defaults.limits.memory`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_RUN_TASKS_AS_NON_ROOT` | `executor.defaults.run_tasks_as_non_root` | bool | `true` | Cluster | **Refuses to start a task container whose image resolves to UID 0**, completing Pod Security Admission's `restricted` set. On by default: the images this repo ships carry a numeric non-root UID (`USER 65532:65532`) and the executor pairs it with a pod-level `fsGroup` so the staging PVC stays writable. Turn it off only for a cluster whose task images legitimately run as root. Operator-scoped (never a DAG field). |
| `DEXAFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM` | `executor.defaults.read_only_task_root_filesystem` | bool | `false` | Cluster | Mounts every task container's root filesystem read-only. Off by default (`restricted` does not require it and it breaks ordinary Python tasks: pip cache, `/tmp`, matplotlib config). Turn on for a fleet of tasks known not to write outside their volumes. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_FIELDS` | `executor.defaults.execution_allowlist.fields` | list | `*` | Cluster | **Which task `execution` fields a DAG may set** (comma-separated `dag.json` names): `node_selector`, `tolerations`, `affinity`, `topology_spread_constraints`, `priority_class_name`, `runtime_class_name`, `resource_claims`, `termination_grace_period_seconds`, `labels`, `annotations`. `*` (default) allows all of them, `none` allows none (so does an explicitly empty list, `fields: []`, in a server config file or in Helm; an empty environment variable counts as unset). A DAG that sets a field outside the list is refused at registration (400 naming the task and the field), and a DAG registered before the list changed fails that task at dispatch (once, no retries). `service_account` and `image_pull_policy` are not governed. An unknown name fails boot. Operator-scoped (never a DAG field). See [Placement allowlist](/operate/placement-allowlist/). Helm: `executor.defaults.executionAllowlist.fields`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_PRIORITY_CLASSES` | `executor.defaults.execution_allowlist.priority_classes` | list | _(empty)_ | Cluster | When set (comma-separated), the only `execution.priority_class_name` values a DAG may use; refused like a disallowed field. Empty (default) allows any priority class. Helm: `executor.defaults.executionAllowlist.priorityClasses`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_SERVICE_ACCOUNTS` | `executor.defaults.execution_allowlist.service_accounts` | list | _(empty)_ | Cluster | When set (comma-separated), the only `execution.service_account` values a DAG may name. A task that names none runs as `DEXAFLOW_EXECUTOR_TASK_SERVICE_ACCOUNT`, which is not checked. Refused at registration and at dispatch like a disallowed field. Empty (default) allows any. Helm: `executor.defaults.executionAllowlist.serviceAccounts`. |
| `DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_IMAGES` | `executor.defaults.execution_allowlist.images` | list | _(empty)_ | Cluster | When set (comma-separated), the only DAG images that may run: an exact reference (`busybox:1.36`) or a prefix ending in `*` (`registry.example.com/team-a/*`); `*` is allowed only at the end. A DAG with another image is refused at registration (400 naming the image) and at dispatch, and gets no warm workers. Empty (default) allows any. Helm: `executor.defaults.executionAllowlist.images`. |

<!-- END GENERATED configref -->

### Warm worker pools (`execution.*`)

Cluster-gated N:1 pod reuse ([ADR 0058](/project/adrs/0058-warm-worker-pools/)). Every field
is operator-set. The default is a byte-for-byte no-op — warm pools OFF means a
dedicated pod per task attempt.

<!-- BEGIN GENERATED configref settings execution. !execution.source_mode. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_EXECUTION_WARM_POOLS_ENABLED` | `execution.warm_pools_enabled` | bool | `false` | Cluster | Reuse one task pod across many attempts of the same DAG version. Off = dedicated pod-per-task. Validated fail-closed at boot: requires `agent_token_transport=exchange` **and** `secret_liveness_mode=enforce`. Helm: `execution.warmPoolsEnabled`. |
| `DEXAFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER` | `execution.max_attempts_per_worker` | int | `50` | Cluster | Attempts a warm worker serves before it drains and recycles (D9). |
| `DEXAFLOW_EXECUTION_MAX_WORKER_LIFETIME` | `execution.max_worker_lifetime` | duration | `1h` | Cluster | Wall-clock lifetime of a warm worker before it drains and recycles, independent of the attempt count (D9). A duration string. |
| `DEXAFLOW_EXECUTION_MIN_IDLE_WORKERS` | `execution.min_idle_workers` | int | `0` | Cluster | Warm workers kept ready per DAG version when the DAG declares no warmth of its own (D6). `0` is scale-to-zero. Read only while warm pools are on. |
| `DEXAFLOW_EXECUTION_WORKER_IDLE_TTL` | `execution.worker_idle_ttl` | duration | `5m` | Cluster | How long an idle warm worker is kept before it is recycled (D6). A duration string. |
| `DEXAFLOW_EXECUTION_MAX_POOL_SIZE` | `execution.max_pool_size` | int | `8` | Cluster | Cap on the warm workers one DAG version may hold, and the ceiling a DAG author's warmth request is clamped to (D6). |
| `DEXAFLOW_EXECUTION_MAX_WARM_PODS_PER_TENANT` | `execution.max_warm_pods_per_tenant` | int | `100` | Cluster | Cap on the total warm pods one tenant may hold across all its DAG versions (M4), so one team cannot pin idle pods and starve neighbours on a shared cluster. |
| `DEXAFLOW_EXECUTION_WARM_READ_ONLY_ROOT_FILESYSTEM` | `execution.warm_read_only_root_filesystem` | bool | `false` | Cluster | Mount every warm worker's root filesystem read only, give each attempt its own `HOME` and XDG dirs inside the scratch the worker wipes between attempts, and empty the `/tmp` emptyDir and `/dev/shm` before each attempt and again as soon as it ends, so nothing one attempt writes reaches the next one on the same worker. A task that writes outside `$HOME`, `$TMPDIR`, `/tmp` and `/dev/shm` fails with it on. Takes effect on warm pods created after it is turned on; running warm pods keep their spec until they recycle. Dedicated task pods are not affected. Helm: `execution.warmReadOnlyRootFilesystem`. |
| `DEXAFLOW_EXECUTION_WARM_REQUIRE_ATTEMPT_HOME_SWEEP` | `execution.warm_require_attempt_home_sweep` | bool | `false` | Cluster | With `DEXAFLOW_EXECUTION_WARM_READ_ONLY_ROOT_FILESYSTEM` on, refuse a warm worker whose agent does not confirm, when it registers, that it empties `/tmp` and `/dev/shm` between attempts, so that DAG version's attempts run on dedicated pods. Off, such a worker is logged with `reason=attempt_home_sweep_unconfirmed`, counted in `dexaflow_scheduler_decisions_total{decision_type="warm_worker_sweep_unconfirmed"}` and kept, because task images built on runtimes from v0.5.1 sweep but only newer ones confirm it. Helm: `execution.warmRequireAttemptHomeSweep`. |
| `DEXAFLOW_EXECUTION_WARM_POOL_EVENT_REFILL` | `execution.warm_pool_event_refill` | bool | `false` | Cluster | Refill warm pools on events instead of every 30s: the reconciler reads the warm fleet from a dedicated pod informer instead of a LIST per tick, reacts at once when a warm worker is deleted, fails or is claimed by an attempt, and creates replacements concurrently (up to 4 at a time). The periodic tick stays as a backstop. Helm: `execution.warmPoolEventRefill`. |

<!-- END GENERATED configref -->

### Source mode (`execution.source_mode.*`)

Cluster runs a DAG version straight from the `dag.py` it was registered with, with
no image build ([ADR 0067](/project/adrs/0067-mcp-run-control-scopes-source-mode/)).
A version runs in source mode when the mode is on, its `image` is exactly the
runtime image below, and it carries a source. Every other version runs as
before. See [Source mode](/operate/source-mode/) for what a source-mode DAG can
and cannot do.

<!-- BEGIN GENERATED configref settings execution.source_mode. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED` | `execution.source_mode.enabled` | bool | `false` | Cluster | Turn source mode on. Off, Cluster ignores a version's source, as before. Helm: `execution.sourceMode.enabled`. |
| `DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE` | `execution.source_mode.image` | string | _(empty)_ | Cluster | The runtime image source-mode versions name as their `image`, pinned by a full digest (`image@sha256:` and 64 hex characters). Required when source mode is on; the server refuses to start with a tag alone or a short digest. Helm: `execution.sourceMode.image`; the chart refuses to render the same values the server refuses. |
| `DEXAFLOW_EXECUTION_SOURCE_MODE_WARM_POOLS` | `execution.source_mode.warm_pools` | bool | `false` | Cluster | Let source-mode versions run on warm workers: the control plane sends the version's `dag.py` with each warm assignment and the worker runs the attempt from it ([ADR 0095](/project/adrs/0095-source-mode-on-warm-workers/)). Needs `DEXAFLOW_EXECUTION_WARM_POOLS_ENABLED` and a runtime image whose agent is from 0.5.7 or later; a worker on an older agent never gets a source-mode attempt, which then takes a dedicated pod, and is counted in `dexaflow_scheduler_decisions_total{decision_type="warm_worker_no_source_delivery"}`. Ignored while source mode is off. See [Source mode](/operate/source-mode/#warm-workers). Helm: `execution.sourceMode.warmPools`. |

<!-- END GENERATED configref -->

### Logs (`logs.*`)

<!-- BEGIN GENERATED configref settings logs. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_LOGS_DIR` | `logs.dir` | string | `/var/log/leoflow` | both | Task-log sink directory (used by the default `disk` backend). |
| `DEXAFLOW_LOGS_BACKEND` | `logs.backend` | string | `disk` | Cluster | Durable task-log store: `disk` (default: the on-disk sink, unchanged; the only backend Lite uses), `s3` (AWS S3, MinIO, Ceph RGW), or `gcs` (Google Cloud Storage, native SDK). See [ADR 0056](/project/adrs/0056-task-log-object-sink/). |
| `DEXAFLOW_LOGS_PERSISTENCE_ENABLED` | `logs.persistence_enabled` | bool | `true` | both | Whether task-log storage is backed by durable persistence (a PVC or host disk). When false with backend `disk`, task logs reside on an ephemeral emptyDir volume and do not survive a pod restart or upgrade. |
| `DEXAFLOW_LOGS_SINK_BUCKET` | `logs.sink.bucket` | string | _(empty)_ | Cluster | Target bucket. Required when the backend is `s3` or `gcs` (boot fails otherwise). |
| `DEXAFLOW_LOGS_SINK_PREFIX` | `logs.sink.prefix` | string | _(empty)_ | Cluster | Optional key prefix; objects are laid out at `{prefix}/{tenant}/{dag}/{run}/{task}/{try}.e{epoch}.log`, one object per execution of the try (an infra re-place or a reschedule poke runs the try again under a new epoch). Logs written before 0.5.1 keep `{try}.log`. The log endpoint serves all of a try's executions in order. |
| `DEXAFLOW_LOGS_SINK_REGION` | `logs.sink.region` | string | _(empty)_ | Cluster | **s3-only.** Store region (e.g. `us-east-1`). Required by AWS S3; ignored by some S3-compatible stores. |
| `DEXAFLOW_LOGS_SINK_ENDPOINT` | `logs.sink.endpoint` | string | _(empty)_ | Cluster | **s3-only.** Endpoint override for S3-compatible stores (MinIO, Ceph RGW). Empty uses the AWS default. Not a path to GCS: use `gcs`. |
| `DEXAFLOW_LOGS_SINK_FORCE_PATH_STYLE` | `logs.sink.force_path_style` | bool | `false` | Cluster | **s3-only.** Use path-style addressing (bucket in the path, not the host). Required by MinIO and some S3-compatible stores. |
| `DEXAFLOW_LOGS_SINK_ACCESS_KEY_ID` | `logs.sink.access_key_id` | string | _(empty)_ | Cluster | **s3-only.** Static credentials: **discouraged**. Leave empty (recommended) to use the keyless chain (IRSA / instance profile), per [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/). |
| `DEXAFLOW_LOGS_SINK_SECRET_ACCESS_KEY` | `logs.sink.secret_access_key` | string | _(empty)_ | Cluster | **s3-only.** Static credentials: **discouraged**. Leave empty (recommended) to use the keyless chain (IRSA / instance profile), per [ADR 0035](/project/adrs/0035-cloud-connector-auth-keyless-first/). |
| `DEXAFLOW_LOGS_SINK_CREDENTIALS_FILE` | `logs.sink.credentials_file` | string | _(empty)_ | Cluster | **gcs-only.** Path to a service-account JSON key: **discouraged**. Leave empty (recommended) to use Application Default Credentials (GKE Workload Identity). |
| `DEXAFLOW_LOGS_SINK_LAYOUT` | `logs.sink.layout` | string | `single` | Cluster | How new attempts are written. `single` keeps one object per attempt at `{try}.log`, rewritten on every flush. `segmented` writes numbered segments under `{try}.log.d/` (`{try}.e{epoch}.log.d/` for a later execution of the try) so each flush uploads only the open segment (up to 4 MiB) and the control plane holds one segment per attempt instead of the whole log. Both layouts are always readable, but a server older than this setting reads only `{try}.log`: enable `segmented` once every replica is upgraded. Before downgrading to an older version, switch back to `single`; attempts already written as segments stay unreadable by older versions. On S3, `segmented` needs `s3:ListBucket` on the bucket so a missing segment answers not-found; the server checks this at startup and refuses to start when a missing key is not answered with not-found. |
| `DEXAFLOW_LOGS_TAIL_PUBLISH` | `logs.tail.publish` | string | `always` | both | When the control plane publishes received task-log lines for live followers. `always` publishes every line as it arrives. `on_demand` publishes only while someone follows the attempt: each log stream checks for followers at most once a second (Redis `PUBSUB NUMSUB`/`NUMPAT`), or once per 512 lines or 512 KiB of output when the task logs faster than that, and replays to a new follower every line received since the last check that found none (a single line over 1 MiB is not replayed). A new follower can see its first live lines up to about a second late. Enable `on_demand` once every API replica runs a version that skips replayed lines, or followers on older replicas may see a few lines twice. |
| `DEXAFLOW_LOGS_TAIL_REPLAY_BUDGET_BYTES` | `logs.tail.replay_budget_bytes` | int | `0` | both | With `logs.tail.publish: on_demand`, the most bytes the log streams of one control-plane process hold together for a follower who may subscribe. Each stream holds at most 1 MiB, so a thousand streams that went quiet after a burst can hold about 500 MiB; over the budget the held lines of the stream silent the longest are dropped first, and a follower of that stream reads them from the stored log instead of the replay. `dexaflow_log_tail_replay_bytes` reports what is held and `dexaflow_log_tail_replay_evictions_total` the drops. `0` means no budget; a negative value fails boot. Helm: set it through `extraEnv`. |

<!-- END GENERATED configref -->

### External secrets (`secrets.*`)

Operator-only ([ADR 0060](/project/adrs/0060-external-secrets-resolution/)):
delivered to the task pod as `DEXAFLOW_SECRETS_*`, which an author's task env can
never set. Empty `backend` keeps the Dexaflow vault as the only source —
byte-identical to having no external secrets at all. See
[External secrets](/operate/external-secrets/) and run the
[cluster validation runbook](/operate/external-secrets-cluster-validation/)
before enabling it in production.

<!-- BEGIN GENERATED configref settings secrets. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_SECRETS_BACKEND` | `secrets.backend` | string | _(empty)_, disabled | Cluster (K8s) | Provider secrets-backend class the in-pod resolver drives (e.g. `airflow.providers.amazon.aws.secrets.secrets_manager.SecretsManagerBackend`). When set, a Connection/Variable a DAG declares can be resolved pod-side from the provider store under the pod's keyless identity. Helm: `secrets.backend`. |
| `DEXAFLOW_SECRETS_BACKEND_KWARGS` | `secrets.backend_kwargs` | string | _(empty)_, treated as `{}` | Cluster (K8s) | Provider kwargs as a JSON **object string** (`connections_prefix`, `variables_prefix`, `region_name`, …), delivered to the pod verbatim. A kind is served only if its `*_prefix` kwarg is present. A JSON string rather than a map so a single env var sets it, matching the env-only control-plane chart. Keyless auth (IRSA / Workload Identity) uses the task pod's ServiceAccount: set `executor.task_service_account` accordingly. Helm: `secrets.backendKwargs`. |

<!-- END GENERATED configref -->

### Retention (`retention.*`)

The retention janitor deletes metadata rows past an age you choose, per data
class. Every class is off by default (`0` days): a default install never
deletes a row. It runs on the scheduler leader only and rechecks leadership
before every batch. A run is eligible once it settled (`success` or `failed`)
more than `dag_runs_days` ago, every task instance of it is settled
(`success`, `failed`, `skipped` or `upstream_failed`; a task in `none` is
waiting to be scheduled, for example after a clear, so its run is kept), no
staging volume of it is still live, and it is not the newest run of its DAG.
The newest run (by logical date) is always kept, however old: the scheduler
reads a DAG's last logical date from it, and without it a scheduled DAG would
run its latest slot again, or its whole history with `catchup`. An eligible
run goes with its task instances, attempt history and XCom index rows,
children first. Every batch is
one short transaction of at most `batch_size` rows, run rows included; a run
with more rows than that is finished by later batches, each of which checks
again that the run is still eligible. A run's task instances are deleted all
together or not at all, so a clear between two batches never re-opens a run
missing some of its tasks (a run whose task instances are all gone re-runs
from scratch); a run with more task instances than `batch_size` has them
removed alone in one batch, which is the only time a batch exceeds it.
Before deleting, a batch locks the task instances of its runs and checks them
again, so a run a clear touched after it was selected is kept whole; a run a clear is writing at that moment is skipped
until a later cycle instead of waited on. Active and recent runs are never
touched. The janitor sleeps `batch_pause` between batches and a cycle stops
once it deleted `max_rows_per_cycle` rows; the rest waits for the next cycle.
Each tenant's runs and each tenant's audit rows take turns, one batch each,
so no tenant and neither class waits behind another's backlog. Every cycle
that deletes audit rows writes a `retention.purge` audit entry in the same
scope with the row count and the cutoff. Turn on `dry_run` first to see what
a cycle would delete: it only counts (summed across all tenants, at most
10000 runs and 10000 audit rows per tenant, so its cost stays bounded; the
log says `lower_bound=true` when a tenant reached that cap), logs the counts
and sets `dexaflow_retention_rows_eligible`, which a deleting cycle
resets to zero. Metrics: `dexaflow_retention_rows_deleted_total{table}`,
`dexaflow_retention_rows_eligible{table}` and
`dexaflow_retention_cycle_duration_seconds`. What the janitor does not delete:
task logs, which the `disk` log backend prunes after 30 days and an object
store keeps by its bucket lifecycle policy, and the bookkeeping rows of
staging volumes already marked deleted. The chart has no values for these
keys; set them through `extraEnv`.

<!-- BEGIN GENERATED configref settings retention. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_RETENTION_DAG_RUNS_DAYS` | `retention.dag_runs_days` | int | `0`, off | both | Delete settled runs, with their task instances and history, that ended more than this many days ago. The newest run of each DAG is always kept. `0` keeps every run. |
| `DEXAFLOW_RETENTION_AUDIT_LOG_DAYS` | `retention.audit_log_days` | int | `0`, off | both | Delete audit log entries older than this many days, per tenant and for the tenant-less system entries. `0` keeps the whole audit log. |
| `DEXAFLOW_RETENTION_USAGE_EVENTS_DAYS` | `retention.usage_events_days` | int | `30` | both | Delete usage events ([ADR 0096](/project/adrs/0096-task-attempt-usage-events/)) recorded more than this many days ago, oldest first in the order the service API serves them, so a consumer that is down can catch up for this long. The janitor starts for it only while `usage.events.enabled` is on. `0` keeps every event. |
| `DEXAFLOW_RETENTION_DRY_RUN` | `retention.dry_run` | bool | `false` | both | Only count what a cycle would delete; delete nothing. |
| `DEXAFLOW_RETENTION_INTERVAL` | `retention.interval` | duration | `1h` | both | How often a cycle runs. Must be greater than zero when a class is on. |
| `DEXAFLOW_RETENTION_BATCH_SIZE` | `retention.batch_size` | int | `1000` | both | Most rows one DELETE statement removes (1 to 50000), except the task instances of a single run larger than that, which go together. Also caps the runs per batch at 100. |
| `DEXAFLOW_RETENTION_BATCH_PAUSE` | `retention.batch_pause` | duration | `100ms` | both | Sleep between two batches, so the janitor never keeps the database busy. |
| `DEXAFLOW_RETENTION_MAX_ROWS_PER_CYCLE` | `retention.max_rows_per_cycle` | int | `100000` | both | A cycle starts no new batch once it deleted this many rows, children included. |

<!-- END GENERATED configref -->

### Usage events (`usage.*`)

With `usage.events.enabled` on, every write that finishes a task attempt also
writes one row to `usage_events`, in the same transaction
([ADR 0096](/project/adrs/0096-task-attempt-usage-events/)): the tenant, the
run, the task, the try, the attempt epoch, a revision for a later re-mark,
the state, the size (`pool_slots`) and the times. The scheduler leader writes
the setting to the database within 10 seconds of taking leadership and keeps
it there, so every replica records the same way. Turning it off stops the recording; the rows already
written stay. The chart has no value for this key; set it through `extraEnv`.

<!-- BEGIN GENERATED configref settings usage. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_USAGE_EVENTS_ENABLED` | `usage.events.enabled` | bool | `false` | both | Record one usage event per finished task attempt, in the transaction that finishes it ([ADR 0096](/project/adrs/0096-task-attempt-usage-events/)). The scheduler leader writes this value to the database within 10 seconds of taking leadership and keeps it there, so the leader's setting wins when replicas disagree; its log says which value it wrote. Off by default. |

<!-- END GENERATED configref -->

### Observability (`observability.*`)

<!-- BEGIN GENERATED configref settings observability. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_OBSERVABILITY_OTEL_ENABLED` | `observability.otel.enabled` | bool | `false` | both | Enable OpenTelemetry trace export. |
| `DEXAFLOW_OBSERVABILITY_OTEL_ENDPOINT` | `observability.otel.endpoint` | string | `localhost:4317` | both | OTLP collector endpoint (when OTel is enabled). |
| `DEXAFLOW_OBSERVABILITY_OTEL_SAMPLE_RATIO` | `observability.otel.sample_ratio` | float | `1` | both | Share of request traces kept, from `0` to `1`. An incoming `traceparent` header is not propagated, so every request starts its own trace and is sampled at this ratio; spans within a request follow its decision. `1` traces every request; other values outside the range fail boot. |
| `DEXAFLOW_OBSERVABILITY_OTEL_SKIP_PROBE_SPANS` | `observability.otel.skip_probe_spans` | bool | `false` | both | When `true`, no spans are recorded for `/healthz`, `/readyz` and `/static/*`. Their HTTP metrics are still recorded. |
| `DEXAFLOW_OBSERVABILITY_LOG_LEVEL` | `observability.log_level` | string | `info` | both | Control-plane log verbosity: `debug`, `info`, `warn` (alias `warning`), or `error`. Unknown values fall back to `info`. |
| `DEXAFLOW_OBSERVABILITY_LOG_FORMAT` | `observability.log_format` | string | `json` | both | Control-plane log format: `json` (default) or `text`. |
| `DEXAFLOW_OBSERVABILITY_METRICS_DROP_LEGACY_NAMES` | `observability.metrics.drop_legacy_names` | bool | `false` | both | Stop publishing every `dexaflow_*` metric a second time under its pre-rename `leoflow_*` name. The default keeps both, so dashboards and alerts on either name work. An opt-in for operators who do not need the `leoflow_*` names; it halves the scrape. Helm: set it through `extraEnv`. |

<!-- END GENERATED configref -->

### UI (`ui.*`)

<!-- BEGIN GENERATED configref settings ui. -->

| Variable | Key | Type | Default | Edition | Purpose |
|---|---|---|---|---|---|
| `DEXAFLOW_UI_INSTANCE_NAME` | `ui.instance_name` | string | `Dexaflow` | both | UI navbar label (`dexaflow lite` sets it to mark the environment). |
| `DEXAFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS` | `ui.auto_refresh_interval_seconds` | int | `0` | both | SPA polling cadence for DAG / DagRun / task-instance state. `0` falls back to the production-safe 30s default; `dexaflow lite` sets 1s for a snappy inner loop. Helm: `ui.autoRefreshIntervalSeconds`, which the chart omits entirely when unset so the server default decides. |
| `DEXAFLOW_UI_EDITION` | `ui.edition` | string | _(empty)_ | both | The edition the server runs as ([ADR 0027](/project/adrs/0027-product-editions-executors-delivery/)): `cluster` for the Helm chart on Kubernetes, `lite` for the installable binary. `pro` is the earlier name of `cluster` and is still accepted; the Helm chart currently sets `pro`. `cluster` turns on named pools, allows `auth.provider: oidc`, enforces TLS on the agent channel and shows the gold CLUSTER badge; `lite` shows the silver LITE badge. Empty or any other value shows no badge. |
| `DEXAFLOW_UI_WORKSPACE` | `ui.workspace` | string | _(empty)_ | both | DAG project directory the Lite web editor edits ([ADR 0025](/project/adrs/0025-lite-embedded-web-editor/)). Empty disables the editor. |
| `DEXAFLOW_UI_MONACO_DIR` | `ui.monaco_dir` | string | _(empty)_ | both | Where the pinned Monaco bundle was fetched by `dexaflow setup`; the editor page is served Monaco from it. Empty shows a setup hint. |
| `DEXAFLOW_UI_HOME_LINK_LABEL` | `ui.home_link.label` | string | _(empty)_ | both | Text of an optional link from the UI back to the platform you serve it from, shown on every page at the bottom-left and opened in the same tab. Set it together with `DEXAFLOW_UI_HOME_LINK_URL`. Helm: `ui.homeLink.label`. |
| `DEXAFLOW_UI_HOME_LINK_URL` | `ui.home_link.url` | string | _(empty)_ | both | Absolute `http://` or `https://` URL of the home link. Empty shows no link. Boot fails on another scheme, a missing host, or a URL without a label. Helm: `ui.homeLink.url`. |
| `DEXAFLOW_UI_THEME` | `ui.theme` | string | _(empty)_ | both | Theme for the UI as a JSON object, the same shape as Airflow's `[api] theme`: `tokens` (Chakra design tokens such as `colors.brand` and `fonts`), `globalCss`, `icon`, `icon_dark_mode`. Served in `/ui/config`, so the UI applies it through its own theming. Boot fails on invalid JSON, an unknown top-level key, or an icon that is not http(s) or root-relative. Helm: `ui.theme` (YAML, rendered as JSON). See [Branding the UI](#branding-the-ui). |
| `DEXAFLOW_UI_FAVICON_URL` | `ui.favicon_url` | string | _(empty)_ | both | Favicon for the UI, http(s) or root-relative. Empty keeps the stock icon. Helm: `ui.faviconUrl`. |
| `DEXAFLOW_UI_STYLESHEET_URLS` | `ui.stylesheet_urls` | list | _(empty)_ | both | Comma-separated stylesheets every UI page loads in `<head>`, typically the web fonts a theme names. Each must be http(s) or root-relative and contain no comma. Helm: `ui.stylesheetUrls`. |
| `DEXAFLOW_UI_ETAG_REVALIDATION` | `ui.etag_revalidation` | bool | `false` | both | Lets the browser revalidate the grid's task summaries (`/ui/grid/ti_summaries/*`), the one UI route that computes an `ETag`: that route answers `Cache-Control: private, no-cache` with `Vary: Authorization, Cookie` instead of `no-store`, so an unchanged poll gets `304 Not Modified` and no body. Every revalidation still runs authentication and authorization. With it on, the browser keeps the last grid body in its private cache after logout (on a shared machine it stays on disk until evicted); it is never shown without a revalidation, so a signed-out user gets `401`, not the cached grid. Off keeps `no-store` on every UI route. Helm: set it through `extraEnv`. |

<!-- END GENERATED configref -->

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
`403` (origin not allowed, or no active linked user in that tenant), `429`
(more than 30 posts a minute from one address) or `500`, with the reason in
the server log and the audit trail (`issuer.login.success` /
`issuer.login.failure`), never in the response body. With an external sign-in
URL set, only a stable code reaches the browser, as described next.

With `DEXAFLOW_AUTH_EXTERNAL_SIGNIN_URL` set, a refusal
sends the browser back to that URL with `303 See Other` instead of leaving it
on a JSON error page, with a stable `error` code appended to the URL's own
query (`https://portal.example.com/sign-in?lang=pt-BR&error=user_not_linked`).
Your page can show a message for the code; it must not post a new token on its
own when `error` is present, or a lasting refusal becomes a redirect loop. The
parameter is not authenticated: anyone can link to your page with any
`?error=` value, so map the known codes below to fixed messages and never print
the value back. An `error` parameter already in the configured URL is replaced,
not duplicated. Request metrics and traces count a redirected refusal under the
status it stands for (`dexaflow.refusal_status` on the span), not as a `303`.

| Refusal | Without the URL | `error` with the URL |
|---|---|---|
| Too many posts from one address | `429` | `rate_limited` |
| `Origin` missing or not in `allowed_origins` | `403` | `origin_not_allowed` |
| No `token` field | `400` | `token_missing` |
| Token already used (or the server could not record its `jti`) | `401` | `token_replayed` |
| Token without `iat`, issued in the future, or living longer than `max_lifetime_seconds` | `401` | `token_lifetime` |
| Tenant claim missing or not in `allowed_tenants` | `401` | `tenant_not_allowed` |
| Any other bad token (signature, issuer, audience, expired, no `sub` or `jti`) | `401` | `invalid_token` |
| Subject not linked to the issuer | `403` | `user_not_linked` |
| Linked user deactivated | `403` | `user_inactive` |
| User linked in another tenant than the token names | `403` | `tenant_mismatch` |
| Token for a tenant posted on another tenant's host ([host binding](#binding-sessions-to-tenant-hosts)) | `403` | `host_tenant_mismatch` |
| Dexaflow could not look up the user or open the session | `500` | `server_error` |
| The issuer's JWKS could not be downloaded, so the token was not checked | `503` (with `Retry-After` when answered as a problem) | `server_error` |

A JWKS outage writes no `issuer.login.failure` audit row: the token was never
checked, so it is not a failed sign-in. It is logged at error level instead.

The code is the only thing added to the URL: never the token, the subject,
the email, the tenant or the origin. The redirect always goes to the
configured URL and nowhere a request names. The audit event and the server log
are the same either way, and the request's log line keeps the refusal's status
as `refusal_status` and its level. A caller whose `Accept` asks for
`application/json` or `application/problem+json` and not `text/html` is a
script rather than a browser, and keeps the problem response with the status
above even with the URL set.

Only pages on `allowed_origins` can post a handoff: browsers send `Origin` on
every cross-site form post, and Dexaflow refuses any other, so a page elsewhere
cannot sign a visitor in as someone else. A token must also have been issued
no later than a minute from now and live no longer than
`max_lifetime_seconds` (120 seconds unless set, at most 600). Each token opens
one session: a second post of the same `jti` is refused until the token
expires. That memory is per server process, so with several replicas a token
could be accepted once by each; the short lifetime is what bounds that
window. Mint each token right before posting it, and never put one in a URL.

### Binding sessions to tenant hosts

When you serve each tenant on its own host (`acme.example.com`,
`globex.example.com`) in front of one Dexaflow, set `host_pattern` so a
session works only on its own tenant's host:

```yaml
auth:
  trusted_issuer:
    # ... the handoff keys above ...
    host_pattern: "{tenant}.example.com"
```

With it set:

- A handoff is accepted only when the request's host is the pattern with the
  token's tenant in place of `{tenant}`. On any other host it answers `403`
  (or redirects to the external sign-in with `error=host_tenant_mismatch` when
  one is set), sets no cookie, and records `issuer.login.failure` with reason
  `host_tenant_mismatch`.
- A request that authenticates with the session cookie is accepted only on the
  host of the session's tenant, however the session was opened (handoff,
  password or single sign-on). On any other host the API answers `401` and the
  UI sends the browser to sign in, and both clear the cookie. A password or
  single sign-on login for the `default` tenant then works only on
  `default.example.com` (with the pattern above).
- A token in the `Authorization` header (the CLI, API clients, a
  [trusted-issuer bearer](#trusted-issuer-bearer-tokens)) is not bound to a
  host: it never travels on its own the way a cookie does.

The host is the request's `Host` header, compared without the port unless the
pattern names one, and ignoring case. Behind an ingress that rewrites `Host`,
list the ingress in `server.trusted_proxies`: the `X-Forwarded-Host` it sets is
then used, and only from those peers. This also stops a session that a page
on one tenant's host plants for its own tenant from being used on another
tenant's host.

### Trusted-issuer bearer tokens

A client that calls the API itself, such as a remote MCP client
([ADR 0050](/project/adrs/0050-mcp-server/) D9), sends a token on every request
and cannot use the one-use browser handoff. With `bearer_audiences` set, the
same trusted issuer can mint tokens for it:

```yaml
auth:
  trusted_issuer:
    # ... the handoff keys above ...
    bearer_audiences: [leoflow-mcp]
    bearer_max_lifetime_seconds: 300   # 0 uses 900
```

A request whose `Authorization: Bearer` token is not one Dexaflow signed is
checked against the issuer's JWKS: signature, `iss`, an `aud` listed in
`bearer_audiences` and not the handoff `audience`, `exp`, an `iat` no later
than a minute from now, `exp - iat` within `bearer_max_lifetime_seconds`, a
`sub`, and an allowed tenant. There is no `jti` rule: the token is reused until
it expires. On every request Dexaflow then reloads the active user linked to
(`issuer:<name>`, `sub`) in the token's tenant, with the roles Dexaflow holds,
so deactivating or unlinking the user ends its access on the next request,
whatever the token's expiry.

Anything wrong answers `401`, the same as a bad Dexaflow token; the reason
stays in the server log. A token that does not name the issuer and a bearer
audience is refused before any signature check. When the token could not be
checked at all, because the issuer's JWKS or Dexaflow's user store is
unreachable, the answer is `503` with `Retry-After`, so the client waits and
retries instead of signing in again. A token that verified but names no active linked user in its tenant is
also recorded in the audit trail as `issuer.bearer.failure`. The bearer is read
from the `Authorization` header only, never from the session cookie, and a
handoff token is never accepted as a bearer, nor a bearer as a handoff.

Dexaflow caches the issuer's keys and downloads the JWKS again only when a
token names a key it does not hold, at most once every 30 seconds, for the
handoff and the bearer together. A forged token therefore cannot make
Dexaflow call your issuer on every request. After you rotate keys, tokens
signed with the new key can be refused for up to 30 seconds; publish the new
key in the JWKS before you sign with it.

Keep bearer tokens short-lived. Dexaflow cannot revoke one before it expires,
only the user behind it; an MCP gateway that mints one per client for a few
minutes and caches it is the intended shape.

#### Scopes

A bearer token's `scope` claim, a space-separated string as in RFC 9068,
narrows what it may do ([ADR 0067](/project/adrs/0067-mcp-run-control-scopes-source-mode/)),
so a user can grant a client less than their own rights:

| Scope | Grants |
|---|---|
| `dexaflow:read` | Every route that needs a `read` permission, plus the control-plane health and version (`/api/v2/monitor/health`, `/api/v2/monitor/executor`, `/api/v2/version`) and the import errors feed. |
| `dexaflow:run` | Triggering a run, setting a run's state, clearing or marking task instances, and pausing or unpausing a DAG. |
| `dexaflow:deploy` | Registering a DAG version (`POST /api/v2/dags/{id}/versions`) and setting the current version (`PUT /api/v2/dags/{id}/currentVersion`), plus the reads a deploy makes: the DAG list, a DAG, its details and its versions ([ADR 0074](/project/adrs/0074-scoped-api-tokens/)). |

- The scope check runs after the role check, and both must pass. A viewer's
  token with `dexaflow:run` still cannot trigger.
- No scope implies another, with one exception: `dexaflow:deploy` opens the
  reads a deploy makes (listed above). A client that runs and reads needs both.
- A token **without** a `scope` claim may only read. A claim that is not a
  string is refused with `401`.
- Every other write (connections, variables, pools, users, deleting a DAG or
  a run, the IDE) is refused to a bearer token whatever its scopes. Use the UI
  or a Dexaflow token for those.
- Routes that check no permission of their own (the UI's dashboard and
  menus, `/ui/auth/me`, and the screens Dexaflow only stubs) are refused to a
  bearer token whatever its scopes, so a route added without a permission
  check never ignores a token's scopes.
- A refusal is `403` with a detail that names the missing scope.
- The same scopes narrow Dexaflow's own API tokens (`dexaflow auth create-token
  --scope deploy`, [ADR 0074](/project/adrs/0074-scoped-api-tokens/)): a deploy
  token carries `dexaflow:deploy` alone. See
  [credentials for the control plane](/operate/cicd-deploy/#credentials-for-the-control-plane).
- Pausing and unpausing is `PATCH /api/v2/dags/{id}` with
  `{"is_paused": true}` or `false`. A body without `is_paused` is refused
  with `400` rather than read as an unpause.

Dexaflow tokens and the sessions the browser handoff opens carry no scopes;
only their roles decide.

#### Deploys through the issuer only

An operator whose deployments belong to a platform (a CD system, a
review-and-approve service) can make that platform the only way to deploy, so
nobody skips its checks and audit by calling the API directly. Set a deploy
audience, minted by the issuer for that platform only:

```yaml
auth:
  trusted_issuer:
    # ... the handoff keys above ...
    deploy_audience: dexaflow-deploy
```

Then `POST /api/v2/dags/{id}/versions`, its dry run
(`POST /api/v2/dags/{id}/versions/dry_run`),
`PUT /api/v2/dags/{id}/currentVersion` (rollback) and
`DELETE /api/v2/dags/{id}` (an undeploy) accept only a bearer of the trusted
issuer whose `aud` is `deploy_audience` alone and that carries
`dexaflow:deploy`, and the linked user's roles must still allow it (`deploy`
or `write` on dags; `write` on dags to delete). Every other credential is refused with `403`,
a detail saying that deploys go through the trusted issuer and the header
`Dexaflow-Deploy-Via: trusted-issuer`, whatever its roles and scopes: a UI
session, a password login token, an API token (`dexaflow auth create-token
--scope deploy` included), a bearer of one of the `bearer_audiences`, a token
whose `aud` names the deploy audience next to another audience, the session a
handoff opens, and the `auth.dev_no_auth` principal. `dexaflow deploy` turns
that `403` into a message that says so. Reads, run control and every other
route are unchanged. Pausing and unpausing a DAG (`PATCH /api/v2/dags/{id}`)
stays under roles and the `dexaflow:run` scope on purpose: it is run control,
not a deploy, so an operator can still stop a DAG without the platform.

- The deploy audience is a bearer audience of its own: it turns the bearer
  checks above on even with no `bearer_audiences`, and its tokens follow the
  same rules (lifetime, linked user, tenant, no `jti` rule).
- A deploy-audience token gets the scopes it carries, like any bearer. Mint
  it with `dexaflow:deploy` alone, so it reaches only the deploy routes and
  the reads a deploy makes; with more scopes it can do more, as any bearer can.
- A deploy token's `aud` is the deploy audience alone (a string, or an array
  of that one entry). A token minted for the deploy audience and another
  bearer audience, such as `[leoflow-mcp, dexaflow-deploy]`, is an ordinary
  bearer of that other audience and is refused on these routes, so the
  client it was minted for cannot deploy with it. Audiences are compared
  exactly, case included.
- `deploy_audience` must differ from `audience` and from every
  `bearer_audiences` entry, so neither a handoff token nor another client's
  bearer is ever a deploy token, and has no surrounding spaces. The server
  refuses to start otherwise, and when the trusted issuer itself is not
  configured. The Helm chart refuses to render `deployAudience` without
  `issuer`.
- Nothing else registers a version or changes the current version: the UI's
  version screens only read, and the MCP server has no deploy tool (it reads
  and controls runs through the same API with its client's token).

### Operator service API

An operator that serves several organizations from one Dexaflow (a hosting
provider, an internal platform team) creates tenants and their users from its
own automation instead of writing to the database. With `auth.service_token`
set, the endpoints below accept `Authorization: Bearer <service token>`; a
user session never reaches them. Each is idempotent.

`PUT /api/v2/service/tenants/{tenant}` with an optional
`{"display_name": "Acme Corp"}` creates the tenant (1-63 lowercase letters,
digits or `-`) with the same built-in roles, role permissions and default pool
as the `default` tenant, copied from it so every tenant's ladder stays equal.
It answers `201` when the tenant is new and `200` when it already existed; a
second call fills in anything missing and, apart from `default_pool_slots`
below, changes nothing else.

The same body may carry `"default_pool_slots": 8` to size the tenant's
`default_pool`, the slot cap every task without an explicit pool shares within
the tenant. Without it a new tenant gets the `default` tenant's size (128
unless an operator changed it), which on an engine shared by many tenants lets
each of them run that many tasks at once. Given on a later call, it re-sizes
the existing pool, including a size a tenant admin set through the pools API,
so an automation that re-applies its tenants should send the size it wants to
keep; left out, the pool is not touched. It must be a whole number from 1 to
2147483647 (`400` otherwise), and the audit entry records it. It sets the pool's size,
not a ceiling on the tenant: a tenant role that may write pools (`operator`,
`admin`) can still resize it or create other pools. Pools apply to the cluster
edition only; Lite ignores the value.

The same body may also carry tenant limits, each a whole number from 0 to
2147483647 (`400` otherwise), where `0` means unlimited:

| Field | Limit | Enforced when |
|---|---|---|
| `max_dags` | DAGs the tenant may register | a DAG version is registered (`POST /api/v2/dags/{dag_id}/versions`) for a DAG the tenant does not have yet; new versions of its existing DAGs are always accepted |
| `max_runs_per_day` | DAG runs, manual and scheduled together, the tenant may create in one UTC calendar day (00:00 to 24:00 UTC) | a run is triggered (`POST /api/v2/dags/{dag_id}/dagRuns`) or the scheduler creates a scheduled run |
| `min_schedule_interval_seconds` | shortest gap a DAG's schedule may leave between two consecutive runs | a DAG version is registered |
| `max_task_pool_slots` | largest `pool_slots` (the task's `size` in `dexaflow.yaml`, [ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/)) any task of a DAG may have | a DAG version is registered; the detail names the task, its size and the limit |

A tenant created without limits has none, and a later call changes only the
limits it carries: one left out keeps its value, so existing automation that
sends only `display_name` or `default_pool_slots` is unaffected. The audit
entry records each limit given.

A request a limit refuses answers `403` with a detail that names the limit,
for example `the tenant reached its limit max_runs_per_day of 50 for today
(UTC)`. A scheduled run the daily limit refuses is skipped, not failed: the
scheduler logs one warning per tenant and day and counts it in
`dexaflow_scheduler_decisions_total{decision_type="tenant_daily_run_cap"}`, and the slot
is created on a later tick once the UTC day turns (with `catchup`, the slots
missed in between are created too, and count against the new day).

How each limit is measured:

- The daily run count is kept on the tenant and charged in the same
  transaction that creates the run, so concurrent triggers can never take the
  tenant past the limit, and deleting a DAG or a run does not give runs back.
  Counting starts when the limit is set; runs created earlier that day do not
  count.
- The schedule gap is computed from the cron expression: the shortest gap
  between consecutive fire times inside a day and across days, including the
  days it skips (`0 9 * * 1-5` is 24 hours, `0,59 0,23 * * *` is one minute,
  across midnight). `@every <duration>` is its duration. Manual DAGs, `@once`
  and `@continuous` are not limited. Times are UTC; with `CRON_TZ`, the two
  days a year the clocks change differ: a gap that spans the change is an hour
  shorter or longer, a time inside the repeated hour fires twice, one hour
  apart (`30 1 * * *` in `Europe/London` fires at 00:30 and 01:30 UTC on the
  last Sunday of October), and a time inside the skipped hour does not fire
  that day.
- `max_task_pool_slots` compares each task's `pool_slots` (1 when unset). A
  platform that sizes each tenant's `default_pool_slots` sets it to the same
  number, so a task that could never fit the pool is refused when it is pushed
  instead of waiting forever.
- `max_dags` counts the tenant's DAGs. Two different new DAGs registered at
  the same moment while the tenant is one below the limit can both be
  accepted; it is checked, not locked, like `max_active_runs`.

Lowering a limit never removes anything: a tenant above a new `max_dags`
keeps its DAGs (and can update them) but cannot add more, and a DAG whose
schedule is now too frequent, or whose task is now larger than
`max_task_pool_slots`, keeps running until its next registration.

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

`PUT /api/v2/service/tenants/{tenant}/suspension` with
`{"mode": "hold", "reason": "contract ended"}` suspends the tenant. From then
on it gets no new run: a triggered run answers `409` with the reason, for
example `the tenant is suspended: contract ended`, and the scheduler leaves
the tenant's DAGs out of its list. Work already queued or running finishes.
The tenant's DAGs keep their own paused flag, so nothing about them changes
and lifting the suspension restores exactly what the tenant had. Its users
still sign in and read; DAG versions can still be registered.

`hold` is the only mode today; `drain` and `stop` answer `400`. The reason is
optional, at most 500 bytes, and is shown to the tenant's users in the refusal,
so it should say what they can do, not why internally. A second call changes
the reason and keeps the time the suspension started. The answer, and
`GET` on the same path, is
`{"tenant": "acme", "suspended": true, "mode": "hold", "reason": "...", "suspended_at": "..."}`,
or `{"tenant": "acme", "suspended": false}`. `DELETE` on the same path lifts
the suspension and answers `204`, also when the tenant was not suspended; each
DAG's due slot is created on the next scheduler tick, and with `catchup` the
slots missed while suspended are created too. An unknown tenant is `404`.

A suspension that lands while the scheduler is creating runs for the tenant is
caught when the run is created: the run is not created, the scheduler logs one
warning for the tenant and counts it in
`dexaflow_scheduler_decisions_total{decision_type="tenant_suspended"}`.

`GET /api/v2/service/usage?after=<cursor>&limit=<n>` reads the usage events
of every tenant ([ADR 0096](/project/adrs/0096-task-attempt-usage-events/)):
one event each time a task attempt reaches an attempt-ending state
(`success`, `failed`, `skipped`, `upstream_failed`, `removed`,
`up_for_reschedule`), written by the database in the transaction that made
the change. It answers `404` while `usage.events.enabled` is off. The answer
is `{"events": [...], "next_cursor": "...", "has_more": true}`, at most
`limit` events (default 500, at most 5000); each event carries `cursor`,
`tenant`, `dag_id` and `run_id` (empty once the run is deleted),
`task_instance_id`, `task_id`, `map_index`, `try_number`, `attempt_epoch`,
`revision`, `state`, `previous_state`, `pool_slots` (the task's size),
`started_at` and `ended_at` (null when the task never started or has no end
time), `last_failure_kind` (`infra` when an infrastructure fault, not the
task, failed it; left out otherwise), and `recorded_at`. The duration is `ended_at` minus `started_at` when both are
set.

How to consume it:

- Start without `after`, then pass the `next_cursor` of each answer, also of
  an empty one: it is the same cursor, so it can be stored every time. The
  cursor is opaque.
- Delivery is at least once. The key of an event is `task_instance_id`,
  `try_number`, `attempt_epoch` and `revision`: dedupe on it, and keep the
  highest revision of an attempt as its state, since a user marking a task
  `success` or `failed` after it ended writes the next revision. To bill per
  try rather than per execution, group on `task_instance_id` and
  `try_number`. Each poke of a sensor in reschedule mode is its own attempt.
- An event is served only once every transaction older than it has
  finished, so a later page never brings an event that sorts before a cursor
  already returned. A long transaction anywhere in the database delays
  events, never loses them; `dexaflow_usage_events_lag_seconds` shows by how
  much.
- Events are deleted after `retention.usage_events_days` (default 30). A
  cursor older than what is kept answers `410` with `oldest_cursor`: the
  events between the two are lost, and the consumer resumes from there.

Every call that reaches the database is recorded in the audit trail, as
`service.tenant.ensure` (with whether the tenant was created),
`service.user.ensure` (with the subject, email, roles and whether the user was
created), `service.tenant.suspend` (with the mode and reason) and
`service.tenant.resume`, each with outcome `success` or `failure`.

The service token is a root-level credential: whoever holds it can create
tenants and grant any role, `admin` included, in every tenant the trusted
issuer covers. Keep it in a Secret, give it only to the automation that
provisions tenants, and rotate it by changing the Secret and restarting. It
also reads the usage of every tenant; the usage route is a read and is not
audited.

### Alert destinations

An on-failure alert ([alerting](/author-dags/alerting/)) is posted by the control
plane to the URL, with the headers, of a connection the DAG's tenant manages.
When tenants that do not trust each other share one engine, that URL is
untrusted input: it can name the control plane's own loopback, a private
service in the cluster, or the cloud metadata endpoint.

Set `scheduler.alerts.block_private_destinations: true` (env
`DEXAFLOW_SCHEDULER_ALERTS_BLOCK_PRIVATE_DESTINATIONS`, chart
`config.alerts.blockPrivateDestinations`) to refuse those destinations. The
check runs on the address the control plane is about to connect to, after DNS
resolution, so a host name that resolves to an internal address is refused even
if it resolved to a public one earlier (DNS rebinding), and every redirect hop
is checked the same way (at most three redirects are followed). A NAT64
(`64:ff9b::/96`) or 6to4 address is checked as the IPv4 address it carries, so
an IPv6-only cluster behind DNS64 still reaches a public IPv4-only endpoint.
The block also covers the Azure host endpoint `168.63.129.16`, which looks
public but is node-local. With the block on, alert requests are dialed directly
and do not use the `HTTP_PROXY` / `HTTPS_PROXY` environment, since through a
proxy the real destination could not be checked. A refused alert is logged and
counted as a failed delivery, like any other send error.

If an alert endpoint legitimately lives on a private network (an on-premises
chat server, for example), list its range in `scheduler.alerts.allowed_cidrs`
(chart `config.alerts.allowedCIDRs`). A range broad enough to include loopback
or a metadata endpoint is accepted but logged as a warning at startup.

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

## Other environment variables

The variables below are read directly by one binary instead of through the
config file, so they have no config key. Like every other variable, each also
works with the `LEOFLOW_` prefix.

### Server process (`dexaflow-server`)

<!-- BEGIN GENERATED configref env server -->

| Variable | Default | Purpose |
|---|---|---|
| `DEXAFLOW_CONFIG` | _(empty)_ | Path of the YAML config file `dexaflow-server` reads before the environment. Unset, the server runs on built-in defaults and environment variables alone. The Helm chart points it at a mounted ConfigMap when it has map-valued keys to deliver (`auth.oidc.tenant_claims`, `auth.oidc.role_mappings`). |
| `DEXAFLOW_LITE_VENVS_ROOT` | _(empty)_ | Lite only, set by `dexaflow lite`: the directory holding one virtual environment per DAG (`<root>/<dag_id>/bin/python`). The subprocess executor runs each task with its DAG's interpreter. |

<!-- END GENERATED configref -->

### MCP server (`dexaflow-mcp`)

Each variable is the default of the flag named in its row; see the
[MCP server](/reference/mcp/) page.

<!-- BEGIN GENERATED configref env mcp -->

| Variable | Default | Purpose |
|---|---|---|
| `DEXAFLOW_SERVER_URL` | `http://localhost:8080` | Control plane base URL (`--server`). The CLI reads the same variable for `server_url`. |
| `DEXAFLOW_TOKEN` | _(empty)_ | Bearer token for the stdio transport. The http transport ignores it and uses each request's own bearer. The CLI reads the same variable for `token`. |
| `DEXAFLOW_MCP_TRANSPORT` | `stdio` | `stdio` or `http` (`--transport`). |
| `DEXAFLOW_MCP_LISTEN` | `:9099` | Listen address of the http transport (`--listen`). |
| `DEXAFLOW_MCP_UI_BASE_URL` | _(empty)_ | URL of the Dexaflow UI; when set, results carry links into it (`--ui-base-url`). |
| `DEXAFLOW_MCP_RUN_CONTROL` | `false` | Registers the run control tools (`--run-control`). Takes `true`/`false`, `1`/`0` or `t`/`f`; any other value stops the server. |
| `DEXAFLOW_MCP_PLAN_KEY_FILE` | _(empty)_ | File holding the key (at least 32 bytes) that signs run control plans (`--plan-key-file`). Required by `--run-control` on the http transport, the same file on every replica. |
| `DEXAFLOW_MCP_RESOURCE` | _(empty)_ | http transport: this endpoint's URL as clients reach it (`--resource`). |
| `DEXAFLOW_MCP_AUTHORIZATION_SERVERS` | _(empty)_ | http transport: comma-separated issuer URLs of the OAuth authorization servers for `--resource` (`--authorization-servers`). |
| `DEXAFLOW_MCP_SCOPES` | _(empty)_ | http transport: comma-separated scopes advertised in the protected resource metadata (`--scopes`). |

<!-- END GENERATED configref -->

### CLI (`dexaflow`)

The CLI reads `~/.dexaflow/config.yaml` (written by `dexaflow auth login` and
`dexaflow setup`). Keys with a variable can also be set from the environment,
which overrides the file; flags override both.

<!-- BEGIN GENERATED configref cli -->

| Key | Variable | Type | Default | Purpose |
|---|---|---|---|---|
| `server_url` | `DEXAFLOW_SERVER_URL` | string | `http://localhost:8080` | The control plane base URL used by push and auth create-token. |
| `token` | `DEXAFLOW_TOKEN` | string | _(empty)_ | The JWT bearer token persisted by `dexaflow auth login` and used by push and deploy when no --token flag or DEXAFLOW_TOKEN env is set. |
| `log_level` | `DEXAFLOW_LOG_LEVEL` | string | `info` | Reserved for CLI log verbosity (not yet wired). |
| `registry` | `DEXAFLOW_REGISTRY` | string | _(empty)_ | Reserved for the image registry used by image build (ADR 0003). |
| `parser_cmd` | `DEXAFLOW_PARSER_CMD` | string | `python3 -m leoflow_parser` | The command used to invoke the Python parser from compile. |
| `workspace` | _(config file only)_ | string | _(empty)_ | The default directory holding the user's DAG projects. |
| `lite_executor` | _(config file only)_ | string | _(empty)_ | The chosen executor: "subprocess" (local) or "k8s" (mini-cluster). |
| `lite_port` | _(config file only)_ | int | _(empty)_ | The UI/API port for the Lite control plane. |
| `admin_email` | _(config file only)_ | string | _(empty)_ | The Lite admin login created at bootstrap. |
| `admin_password_hash` | _(config file only)_ | string | _(empty)_ | The bcrypt hash of the generated admin password; the plaintext is shown once at setup and never stored (Lite only). |
| `jwt_secret` | _(config file only)_ | string | _(empty)_ | The per-install Lite JWT signing secret, generated by `dexaflow setup` (random, 64 hex chars) and persisted alongside the admin hash. |
| `secret_key` | _(config file only)_ | string | _(empty)_ | The per-install key encrypting connection passwords and extra at rest, generated by `dexaflow setup` (random, 64 hex chars). Lite reads it from this file only: an exported DEXAFLOW_SECRET_KEY does not replace it. |
| `secret_key_previous` | _(config file only)_ | string | _(empty)_ | A comma-separated list of decrypt-only predecessors. `dexaflow lite migrate-key` writes it while a migration is unfinished and removes it once every stored secret was moved and re-checked; an operator may also set it by hand. |

<!-- END GENERATED configref -->

The CLI also reads these variables, which have no config key:

<!-- BEGIN GENERATED configref env cli -->

| Variable | Default | Purpose |
|---|---|---|
| `DEXAFLOW_USERNAME` | _(empty)_ | Default of `--username` for `dexaflow auth create-token`. |
| `DEXAFLOW_PASSWORD` | _(empty)_ | Default of `--password` for `dexaflow auth create-token` and `dexaflow auth create-user`. Prefer `--password-stdin`. |
| `DEXAFLOW_TARGET` | _(empty)_ | Named control plane a command uses when `--target` is not given; it beats the target selected for the workspace, and `--server` or `--server-url` beat it. See [Workspaces and targets](/operate/workspaces-and-targets/). |
| `DEXAFLOW_OIDC_TOKEN` | _(empty)_ | The OIDC token a CI provider minted for the job, which `dexaflow auth ci-token` exchanges for a short-lived deploy session. Pipelines written by `dexaflow ci init` set it when the control plane offers a CI OIDC issuer. See [Deploying without a stored token (CI OIDC)](/operate/cicd-deploy/#deploying-without-a-stored-token-ci-oidc). |
| `DEXAFLOW_TRUSTED_WORKSPACES` | _(empty)_ | Workspace roots whose `dexaflow.workspace.yaml` is read even when another user owns it, separated by the OS path-list separator (`:` on Linux and macOS), or `*` for every workspace. A CI container that runs as root over a checkout the runner owns typically sets `*`. See [Workspaces and targets](/operate/workspaces-and-targets/). |
| `DEXAFLOW_BUILD_INDEX_URL` | _(empty)_ | Not read from your environment: a build by `dexaflow deploy` or `dexaflow compile --build` sets it on the container tool's process only, to the package index URL with its credentials, and the build reads it as the `dexaflow_index_url` build secret ([ADR 0088](/project/adrs/0088-build-args-secrets-and-package-index/)). See [Builders](/operate/builders/). |

<!-- END GENERATED configref -->

### Set by Dexaflow on task processes

The control plane sets these on every task pod (or, in Lite, on every agent
process) from the settings above. They are listed so you can recognize them
when debugging a task; do not set them yourself.

<!-- BEGIN GENERATED configref env task -->

| Variable | Default | Purpose |
|---|---|---|
| `DEXAFLOW_PYTHON` | `python` | Interpreter the agent runs Python tasks with. The Lite subprocess executor sets it to the DAG's virtual environment; task images leave it unset. Not passed on to the task. |
| `DEXAFLOW_CONTROL_PLANE_ADDR` | _(empty)_ | gRPC address of the control plane the agent dials, from `executor.agent_control_plane_addr` (or `server.grpc_addr`). |
| `DEXAFLOW_AGENT_TOKEN` | _(empty)_ | The agent's bearer token under the `envvar` agent token transport. |
| `DEXAFLOW_AGENT_TOKEN_TRANSPORT` | _(empty)_ | `exchange` when `auth.agent_token_transport` is `exchange`: the agent then reads a projected ServiceAccount token from `DEXAFLOW_AGENT_TOKEN_PATH` and exchanges it for its bearer. |
| `DEXAFLOW_AGENT_TOKEN_PATH` | _(empty)_ | Path of the projected ServiceAccount token under the `exchange` transport. |
| `DEXAFLOW_AGENT_INSECURE` | _(empty)_ | `false` makes the agent dial the control plane over TLS. Any other value, or unset, dials in plaintext (the Lite loopback). Set to `false` when the server has a gRPC certificate. |
| `DEXAFLOW_AGENT_TLS_CA` | _(empty)_ | Path of the PEM CA bundle the agent verifies the control plane's certificate with, mounted from `executor.agent_tls_ca_configmap`. |
| `DEXAFLOW_TERMINATION_LOG_PATH` | _(empty)_ | File the agent writes its classified outcome to, the container termination message the reconciler reads when the agent could not report. |
| `DEXAFLOW_TASK_INSTANCE_ID` | _(empty)_ | Id of the task instance the pod runs. Visible to task code. |
| `DEXAFLOW_STAGING_DIR` | _(empty)_ | Mount path of the run's staging volume when the DAG enables `staging` ([ADR 0022](/project/adrs/0022-ephemeral-per-run-staging-volume/)). Visible to task code. |
| `DEXAFLOW_TASK_PGID_FILE` | _(empty)_ | Lite only: file the agent records its task's process group in, so the server can stop a task its agent left behind. |
| `DEXAFLOW_WARM_WORKER` | _(empty)_ | `1` runs the agent as a warm worker that serves attempt after attempt ([ADR 0058](/project/adrs/0058-warm-worker-pools/)). |
| `DEXAFLOW_DAG_VERSION_ID` | _(empty)_ | DAG version a warm worker serves. Required with `DEXAFLOW_WARM_WORKER`. |
| `DEXAFLOW_POD_NAME` | _(empty)_ | The warm worker's own pod name, from the downward API. |
| `DEXAFLOW_MAX_ATTEMPTS_PER_WORKER` | _(empty)_ | Warm worker cap from `execution.max_attempts_per_worker`. `0` disables the bound. |
| `DEXAFLOW_MAX_WORKER_LIFETIME_SECONDS` | _(empty)_ | Warm worker lifetime cap from `execution.max_worker_lifetime`, in seconds. `0` disables the bound. |
| `DEXAFLOW_WORKER_IDLE_TTL_SECONDS` | _(empty)_ | Idle time after which a warm worker exits, from `execution.worker_idle_ttl`, in seconds. `0` disables the bound. |
| `DEXAFLOW_ATTEMPT_WATCHDOG_SECONDS` | _(empty)_ | Longest one attempt may run on a warm worker before the worker exits, from `auth.max_attempt_credential_lifetime`, in seconds. |
| `DEXAFLOW_WARM_ATTEMPT_HOME` | _(empty)_ | `1` gives every attempt on a warm worker its own `HOME` under the scratch directory, set when `execution.warm_read_only_root_filesystem` is on. |
| `DEXAFLOW_UPSTREAM_XCOM` | _(empty)_ | JSON map of upstream task id to return value, read by the runtime's `ti.xcom_pull`. |
| `DEXAFLOW_XCOM_<NAME>` | _(empty)_ | One XCom input bound to the operator parameter `<name>`, as JSON. |
| `DEXAFLOW_PROJECT_CONFIG_JSON` | _(empty)_ | The project's `dexaflow.yaml`, as JSON, passed by `dexaflow compile` to the DAG parser. |

<!-- END GENERATED configref -->

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
