---
title: "ADR 0094: Lite's auto datastore follows the datastore the install is pinned to"
linkTitle: "0094 · Auto follows the pinned datastore"
weight: 940
description: "ADR 0094: an install is pinned to a Postgres backend by `dexaflow lite restore` (managed) or by an explicit --postgres. An auto run follows a managed pin instead of picking the Docker Postgres, so a restored install boots its data on a host with Docker. An auto run never pins, and an install without a pin keeps the ADR 0030 rule."
---

**Status:** Accepted
**Date:** 2026-10-26
**Amends:** ADR 0030 (Lite datastore auto-selects)
**Relates:** ADR 0065 (Lite key migration, restore provisions the managed Postgres)
**Issues:** #1997

## Context

ADR 0030 resolves `--postgres auto` from the host alone: the Docker Postgres
when Docker answers, else the managed Postgres. `dexaflow lite restore` always
replays into the managed Postgres, because `backup` covers that path only. On
a machine with Docker the documented flow

```sh
dexaflow setup && dexaflow lite restore --input snap.tar.gz && dexaflow lite
```

therefore started an empty Docker datastore next to the restored one. The
restore ended with a line asking for `dexaflow lite --postgres managed`, and
every later run needed the flag too.

Picking the managed Postgres whenever its data directory exists was
considered and rejected: a user who ran Lite without Docker once and then
installed Docker has been on the Docker Postgres since, and would be moved
back to the stale managed data without warning.

## Decision

An install can be **pinned** to a Postgres backend. The pin is the file
`~/.dexaflow/dev/datastore`, holding `managed` or `docker`.

- `dexaflow lite restore` pins the install to `managed` once the replay
  succeeded.
- A run with an explicit `--postgres managed` or `--postgres docker` pins the
  install to that backend once its datastore is up.
- A run with `--postgres auto` never pins. A Docker daemon that was down for
  one run must not move the install to the managed Postgres for good.

`auto` follows a `managed` pin and does not probe Docker. With a `docker` pin,
no pin, or a file it cannot read, `auto` keeps the ADR 0030 rule, including
its fallback to the managed Postgres when Docker does not answer. The note
printed for a managed pin names `--postgres docker` as the way back.

## Consequences

- The documented restore flow works on a host with Docker: the restore ends
  with "run `dexaflow lite` to start", and later runs need no flag.
- Installs that never restored and never passed `--postgres` behave as before.
- Moving a restored install to the Docker Postgres takes one explicit
  `--postgres docker`, which starts an empty Docker datastore and pins it.
- If the pin cannot be written, the restore falls back to the old closing
  line, and an explicit run warns; nothing fails over the pin.
