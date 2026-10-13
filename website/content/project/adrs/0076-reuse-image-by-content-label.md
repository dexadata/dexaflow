---
title: "ADR 0076: deploy reuses the image already in the registry under the content label"
linkTitle: "0076 · Reuse the image by content label"
weight: 760
description: "ADR 0076: the first form of the skip path of ADR 0073. When the image tag is the default content label and the registry already holds an image under it for every requested platform, deploy pins and registers that image without building or pushing. Records what the label does not cover and who decides what a reused image contains."
---

**Status:** Accepted
**Date:** 2026-10-13
**Relates:** ADR 0073 (daemonless image builds, the skip path), ADR 0075 (the registry is the source of the digest), ADR 0041 (`deploy` and digest pinning), ADR 0003 (a DAG version is a `dag.json` and an image)
**Issues:** #1706

## Context

ADR 0073 makes `skip` the first builder path: no build when the dependency
image for the build's dependency key is already in the registry. That key
needs the split of the image into a dependency image and a code layer, which
lands later in 0.5.4.

Until then, every deploy whose content is not already the current version
builds and pushes, even when the registry already holds the image for exactly
that content: a deploy to a second target that shares the registry, an image
a CI job already pushed, a retry after a register that failed.

The default version label already names content. It is a hash of the resolved
`dexaflow.yaml` (dependencies, connectors, `base_image`, platforms, registry),
the files the image copies, the project Dockerfile when there is one, and the
CLI version. With `tag_strategy: version` it is also the image tag. `deploy`
already trusts it: when the label is the current version, nothing is built.

## Decision

1. **Reuse by content label.** When the image tag is the default content
   label, `deploy` reads the registry before building. If the registry holds
   an image under that tag for every requested platform (an index listing
   each one, or a single image whose config names it), the deploy pins that
   image by the digest the registry returned and registers it. Nothing is
   built or pushed, and the output says `builder: skip`.
2. **When it never reuses.** An explicit `--dag-version`, a tag from git
   (`git_sha`, `git_tag`) that did not fall back to the label, `--force`, and
   the new `--rebuild` always build. An empty platform list, a missing
   platform, a single image whose config names no platform, and any registry
   error build too, and the builder line names the reason.
3. **The checks before a build still run.** A `build.dockerfile` outside the
   project is refused and secrets the image would carry are reported on the
   skip path as well, because the reused image was built from the same inputs.
4. **`deploy --plan` reads the registry** the same way and reports a reuse.
5. **This is the first form of the skip path.** Once images are split, the
   skip path keys on the dependency image of ADR 0073 section 4, and this
   whole-image reuse remains the case where nothing changed at all.

## Consequences

- **The label decides, as it already does for "already deployed".** A
  dependency without a pinned version, or a `base_image` tag, that gained a
  release upstream is not picked up by a reuse; neither is a change the label
  does not hash (an environment variable the image build reads).
  `--rebuild` builds again. Projects that need reproducible images pin their
  dependencies and their base image by digest, or commit a lock.
- **Push access to the image repository decides what a reusing deploy
  registers.** The tag is predictable from the sources, so whoever can push to
  the DAG's repository can place an image under a label before its deploy
  runs. ADR 0073 accepts the same model for dependency images. The docs tell
  operators to limit push access to the deploy identity, and to enable tag
  immutability where the registry offers it, so a tag that exists cannot be
  replaced. With immutable tags, `--rebuild` of a label already in the
  registry fails at the push; a pipeline that must always build gives
  every build its own `--dag-version` instead.
- **The digest registered stays the one the registry returned** (ADR 0075):
  the reuse pins exactly the bytes it checked, so there is no window between
  the platform check and the pin.
- A redeploy of unchanged content to a second target costs a few registry
  reads instead of a build and a push.
