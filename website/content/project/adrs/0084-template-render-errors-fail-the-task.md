---
title: "ADR 0084: a template that fails to render fails the task attempt"
linkTitle: "0084 · Template render errors fail the task"
weight: 840
description: "ADR 0084: when a task's Jinja templates cannot render, on a captured operator's template_fields or on a templated bash command, the attempt fails before execute() or bash runs, with the field name and the Jinja error in the task log, as on Airflow. The run context gains macros and logical_date in the same change. Replaces the best-effort templating of ADR 0040 Phase A."
---

**Status:** Accepted
**Date:** 2026-10-16
**Relates:** ADR 0040 (operator and sensor execution; this ADR replaces the best-effort templating its Phase A runtime shipped with, and keeps the rest), ADR 0070 (secrets by environment reference, not rendered into commands)
**Issues:** #1845, #1843

## Context

ADR 0040 Phase A built the generic executor as
`import_string → instantiate → render_template_fields → execute`, with a
minimal run context. The runtime treated rendering as best-effort, and the
code said so:

- `run_operator()` caught any exception from `op.render_template_fields(context)`,
  logged `render_template_fields skipped`, and called `execute()` with the fields
  that had not rendered still holding their raw template text.
- `_render_bash()` returned the raw command when rendering failed ("a
  broken/blocked template must not fail the task") or when Jinja2 was not
  installed, and bash ran it.

The minimal context lacked keys that common templates use. `{{ macros.ds_add(ds, 1) }}`
raised `UndefinedError` because `macros` was absent, so the task sent the literal
`{{ macros.ds_add(ds, 1) }}` to the external system: a query, a path, an API
call. The failure surfaced later, if at all, far from its cause, which is how
#1841 was found. On Airflow a render error fails the task attempt before
`execute()`.

The best-effort choice was made so that a context gap would not stop a task
that might still work. In practice it turned a loud failure into wrong data
written in silence.

## Decision

1. **A render error fails the attempt.** On the operator path, an exception from
   `render_template_fields` fails the attempt before `execute()`. On the bash
   path, a templated command that cannot render fails before `bash -c` runs.
   The raw template text is never executed or sent anywhere.
2. **The log says what failed.** The task log carries one line naming the
   template field and the Jinja error, for example
   `template field 'sql' of BigQueryInsertJobOperator failed to render:
   UndefinedError: 'macros' is undefined`, followed by the traceback. The
   bash path names `bash_command`. On the operator path the field is found by
   rendering each field's original value on its own, in order, after the
   failure; when none fails alone, the line says the operator's template fields
   failed, with the error.
3. **It is an ordinary task failure.** The attempt retries under the task's
   retry policy, and the task's `on_failure_callback` runs on its last attempt,
   as for an `execute()` failure.
4. **No Jinja2 means no render.** A templated bash command in an image without
   Jinja2 fails the same way instead of running raw. The base image ships
   Jinja2 with the Task SDK.
5. **The bash quoting stays.** Every value a bash template interpolates is still
   shell-quoted with `shlex.quote` (#489); this ADR changes what happens when
   rendering fails, not how a successful render is quoted. A sandbox refusal
   (`SecurityError`) is a render error and fails the attempt.
6. **The context gains `macros` and `logical_date` in the same change**, so DAGs
   that only rendered by accident get a working context, not a new failure.
   `macros` is the Task SDK's `airflow.sdk.execution_time.macros` module,
   imported on first use, with a small built-in subset (`ds_add`, `ds_format`
   and the standard modules the SDK re-exports) when the SDK is not installed.
   `logical_date` is the logical date the agent delivers, or `None` on a run
   without one.

Undefined names keep the semantics they have today: a bare `{{ name }}` for a
key the runtime does not provide renders as an empty string, while an
attribute or call on it (`{{ name.x }}`, `{{ name() }}`) is a render error.
Airflow's default `StrictUndefined` would fail on both; adopting it belongs with
the rest of the context parity work of #1843, once its contract test shows
which keys are still missing.

## Alternatives considered

- **Keep best-effort and add the missing keys only.** Fixes the known templates
  and leaves the next gap to fail in silence again, which is the problem.
- **Fail only when a field still contains `{{` after rendering.** A heuristic:
  a field can legitimately contain braces, and a render that raised halfway
  through leaves no reliable trace in the value.
- **Log a warning and keep running.** The current behavior with a louder line;
  the external system still receives the raw template.

## Consequences

- A DAG whose templates fail today, and which ran with raw template text, now
  fails its attempt with the field and the error in the log. That is a
  behavior change for users, recorded under Changed in the changelog.
- Templates that use `macros` or `logical_date`, which failed or rendered empty
  before, now render as on Airflow.
- The operator path renders a failing operator's fields a second time to name
  the field. This only happens on the failure path.
- Each later gap in the context surfaces as a failed attempt naming the field,
  which is what the #1843 contract test is meant to prevent ahead of time.
