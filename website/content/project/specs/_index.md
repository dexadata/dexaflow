---
title: Specs
linkTitle: Specs
weight: 15
description: Design specs for large changes, written before the code; what will be built, how it upgrades, and how it is tested.
---

A spec describes what a large change will build before the code exists, so
review happens on the design and the PRs only implement it
([ADR 0069](/project/adrs/0069-contributor-context/)). An ADR records *why* a
choice was made; a spec records *what* gets built and how it ships. A spec
can lead to an ADR, and an ADR can call for a spec.

**Write a spec when** a change spans several PRs, adds a migration or a public
surface (API, configuration, CLI, Helm values, the agent protocol), or changes
scheduling, dispatch or security behavior.

**File it** as `NNNN-short-title.md` in this directory, copying the template
below, and open it as its own PR. Status moves from `Draft` to `Accepted` when
the PR merges, then to `Implemented` with links to the PRs that delivered it.

## Template

```markdown
---
title: "Spec NNNN: <title>"
linkTitle: "NNNN · <short title>"
weight: NNNN0
description: "<one sentence>"
---

**Status:** Draft | Accepted | Implemented | Withdrawn
**Issue:** #<n>
**Related ADRs:** <list>
**Target release:** vX.Y.Z

## Problem
What users or operators cannot do today, with evidence.

## Scope
In scope, and explicitly out of scope.

## Design
Components touched, data flow, and the interfaces that change:
- API (`docs/api/openapi.yaml`), with request and response examples
- configuration (`dexaflow.yaml`, `DEXAFLOW_*`, Helm values)
- CLI flags
- database schema and migrations

## Compatibility and safety
How each point of the ADR 0068 safety bar is met, or why this needs a minor.
Upgrade, rollback and the behavior when the feature is not used.

## Test plan
Unit, integration and end-to-end tests to add, written before the code
(ADR 0011), and the failure each one catches.

## Rollout
Gate name and default, docs pages to add or change, release notes text.

## Open questions
```
