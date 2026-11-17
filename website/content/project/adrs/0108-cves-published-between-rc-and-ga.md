---
title: "ADR 0108: A CVE published between the rc and the GA does not stop the GA unless it is critical"
linkTitle: "0108 · CVEs between rc and GA"
weight: 1080
description: "ADR 0108: the rc gate keeps refusing every fixable HIGH or CRITICAL finding in the images we build. At the GA, a fixable CRITICAL still stops the release, while a fixable HIGH the vulnerability database learned about after the last rc opens an issue for the next patch with a seven day deadline and the release goes on. Nothing is tagged or published before the image scan has run."
---

**Status:** Accepted
**Date:** 2026-11-17
**Relates:** ADR 0033 (rc tags and E2E gates), ADR 0071 (stage, verify, promote), ADR 0102 (patches from main), #2247 (data image scan), #2484 (no publish before the data scan), #2485

## Context

The Release workflow scans the `data` runtime image's language packages and
refuses to publish it when Trivy reports a fixable HIGH or CRITICAL finding
that `.trivyignore.yaml` does not accept (#2247). Trivy downloads its
vulnerability database when the cached one is older than its next update time,
so two scans of the same image a few hours apart can disagree.

That is what stopped v0.5.10 on 2026-11-17. The v0.5.10-rc.5 scan ran at
12:44 UTC on a cached database and reported nothing. The GA scan ran at
14:28 UTC, downloaded a newer database, and reported eight HIGH findings in the
aiohttp 3.14.3 that ray 2.59.0 vendors for its runtime-env agent. The code was
the same in both images. The tag already existed, the GitHub release was a
draft, and the only ways forward were to delete the tag and cut again or to
skip to the next patch.

A release gate is useful when it stops something we shipped by mistake. A
finding published an hour before the GA is not that: the image was already
built and tested as an rc, and the same packages are running wherever the rc
or the previous patch is installed. Refusing the GA keeps users on the
previous patch, which carries the same finding plus whatever the new patch
fixes.

Projects that publish images on a schedule tend to treat the rc and the GA
differently. The rc gate refuses what is known. A finding that appears after
the freeze goes into a continuous process: a daily scan opens or updates an
issue, someone decides whether the vulnerable code is reachable, and a
deadline by severity sets when the fix ships. The exception document (VEX, or
our `.trivyignore.yaml`) records the decision when the answer is "not
reachable".

## Decision

1. **The rc gate stays as it is.** An rc tag refuses every fixable HIGH or
   CRITICAL finding in the images we build, unless `.trivyignore.yaml` accepts
   it with a statement and an expiry. That is where a fix costs the least.
2. **At the GA, only a fixable CRITICAL stops the release.** A fixable HIGH
   found by the GA scan does not, because the rc gate already refused every
   HIGH the database knew about. The Release workflow writes the findings to the
   job summary and opens one issue per release, titled
   `vX.Y.Z: HIGH findings published after its rc`, with the report attached.
3. **That issue has a deadline.** It goes into the next patch's milestone and
   is fixed or accepted in `.trivyignore.yaml` within seven days. A CRITICAL is
   never deferred: it stops the GA and is fixed before the cut.
4. **Nothing is tagged or published before the scan.** `scripts/cut-release.sh`
   scans the last rc's published images with a fresh database right before it
   pushes the GA tag, with the same rule as point 2, so a CRITICAL stops the cut
   while it is still a local command. In the workflow, the binaries, the GitHub
   release and the other images wait for the data image scan (#2484).
5. **The daily scan keeps watching.** `image-scan.yaml` already builds and scans
   every runtime variant, the data one included, every day at 06:20 UTC and keeps
   one tracking issue current. A finding that appears between two cuts shows up
   there first.

## Alternatives

- **Keep the GA strict.** It holds every published image to the newest
  database, but it turns a database update into a failed release, as on
  2026-11-17, and the fix then has to land under time pressure. The rc gate and
  the daily scan already cover what the strict GA gate adds.
- **Make the GA scan report only.** It removes the failure, but a CRITICAL that
  is published during the cut would ship without anyone deciding to ship it.
- **Accept the findings in `.trivyignore.yaml` during the cut.** It works, but
  it puts a security exception on the critical path of every unlucky cut, which
  is the pressure this ADR is meant to remove.

## Consequences

- A GA can ship with a HIGH finding that was published after its last rc. The
  release has an issue that says so, and the next patch carries the fix.
- A CRITICAL published during the cut still stops it, before the tag when the
  pre-tag scan catches it.
- The rc gate and the GA gate now use different severities, so the workflow
  decides from the tag name (`-rc.` in the tag means rc).
- The pre-tag scan in `cut-release.sh` and the Release workflow must keep the
  same rule. Changing one means changing the other.
