---
title: "ADR 0071: Releases stage, verify, then promote; only a promote job moves public pointers"
linkTitle: "0071 · Stage, verify, promote"
weight: 710
description: "ADR 0071: a release tag publishes only immutable, versioned artifacts and a GitHub release that is not Latest; one promote job, after every gate is green, pushes the Helm chart, marks Latest (GA of the newest minor only) and moves the py3.N runtime tags."
---

**Status:** Accepted
**Date:** 2026-10-12
**Relates:** ADR 0014 (supply chain signing), ADR 0028 (Helm chart as an OCI artifact, co-versioned with the tag), ADR 0033 (RC tags and E2E gates), ADR 0062 (release branches per minor). This ADR keeps all four. It changes when each public pointer moves, not what is built or how it is versioned.
**Issues:** #1684 (this change), #1685 (cut-release watch and queue preflight), #1686 (faster critical path), #1687 (signature coverage), #1688 (tag-triggered E2E), #878 (build once, promote the rc).

## Context

A tag push starts two workflows. `release.yaml` runs GoReleaser first, which
creates the GitHub release as a full release (Latest for a GA), and only then
builds the migrate and runtime images and runs the install, upgrade and cold
start smokes. A final `gate` job retracts the release to a draft when anything
failed. `helm-release.yaml` runs on the same tag event with no dependency on
either, and pushes the chart as soon as it gets a runner.

So three public pointers move before the artifacts they point at exist or have
been verified:

| Pointer | Followed by | Moves today |
|---|---|---|
| Helm chart `X.Y.Z` in `oci://ghcr.io/dexadata/charts` | every Kubernetes install and upgrade | about 30 seconds after the tag, before any image |
| GitHub release "Latest" | `install.sh` without a pinned version, people, Renovate and Dependabot | when GoReleaser publishes, before any smoke |
| `dexaflow-runtime:py3.N` | the generated DAG Dockerfile on development builds, the docs | when each runtime leg finishes, on rc tags too |

With an idle queue the window was 15 to 30 minutes on every 0.5.x release. On
v0.5.3 it passed an hour: the chart referenced a server image that did not exist
yet and a migrate image that took another hour, so `helm upgrade` failed on
`ImagePullBackOff` in the migration hook. The timeline of every 0.5.x release
is in #1684.

The gate cannot fix this by retracting harder. It runs last, it only knows how
to draft the GitHub release, and a chart version in an OCI registry is in
practice immutable once someone pulled it.

Two constraints shape the answer:

1. The smokes must keep testing the real public install path. `install.sh`
   downloads from the public `releases/download` URL, and a draft release does
   not serve its assets there (#979). So the release has to be public while the
   smokes run.
2. Public is not the same as Latest. `install.sh` without a pinned version reads
   `/releases/latest`, and GitHub lets a published release stay out of that
   pointer.

## Decision

**A release tag stages, the gate verifies, and a single `promote` job moves
every public pointer.**

1. **Stage.** The tag builds, pushes and signs only immutable, versioned
   artifacts: binaries, `dexaflow-server` and `dexaflow-mcp` at `vX.Y.Z` and
   `X.Y.Z`, `dexaflow-migrate` at the same tags, and `dexaflow-runtime` at
   `py3.N-vX.Y.Z`. GoReleaser publishes the GitHub release with
   `make_latest: false`, so it is public (the smokes can download it) but
   `/releases/latest` keeps serving the previous GA. An rc is a prerelease, as
   before.
2. **Verify.** The existing smokes, image builds and signature checks run, and
   the existing `gate` job decides. Its retract path is unchanged.
3. **Promote.** A `promote` job in `release.yaml` with `needs: gate`, which runs
   only when the gate succeeded, and in this order:
   1. verifies with cosign, pinned to this repository's release workflow
      identity, every image the chart or the moving tags reference
      (`dexaflow-server`, `dexaflow-mcp`, `dexaflow-migrate`, and each
      `dexaflow-runtime:py3.N-vX.Y.Z`); a missing image fails the same check;
   2. packages, pushes and signs both charts (`dexaflow`, and `leoflow` for
      installs from before the rename), for rc and GA alike, since the team
      installs rcs through the chart;
   3. for a GA of the newest minor only, marks the release Latest and moves
      `dexaflow-runtime:py3.N` and `leoflow-runtime:py3.N` to the digest of the
      versioned tag. An rc never moves either. A patch of an older minor, once a
      newer minor shipped, moves neither: it is published and pinnable, but it
      is not what an unpinned user should get. "Newest" is decided the same way
      the docs root promotion decides it: the tag is not older than any
      published, non-prerelease release.

   The chart preflight that lived in `helm-release.yaml` (chart version matches
   the tag, `helm lint`, the template contract) runs in the first job of
   `release.yaml`, so a bad chart fails the run before anything is published.
   `helm-release.yaml` loses its tag trigger and keeps `workflow_dispatch` as a
   manual recovery path.
4. **Every un-draft passes `--latest=false`.** The gate's green step and
   `cut-release.sh` both un-draft a release that an earlier attempt retracted.
   GitHub marks a release Latest by default when it goes from draft to
   published, so without the flag a recovery would move Latest before
   `promote` ran.
5. **The cut reads `promote`.** `cut-release.sh` treats the release as
   PUBLISHED only when the release run is green and its `promote` job concluded
   `success`, and opens the docs root promotion after that, as it does today.

`promote` is idempotent: rerunning it re-pushes the same chart bytes, re-signs
(an extra signature on the same digest is harmless), and re-points tags that
already point at the right digest. Recovery stays "fix, rerun the failed jobs".

## Consequences

- **Users who follow a pointer never see an unverified release.** The chart,
  `install.sh` without a pin, and the moving runtime tags all move in one job,
  after every smoke and signature check passed. This extends the rule the cut
  already applied to the docs root ("the root must never advertise a release
  whose artifacts are red or still draft") to every pointer.
- **Users who pin `vX.Y.Z` can see it early,** while the gate runs, as they can
  today. This is the accepted behavior for staged artifacts in the Kubernetes
  release process: a staged artifact is addressable, an announcement is not.
- **A retracted release no longer leaves a chart behind.** Before this change a
  gate failure drafted the GitHub release but left chart `X.Y.Z` in the
  registry, pointing at images that might not exist.
- **The chart arrives later than before** for rc testers: after the gate rather
  than 30 seconds after the tag. That is the point; rc testers who need the
  chart earlier can still install from the tag's source tree.
- **Release notifications still fire when GoReleaser publishes,** before the
  gate. Watchers see a release that is not Latest. Avoiding that would need a
  draft release, which breaks the smokes (constraint 1), so it is accepted.
- **The workflow file is the only place that publishes.** The operator's
  `cut-release.sh` can no longer be the thing that holds a pointer back, which it
  never could (the tag push triggers publication on its own); it watches and
  reports.
- **`release.yaml` only runs on a tag, so the first real execution is the next
  rc.** The change is validated before merge on a disposable tag in a fork,
  where `promote` exercises every step against the fork's own registry.

## Alternatives considered

- **Create the release as a draft and publish it at the end of the gate.** Breaks
  every smoke, since draft assets are not served from the public URL (#979), and
  making the smokes download with a token stops testing the public install path.
  It also does nothing for the chart.
- **Keep the chart in its own workflow and make it wait for the images.** Polling
  for images from a second workflow duplicates the gate, and the chart would
  still not wait for the smokes. A job inside `release.yaml` that needs the gate
  gets both for free.
- **Hold the pointers in `cut-release.sh`.** The tag push is what triggers
  publication; a script on the operator's machine cannot hold back what the
  workflow does, and a cut that dies mid-way must not leave the release
  half-promoted.
- **Dedicated runners for releases.** Shortens the window by removing the queue
  but does not close it; the ordering problem remains. Tracked as process work in
  #1685 (queue preflight) and #1686 (faster critical path).
- **Build once and promote the rc's artifacts at GA (#878).** Compatible with
  this ADR and the natural next step: `promote` is where a GA would retag the
  rc's images instead of rebuilding them. It needs reproducible version stamping
  first, so it is not part of this change.
