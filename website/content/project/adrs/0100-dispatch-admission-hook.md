---
title: "ADR 0100: an admission hook admits, changes or refuses each task attempt before its token is minted"
linkTitle: "0100 · Dispatch admission hook"
weight: 1000
description: "ADR 0100: the dispatcher calls an operator's admission hook for every attempt, after its built-in refusals and before the agent token and the pod. The hook admits, admits with changes to the pod's placement and metadata, or refuses with a reason the task instance shows. Changes are checked again by the execution allowlist, so a hook cannot widen it. Without a hook nothing changes. ADR 0063's executor policy becomes a built-in hook. A webhook implementation follows for installs that do not build their own binary."
---

**Status:** Proposed
**Date:** 2026-10-29
**Relates:** ADR 0053 (admission and placement), ADR 0055 (agent token transport), ADR 0058 (warm pools), ADR 0062 (performance review), ADR 0063 (executor policy, proposed), ADR 0066 (resource unit), ADR 0099 (public server package)
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
   source mode. `Decision` is one of `Admit()`, `AdmitWith(Changes)` or
   `Refuse(reason)`.

2. **Where it runs.** After the built-in refusals of `refuseBeforeToken` (the
   unit, the allowlist, the image allowlist, the source-mode size), and before
   the agent token is minted and before warm placement. A refused attempt
   therefore gets no token and no pod, on Lite as on Kubernetes.

3. **What a hook may change.** The node selector (merged, the hook's keys
   win), tolerations (appended), the priority class, the service account,
   and labels and annotations outside the reserved `leoflow.io/` and
   `dexaflow.io/` prefixes. Not the image, the command, the environment, the
   namespace or the resources: the namespace is one per install (its RBAC,
   task secret and network policy are built for it), and a size change would
   charge the attempt a different number of pool slots than the scheduler
   admitted.

4. **Changes are checked again.** The changed fields go through the same
   execution allowlist check as a DAG's own fields, so a hook can narrow
   what runs but never widen what the allowlist forbids; a change the
   allowlist refuses refuses the attempt, naming the hook. An attempt the
   hook changed takes a dedicated pod, the same rule as a task that sets its
   own placement (`domain.WarmPolicy`).

5. **Refusals and failures.**
   - `Refuse(reason)` fails the attempt through `FailDispatchRefused` with
     `admission: <reason>`, the reason stripped of control characters and cut
     to 512 bytes, and counts it on a metric labelled by outcome.
   - An error, a timeout (`execution.admission.timeout`, default 2s) or a
     panic (recovered) is transient: the attempt goes back to the dispatch
     backoff without a token, like an unavailable Kubernetes API, and is
     counted. A hook that is down holds tasks rather than letting them run
     unchecked; `execution.admission.fail_open: true` admits them unchanged
     instead, and is logged at boot.

6. **No hook, no change.** Without one the dispatcher does one nil check
   and builds the request exactly as before, so ADR 0068's patch bar holds.
   A hook is registered with `pkg/server.WithAdmission`. Hooks chain in
   registration order; the first refusal wins, and each later hook sees the
   earlier changes.

7. **ADR 0063 becomes a built-in hook.** The executor policy from
   configuration is the first hook of the chain when it is set, so there is
   one place where an attempt is admitted. #1372 is rebased onto this
   interface instead of adding its own disposition path.

8. **A webhook for installs without their own binary**, as a second slice:
   `execution.admission.webhook.url` receives the request as JSON
   (`apiVersion: dexaflow.io/admission/v1`) and answers with the decision;
   TLS with an optional CA bundle, a bearer from a mounted file, the same
   timeout and fail mode. It is a `Hook` like any other.

## Consequences

- An operator expresses per-tenant placement or a refusal policy in a few
  lines of Go, or behind a webhook, and the user sees the reason on the task
  instance instead of a pod error.
- The hook runs on the dispatch path, so it costs every attempt one call.
  The PR that adds it gets the ADR 0062 specialist review, with a benchmark
  of the no-hook path and of a no-op hook.
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
2. `pkg/admission`, the dispatcher call, the re-check and the metric, with
   unit tests for each decision and for a hook that errors, times out or
   panics; the ADR 0062 review.
3. A k3d integration test: a refused task shows the reason, and no pod and
   no token were created.
4. The docs page and an `Example` hook.
5. The webhook implementation.
6. #1372 on top of the hook (ADR 0063).
