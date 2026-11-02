---
title: Placement allowlist
description: Restrict which task placement fields (priority class, node selector, affinity, tolerations and the rest) DAG authors may set on a shared cluster.
weight: 79
---

A DAG author can set placement and scheduling fields on each task under
`execution` in `dexaflow.yaml`: `priority_class_name`, `node_selector`,
`affinity`, `tolerations`, `topology_spread_constraints`, `runtime_class_name`,
`resource_claims` and more. Dexaflow applies every declared field to the task
pod. That is what a single team wants, but on a cluster shared by several teams
these fields reach beyond the DAG:

- `priority_class_name` can jump the preemption queue above other teams' work.
- `node_selector`, `affinity` and `tolerations` can target another team's
  dedicated (tainted) node pool.
- `labels` can match another team's NetworkPolicy or other selectors.

The structured fields (`tolerations`, `affinity`,
`topology_spread_constraints`, `resource_claims`, and `claims` under
`resources`) are checked against the Kubernetes types when the DAG is
registered, whatever the allowlist says. A misspelled key or a value of the
wrong type refuses the push with `400`, naming the task and the field, for
example `task "train" declares invalid execution.tolerations[0]: json: unknown
field "keyy"`. Keys use the Kubernetes spelling (`tolerationSeconds`,
`maxSkew`, `nodeAffinity`). Before this check such an entry was dropped when
the pod was built, so the task ran without it
([#628](https://github.com/dexadata/dexaflow/issues/628)).

The execution allowlist lets the cluster operator decide which of these fields
an author may set, and which priority classes. It is a cluster setting, like
[task pod hardening](/operate/task-pod-hardening/): an author cannot widen it
from a DAG.

## Default: everything allowed

Without configuration every field is allowed and any priority class is
accepted, exactly as before. Nothing changes until you set the list.

## Restricting fields

```yaml
executor:
  defaults:
    executionAllowlist:
      fields: [tolerations, node_selector, priority_class_name]
      priorityClasses: [etl-low, etl-normal]
```

As environment variables (both `DEXAFLOW_` and `LEOFLOW_` prefixes work, each
list is one comma-separated value):

```bash
DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_FIELDS=tolerations,node_selector,priority_class_name
DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_PRIORITY_CLASSES=etl-low,etl-normal
```

In a server config file the key is
`executor.defaults.execution_allowlist.fields` and `.priority_classes`.

The fields the list governs, by their `dag.json` names:

| field | why it matters on a shared cluster |
|---|---|
| `priority_class_name` | preemption order against other teams |
| `node_selector`, `affinity`, `tolerations` | which nodes the pod can land on |
| `topology_spread_constraints` | how pods spread across zones and nodes |
| `runtime_class_name` | which container runtime runs the pod |
| `resource_claims` | which devices (for example GPUs) the pod claims |
| `termination_grace_period_seconds` | how long the pod holds its node after a stop |
| `labels`, `annotations` | what other selectors and controllers see on the pod |

`*` (the default) allows all of them and `none` allows none. An explicitly
empty list (`fields: []`) also allows none, in a server config file and in Helm
alike, so writing an empty list to lock placement down never opens it. Only a
list you leave unset (or an empty environment variable, which counts as unset)
falls back to `*`. `service_account` and `image_pull_policy` are not
governed by this list. An unknown name fails boot, so a typo cannot silently
allow or refuse the wrong field.

`priorityClasses`, when set, is the list of values `priority_class_name` may
take. Leave it empty to accept any priority class (as long as
`priority_class_name` itself is allowed).

## Restricting service accounts and images

The same section also limits who a task runs as and what it runs:

```yaml
executor:
  defaults:
    executionAllowlist:
      serviceAccounts: [etl-reader, reports]
      images: ["registry.example.com/team-a/*", "busybox:1.36"]
```

```bash
DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_SERVICE_ACCOUNTS=etl-reader,reports
DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_IMAGES=registry.example.com/team-a/*,busybox:1.36
```

- `serviceAccounts`, when set, is the list of values `execution.service_account`
  may take. A task that names no service account runs as the operator's task
  ServiceAccount (`executor.task_service_account`, Helm `taskServiceAccount`),
  which is never checked. Without this list an author can name any
  ServiceAccount in the task namespace, and with it whatever cloud identity that
  account is bound to.
- `images`, when set, is the list of DAG images that may run. An entry is an
  exact image reference, or a prefix ending in `*` (`*` is allowed only at the
  end). Prefer a prefix that ends in `/` so `registry.example.com/team-a*` does
  not also match `team-ab`.
  With [source mode](/operate/source-mode/) on, a DAG pushed in source mode
  runs on the runtime image, so list that image too or those DAGs are refused.
  With [warm pools](/operate/warm-pools/) on, a DAG version whose image is
  outside the list gets no warm workers, so no warm pod ever starts on it;
  warm workers left over from before the list changed are drained.

Both are empty by default, which allows any service account and any image.

## What an author sees

A DAG whose task sets a field outside the list is refused when it is pushed:
the registration answers `400` naming the task and the field, for example

```text
task "train" sets execution.affinity, which this cluster does not allow DAGs to
set (executor.defaults.execution_allowlist.fields allows: tolerations,
node_selector, priority_class_name)
```

A DAG registered before you tightened the list is still checked: each task that
breaks the new list fails at dispatch with the same message, once, without
spending dispatch retries or the task's own `retries`. The task pod is never
created.

To set placement per tenant yourself instead of leaving it to DAG authors, or
to refuse work by a rule of your own, use an
[admission hook](/operate/admission-hooks/): it runs after this check and may
set fields this list keeps from authors.
