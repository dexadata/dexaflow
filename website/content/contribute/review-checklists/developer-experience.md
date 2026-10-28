---
title: "Review checklist: developer experience"
linkTitle: Developer experience
weight: 40
description: Review a new setting, flag, value, command or error from the side of the person who meets it, starting with what happens when they set nothing.
---

Use this checklist when a change adds or changes something a DAG author or an
operator touches: a `dexaflow.yaml` key, a server setting, a Helm value, a CLI
command or flag, or an error message.

## The default when nothing is set

- [ ] **Name the default and what it does.** Read it from the code
  (`serverDefaults` in `internal/config/server.go`, `helm/dexaflow/values.yaml`,
  the flag definition), not from the PR description.
- [ ] **An install that sets nothing behaves as before.** A new setting is
  optional and inert until used, and nothing previously valid is refused
  ([ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/), safety bar
  points 1 and 2). Warm pools, for example, default to off, which is the
  dedicated pod per task of earlier releases.
- [ ] **The same default everywhere.** The server default, the chart default
  and the documented default agree. `TestRegisteredKeysAreDocumented` checks
  that a server key is documented at all; whether the stated default is right
  is the reviewer's job.

## Author surface or operator surface

- [ ] **The setting is on the right side.** A DAG author controls
  `dexaflow.yaml` (validated by `docs/api/leoflow-yaml-schema.json`, layered as
  [ADR 0023](/project/adrs/0023-dag-authoring-config-binding/) describes); an
  operator controls server settings and Helm values. A choice about isolation
  or shared capacity belongs to the operator (`ExecutionSection` in
  `internal/config/server.go`).
- [ ] **The docs follow the side.** Author surface is described under
  [Author DAGs](/author-dags/) and in the `dexaflow.yaml` section of the
  [configuration reference](/reference/configuration/); operator surface under
  [Operate](/operate/) and in the server section of the same reference.
- [ ] **Both names work.** A new environment variable reads from `DEXAFLOW_*`
  and from `LEOFLOW_*` (`internal/config/AGENTS.md`); a new file key reads from
  `dexaflow.yaml` and `leoflow.yaml` alike.

## Footguns

- [ ] **A bad value fails early, with the fix in the message.** The server
  refuses it at boot, naming the key and what it accepts
  (`(*ServerConfig).Validate`); the chart refuses a combination the server
  would refuse with a `fail` that says what to set
  (`helm/dexaflow/templates/deployment.yaml`); compile refuses what the
  runtime cannot do. A crash loop or a silently ignored value is the bug.
- [ ] **A value that is set takes effect.** A documented variable that never
  binds is the failure of #725 (`TestDocumentedEnvVarsBind`). Drive the real
  entry point and check the effect, not only the parsed struct.
- [ ] **Turning on a stricter rule can be done in steps.** When enforcement
  would start failing existing work, a warn mode comes first, as
  `executor.unit.enforce: warn` does for the resource unit
  ([ADR 0066](/project/adrs/0066-weighted-pool-slots-and-resource-unit/)).
- [ ] **Destructive commands ask first.** `dexaflow uninstall` asks for
  confirmation unless `--yes` is given, and `dexaflow deploy` asks in an
  interactive session before it deploys to a server that is not on the local
  machine.
- [ ] **Errors say what to do next.** A refusal names the value that would be
  accepted (ADR 0066: `set size: 8`), and a deprecated setting says what to
  change and whether existing images keep running
  (`deprecationRemedy` in `internal/cli/compile_python_deprecation.go`).
