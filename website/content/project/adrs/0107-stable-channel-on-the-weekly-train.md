---
title: "ADR 0107: A stable channel on top of the weekly patch train"
linkTitle: "0107 · Stable channel"
weight: 1070
description: "ADR 0107 (proposed): keep the weekly patch train and mark a patch stable once it has soaked, passed the chaos run and been out seven days without a regression report. The install script, the chart README and the production profile point production users at the newest stable patch, and every release body states its rollback class."
---

**Status:** Proposed
**Date:** 2026-11-06
**Relates:** ADR 0033 (rc tags and E2E gates), ADR 0068 (patch content gated by safety), ADR 0071 (stage, verify, promote), ADR 0102 (patches from main), #1557 (release notes opening), #2308

## Context

Dexaflow ships a patch a week. ADR 0068 lets a 0.x patch carry features and
migrations when they meet a safety bar, and ADR 0102 cuts every patch from
`main`. That suits development: a fix reaches users in days, and nothing waits
for a minor.

It does not suit a production operator. A patch a week means a possible
schema change a week, and nothing in a release says which patch has had time
on a running install. Every GA is equally "Latest" on GitHub, so the default
`install.sh` and a chart pinned to the newest version always take the newest
patch, the one with the least time in use. The project already has the signals
a production operator would want, but they are not attached to a version:

- The soak battery (`test/soak`) runs for 48 hours on the maintainer's
  hardware; CI only runs its six minute smoke, nightly and on rc tags.
- The chaos harness (`scripts/chaos`) exists but runs by hand.
- Regressions are reported as issues, with no label that ties them to the
  version they appeared in.

The release body states what changed. Since #1557 it opens with Breaking
changes and Action required; it still does not say how an operator gets back
if the upgrade goes wrong, which is the first question before a production
upgrade.

## Decision (proposed)

1. **The weekly train stays.** Every patch is still cut, tagged and published
   as today (ADR 0033, ADR 0071, ADR 0102). Nothing about its content changes.
2. **A patch becomes stable when all of these hold:**
   - it has been published for at least 7 days;
   - the 48 hour soak (`test/soak`) passed on that exact tag, recorded in its
     verdict file;
   - the chaos run (`scripts/chaos/run.sh`) passed on that tag;
   - no open issue labeled `regression` names it (the label is new; an issue
     gets it when a behavior that worked in an earlier GA broke, and names the
     first version it broke in).
   A later patch of the same minor that meets the bar replaces it. A patch
   found to carry a regression after it was marked stable loses the mark, and
   the previous stable patch is named again until a fixed one qualifies.
3. **One file records the channel.** `.github/stable.txt` on `main` lists the
   stable patch of each supported minor, one `X.Y vX.Y.Z` line each, changed
   only by pull request with the evidence above linked in its body. Tags stay
   immutable (ADR 0033); the mark is the file, not a moving tag.
4. **The defaults point at stable.**
   - `install.sh` gains `DEXAFLOW_CHANNEL=stable|latest`, default `stable`,
     which reads `.github/stable.txt` from `main`; `DEXAFLOW_VERSION` still
     pins an exact release.
   - The chart README, the quickstart and the production profile name the
     stable patch and say how to follow it.
   - The GitHub "Latest" mark stays on the newest patch: it is what GitHub
     shows on the repository page, and moving it backward would hide new
     patches from people who want them.
5. **Every release body states its rollback class**, in the opening #1557
   added, next to Action required:
   - **Binary swap:** no migration; rolling back is installing the previous
     version.
   - **Schema step:** migrations with down files that lose nothing; rolling
     back is `helm rollback` after running the down migrations to the
     previous schema, as `operate/upgrades.md` describes for that version
     (Lite has no down command and restores its snapshot instead).
   - **Restore:** a migration the previous version cannot run on, or one whose
     down loses data; rolling back is restoring the pre-upgrade backup
     (`operate/backup-restore.md`).
   The class is the most severe of the release's changes. A new changelog kind
   is not needed: the release notes script reads the class from the `Upgrade`
   entries, and a release with no `Upgrade` entry and no new migration is a
   binary swap. It also gains a **Known issues** section, from the open
   `regression` issues that name the version.
6. **The rc and release checks do not change.** Stable is a statement made
   after a release, from evidence gathered after it; it never blocks a cut.

## Alternatives

- **Fix-only patches, features in minors.** A patch would carry only fixes,
  and features would wait for a minor every few weeks. It is the usual model
  and the one ADR 0068 replaced: in the 0.5 line almost every patch carried a
  feature someone was waiting for, and splitting them would mean either slower
  features or a second branch to pick into, the cost ADR 0102 removed. It
  makes every patch safer, but it does not tell an operator which one has been
  run for a week.
- **No change until 1.0.** Production users take the newest patch and read
  its notes. Cheapest now, but it leaves every production operator to judge
  each patch alone, without a signal from the project, for the months until
  1.0.
- **A moving `stable` git tag or a `stable` image tag.** Simple to consume,
  but it breaks tag immutability (ADR 0033) and the signed provenance of an
  image tag. The file keeps every artifact immutable and points at one.

## Consequences

- Production users get a default that has run for at least a week, and the
  weekly train keeps its pace for everyone else.
- The 48 hour soak and the chaos run become release evidence and need a place
  to run every week: today that is the maintainer's hardware, which is the
  bottleneck this ADR adds. A self-hosted runner for both is the follow-up
  that removes it.
- The `regression` label needs triage discipline: a report has to name the
  version it first appeared in, or the mark cannot be judged.
- A stable patch can be up to two or three weeks behind the newest one, and a
  security fix in a newer patch is the exception: a `Security` entry fixing a
  vulnerability rated Medium or higher makes the stable mark move to that patch
  once its rc checks pass, without the 7 days.

## Open questions

- Who marks a patch stable: the maintainer by hand, or a scheduled workflow
  that opens the `.github/stable.txt` pull request when the evidence is in?
- Whether the soak and the chaos run on every patch or only on the candidate
  for stable each week.
- How many minors keep a stable line once 0.6 ships: only the newest, or the
  newest two while users move.
