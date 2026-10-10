---
title: Backport to a release branch
linkTitle: Backport
weight: 30
description: Carry a change merged to main into release-X.Y so the next patch ships it, in the shape release-gap.sh recognizes.
---

Every change lands on `main` first; a patch ships it through a cherry-pick to
`release-X.Y` ([ADR 0062](/project/adrs/0062-release-branches-and-open-main/)).
Usually the maintainer does this step.

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
   guards run on release branches too.
4. **Do not stack picks.** A PR whose base is another `cp/*` branch runs no CI,
   and retargeting it later does not start one. Open each pick against
   `release-X.Y`; when one depends on another, wait for the first to merge.
5. **Check the gap** before asking for a cut:
   `scripts/release-gap.sh X.Y.Z` lists what `main` has that the release branch
   lacks. A change that will never ship in the minor goes in
   `.github/release-skip.txt` on the release branch, with its reason.
