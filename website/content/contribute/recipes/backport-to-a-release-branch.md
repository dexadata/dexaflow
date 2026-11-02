---
title: Backport to a release branch
linkTitle: Backport
weight: 30
description: Carry a change merged to main into release-X.Y so the next patch ships it, in the shape release-gap.sh recognizes.
---

Every change lands on `main` first. Patches are cut from `main` too, so most
changes need no backport. Only a minor that `.github/patch-branches.txt` maps
to `release-X.Y` takes its fixes through a cherry-pick
([ADR 0102](/project/adrs/0102-patches-from-main-until-main-diverges/)).
Usually the maintainer does this step, batching the day's picks (or a
candidate's) in one pull request with one `cherry-pick -x` commit each.

1. **Branch from the release branch**, not from `main`:

   ```sh
   git fetch origin release-X.Y
   git switch -c cp/<pr-number>-<slug> origin/release-X.Y
   git cherry-pick -x <merge-sha-on-main>
   ```

   The `-x` line `(cherry picked from commit ...)` is what
   `scripts/release-gap.sh` matches; a pick without it counts as missing.
2. **Keep migration order.** Picks that carry migrations go in the order they
   merged on `main`, so the release branch keeps the same numbers.
3. **Open the PR against `release-X.Y`**, titled `[release-X.Y] <original
   title>`, linking the original PR, with its changelog fragment, and copying
   its `skip-changelog` and `skip-docs` labels and its `Skip-docs:` line; the
   guards run on release branches too. Keep the fragment's file name and text
   exactly as on `main`: after the patch ships, the changelog sync finds the
   copy on `main` by that name and text and deletes it, so the next minor does
   not publish it again
   ([what happens to the fragment](/contribute/recipes/record-a-change/#what-happens-to-the-fragment)).
4. **Do not stack picks.** A PR whose base is another `cp/*` branch runs no CI,
   and retargeting it later does not start one. Open each pick against
   `release-X.Y`; when one depends on another, wait for the first to merge.
5. **Check the gap** before asking for a cut:
   `scripts/release-gap.sh X.Y.Z` lists what `main` has that the release branch
   lacks. A change that will never ship in the minor goes in
   `.github/release-skip.txt` on the release branch, with its reason.
