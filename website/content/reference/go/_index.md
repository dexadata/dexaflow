---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /go-api.html
# --- end AUTO redirect aliases ---
title: Go packages (GoDocs)
linkTitle: Go packages
weight: 30
description: GoDocs for the control plane, scheduler, executor, agent, and storage — one page per package.
cascade: { type: docs }
---

Dexaflow's control plane, agent, and CLI are Go. Every exported identifier carries a
GoDoc (Go Report Card A+ is the quality floor), and each symbol links to its source
on GitHub. One page per package keeps each reference a readable length.

The per-package pages are **generated from source** by `gomarkdoc`
(`website/scripts/gen-go.sh`) and listed in the sidebar. Packages exported to this
reference:

- `pkg/client` — the typed `/api/v2` client (**public**)
- `pkg/dagspec`: validates a compiled `dag.json` with the registration rules (**public**)
- `pkg/mcp`: the MCP server core, to build a server with tools of your own (**public**)
- `pkg/admission`: the dispatch admission hook contract (ADR 0100) (**public**)
- `pkg/server`: runs the control plane from your own Go program (ADR 0099) (**public**)
- `pkg/logsink`: the task log sink contract, for `server.WithLogSink` (ADR 0099) (**public**)
- `internal/domain` — core types (DAG, Task, Run, …)
- `internal/scheduler` — the state machine
- `internal/executor` — K8s / subprocess executors
- `internal/dispatch` — executor routing
- `internal/agent` — the in-container agent
- `internal/agentrpc` — agent gRPC
- `internal/storage` — Postgres / Redis
- `internal/auth` — JWT + RBAC
- `internal/config` — configuration
- `internal/cli` — the `leoflow` CLI
- `internal/api` — HTTP handlers

## Compatibility of the public packages

The packages under `pkg/` are public API ([ADR 0099](/project/adrs/0099-public-server-package/)),
and the `internal/` ones are not: Go refuses to import them from another module,
and they change in any release.

- **A patch release never breaks an exported identifier of `pkg/`.** Adding a
  function, an option, a struct field or a method to a struct is not a break.
- **A minor release may**, once the identifier was marked `Deprecated:` in the
  previous minor. The changelog then says what changed and what to use instead.
- **An interface you implement does not grow.** A method added to an exported
  interface would break every implementation, so a new capability comes as an
  optional second interface that the server checks for.

Every pull request runs `apidiff` on each `pkg/` package against its base
branch, and an incompatible change fails unless it is announced as above.
