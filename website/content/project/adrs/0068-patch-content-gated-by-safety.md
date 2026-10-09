---
title: "ADR 0068: Patch content in 0.x is gated by safety, not by kind"
linkTitle: "0068 · Patch content gated by safety"
weight: 680
description: "ADR 0068: while Dexaflow is 0.x, a patch may carry features and migrations when they meet a fixed safety bar; reviews check the bar instead of asking for a minor."
---

**Status:** Accepted
**Date:** 2026-10-09 (proposed and accepted the same day by the project owner)
**Relates:** ADR 0037 (version scheme), ADR 0062 (release branches; this ADR replaces the fix-only list in its section 3 and the per-change exceptions in its section 6), ADR 0033 (RC tags and E2E gates).

> **Numbering.** 0063 (#1365) and 0067 (#1517) are open PRs, so this record
> takes 0068.

## Context

ADR 0062 allows only bug, regression and security fixes on a release branch,
and every other change needs a recorded exception. In practice every patch of
the 0.5 line needed one: the performance work in v0.5.1 (section 6), the
`default_pool_slots` field (6.1) and weighted pool slots with two migrations
in v0.5.2 (6.2). Each exception was granted, and each was granted for the same
reason: the change was inert until used, it upgraded and rolled back cleanly,
and waiting for a minor with no date would have held back work that was ready.

The rule therefore did not decide anything; the safety analysis did. Reviews
keep raising "this adds a migration, so it belongs in a minor", and the owner
keeps overruling it with the same argument. This record writes that argument
down once, as the rule.

## Decision

### 1. What a patch may carry

While the major version is 0, a patch (`v0.Y.Z`, Z > 0) may carry any change,
features and migrations included, that meets every point of the safety bar in
section 2. What decides the version is the effect on the operator, not whether
the change is a fix or a feature.

### 2. The safety bar

1. **Upgrade is uneventful.** An operator moves from the previous patch of the
   minor with the documented upgrade steps and no other action. New settings
   are optional; nothing previously valid is rejected.
2. **Inert until used.** New behavior is opt-in, or keeps the previous default
   and output byte for byte for a workload that does not use it. Behavior an
   operator would notice sits behind a gate (ADR 0062 section 4).
3. **Migrations are compatible both ways.** Each migration is additive (new
   table, new nullable column or column with a constant default, new index
   built concurrently), the previous patch's binary still runs against the
   migrated schema, and its down file is tested. A migration that rewrites or
   drops data, or locks a large table, is not patch material.
4. **No break of a public surface.** `/api/v2/`, the agent protocol,
   `dexaflow.yaml`, environment variables, CLI flags and Helm values only gain
   things in a patch. The API break checks are green or the change waits.
5. **The candidate passed its reviews.** Every review of AGENTS.md section 1
   is recorded for the rc, including upgrade and rollback on realistic data
   for each new migration.

### 3. What goes to the next minor

A change that fails any point of the bar: a breaking change, a removal, a
migration that rewrites data, a new required setting, or a default that
changes behavior for existing workloads.

### 4. How reviews apply this

A review does not ask to move a change to a minor because it is a feature or
because it adds a migration. A review that thinks a change does not belong in
a patch names the point of section 2 it fails and the evidence. Release-branch
cherry-picks of such changes need no separately recorded exception.

### 5. End of this rule

This rule ends at 1.0.0. The 1.0 release decides the patch policy for the
stable line and records it in a new ADR.

## Alternatives considered

- **Fixes only in patches, everything else in a minor.** Standard SemVer and
  the Kubernetes practice. Rejected for 0.x: the minor number would move every
  few weeks to carry what is already safe, and the safety bar is the guarantee
  operators actually rely on.
- **Keep fix-only plus recorded exceptions (ADR 0062 as is).** Rejected: every
  patch needed exceptions argued from the same criteria, which made the rule
  noise and the reviews repetitive.

## Consequences

- Patches stay the delivery vehicle for finished work; the release cadence
  does not depend on minor cuts.
- Features with migrations reach `release-X.Y` as cherry-picks, in migration
  order, so the cherry-pick work and `scripts/release-gap.sh` stay part of
  every patch.
- Release notes of a patch that carries migrations list them under
  "Action required" with the expected run time.
- The versioning policy page and RELEASING.md state this rule; operators who
  automate patch upgrades are told that patches may run migrations.
