---
title: "ADR 0062: Release branches per minor, main always open"
linkTitle: "0062 · Release branches per minor, main always open"
weight: 620
description: "ADR 0062: main stays open for every change; each minor gets a release-X.Y branch that tags its patches; fixes land on main first and are cherry-picked after review."
---

**Status:** Accepted
**Date:** 2026-10-02
**Accepted:** 2026-10-02
**Relates:** ADR 0033 (RC tags and E2E gates), ADR 0037 (version scheme). This ADR keeps both: the RC discipline and SemVer are unchanged. It changes only which branch a tag is cut from and how a change reaches a release.

## Context

Every change lands on `main`, and every release is tagged on `main`:
`scripts/cut-release.sh` opens a `release/<tag>` prepare PR, squash-merges it
into `main` and tags the merge commit. That works while one release is in
flight and nothing else is. It stops working the moment a release has to wait.

v0.5.0 is the case that exposed it. The release is held on pending fixes and
an explicit go from the owner, while a performance review produced a batch of
changes (scheduler tick cost, dispatch buffering, Kubernetes client limits,
object log uploads, database indexes) that are ready to start. Under the
current flow there are two options and both are bad:

- Merge the performance work to `main` now, and v0.5.0 either ships it
  unreviewed in a release that was meant to be closed, or the cut has to
  exclude it by hand.
- Hold it on a long-lived integration branch until v0.5.0 is out, and pay for
  that with drift from `main`, merge conflicts on shared files and a CI that
  does not run there (every workflow triggers on `pull_request` to `main`
  only).

Large projects with continuous contribution solved this the same way.
Kubernetes keeps `master` open at all times, cuts a `release-1.N` branch per
minor, tags every `v1.N.x` from that branch, lands every fix on `master` first
and cherry-picks it back through a reviewed PR, and hides unfinished behavior
behind feature gates instead of behind a frozen branch.

## Decision

### 1. `main` is always open

Every change, including work meant for a patch release, merges to `main`
through a PR. A release in progress never freezes `main`. Branch protection
on `main` is unchanged.

### 2. One release branch per minor

When `vX.Y.0` is cut, a branch `release-X.Y` is created from that tag. Every
later tag of that minor (`vX.Y.Z-rc.N` and `vX.Y.Z`) is cut from
`release-X.Y`, never from `main`. The next minor cuts a new branch from
`main`; nothing ever merges a release branch back into `main`.

The name `release-X.Y` is distinct from the short-lived `release/<tag>`
prepare branches `cut-release.sh` creates, so the two never collide.

Only the newest release branch receives patches. An older one receives a
security fix only when the owner decides so for that fix.

### 3. Fix on `main` first, then cherry-pick

A change reaches a release branch only as a cherry-pick of a commit already
merged to `main`, opened as its own PR against `release-X.Y` with the title
prefixed `[release-X.Y]` and a link to the original PR. The one exception is
a fix for code that no longer exists on `main`; that PR says so.

What may be cherry-picked:

- bug fixes, regressions and security fixes;
- documentation for the release;
- changes allowed by a recorded exception (see section 6).

New features are not cherry-picked. They ship in the next minor.

### 4. Feature gates for behavior changes

A change that alters runtime behavior in a way an operator would notice, and
that is not plainly a bug fix, lands behind a configuration gate:

- The gate is a key in `dexaflow.yaml` with a matching `DEXAFLOW_*`
  variable. The legacy `leoflow.yaml` and `LEOFLOW_*` names are read as
  fallbacks, as for every other setting.
- A new gate defaults to off. It may default to on after it has shipped in
  at least one release and nothing in that release argued against it.
- Removing a gate (making the behavior permanent) is its own PR.

This is what lets risky work merge to `main` early without forcing it on
everyone who builds from `main` or takes the next release.

### 5. Review before merge and before cherry-pick

A change in a sensitive area (performance, scheduling, dispatch, executor,
storage, auth, migrations) needs a specialist review that covers five checks
before it merges to `main`, and the same review again on the cherry-pick PR:

| Check | Question the review answers |
|---|---|
| Regression | Do the benchmarks and the full test suite show no slowdown or behavior change outside the intended one? |
| Security | Does the change keep every isolation, authorization and secret handling guarantee? |
| Reliability | What happens on restart, leader change, partial failure and retry? |
| Compatibility | Do old configs, the Helm chart upgrade path and the Airflow 3.2 API surface still work? |
| Rollback | Can the change be turned off (gate) or reverted without a data migration? |

The cherry-pick review is not a formality: the release branch may lack
changes the original PR relied on.

### 6. Recorded exception: performance work ships in v0.5.1

The changes that come out of the October 2026 performance review are
cherry-picked into `release-0.5` and ship as `v0.5.1-rc.1` and then `v0.5.1`,
instead of waiting for a `v0.6.0` that has no date. Each of them still lands
on `main` first, passes the review in section 5, and sits behind a gate when
it changes behavior. This is allowed by ADR 0037, which already permits
non-breaking improvements in a patch; it is recorded here because it is wider
than the fix-only rule in section 3.

The exception covers only that review's items. Any other non-fix change for
`release-0.5` needs its own recorded exception.

### 7. Versions do not change

SemVer and the RC discipline of ADR 0033 and ADR 0037 stay as they are: a
patch is `vX.Y.Z+1`, every release goes through `-rc.N`, tags are immutable.

## Transition

1. v0.5.0 is cut with the current flow, unchanged.
2. Right after, `release-0.5` is created from the `v0.5.0` tag.
3. Before the first cherry-pick, two tooling changes land on `main`:
   - CI, changelog, docs and security workflows also trigger on pull
     requests to `release-*`.
   - `cut-release.sh` learns to prepare and tag on a release branch, while
     docs promotion and the version menu keep targeting `main`.
4. Performance PRs may merge to `main` before v0.5.0 is cut only when they
   are test-only (benchmarks, load harness) or sit behind a gate that
   defaults to off.

## Consequences

- Nobody waits on a release to merge. The cost moves to cherry-picks: each
  one is a second PR and a second review.
- `main` builds may contain gated, unreleased behavior. Gates default to off,
  so a build from `main` behaves like the last release unless an operator
  opts in.
- A fix that conflicts on cherry-pick needs a hand-adapted version for the
  release branch. The more the branches drift, the more often this happens,
  which is one more reason to keep patches to the newest minor only.
- Release notes for a patch are built from the fragments on the release
  branch, so a cherry-pick PR carries its changelog fragment with it.
