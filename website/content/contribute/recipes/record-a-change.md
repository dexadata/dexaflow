---
title: "Record a change: changelog and docs"
linkTitle: Record a change
weight: 20
description: Write the changelog fragment and the docs a pull request needs to pass the changelog and docs guards and the release cut.
---

1. **Changelog fragment.** Run `make changelog` and pick the kind:
   - `Added` for something new;
   - `Changed` for any behavior an operator or DAG author can notice, even
     when it fixes a bug: the docs gate at release cut reads `Added`,
     `Changed`, `Deprecated` and `Removed`, and a behavior change filed as
     `Fixed` slips past it;
   - `Fixed` for a bug fix that changes nothing else;
   - `Security`, `Deprecated` or `Removed` when they apply.

   One file per PR in `.changes/unreleased/`, so fragments never conflict. A PR
   with nothing user-facing uses the `skip-changelog` label instead.
2. **Docs.** Update the pages under `website/content/` that describe what you
   changed: reference for flags, configuration and API, how-to for procedures,
   [upgrades](/operate/upgrades/) for migrations and new settings. With
   nothing user-facing, use the `skip-docs` label and add a line
   `Skip-docs: <reason>` to the PR description.
3. **Milestone.** Every PR to `main` names the release it targets (`v0.5.3`,
   `v0.6.0`); the milestone guard fails without it.
4. **Check locally:** `bash scripts/check-changelog-entry.sh` and
   `bash scripts/check-docs-updated.sh`, or `make ci-local` for every gate.
