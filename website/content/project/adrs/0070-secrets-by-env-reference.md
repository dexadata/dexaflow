---
title: "ADR 0070: Secrets reach a process by environment reference, never rendered into a command string"
linkTitle: "0070 · Secrets by env reference, not in command strings"
weight: 700
description: "ADR 0070: a secret reaches task code as an environment variable (or a Secret the kubelet resolves), never as text rendered into a command line; templated bash keeps its Airflow parity for non-secret values only, and a lint becomes a policy once multi-tenancy needs it."
---

**Status:** Accepted
**Date:** 2026-10-09
**Relates:** ADR 0019 (secret encryption at rest), ADR 0021 (exposing Variables and Connections to pods), ADR 0040 (Airflow operator support; the bash render hop), ADR 0045 (declared secret delivery), ADR 0055 (secret scoping and token liveness; the agent token transport), ADR 0061 (secret locality, masked on read).
**Issues:** #388, the #476 residual folded into #388 (agent token as a plaintext pod spec value), #489 (shell-quoting of rendered values).

> **Numbering.** Proposed as 0068 and renumbered to 0070 because 0067 to 0069
> merged first; 0063 is claimed by an open PR (#1365). If another ADR merges
> first under 0070, this file is renumbered before merge.

## Context

A native `bash` task whose command contains `{{` is not run as a plain
`bash -c`. The agent hands it to the runtime (`internal/agent/command.go`), which
Jinja-renders the command with the run context (`ds`, `params`, `var`, `conn`)
and then execs `bash -c <rendered>` (`_render_bash` and `run_bash` in
`runtime/python/leoflow_runtime/runner.py`). That is Airflow parity: a
`BashOperator` with `{{ var.value.api_key }}` in its command works unchanged.

Two hardening steps already shipped on that path:

- **Template injection.** The render uses Jinja's `SandboxedEnvironment`, so a
  template that reaches for Python internals (`{{ ''.__class__ }}`) fails closed
  instead of executing.
- **Shell injection through values (#489).** Every interpolated value passes
  through `shlex.quote` (the environment's `finalize` hook). The command text the
  author wrote is the shell structure; a value, including `params` that anyone
  with `execute:dag` supplies through the run's `conf`, stays one inert argument.

Neither step addresses the remaining problem, which is not about what a value
**does** to the shell but about **where a secret ends up** once it is part of the
command string:

1. **The command line is readable.** The rendered string is `argv[2]` of the
   `bash` process. It is visible in `/proc/<pid>/cmdline` and `ps` output to any
   process in the same PID namespace (the task container, a debug sidecar, a
   shared-PID pod), to node-level tooling with host PID access, and on Lite to
   every process of the same user on the host. A secret in the environment is
   readable through `/proc/<pid>/environ` only by the same user or root, a
   narrower set.
2. **The command line gets copied.** Shells and tools that echo a command
   (`set -x`, an error message quoting the failing command, a wrapper that logs
   its arguments, an audit trail of executed commands) copy the secret into task
   logs, which ADR 0061 says must never carry it.
3. **The trust boundary is not the author's alone.** In a multi-tenant
   deployment the principal that sets a Variable may differ from the one whose
   DAG renders it. #489 makes the value inert as shell syntax, but the value is
   still the other principal's secret, placed in a string with a wider audience.

Kubernetes frames the same problem the same way: it never interpolates a Secret
into a shell command. A Secret reaches a container as an environment variable
(`secretKeyRef`) or a mounted file, and the kubelet resolves it by reference.
The Pod spec (code) and the Secret (data) stay separate.

Dexaflow already has the reference path. The Variables and Connections a task
receives reach it as `AIRFLOW_VAR_<KEY>` and `AIRFLOW_CONN_<ID>` environment
variables (ADR 0021, scoped by ADR 0045 and ADR 0055). Under the default
`auth.secret_scoping: permissive`, and under `off`, that is every Variable and
Connection of the tenant; under `enforce` it is only the names the DAG declares
in `dexaflow.yaml`. A bash task that writes `"$AIRFLOW_VAR_API_KEY"` gets the same
secret a template would render, under the same declaration rule, without it
ever being part of the command line.

The same distinction applies to the control plane's own credential (the #476
residual folded into #388). With `auth.agent_token_transport: envvar`, the
shipped default, the agent token is set as a plaintext `Value:` on the task pod
spec, readable by anyone with `get pods` in the task namespace. ADR 0055 added
the reference forms: the `exchange` transport projects a ServiceAccount token
the agent exchanges for a task-scoped token, with a `secretKeyRef` fallback for
clusters that cannot project one. Neither is the default yet.

## Decision

1. **A secret reaches a process by reference, never as text in its command.**
   The supported ways to hand a secret to task code are, in order of preference:
   an environment variable the platform sets (`AIRFLOW_VAR_*`, `AIRFLOW_CONN_*`,
   or a Kubernetes `secretKeyRef`), a file in the task's private scratch
   (ADR 0061), or the native APIs that read either (`Variable.get`,
   `BaseHook.get_connection`). Rendering a secret into a string that becomes a
   process's `argv` is not a supported way, on any edition.

2. **Templated bash keeps its Airflow parity, for non-secret values.**
   `{{ ds }}`, `{{ params.x }}` and `{{ var.value.x }}` keep rendering, with the
   sandbox and the per-value `shlex.quote` of #489 as standing invariants of the
   render hop: a change that drops either is a regression. The documentation says
   plainly that `{{ var.value.x }}` and `{{ conn.x }}` are for non-secret
   configuration and that a secret is referenced as `$AIRFLOW_VAR_X` or
   `$AIRFLOW_CONN_X` instead. Rendering a secret is not blocked today, because
   Dexaflow has no notion of a secret-typed Variable to block on, and blocking
   every `var` reference would break Airflow DAGs that use it for plain
   configuration.

3. **Guidance first, a policy when multi-tenancy needs one.** The path is staged:
   - **Now:** documentation (this ADR, the DAG authoring and Variables pages).
   - **Next, behavior neutral:** a compile-time warning when a `bash` command
     template references `var.` or `conn.`, naming the `$AIRFLOW_VAR_*` or
     `$AIRFLOW_CONN_*` form to use instead. A warning changes no outcome.
   - **When real multi-tenancy lands:** an operator setting that turns the
     warning into a compile and deploy rejection, and, if Variables gain a
     sensitivity flag, a runtime refusal to render a sensitive value into a
     command. Each of these is its own change with its own default.

4. **Control-plane credentials follow the same rule.** The target for the agent
   token is the reference forms of ADR 0055 (`exchange`, or its `secretKeyRef`
   fallback), never a plaintext `Value:` on the pod spec. The `envvar` transport
   stays the default until ADR 0055's rollout flips it; this ADR does not change
   that default, it records that the plaintext form is the exception the
   hardening removes. On Lite, the agent token stays in the agent process
   environment: there is no `get pods` boundary there, and the agent already
   strips it from the environment task code inherits (#476).

5. **Exec form is evaluated, not adopted.** Running a command as an argument
   array (`execve`, no `/bin/sh -c`) removes the shell from the path entirely.
   It does not fit the `bash` task type, whose contract is a shell command, and
   it does not stop a secret from landing in `argv`. It stays an option for a
   future task type that takes `command` and `args` the way a Pod spec does, and
   for internal invocations Dexaflow controls.

## Consequences

- DAG authors get one rule: secrets are referenced as environment variables in
  bash, read through the native APIs in Python, and never interpolated into a
  command. The documentation for bash templating and for Variables and
  Connections states it where an author looks.
- No behavior changes with this ADR. Existing DAGs that render a Variable into a
  command keep working; the staged lint and policy of Decision 3 each ship
  separately and behavior neutral by default.
- A reviewer of any change to the render hop checks that the sandbox and the
  per-value quoting both hold, and a reviewer of any change to how a credential
  reaches a pod or process checks that it travels by reference.
- Operators who need the agent token off the pod spec today set
  `auth.agent_token_transport: exchange` (ADR 0055); nothing here makes that
  mandatory.

## Alternatives

- **Block `{{ var }}` and `{{ conn }}` in bash outright.** Rejected: it breaks
  Airflow DAGs that use Variables for plain configuration, which is the common
  case, and it would be enforced without knowing which values are secret.
- **Mask rendered secrets in logs only.** Rejected as the main answer: masking
  is a backstop (ADR 0061 already requires it on read paths) but does nothing
  for `argv` readers, and a masker that misses one encoding leaks the secret.
- **Move bash execution to exec form.** See Decision 5: it changes the task
  type's contract and does not keep a secret out of `argv`.
