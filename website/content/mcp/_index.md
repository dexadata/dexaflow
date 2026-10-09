---
title: MCP server
linkTitle: MCP
weight: 25
description: The Dexaflow MCP server — let an AI agent read, reason about, and diagnose your DAGs, runs, and logs over the Model Context Protocol.
cascade: { type: docs }
menu:
  main:
    weight: 25
---

`dexaflow-mcp` is Dexaflow's **Model Context Protocol** server
([ADR 0050](/project/adrs/0050-mcp-server/)). Point an LLM agent — Claude Desktop,
Claude Code, or any MCP client — at your control plane and it can read and reason
about your DAGs, runs, task instances, and logs: *"which task failed in last
night's `sales` run, and why?"*

{{% pageinfo %}}
The MCP server is **read-only** and carries **the caller's own token** — its blast
radius equals your existing API rights. No tool triggers, clears, edits, or deletes
anything. See [the security model](#security-the-blast-radius) below.
{{% /pageinfo %}}

<div class="lf-cards">
  <a class="lf-card lf-card--hero" href="#running-it">
    <span class="lf-card__badge">Start here</span>
    <span class="lf-card__icon"><i class="fa-solid fa-play"></i></span>
    <span class="lf-card__title">Run the server</span>
    <span class="lf-card__desc">Export a token, launch <code>dexaflow-mcp</code> over stdio, and you are talking MCP in two commands.</span>
    <span class="lf-card__more">Run it now →</span>
  </a>
  <a class="lf-card" href="#tools">
    <span class="lf-card__icon"><i class="fa-solid fa-screwdriver-wrench"></i></span>
    <span class="lf-card__title">Tools</span>
    <span class="lf-card__desc">High-value, read-only actions — list DAGs, diagnose a run in one call, search a task log.</span>
    <span class="lf-card__more">See the tools →</span>
  </a>
  <a class="lf-card" href="#resources">
    <span class="lf-card__icon"><i class="fa-solid fa-database"></i></span>
    <span class="lf-card__title">Resources</span>
    <span class="lf-card__desc">Addressable URIs the agent can read — run detail, task instances, sanitized logs and DAG source.</span>
    <span class="lf-card__more">Browse resources →</span>
  </a>
  <a class="lf-card" href="#wiring-an-mcp-client-claude-desktop">
    <span class="lf-card__icon"><i class="fa-solid fa-plug"></i></span>
    <span class="lf-card__title">Wire a client</span>
    <span class="lf-card__desc">Drop <code>dexaflow-mcp</code> into Claude Desktop or Claude Code and ask <em>"list my DAGs"</em>.</span>
    <span class="lf-card__more">Connect a client →</span>
  </a>
</div>

## Why it matters

Operating an orchestrator is a diagnosis loop: something failed, and you chase it
across runs, task instances, and logs. The MCP server hands that loop to an agent.
Instead of clicking through the grid or chaining four API calls, you ask a question
in plain language and the agent walks the same read surface you would — safely,
because it can only see what your token already authorizes.

It is a **separate, read-only process**. It reaches the control plane only through
the Airflow-compatible `/api/v2` (via the typed [`pkg/client`](/reference/go/)),
carrying **the caller's token**, and is never compiled into `leoflow-server`. It
holds no database, Redis, or Kubernetes access of its own. Built on the official
[`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).

## Running it

`dexaflow-mcp` ships alongside the other binaries (installed by the one-command
[install](/get-started/installation/), or `go build ./cmd/dexaflow-mcp`). It has two
transports.

{{< tabpane text=true >}}
{{% tab header="stdio (default) — local Lite dev" %}}

```bash
export DEXAFLOW_SERVER_URL=http://localhost:8088     # your Lite control plane
export DEXAFLOW_TOKEN="$(dexaflow auth create-token \
  --server http://localhost:8088 \
  --username admin@leoflow.local --password <your-admin-password>)"
dexaflow-mcp                                          # speaks MCP over stdin/stdout
```

On the **stdio** transport the process token **is** the caller's identity: the
server reads it once from `DEXAFLOW_TOKEN` and every `/api/v2` call carries it. Logs
go to **stderr** — stdout is the MCP protocol channel and carries nothing else.
This is the transport an MCP client (Claude Desktop / Code) launches for you; you
rarely run it by hand.
{{% /tab %}}
{{% tab header="Streamable HTTP — the Pro service" %}}

```bash
dexaflow-mcp --transport http --listen :9099 --server https://leoflow.internal
```

The HTTP transport serves `POST /mcp` (plus `GET /healthz`) and is **stateless**:
identity is a **per-request bearer**, never an ambient process token (ADR 0050 D9).
A request without an `Authorization: Bearer <jwt>` header is refused — the server
never falls back to a process credential. `DEXAFLOW_TOKEN` is ignored in this mode.
{{% /tab %}}
{{< /tabpane >}}

### Flags and environment

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `--server` | `DEXAFLOW_SERVER_URL` | `http://localhost:8080` | Control-plane base URL (`/api/v2` origin). For Lite, use `http://localhost:8088`. |
| `--transport` | `DEXAFLOW_MCP_TRANSPORT` | `stdio` | `stdio` or `http`. |
| `--listen` | `DEXAFLOW_MCP_LISTEN` | `:9099` | Listen address for the `http` transport. |
| `--run-control` | `DEXAFLOW_MCP_RUN_CONTROL` | off | Register the [run control tools](#run-control). The env variable takes `true`/`false`, `1`/`0` or `t`/`f` in any case; any other value stops the server (exit 2). |
| `--plan-key-file` | `DEXAFLOW_MCP_PLAN_KEY_FILE` | — | File holding the key, at least 32 bytes, that signs run control plans. Required with `--run-control` on the `http` transport, and the same file on every replica. On stdio a random key is used when unset. |
| `--ui-base-url` | `DEXAFLOW_MCP_UI_BASE_URL` | — | Address of the Dexaflow UI, such as `https://flow.example.com`. When set, results carry `web_url` links into it (see [Links into the UI](#links-into-the-ui)). Must be an absolute `http` or `https` URL without a query or fragment. |
| `--resource` | `DEXAFLOW_MCP_RESOURCE` | — | `http` only. This endpoint's URL as clients reach it, such as `https://dexaflow.example.com/mcp`. With `--authorization-servers`, turns on [OAuth sign-in discovery](#oauth-sign-in-discovery). `https`, or `http` on a loopback host; no query or fragment. |
| `--authorization-servers` | `DEXAFLOW_MCP_AUTHORIZATION_SERVERS` | — | `http` only. Comma-separated issuer URLs of the OAuth authorization servers that mint tokens for `--resource`. Required with it. |
| `--scopes` | `DEXAFLOW_MCP_SCOPES` | — | `http` only. Comma-separated scopes advertised as `scopes_supported`. Omitted when empty. |
| — | `DEXAFLOW_TOKEN` | — | Bearer JWT for the **stdio** transport (ignored on `http`). |
| `--version` | — | — | Print the version and exit. |

### OAuth sign-in discovery

MCP clients that sign in with OAuth, as the MCP authorization specification
describes (claude.ai connectors and ChatGPT among them), find the
authorization server from the MCP server itself. With `--resource` and
`--authorization-servers` set, the `http` transport:

- serves the protected resource metadata ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728))
  at the path derived from the resource (`/.well-known/oauth-protected-resource/mcp`
  for a resource ending in `/mcp`) and at `/.well-known/oauth-protected-resource`:

  ```json
  {
    "resource": "https://dexaflow.example.com/mcp",
    "authorization_servers": ["https://auth.example.com"],
    "scopes_supported": ["dexaflow:read"],
    "bearer_methods_supported": ["header"]
  }
  ```

- answers a request to `/mcp` without a bearer with `401` and
  `WWW-Authenticate: Bearer resource_metadata="<metadata URL>"`, which starts
  the client's sign-in.

```bash
dexaflow-mcp --transport http --server https://dexaflow.internal \
  --resource https://dexaflow.example.com/mcp \
  --authorization-servers https://auth.example.com \
  --scopes dexaflow:read
```

Dexaflow is not the authorization server. It points clients at yours, and the
tokens they bring are verified by `/api/v2` on every call like any other
bearer, so they must be ones the control plane accepts, such as tokens of
your [trusted issuer](/reference/configuration/#trusted-issuer-handoff) for
one of its bearer audiences. Without these flags nothing changes.

## Auth: getting a token

The MCP **passes the caller's Dexaflow JWT through** to `/api/v2` and never mints one
(ADR 0050 D9). Obtain one from the control plane with your admin login:

```bash
dexaflow auth create-token \
  --server http://localhost:8088 \
  --username admin@leoflow.local \
  --password <your-admin-password>
```

Use that token as `DEXAFLOW_TOKEN` (stdio) or as the request `Authorization: Bearer`
header (http). Tokens are short-lived; treat them as secrets (never log them, never
commit them).

## Tools

Tools are the surface most MCP clients render first (ADR 0050 D7), so the server
leads with a few high-value ones. All are **read-only**.

| Tool | What it does | Key inputs |
|---|---|---|
| `list_dags` | List registered DAGs with their paused state (compact). | `limit` (default 25, max 200), `tag` |
| `diagnose_run` | Diagnose one DAG run in a single call — its state, which task instances failed, a truncated tail of each failed task's log, the tasks each failure blocks downstream, and any dbt models involved. Replaces chaining list-runs → get-run → list-tasks → get-logs. | `dag_id`, `run_id`, `log_tail_lines` (default 40, max 200) |
| `search_logs` | Search one task attempt's log for a case-insensitive substring, returning matching lines with line numbers instead of the whole log. | `dag_id`, `run_id`, `task_id`, `try_number` (default 1), `query`, `max_matches` (default 20, max 100) |

## Run control

With `--run-control`, the server also registers tools that change state
([ADR 0067](/project/adrs/0067-mcp-run-control-scopes-source-mode/)). Without the
flag they do not exist at all. Each call uses the caller's token, so the control
plane's roles decide, and for a trusted-issuer bearer token, its `dexaflow:run`
scope too.

| Tool | What it does | Plan first when |
|---|---|---|
| `trigger_run` | Starts a run now (`dag_id`, optional `conf` and `note`). | never |
| `clear_task` | Clears task instances of a run so they run again: `dag_id`, `run_id`, optional `task_ids`, `include_downstream`, `include_upstream`, `only_failed` (default true) and `run_on_latest_version`. It previews the clear first. | the clear touches more than one task instance |
| `pause_dag` | Pauses a DAG. | never |
| `unpause_dag` | Unpauses a DAG. | the DAG has a schedule, since unpausing starts runs |
| `apply_plan` | Carries out a plan (`plan_id`). | — |

A call that needs a plan changes nothing. It returns what would happen and a
`plan_id`, and the model is told to show it to the user and to call
`apply_plan` only if they agree.

- **A plan carries the exact operation**, signed with the plan key. The model
  cannot change it, only hand it back.
- **A plan lasts 10 minutes** and is bound to the caller. Over HTTP that is
  the bearer's issuer (`iss`), subject (`sub`), tenant (`tenant_id`, `tenant`
  or `tid`), client (`azp`, else `client_id`) and scope set, so another user,
  tenant, client or narrower token cannot apply it. A token without `iss`,
  `sub` or a tenant cannot plan. A refreshed token, or one whose roles or
  email changed, still applies the plan; the control plane checks the
  caller's permissions on the apply call itself.
- **Apply checks again before acting.** If the task instances are no longer in
  the states the plan showed, or the DAG's paused flag or schedule changed,
  `apply_plan` refuses and asks for a new plan. A plan therefore applies once.
- **A clear over more than 200 task instances is refused.** Narrow it with
  `task_ids` or use the UI.
- **A clear runs only what its preview showed.** The clear that follows a
  preview names the previewed tasks and expands no further, so a task that
  fails in between is not cleared unseen.

Generate the plan key once and give every replica the same file:

```bash
openssl rand -base64 48 > plan.key
chmod 600 plan.key
dexaflow-mcp --transport http --run-control --plan-key-file plan.key
```

Changing the key invalidates the plans made in the last 10 minutes, nothing
else.

## Resources

Addressable, read-only resources — the agent picks the URI; the control plane
authorizes each read via the pass-through token, so a resource can only surface what
the caller may already see. Log and source reads are sanitized and truncated by
construction (untrusted content, ADR 0050 D10).

| Resource URI | Returns |
|---|---|
| `dag://list` | All registered DAGs (compact). |
| `run://detail/{dag_id}/{run_id}` | A DAG run's detail (state, type, timing). |
| `task://instances/{dag_id}/{run_id}` | The task instances of a run (state, try, duration). |
| `log://task/{dag_id}/{run_id}/{task_id}/{try_number}` | A task attempt's log, last lines only, sanitized. |
| `dag://source/{dag_id}` | The DAG's `dag.py` source, sanitized and size-capped. |
| `dag://spec/{dag_id}` | The compiled `dag.json` artifact (the structured task graph). |
| `health://control-plane` | Control-plane health: component status, executor capability, and version. |

## Prompts

Prompts are ready-made requests a client offers its user, often as a slash command.
Both are **read-only**: they read the control plane with the caller's token to find
the runs to look at, then ask the model to make the tool calls they name.

| Prompt | What it asks for | Arguments |
|---|---|---|
| `diagnose_latest_failure` | Finds the most recent failed run (by end time) and asks the model to call `diagnose_run` on it, then `search_logs` if a log tail does not show the cause, and to explain the root cause and a fix. | `dag_id` (optional; omit to search every DAG) |
| `pipeline_health_today` | Counts today's runs (since midnight UTC) by state, lists the failed ones, and asks the model to read `health://control-plane`, call `diagnose_run` on each failure, and summarize. | none |

The control plane has no cross-DAG run query, so without a `dag_id` the prompts
read the first page of DAGs (up to 200) and a page of runs for each. When there are
more DAGs, the prompt says how many it did not check.

A prompt reaches the model as the user's own message, so it repeats a DAG or
run id only when the id is plain: 1 to 128 ASCII letters, digits or `_.:+@~=-`,
which covers generated run ids such as `manual__2026-10-08T12:00:00+00:00`. Any
other id (a run id with spaces or quotes, say, which whoever triggered the run
chose) is withheld, even from links. The prompt then links the DAG and asks the
model to get the ids from the user.

## Links into the UI

With `--ui-base-url` set, results carry a `web_url` that opens the entity in the
Dexaflow UI. Without it the field is absent.

| Where | `web_url` opens |
|---|---|
| `list_dags`, `dag://list` | the DAG: `<base>/dags/<dag_id>` |
| `diagnose_run`, `run://detail/...` | the run: `<base>/dags/<dag_id>/runs/<run_id>` |
| `diagnose_run` failed tasks, `task://instances/...` | the task attempt: `.../runs/<run_id>/tasks/<task_id>?try_number=<n>` |
| `search_logs` matches | the log line: the task attempt link plus `#<index>`, the line's 0-based position in the UI log viewer |

On initialize, the server's instructions tell the model to link the DAGs, runs,
tasks and log lines it mentions with `web_url`, and never to build UI links itself.
Text resources (`log://`, `dag://source/`) carry no link.

## Wiring an MCP client (Claude Desktop)

Add `dexaflow-mcp` to your client's MCP server config. For **Claude Desktop**
(`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "dexaflow": {
      "command": "dexaflow-mcp",
      "env": {
        "DEXAFLOW_SERVER_URL": "http://localhost:8088",
        "DEXAFLOW_TOKEN": "<paste a JWT from `dexaflow auth create-token`>"
      }
    }
  }
}
```

If `dexaflow-mcp` is not on the launcher's `PATH`, use its absolute path as
`command` (e.g. `~/.dexaflow/bin/dexaflow-mcp`). Restart the client, and Dexaflow's
tools and resources appear. Start with *"list my DAGs"* or *"diagnose the latest
failed run of `<dag_id>`"*.

{{% alert title="One process, one control plane" color="info" %}}
A single `dexaflow-mcp` targets exactly one control plane (`--server`). To reach
several environments, add one MCP-client entry per environment — the server never
routes by environment (ADR 0050 D4).
{{% /alert %}}

## Security: the blast radius

{{% alert title="Read-only, and scoped to the caller's token" color="success" %}}
The MVP exposes **reads only** — no tool triggers, clears, edits, or deletes
anything. Because every `/api/v2` call carries **the caller's token** and the server
holds no credentials of its own, a bug in tool code can only do what that token
already authorizes: its blast radius equals your own API rights. Log and DAG-source
reads are **sanitized and size-capped** by construction, treated as untrusted content
(ADR 0050 D10).
{{% /alert %}}

## See also

- [ADR 0050 — Model Context Protocol server](/project/adrs/0050-mcp-server/): the full design, the security posture, and the untrusted-content threat model.
- [Go packages → `pkg/client`](/reference/go/): the typed `/api/v2` client the MCP is built on.
- [HTTP API (Scalar)](/api-reference.html): the `/api/v2` surface itself.
