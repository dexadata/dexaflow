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
