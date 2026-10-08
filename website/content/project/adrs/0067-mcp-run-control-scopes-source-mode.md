---
title: "ADR 0067: Run control on the MCP, scoped issuer tokens, and Pro source mode"
linkTitle: "0067 · MCP run control, scopes and source mode"
weight: 670
description: "ADR 0067: the MCP gains trigger, clear, pause and unpause behind an operator flag, with plan + apply for risky calls; trusted-issuer bearer tokens carry a scope claim that narrows what they may do; and Pro can run a version's dag.py on an operator-pinned runtime image, as Lite does. Amends ADR 0050 D7 phase 4 and open question 1."
---

**Status:** Accepted
**Date:** 2026-10-08 (proposed and accepted the same day by the project owner)
**Amends:** ADR 0050 D7 (the side-effect tier and phase 4) and its open question 1 (scope enforcement). ADR 0050 D5 and D6 stay as written: the engine MCP does not author or deploy.
**Relates:** ADR 0002 (pod-per-task), ADR 0003 and ADR 0041 (DAG-as-image, build/push/register), ADR 0024 (the shim compiles `dag.py` to `dag.json`), ADR 0048 (no user code in the control plane), ADR 0058 (warm worker pools), ADR 0011 (strict TDD).
**Issues:** #1472 (this ADR), #1473 (scope claim), #1474 (run control tools), #1475 (Pro source mode), #1468 (trusted-issuer bearer tokens, merged first).

## Context

Three gaps stop an agent from closing the loop it opens with `diagnose_run`.

1. **It cannot act.** After a diagnosis, retrying a task, triggering a run or
   pausing a DAG means leaving the conversation for the UI. ADR 0050 D7 put
   `trigger_run` and `clear_task` behind `EnableRunControl` in phase 4, which
   was never built.
2. **A token cannot be narrower than its user.** Since #1468 the engine accepts
   bearer tokens from a trusted issuer for MCP clients. Such a token carries the
   user's full role, so a user cannot grant a client read access only. ADR 0050
   open question 1 left this as later hardening; run control makes it a
   prerequisite, because a prompt injection in a log line (D10) could now ask a
   read-only assistant to act.
3. **A simple DAG still needs an image.** A Pro deploy builds and pushes an image
   with the DAG baked in (ADR 0003, ADR 0041). For one `dag.py` that only uses
   packages already in a runtime image, that step is heavy and cannot be done
   from a chat client. The engine already keeps `dag.py` in every version
   (`DAGSpec.Source`) and the Lite subprocess executor already runs it
   (`materializeWorkDir`), but the Kubernetes executor ignores it
   (`internal/storage/agent_store.go`: "Lite only; Pro ignores it").

## Decisions

### 1. Scopes on trusted-issuer bearer tokens

A trusted-issuer bearer token (one whose `aud` is in
`auth.trusted_issuer.bearer_audiences`, #1468) may carry an OAuth `scope` claim:
a space-separated string, as in RFC 9068. Three values have meaning:

| Scope | Grants |
|---|---|
| `dexaflow:read` | Every route guarded by `RequirePermission("read", ...)`. |
| `dexaflow:run` | Run-state writes: trigger a run (`POST /api/v2/dags/{id}/dagRuns`), set a run's state (`PATCH .../dagRuns/{run_id}`), clear or set task instances (`POST .../clearTaskInstances`, `PATCH .../taskInstances/{task_id}`), and pause or unpause a DAG (`PATCH /api/v2/dags/{id}`, which today sets only `is_paused`). |
| `dexaflow:deploy` | Registering a version (`POST /api/v2/dags/{id}/versions`). |

Rules:

- **Scopes narrow, never widen.** The role check runs as today, and the scope
  check runs after it. Both must pass. A viewer's token with `dexaflow:run`
  still cannot trigger.
- **Each route names its scope.** The mapping lives next to the route
  registration, not in a table keyed by `(action, resource)`. `PATCH
  /dags/{id}` (pause) and `POST /dags/{id}/versions` (register) are both
  `write:dag`, yet one belongs to `run` and the other to `deploy`. A change
  that lets `PATCH /dags/{id}` set more than `is_paused` must revisit its
  scope.
- **Unmapped writes fail closed.** A scoped token is refused on every write
  route that the table above does not name: connections, variables, pools,
  users, deleting a DAG, the IDE, import errors and favorites. These stay with the UI
  and engine-issued tokens.
- **A missing claim means read only.** A trusted-issuer bearer token without
  `scope` gets `dexaflow:read`. That keeps tokens minted before this ADR
  working, since those clients only read, and an issuer has to opt in
  explicitly to anything more.
- **Nothing else changes.** Engine-issued JWTs (HS256), and sessions created
  through the trusted-issuer handoff, carry no scope and keep today's
  behaviour. The scope is a property of the client's token, not of the user.
- A refusal is `403` with a body that names the missing scope, so a client can
  ask for it.

No scope implies another. An issuer that grants `dexaflow:run` normally grants
`dexaflow:read` with it, but the engine does not assume so.

### 2. Run control tools on the MCP

The MCP gains four tools behind one operator flag, `--run-control`
(`DEXAFLOW_MCP_RUN_CONTROL`), default **off**. `dexaflow-mcp` has no config
file, so this is a flag rather than `mcp.run_control.enabled`. When the flag is
off the tools are **not registered** (D7: never a tool that exists and refuses).
The flag works on both transports.

| Tool | Endpoint | Needs | Annotations | Plan first when |
|---|---|---|---|---|
| `trigger_run` | `POST /api/v2/dags/{id}/dagRuns` | `execute:dag`, `dexaflow:run` | not read-only, not destructive, not idempotent | never |
| `clear_task` | `POST /api/v2/dags/{id}/clearTaskInstances` | `write:task_instance`, `dexaflow:run` | destructive, not idempotent | the clear would touch more than one task instance (downstream, upstream or several tasks included) |
| `pause_dag` | `PATCH /api/v2/dags/{id}` `{"is_paused": true}` | `write:dag`, `dexaflow:run` | not destructive, idempotent | never |
| `unpause_dag` | `PATCH /api/v2/dags/{id}` `{"is_paused": false}` | `write:dag`, `dexaflow:run` | not destructive, idempotent | the DAG has a schedule (unpausing starts runs, possibly a catch-up) |

Every call uses the caller's token (ADR 0050 D9), so roles and scopes decide.
The MCP adds no privilege and keeps no allow-list of its own.

**Plan + apply.** Some clients do not ask the user before calling a tool, so one
call must not be able to clear a whole run or start a catch-up. A risky call
returns a plan instead of acting: what will happen, in words and as data, plus a
`plan_id`. The tool `apply_plan(plan_id)` then executes it.

- **The plan carries the exact operation.** It holds the action and its
  arguments, and for a clear, the list of task instances with their state at
  plan time. `apply_plan` takes only the `plan_id`. The model cannot change
  what is applied, and no value read from a log is used as an argument (D10).
- **Apply re-checks state.** If a planned task instance is no longer in the
  state the plan recorded, or the DAG's paused flag or schedule changed,
  `apply_plan` refuses and asks for a new plan. That also makes a plan
  effectively single-use: after a clear, its task instances are no longer
  `failed`.
- **A plan is bound and short-lived.** It expires 10 minutes after it is made.
  It is bound to the tenant, the subject and, when the token carries one, the
  client (`azp` or `client_id`) of the token that made it. `apply_plan` refuses
  a plan from another tenant, user or client. The MCP reads these claims
  unverified, only to compare them. The engine verifies the apply call's token
  as usual, so a forged token fails there.
- **Plans are stateless.** The HTTP transport is stateless and runs
  active-active (D4), so a `plan_id` is the plan itself, encoded and signed
  with HMAC-SHA256. The key comes from `--plan-key-file`, at least 32 bytes and
  shared by every replica. Run control on the HTTP transport refuses to start
  without it. On stdio, a random key per process is enough.

### 3. Pro source mode

Operator config `execution.source_mode.enabled` (default false) and
`execution.source_mode.image`, a runtime image pinned by digest.

- **Which versions.** A version runs in source mode when the flag is on, its
  `image` equals the configured runtime image, and it carries a `Source`. Any
  other version runs exactly as today. With the flag off, `Source` is ignored
  in Pro, as today.
- **How the file reaches the pod.** The Kubernetes executor puts the source in
  a pod annotation and projects that annotation into the task container as a
  read-only `dag.py` through a downward API volume. The container's working
  directory is set to that volume, where the Python runtime imports `dag`
  from, as it does in Lite's materialized work dir. This needs no new
  Kubernetes object, no new RBAC and no cleanup: the file lives and dies with
  the pod.
- **Size cap.** Kubernetes caps all of a pod's annotations together at 256 KiB.
  The source cap is therefore **128 KiB**, which leaves room for the
  annotations the executor and operators already set. When source mode is on,
  version register refuses a source-mode version whose `Source` is empty or
  over the cap (`400`). The cap is a constant, not a knob.
- **Unchanged.** Pod-per-task (ADR 0002), the task NetworkPolicy, secret
  delivery and ADR 0048 stay as they are. The control plane still never
  imports or runs `dag.py`. It carries the text, and only the task pod
  executes it. The DAG source is no more exposed than before, since `GET
  /api/v2/dagSources/{dag_id}` already returns it to readers.
- **Warm workers.** A warm worker (ADR 0058) is started before its task is
  known, so its pod cannot carry the annotation. Source-mode tasks always get a
  cold pod.
- **Limits, stated in the operate docs.** One file, only the packages in the
  runtime image, and no `dexaflow.yaml` build steps. A DAG that needs more
  builds an image as today.

Compiling `dag.py` into `dag.json` (ADR 0024) stays outside the engine, in the
CLI or in an operator's own service. The engine only stores and runs what was
registered.

### 4. Deploy stays outside the engine MCP

D5 and D6 stand. The engine MCP has no deploy or authoring tool on any
transport. An operator that wants agents to deploy builds that flow outside the
engine, on the existing register API, and gates it with `dexaflow:deploy`.
Section 3 is what makes such a flow possible without an image build: register a
version whose image is the runtime image and whose `Source` is the `dag.py`.

## Consequences

- An agent can go from "why did it fail" to "retry it" in one conversation, and
  the riskiest calls need an explicit second step that the model cannot alter.
- Granting an MCP client less than the user's full rights becomes possible, and
  read-only is the default for every issuer token that does not ask for more.
- The scope check adds one claim lookup per request for issuer tokens and
  nothing for engine tokens.
- Running HTTP run control means managing one more secret, the plan key.
- Pro gains a no-build path for single-file DAGs, bounded at 128 KiB and to the
  runtime image's packages. Supply-chain guarantees for that path rest on the
  digest-pinned runtime image, not on a per-DAG image.
- ADR 0050 itself is not edited by this ADR. Once this ADR is accepted, a
  separate change, with owner approval, adds a pointer to it from ADR 0050's
  status line.

## Alternatives considered

- **Deploy tools inside the engine MCP** (#1476, closed). This changes D6, and
  an operator can provide them outside the engine.
- **Plans stored by the engine.** A plan table and endpoints in the control
  plane would make plans single-use by construction. They would also add an
  API surface and state for a feature of one client. Signed plans with an
  apply-time re-check give the same guarantees for the operations in scope.
- **No plan step.** Some clients call tools without asking, so one call could
  clear a whole run.
- **One engine user per MCP client.** This breaks audit (actions no longer
  belong to the person) and membership removal.
- **A ConfigMap per task pod for the source.** This allows up to 1 MiB, but
  the executor would need ConfigMap RBAC in the task namespace, an owner
  reference, and cleanup on every failure path.
- **Fetching the code at task start from a URL.** This adds a network
  dependency and a new trust path into the pod.
- **A build service in the engine.** This is out of scope for the engine, and
  ADR 0048 keeps user code out of the control plane.

## Rollout

Each step is its own pull request, with tests first (ADR 0011):

1. #1473: the scope claim and the per-route scope table, with the docs listing
   the scopes.
2. #1474: the four tools, `apply_plan` and the plan key, with the MCP docs
   listing the tools.
3. #1475: source mode in the Kubernetes executor and the register check, with
   an e2e on kind and the operate docs.
