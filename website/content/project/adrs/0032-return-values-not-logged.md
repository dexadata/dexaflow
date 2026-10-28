---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0032-return-values-not-logged.html
# --- end AUTO redirect aliases ---
title: "ADR 0032: Task Return Values Are Not Logged — Only Their Metadata Is"
linkTitle: 0032 · Task Return Values Are Not Logged — Only Their Metadata Is
weight: 320
description: "ADR 0032: Task Return Values Are Not Logged — Only Their Metadata Is"
---

**Status:** Accepted
**Date:** 2026-06-01

## Context

When a `@task` function returns a value, Leoflow's runtime pushes it as the
task's `return_value` XCom (consumed by downstream tasks and visible in the
UI's XCom tab). The runtime ALSO emits a one-line lifecycle log to the task
log file so the operator sees that the task produced output. Today (after
PR #240 and the prealpha.20 lifecycle work) that line carries only the
return's **type and wire size**:

```
[leoflow] returned str (20 B XCom)
[leoflow] returned dict (1245 B XCom)
[leoflow] returned None (no XCom pushed)
```

The value itself is NEVER written to the log file. It is only persisted
through the XCom path (`xcom` table, ciphered when applicable per ADR 0019).

Airflow takes the opposite stance: its `PythonOperator` emits
`Done. Returned value was: <truncated repr>` into the task log on success,
in addition to storing the value as XCom. This produces a single-place "see
the result of the task" shortcut for the operator but conflates two
semantically different streams (developer-emitted output vs. data-flow
output) and creates a chronic source of operator confusion, secret leakage,
and log bloat. The Airflow issue tracker has a recurring class of issues
about it:

- "Why is my XCom value in the task log?"
- "How do I prevent the return value (containing a secret) from being
  written to the log file?"
- "The 200-character truncation hides the actual debug info I need."

We choose a different default: keep log output (`print()`, `logging`,
stdout/stderr) and data output (`return value` / `xcom_push`) as STRICTLY
separate channels, with no automatic crossing from data → log.

## Decision

The Leoflow runtime emits a metadata-only summary line for every task
return — `[leoflow] returned <type> (<N> B XCom)` (or `returned None
(no XCom pushed)`) — and never dumps the return value into the log file.

To see a return value, the operator opens the XCom tab on the task
instance, which renders the value with the full per-payload policy
(decryption for ciphered keys, truncation for huge payloads, etc.).

Stated invariants:

1. **The log file contains only what user code intentionally wrote** —
   via `print()`, `sys.stderr.write()`, the `logging` module, or any
   process that inherits the task's stdout/stderr file descriptors. The
   runtime's own framing and lifecycle lines (`▸ task started`,
   `[leoflow] loading <entrypoint>`, `[leoflow] pulled <param> (N B)`,
   `[leoflow] returned <type> (N B XCom)`, `✓ task succeeded in N s`)
   are explicitly NOT user data — they carry no payload, only metadata
   the operator needs to understand what happened.
2. **The return value reaches the operator only through XCom** — the
   XCom row, the XCom API, and the XCom UI tab. Treat the log as
   public-by-default and XCom as data-governance-controlled.
3. **The same rule applies to upstream pulls.** `[leoflow] pulled raw
   (87 B)` says "the agent injected an 87-byte upstream value into the
   `raw` parameter" — it does NOT print the value. To inspect, open
   the upstream task's XCom tab.

This is the runtime contract. Connector/operator code (e.g. a future
`PostgresOperator` that wraps SQL execution) is free to log additional
detail at its own discretion, applying the same separation: log what's
useful for debugging without dumping sensitive data.

## Why this is the right default

- **Secrets stay out of logs by construction.** A task that returns a
  password, API token, signed JWT, or query result with PII never has
  its return value journaled to a file that audit and observability
  pipelines treat as public. The XCom row carries the encryption
  policy from ADR 0019; the log file does not.
- **No confusion between `print` and `return`.** The two have different
  intents — output for humans vs. data for downstream — and conflating
  them in the log surface trains users to misuse both. Keeping them
  separate makes the mental model match the code shape: the function
  body's stdout lands in logs, the function's return value lands in
  XCom.
- **No surprise log bloat.** A task that returns 10 MB of JSON does
  not multiply log size 11× across every attempt's log file. The XCom
  store handles oversized values per its own policy (today: rejection
  above 256 KB per ADR pending) without dragging the log subsystem into
  it.
- **Forward-compatible with stricter governance.** A future deployment
  that wants to redact secrets, sign log lines, or ship logs to a
  third-party SaaS does not have to defend "but the XCom value snuck
  through" — the policy is structural, not configuration.

## Consequences

- The lifecycle line ONLY tells the operator the type and size of the
  return. To see the value, they click XCom (one tab, one click, but
  one extra interaction compared to Airflow's PythonOperator default).
  This is a deliberate ergonomic trade for the safety + clarity wins
  above.
- Operators arriving from Airflow may briefly hunt for
  `Done. Returned value was: ...` in the log. Documentation calls this
  out explicitly: "Leoflow shows return values in the XCom tab, not in
  the log."
- The same line emission convention extends to upstream pulls
  (`[leoflow] pulled <param> (N B)`) and to any future runtime
  observability lines: metadata yes, payload no, except where the user
  themselves chose to `print()` it.

## Out of scope

- Optional opt-in to inline-log the value for debug DAGs (e.g.,
  `LEOFLOW_LOG_RETURN_VALUE=1`). If demand surfaces, ship as an
  explicit env var the user sets PER TASK, not as a global default.
- Connector/operator logging policy (a `PostgresOperator` writing
  `executed: SELECT ... → N rows` is fine — that is operator-emitted
  intentional output, not the runtime crossing the line itself).
- Truncation in the XCom UI for very large values (separate concern —
  the XCom tab can paginate or preview as needed without changing the
  log contract).

## Related

- ADR 0019: secret encryption at rest (the XCom path inherits the cipher;
  the log file does not).
- PR #240: introduced the flat `[leoflow] ...` lifecycle lines; this ADR
  codifies the policy for what those lines may and may not contain.
- Issue #243: enriches the audit log with before-state on clears — same
  separation principle (audit context yes, payload no).
