---
title: Versioning and support
weight: 25
description: What a Dexaflow release may change, how long a minor line is supported, how features are deprecated, and what stays compatible across upgrades.
---

This page is the written promise about what a Dexaflow release may change. It
describes how the project already ships; the records it links hold the full
reasoning.

## Version numbers

Releases are tagged `vX.Y.Z`, with release candidates `vX.Y.Z-rc.N` before
each final and no separate alpha or beta
([ADR 0037](/project/adrs/0037-release-version-scheme/)). A new minor
(`X.Y.0`) is cut from `main`; every later patch of that minor is cut from its
`release-X.Y` branch
([ADR 0062](/project/adrs/0062-release-branches-and-open-main/)).

### What a patch may contain while the project is 0.x

Content is gated by safety, not by the version digit
([ADR 0068](/project/adrs/0068-patch-content-gated-by-safety/)). A patch may
carry features and migrations when every point below holds:

- **The upgrade is uneventful.** Moving from the previous patch of the minor
  takes the documented upgrade steps and nothing else. New settings are
  optional, and nothing that was valid before is rejected.
- **New behavior is opt-in**, or keeps the previous default and output for a
  workload that does not use it.
- **Migrations are compatible both ways.** Each one is additive, the previous
  patch's binary still runs against the migrated schema, and its down file is
  tested. The upgrade from the previous patch is exercised on realistic data.
- **No public surface breaks.** `/api/v2/`, the agent protocol,
  `dexaflow.yaml`, environment variables, CLI flags and Helm values only gain
  things in a patch.
- **The release candidate passed its reviews.** The review checklist in
  `AGENTS.md` section 1 is complete before the final.

A change that fails any point waits for the next minor: a breaking change, a
removal, a migration that rewrites data, a new required setting, or a default
that changes behavior for existing workloads.

There is no fixed minimum time between the last release candidate and the
final. The review checklist is the gate.

This rule ends at 1.0.0, which records the patch policy of the stable line in a
new ADR.

## Support window

| Line | What it receives |
|---|---|
| The newest minor | Every fix: bugs, regressions and security, as patches of its `release-X.Y` branch. |
| The previous minor | Security fixes only, for **8 weeks** after the next minor's `X.Y.0` final ships. |
| Older minors | Nothing. Upgrade to a supported line. |

Patches ship about weekly, so 8 weeks leaves room for several patches of the
new minor before the previous one stops receiving security fixes. How fast a
security fix is merged, by severity, is in the
[security policy](https://github.com/dexadata/dexaflow/blob/main/SECURITY.md).

## Upgrade path

A minor is upgraded from the latest patch of the previous minor. Within a
minor, any patch upgrades to any later patch. Migrations are forward-only in
production: every migration has a down file, which is tested, but a rollback
is a restore from backup, not a schema downgrade on live data. The
[upgrades guide](/operate/upgrades/) has the steps for Lite and Cluster.

## Deprecation

A feature, setting, flag or endpoint is removed only after it was deprecated:

1. **Announced.** The release that deprecates it says so in a `Deprecated`
   changelog entry and in the docs, with the replacement.
2. **Warned at runtime.** Using it logs or prints a warning that names the
   replacement.
3. **Removed no earlier than two minors later**, and never in a patch. Something
   deprecated in `0.6.0` can be removed in `0.8.0` at the earliest, with a
   `Removed` changelog entry.

### Names from before the rename

The names Dexaflow had as Leoflow (the `leoflow` command, `leoflow.yaml`, the
`LEOFLOW_*` variables, `~/.leoflow`, the `leoflow-*` images and the
`charts/leoflow` chart) keep working with **no removal date**. The `dexaflow`
names are the documented ones, and when both spellings are set, the new one
wins. The full list is in
[names from before the rename](/reference/configuration/#names-from-before-the-rename),
and [the name](/project/name/) tells the story.

## Compatibility promises

- **API.** `/api/v2/` follows Airflow 3.2.x
  ([API reference](/reference/api/)). Within a minor it only gains
  operations, fields and responses; the API break checks in CI hold every
  pull request to that.
- **DAG authoring.** A DAG that compiles on a patch compiles on every later
  patch of the same minor.
- **Configuration.** A `dexaflow.yaml`, environment or Helm values file that is
  valid on a patch stays valid on every later patch of the same minor.
