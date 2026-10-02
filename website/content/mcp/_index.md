---
title: MCP server
linkTitle: MCP
weight: 25
description: The Leoflow MCP server — let an AI agent read, reason about, and diagnose your DAGs, runs, and logs over the Model Context Protocol.
cascade: { type: docs }
menu:
  main:
    weight: 25
---

`leoflow-mcp` is Leoflow's **Model Context Protocol** server
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
    <span class="lf-card__desc">Export a token, launch <code>leoflow-mcp</code> over stdio, and you are talking MCP in two commands.</span>
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
    <span class="lf-card__desc">Drop <code>leoflow-mcp</code> into Claude Desktop or Claude Code and ask <em>"list my DAGs"</em>.</span>
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

`leoflow-mcp` ships alongside the other binaries (installed by the one-command
[install](/get-started/installation/), or `go build ./cmd/dexaflow-mcp`). It has two
transports.

{{< tabpane text=true >}}
{{% tab header="stdio (default) — local Lite dev" %}}

```bash
export DEXAFLOW_SERVER_URL=http://localhost:8088     # your Lite control plane
export DEXAFLOW_TOKEN="$(leoflow auth create-token \
  --server http://localhost:8088 \
  --username admin@leoflow.local --password <your-admin-password>)"
leoflow-mcp                                          # speaks MCP over stdin/stdout
```

On the **stdio** transport the process token **is** the caller's identity: the
server reads it once from `DEXAFLOW_TOKEN` and every `/api/v2` call carries it. Logs
go to **stderr** — stdout is the MCP protocol channel and carries nothing else.
This is the transport an MCP client (Claude Desktop / Code) launches for you; you
rarely run it by hand.
{{% /tab %}}
{{% tab header="Streamable HTTP — the Pro service" %}}

```bash
leoflow-mcp --transport http --listen :9099 --server https://leoflow.internal
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
| — | `DEXAFLOW_TOKEN` | — | Bearer JWT for the **stdio** transport (ignored on `http`). |
| `--version` | — | — | Print the version and exit. |

## Auth: getting a token

The MCP **passes the caller's Leoflow JWT through** to `/api/v2` and never mints one
(ADR 0050 D9). Obtain one from the control plane with your admin login:

```bash
leoflow auth create-token \
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

## Wiring an MCP client (Claude Desktop)

Add `leoflow-mcp` to your client's MCP server config. For **Claude Desktop**
(`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "leoflow": {
      "command": "leoflow-mcp",
      "env": {
        "DEXAFLOW_SERVER_URL": "http://localhost:8088",
        "DEXAFLOW_TOKEN": "<paste a JWT from `leoflow auth create-token`>"
      }
    }
  }
}
```

If `leoflow-mcp` is not on the launcher's `PATH`, use its absolute path as
`command` (e.g. `~/.dexaflow/bin/leoflow-mcp`). Restart the client, and Leoflow's
tools and resources appear. Start with *"list my DAGs"* or *"diagnose the latest
failed run of `<dag_id>`"*.

{{% alert title="One process, one control plane" color="info" %}}
A single `leoflow-mcp` targets exactly one control plane (`--server`). To reach
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
