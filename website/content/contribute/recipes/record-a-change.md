---
title: "Record a change: changelog and docs"
linkTitle: Record a change
weight: 20
description: Write the changelog fragment and the docs a pull request needs to pass the changelog and docs guards and the release cut.
---

1. **Changelog fragment.** Run `make changelog` and pick the kind:
   - `Added` for something new;
   - `Changed` for any behavior an operator or DAG author can notice, even
     when it fixes a bug: the docs gate reads `Breaking`, `Upgrade`, `Added`,
     `Changed`, `Deprecated` and `Removed`, and a behavior change filed as
     `Fixed` slips past it;
   - `Fixed` for a bug fix that changes nothing else;
   - `Security`, `Deprecated` or `Removed` when they apply;
   - `Breaking` for something that stops working as before, with what users
     do about it (an intended API break needs it, or `Changed` or `Removed`);
   - `Upgrade` for what an operator must do before or after upgrading: a
     migration and how long it takes, a new required setting, a removed flag,
     with a link to its section of [upgrades](/operate/upgrades/).

   A change can need two fragments: the `Changed` entry that says what is
   different, and the `Upgrade` entry that says what to do. `Breaking` and
   `Upgrade` entries open the release notes, under Breaking changes and Action
   required. `Highlights` is written for the release, not by each PR (see
   [RELEASING.md](https://github.com/dexadata/dexaflow/blob/main/RELEASING.md#release-notes)).

   One file per PR in `.changes/unreleased/`, so fragments never conflict. A PR
   with nothing user-facing uses the `skip-changelog` label instead.
2. **Docs.** Update the pages under `website/content/` that describe what you
   changed: reference for flags, configuration and API, how-to for procedures,
   [upgrades](/operate/upgrades/) for migrations and new settings. With
   nothing user-facing, use the `skip-docs` label and add a line
   `Skip-docs: <reason>` to the PR description.
3. **Milestone.** Every PR to `main` names the release it targets (`v0.5.3`,
   `v0.6.0`); the milestone guard fails without it.
4. **Check locally:** `bash scripts/check-changelog-entry.sh`,
   `bash scripts/check-changelog-structure.sh` and
   `bash scripts/check-docs-updated.sh`, or `make ci-local` for every gate.

## What happens to the fragment

You write the fragment; the release tooling does everything else with it.

```mermaid
flowchart LR
  pr["PR to main<br/>adds .changes/unreleased/x.yaml"] --> cutmain["next cut on main<br/>(patch or minor)<br/>folds x.yaml, deletes it"]
  pr -. "minor mapped to release-X.Y" .-> pick["cherry-pick -x<br/>to release-X.Y<br/>(same file, same text)"]
  pick --> cut["patch cut X.Y.Z<br/>folds x.yaml into CHANGELOG.md,<br/>deletes it, dates [X.Y.Z]"]
  cut --> sync["changelog: sync vX.Y.Z to main<br/>adds [X.Y.Z], deletes x.yaml on main"]
```

- **Do not edit `CHANGELOG.md` dated sections** (`## [X.Y.Z] - <date>`).
  They are published release notes, copied between branches verbatim.
- **Do not delete or rename a merged fragment yourself.** The cut deletes it
  when it ships, and after a patch release the sync pull request deletes it
  from `main`. A fragment renamed or reworded on one side of a cherry-pick is
  how one entry gets published twice.
- **When you cherry-pick,** keep the fragment exactly as it is on `main`
  (see [Backport to a release branch](/contribute/recipes/backport-to-a-release-branch/)).
- **The CHANGELOG structure check** (`scripts/check-changelog-structure.sh`)
  runs on pull requests that touch the changelog or a fragment. It fails a
  fragment that does not parse, has a kind `.changie.yaml` does not declare or
  an empty body, or whose text is already an entry of a released section, and
  a `CHANGELOG.md` whose headings are out of shape or that lacks the section of
  a tagged release.

The maintainer side (the fold, the dating, the sync and how to recover when it
drifts) is in
[RELEASING.md](https://github.com/dexadata/dexaflow/blob/main/RELEASING.md#how-the-changelog-flows).
