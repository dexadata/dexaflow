---
title: "Handling secrets in a feature"
linkTitle: "Handling secrets"
weight: 40
description: "The two rules every feature that touches a credential must follow — private locality and masked-on-read — with the patterns to satisfy them."
---

If your change reads, writes, delivers, or displays a **secret** — a connection
URI, a generated `profiles.yml`, a keyfile, a token, an API key — it is bound by
the two invariants in [ADR 0061](/project/adrs/0061-secret-locality/). This page
is the practical how-to. Both rules exist because the same leak has happened three
times ([#882](https://github.com/dexadata/leoflow/issues/882),
[PR #867](https://github.com/dexadata/leoflow/pull/867) (field report #11),
GHSA-3r74-9w27-v32f) — each avoidable.

## Rule 1 — Private locality: never write a secret where it could be committed

A secret-bearing file goes to a **private, ephemeral scratch** (0700, per-task,
removed when the task ends) — **never** the project directory, the process CWD,
the repo, or a committable dotfile. The default output location must be a private
`mkdtemp`, never `os.getcwd()` / `"."`.

leoflow provides the scratch on both execution paths — use it, don't reinvent it:

- **Pod**: the base image sets `DBT_PROFILES_DIR` / `DBT_TARGET_PATH` /
  `DBT_LOG_PATH` to `/tmp/leoflow/...` (`runtime/Dockerfile`).
- **Lite**: the subprocess executor injects the same three at a per-task
  `MkdirTemp` (`internal/executor/subprocess.go`, `dbtScratchEnv`) — this Lite-side
  injection lands with the #882 fix.

So a task reads `DBT_PROFILES_DIR` and writes there. The runtime's own fallback,
for any path that forgot to set it, must be a private `mkdtemp` — **never the CWD**
(`runtime/python/leoflow_runtime/__main__.py`, `_dbt_profiles_dir`, also part of the
#882 fix):

```python
d = os.environ.get("DBT_PROFILES_DIR")
if not d:
    d = tempfile.mkdtemp(prefix="leoflow-dbt-")  # NOT os.getcwd()
```

**Symmetry matters.** A fix that lands only in the pod base image but not in the
Lite executor (or vice-versa) is incomplete — that asymmetry *was* #882. Whatever
locality the pod gets, Lite gets the same.

## Rule 2 — Masked on read: never echo a secret back

A read path — API response, UI, logs, audit, error message — never returns a
secret. The write path accepts it; the read path masks it. Reuse the shared
matcher rather than hand-rolling a key list:

```go
// internal/api — mask secret-bearing keys on serialize; recurse into free-form blobs.
if isSensitiveKey(key) {
    value = "***"
}
```

Prefer omitting a write-only field entirely (as the connection `password` is). For
a free-form blob like a connection's `extra`, redact by key name **recursively**,
and fail closed (redact the whole thing) if it can't be parsed.

## The tests are part of the feature

- **Writes a credential** → a unit test proving the target is not the CWD/project
  (and is `0700`), plus an inner-loop e2e asserting nothing secret lands under the
  repo (see `test/e2e/lite-dbt.sh`'s profiles-less assertion).
- **Reads a credential** → a test proving the response is masked (see
  `TestConnectionGetMasksSensitiveExtra`).
- **Surfaces in the UI** → Playwright against a real backend, per the SPA testing
  rule — the embedded Airflow UI reads the same `/api/v2/*`, so a masked value must
  render masked there too.

If you're adding a connector, an operator, or a task type that handles a
credential and you're unsure, treat ADR 0061 as the checklist: *where does the
secret land on disk, and can any read echo it?*
