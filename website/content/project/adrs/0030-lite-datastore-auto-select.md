---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0030-lite-datastore-auto-select.html
# --- end AUTO redirect aliases ---
title: "ADR 0030: Lite Datastore Auto-Selects — Docker Postgres, or a Managed PG When Docker Is Absent"
linkTitle: "0030 · Lite Datastore Auto-Selects — Docker Postgres, or a Managed PG When Docker Is Absent"
weight: 300
description: "ADR 0030: Lite Datastore Auto-Selects — Docker Postgres, or a Managed PG When Docker Is Absent"
---

**Status:** Accepted
**Date:** 2026-05-27
**Deciders:** Project founder
**Supersedes:** ADR 0029 (Lite Datastore Default — Docker)
**Refines:** ADR 0026 (Lite Datastore — XCom on Postgres, no Redis), ADR 0027 (Product Editions)
**Relates:** ADR 0015 (Kubernetes as the sole container execution path)
**Amended by:** ADR 0094 (auto follows the datastore the install is pinned to)

## Context

prealpha.16 made the **managed relocatable Postgres** Lite's default; real-host
testing exposed its portability cost (it dynamically links an unbundled chain of
system libraries — ICU, Kerberos, zstd, lz4, libxml2 — that is absent on minimal
hosts like Alpine/musl). ADR 0029 reacted by making **Docker Postgres the default**
and demoting the managed PG to an explicit opt-in (`--postgres managed`).

Two problems remained with the opt-in framing:

1. **The Docker-free path was undiscoverable.** A user on a Docker-free host who
   runs `leoflow lite` got a Docker error, with no hint that a flag would make it
   work without Docker.
2. **It was asymmetric with the executor.** The executor already resolves `auto`
   (k3d when Docker is present, else subprocess). The datastore did not — yet the
   two track the *same* host capability (Docker present or not).

A managed runtime is also already Lite's established pattern: `leoflow setup`
downloads a pinned, checksum-verified **managed CPython** so the user installs no
Python. Provisioning a managed Postgres when Docker is absent is the same pattern —
"Leoflow brings its own runtimes; you install nothing."

## Decision

**Lite's `--postgres` flag defaults to `auto`**, resolved for the host:

- **Docker present** → the Docker `postgres:16` (via `docker compose`). This is the
  realistic case, since Lite's default executor (k3d) already needs Docker.
- **Docker absent** → a **managed relocatable Postgres** downloaded under
  `~/.leoflow`, on a per-user Unix socket (no Docker). So `leoflow lite` runs on a
  Docker-free host with **nothing to install** — paired with the `subprocess`
  executor that the `auto` executor already selects when Docker is absent.

`auto` mirrors `resolveExecutor`, so the datastore and executor resolve from one
host probe. Either backend can be forced (`--postgres docker|managed`). On a minimal
host (Alpine/musl, slim container) the managed build's pre-flight (`postgres
--version`) **fails loud** with a clear message pointing at installing Docker — the
fragility is contained to the fallback and never silent.

Connection wiring is unchanged: `devDSNs` already auto-detects a live managed
cluster by its socket file (`~/.leoflow/pgdata/.s.PGSQL.5432`) and otherwise returns
the Docker TCP DSNs, so every entry point (the lite runner, reset-password, db
reset) connects consistently without threading the choice through each.

Lite stays **Redis-free** (ADR 0026), grounded on durability (Postgres XCom
survives a restart) and a single datastore; Redis remains a Production-scale
concern. This introduces **no Docker executor** and does not contradict ADR 0015:
the control plane imports no Docker SDK and runs tasks only via Kubernetes (k3d) or
subprocess. The k3d path **requires Docker to host the cluster**, but Docker is only
k3d's substrate, never an executor.

## Consequences

- **Docker-free out of the box, where possible:** `curl … | sh && leoflow lite`
  runs on a clean Docker-free Ubuntu/macOS host (managed PG + subprocess), with no
  flag and nothing else installed.
- **Real pods need Docker:** a Docker-free host gets the `subprocess` executor only
  (no isolation, dev-only); the k3d pod-per-task path requires Docker. This is made
  explicit in the docs and in the run-time message printed by `autoDatastore` /
  `autoExecutor`.
- **Fragility is contained:** the managed build can still fail on minimal hosts, but
  only on the fallback path and with a loud, actionable error. Hardening the managed
  runtime to run anywhere (bundling its lib chain) stays a quality follow-up (#97),
  not a default-path blocker.
- **Symmetry:** datastore and executor both resolve `auto` from one Docker probe, so
  the two never disagree about the host.
- **Editions framing updated:** ADR 0027's "embedded managed, no Docker" and ADR
  0029's "Docker default, managed opt-in" are replaced here by "Postgres,
  auto-selected: Docker when present, managed (Docker-free) otherwise."
