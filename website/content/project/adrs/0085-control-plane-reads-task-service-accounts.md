---
title: "ADR 0085: the control plane reads task ServiceAccounts for the register dry run"
linkTitle: "0085 · Control plane reads task ServiceAccounts"
weight: 850
description: "ADR 0085: the executor's Role gains one read verb, get on serviceaccounts in the task namespace, so the register dry run can tell a deploy, before the build, that the ServiceAccount a task pod would run as does not exist or carries no pull secret. The grant renders only in the all role; the api role of a split control plane stays without cluster access (ADR 0049) and reports the accounts unchecked."
---

**Status:** Accepted
**Date:** 2026-10-16
**Relates:** ADR 0041 (deploy without a pipeline, cluster wiring), ADR 0049 (split API and scheduler roles), ADR 0063 (executor policy), ADR 0068 (patch content gated by safety)
**Issues:** #1889, #1705

## Context

The deploy preflight (#1705) stops a deploy in seconds, before the build, on
anything the server or the registry would refuse. Two failures stayed out of
its reach because nothing on the deploy path could see them: a task
ServiceAccount that does not exist in the task namespace, and one that
carries no pull secret for a private registry. Both surface only when a task
pod starts, after the version has registered, as a pod creation refusal or
as `ErrImagePull`.

The CLI cannot see the cluster: a deploy from CI rarely has a kubeconfig, and
one that has may point at another cluster. The control plane can, but its
Role in the task namespace granted only what the executor calls (pods, their
logs, the staging PVC and the warm pool anchor ConfigMap), checked both ways
by `scripts/rbac-covers-executor.sh`. A check of the ServiceAccount needs a
new permission, which is a decision about the control plane's privileges,
not only about the deploy.

## Decision

1. **The executor's Role grants `get` on `serviceaccounts` in the task
   namespace.** One verb on one resource: no `list` or `watch`, so the
   control plane cannot enumerate the accounts, and nothing on `secrets`,
   so it never reads the pull secrets an account names. It reports how many
   there are, not their names.
2. **Only the all role holds it.** The rule renders when `split.enabled` is
   off. In a split control plane the Role is bound to the scheduler, which
   does not serve the dry run, and the api role stays without cluster access
   as ADR 0049 decided; its dry run reports each account unchecked, with
   that reason, and the deploy prints a note.
3. **The call lives in `internal/executor`** (`ServiceAccounts`), where
   `scripts/rbac-covers-executor.sh` sees it and fails CI if the Role stops
   granting it. The API consumes it through an interface of its own, so it
   imports no Kubernetes package.
4. **The dry run, not the register, reports it.** The answer of
   `POST /api/v2/dags/{dag_id}/versions/dry_run` gains a `task_pods` field:
   whether task pods run as non-root, and per ServiceAccount the tasks that
   use it, whether it was found, missing or unchecked, and its pull secret
   count. A dry run looks up at most 8 accounts, within 5 seconds in all; a
   lookup that fails is unchecked, with a fixed detail, and its error goes to
   the server log only. The register's answer does not change.
5. **The CLI decides what stops a deploy.** A missing account stops it,
   since those pods would never be created. An account with no pull secret
   is a note: nodes may pull with credentials of their own (IRSA, Workload
   Identity, a credential helper), which the control plane cannot see.

## Consequences

- A deploy names a missing task ServiceAccount, its tasks and the fix before
  anything is built, on the install shape most clusters run (one role).
- An install upgrading the chart gains the verb with the upgrade; an
  operator who manages the Role outside the chart gets an unchecked note on
  each deploy until the verb is granted. Nothing that worked before stops
  working (ADR 0068).
- A deployer of any tenant can learn whether a ServiceAccount name exists in
  the shared task namespace, and how many pull secrets it has. A deployer
  can already name any account allowed by
  `executor.defaults.execution_allowlist.service_accounts` in a task, which
  the dry run checks first, so this tells little more; the names of the
  secrets are never sent.
- A split control plane does not get the check. Granting the api role a
  read would undo the point of ADR 0049; if split installs need it, a later
  decision can let the scheduler answer it.

## Alternatives considered

- **The CLI reads the cluster with the deployer's kubeconfig.** Rejected:
  most CI deploys have none, and a kubeconfig may name another cluster than
  the server deployed to, so the answer would be wrong or missing.
- **No Kubernetes call: report the configured default account and whether
  the chart gave it pull secrets.** Rejected: it cannot see an account the
  operator brought, nor one a task names, which is where the failures come
  from.
