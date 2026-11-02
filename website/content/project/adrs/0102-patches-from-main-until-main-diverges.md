---
title: "ADR 0102: Patches are cut from main until main diverges from the minor"
linkTitle: "0102 · Patches from main until main diverges"
weight: 1020
description: "ADR 0102: every release, patches included, is cut from main for as long as everything on main may ship in the minor's next patch. A release-X.Y branch opens only when main takes a change that may not, and .github/patch-branches.txt records it. Amends ADR 0062, which opened a release branch for every minor right after its GA."
---

**Status:** Accepted
**Date:** 2026-11-02
**Accepted:** 2026-11-02
**Amends:** ADR 0062 (release branches per minor). Main stays open, cherry-picks with `-x` stay the only way into a release branch, and the RC discipline of ADR 0033 and the patch safety bar of ADR 0068 are unchanged. What changes is when a release branch exists.
**Relates:** ADR 0068 (patch content gated by safety), ADR 0071 (release promotion)

## Context

ADR 0062 opened `release-X.Y` from the `vX.Y.0` tag right after every minor
GA and cut every later patch from it, the model Kubernetes uses. It exists to
keep work that is not ready for a patch out of one while `main` stays open.

In the 0.5 line that protection never held anything back. ADR 0068 lets
features and migrations ship in a patch while the project is 0.x, the owner
asked for every change merged to `main` to reach the next patch, and
`.github/release-skip.txt` on `release-0.5` stayed empty from v0.5.1 to
v0.5.8. The branch carried what `main` carried, under other commit ids.

It still had a full cost. In the 30 days before this ADR, 192 of the 682
merged pull requests were cherry-picks to `release-0.5`. Each pick ran the
full CI twice more (its pull request and its merge), on an Actions plan that
runs 20 jobs at once while one CI run alone is about 40. The release tooling
grew around the branch: `release-gap.sh`, the gap watch issue, the changelog
sync back to `main` after every patch, and a cut that has to fold, date and
sync across two branches. A failed candidate cost a fix on `main`, a pick and
a new prepare before the next tag could exist. For comparison, google/adk-go
tags its releases straight from `main` and keeps a branch only for the
previous major, fed by a label-driven backport workflow.

## Decision

1. **Every release is cut from `main` by default**, patches included:
   `X.Y.0`, its candidates, and every `X.Y.Z` (Z > 0) with its candidates.
2. **A minor gets a release branch only when `main` diverges from it**: when
   a change that may not ship in that minor's next patch is about to merge (a
   feature of the next minor that is not ready to ship, a breaking change).
   That pull request creates `release-X.Y` from the minor's newest GA tag and
   adds `X.Y release-X.Y` to `.github/patch-branches.txt` on `main`. From then
   on that minor's patches are cut from the branch, and its fixes reach it as
   ADR 0062 describes: merged to `main` first, then `git cherry-pick -x`,
   batched in one pull request per day or per candidate.
3. **One place decides the base.** `scripts/release-base.sh <version>` reads
   `.github/patch-branches.txt` from `origin/main` and prints `main` or the
   mapped branch. `cut-release.sh`, `release-gap.sh`, `docs-gap.sh` and the
   Docs gap workflow ask it instead of deriving the base from the version.
4. **The cut refuses a patch from `main` once `main` has moved on**: when
   `main`'s chart names a later minor than the patch, the minor needs its
   release branch first.
5. **The 0.5 line returns to `main`.** v0.5.1 to v0.5.8 came from
   `release-0.5`; v0.5.9 and later 0.5 patches come from `main`, and
   `release-0.5` takes no more picks. `docs-gap.sh` judges the first patch
   from `main` from the previous GA reachable from `main` (`v0.5.0`) but
   leaves out what the release branch patches already shipped, read from their
   cherry-pick lines, since each was judged at its own cut.

## Consequences

- A change merged to `main` reaches the next patch with no further pull
  request. For 0.5 that removes the picks and their CI runs, the release gap
  checks and watch issue, and the changelog sync after each patch GA (a patch
  cut from `main` folds and dates on `main` itself).
- `main` has to stay shippable as a patch. A change that is not ready for the
  next patch either waits on its branch or opens the minor's release branch,
  which is now a recorded decision in a reviewed file rather than a standing
  default.
- Patch tags are again on `main`'s history, so `git describe`, bisecting
  between patches and the docs gap range work without cherry-pick bookkeeping.
- While a minor is mapped, everything ADR 0062 built still applies to it
  unchanged: picks, `release-gap.sh`, `release-skip.txt`, the changelog sync.

## Alternatives considered

- **Keep ADR 0062 and only batch the picks.** It cuts the number of pick pull
  requests but keeps two CI runs per batch, the gap tooling, the changelog
  sync and the extra round trip when a candidate fails, all for a branch that
  held nothing back.
- **Release-please style releases from `main` with no release branches at
  all.** Simpler still, but it gives up the option ADR 0062 was written for:
  holding a minor steady while `main` starts the next one. This ADR keeps that
  option and pays for it only when it is used.
