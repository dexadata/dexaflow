---
title: "ADR 0100: an admission hook admits, changes or refuses each task attempt before its token is minted"
linkTitle: "0100 · Dispatch admission hook"
weight: 1000
description: "ADR 0100: the dispatcher calls an operator's admission hook for every attempt, after its built-in refusals and before the agent token and the pod. The hook admits, admits with changes to the pod's placement and metadata, or refuses with a reason the task instance shows. A service account a hook sets is checked against the execution allowlist, and the configured executor policy runs last, so a hook cannot undo it. Without a hook nothing changes. ADR 0063's executor policy becomes a built-in hook. A webhook implementation follows for installs that do not build their own binary."
---

**Status:** Accepted
**Date:** 2026-10-29
**Accepted:** 2026-10-29
**Relates:** ADR 0053 (admission and placement), ADR 0055 (agent token transport), ADR 0058 (warm pools), ADR 0062 (specialist review, §4), ADR 0063 (executor policy, proposed in #1365, not yet in this tree), ADR 0066 (resource unit), ADR 0099 (public server package)
**Issues:** #2177, #2176, #1372

## Context

What a task may put on its pod is decided by configuration only: the
execution allowlist (#629, #537) checks fields, priority classes, service
accounts and images, and the resource unit (ADR 0066) checks sizes. Both run
in `Dispatcher.refuseBeforeToken`, before the agent token is minted, and a
refusal fails the task through `FailDispatchRefused` with a reason the task
instance shows.

Anything richer, such as a node selector or a priority class chosen per
tenant, or a refusal for a reason only the operator knows (a freeze window, a
budget), is today left to Kubernetes admission. That runs after the control
plane built the pod and minted a per-attempt token, and its refusal surfaces as
a pod creation error spent against the dispatch retries, not as a task state
with a readable reason. It also does nothing on Lite, which has no pod.

ADR 0063 (proposed, #1365, first slice in draft #1372) adds an operator policy
from configuration that forces or restricts pod fields at the same point. It
is one fixed policy; the request here is for the operator's own code.

"Admission" here is per task attempt, at dispatch. It is not ADR 0053's
Stage 1 admission, which decides how many tasks may run at once (pools,
concurrency limits) before an attempt reaches the dispatcher.

## Decision

1. **One hook, called for every attempt.** The contract lives in
   `pkg/admission` (ADR 0099):

   ```go
   type Hook interface {
       Admit(ctx context.Context, req *Request) (Decision, error)
   }
   ```

   `Request` is a copy, not the dispatcher's own value: tenant, DAG, run,
   task, try, operator type, the image the run resolved, the task's resources
   and its execution fields (service account, node selector, tolerations,
   priority class, labels, annotations), and whether the attempt would run in
   source mode. It carries the effective values: the resources after the
   resource unit or the defaults are applied, and the service account after
   the default, with a flag for whether the task chose it. Those defaults
   are applied after the token today, so the dispatcher computes them before
   the hook. `Request` never carries the environment, the source, secrets or
   the token. `Decision` is one of `Admit()`, `AdmitWith(Changes)` or
   `Refuse(reason)`.

2. **Where it runs.** After the built-in refusals of `refuseBeforeToken` (the
   unit, the allowlist, the image allowlist, the source-mode size), and before
   the agent token is minted and before warm placement. A refused attempt
   therefore gets no token and no pod, on Lite as on Kubernetes.

3. **What a hook may change.** The node selector (merged, the hook's keys
   win), tolerations (appended), the priority class, the service account,
   and labels and annotations outside the reserved `leoflow.io/` prefix
   (`domain.ReservedMetadataPrefix`) and `dexaflow.io/`, which this ADR
   reserves for hook changes. Not the image, the command, the environment, the
   namespace or the resources: the namespace is one per install (its RBAC,
   task secret and network policy are built for it), and a size change would
   charge the attempt a different number of pool slots than the scheduler
   admitted.

4. **What the allowlist checks.** The execution allowlist is checked on the
   DAG's own fields before any hook runs, as today. A hook's changes are not
   author fields, so they are not checked against
   `execution_allowlist.fields`: an install with `fields: none` still lets a
   hook set the node selector or tolerations, which is the per-tenant
   placement case above. A service account a hook sets must be in
   `execution_allowlist.service_accounts` when that list is set, and is
   exempt when it is the default task ServiceAccount, as in the executor;
   a service account the allowlist refuses refuses the attempt, naming the
   hook. An attempt the hook changed takes a dedicated pod, the same rule
   as a task that sets its own placement (`domain.WarmPolicy`):
   `DedicatedReason` is evaluated on the task with the hook's changes
   applied. The register response may still report warm for a task that a
   hook later sends to a dedicated pod.

5. **Refusals and failures.**
   - `Refuse(reason)` is permanent: it fails the task through
     `FailDispatchRefused` without retries, and the task instance shows
     `dispatch refused: admission: <reason>`, the reason stripped of control
     characters and cut to 512 bytes. A hook that wants a temporary hold
     (a freeze window, a budget that resets) returns an error instead.
   - An error, a timeout (`execution.admission.timeout`, default 500ms) or
     a panic is transient: the dispatcher returns `executor.Unavailable`
     with the cause `admission hook unavailable: ...`, so the task is
     offered again without spending dispatch attempts and fails as
     `dispatch_failed` once the ADR 0093 window (15 minutes) passes. A hook
     that is down holds tasks for that window rather than letting them run
     unchecked; `execution.admission.fail_open: true` admits them unchanged
     instead, and is logged at boot.
   - The panic recovery wraps every hook call, on synchronous dispatch as
     on the buffered workers. On synchronous dispatch (the default,
     `scheduler.dispatch.buffer_size: 0`) the hook runs on the scheduler
     tick, so after a number of consecutive failures a breaker skips the
     hook for its backoff instead of waiting out the timeout on every
     attempt.
   - Outcomes and latency are counted on a metric labelled by outcome
     only, never by tenant or DAG.

6. **No hook, no change.** Without one the dispatcher does one nil check
   and builds the request exactly as before, so ADR 0068's patch bar holds.
   A hook is registered with `pkg/server.WithAdmission`. Hooks chain in
   registration order; the first refusal wins, and each later hook sees the
   earlier changes.

7. **ADR 0063 becomes a built-in hook.** The executor policy from
   configuration is the last hook of the chain when it is set, and its
   restrict rules apply to the result of every earlier hook, so an operator
   hook cannot undo what the policy forces. A built-in hook may also set
   `runtime_class_name` and fill empty resource limits within the unit, as
   ADR 0063 does; operator hooks may not. There is one place where an
   attempt is admitted, and #1372 is rebased onto this interface instead of
   adding its own disposition path.

8. **A webhook for installs without their own binary**, as a second slice:
   `execution.admission.webhook.url` receives the request as JSON
   (`apiVersion: dexaflow.io/admission/v1`) and answers with the decision;
   TLS with an optional CA bundle, a bearer from a mounted file, the same
   timeout and fail mode. No redirect is followed, the response is capped at
   64 KiB and decoded strictly, and each request carries a UID the answer
   must echo. A webhook may change the service account only when
   `execution_allowlist.service_accounts` is set, so whoever controls the
   endpoint cannot move a task onto any ServiceAccount of the namespace. It
   is a `Hook` like any other.

## Consequences

- An operator expresses per-tenant placement or a refusal policy in a few
  lines of Go, or behind a webhook, and the user sees the reason on the task
  instance instead of a pod error.
- The hook runs on the dispatch path, so it costs every attempt one call.
  The PR that adds it gets the ADR 0062 specialist review, with a benchmark
  of the no-hook path, of a no-op hook and of a hook at its timeout on
  synchronous dispatch.
- A slow hook slows dispatch; the timeout bounds it, and the metric on
  outcomes and latency shows it.
- The hook sees tenant and DAG names and the task's execution fields. A
  webhook sends them to the operator's endpoint, so the docs say so and the
  URL must be `https` unless it is loopback.

## Alternatives considered

- **Kubernetes admission only.** Runs after the token and the pod spec, on
  Kubernetes only, and its refusal is a pod error.
- **Call the hook just before `Executor.Execute`**, as #2177 first proposed.
  The token is already minted there and warm placement already happened, so
  a refusal could not keep the promise of no token and no pod.
- **Keep ADR 0063 as its own path.** Two places would decide whether an
  attempt runs, with two orders to reason about.

## Plan

1. This ADR, accepted, together with ADR 0099 (the hook is registered
   through its options).
2. `pkg/admission`, the dispatcher call, the service account check, the
   breaker and the metric, with unit tests for each decision and for a hook
   that errors, times out or panics (on synchronous and buffered dispatch);
   the ADR 0062 review.
3. A k3d integration test: a refused task shows the reason, and no pod and
   no token were created.
4. The docs page and an `Example` hook.
5. The webhook implementation.
6. #1372 on top of the hook (ADR 0063).
