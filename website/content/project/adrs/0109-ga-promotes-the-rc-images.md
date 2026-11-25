---
title: "ADR 0109: A GA publishes the images its last rc validated instead of rebuilding them"
linkTitle: "0109 · GA promotes the rc images"
weight: 1090
description: "ADR 0109: when the GA commit changes nothing an image is built from, the Release workflow does not rebuild the container images of a GA tag. It copies every image of the last green rc of that version under the GA tags with a new image configuration that names the GA version, keeps every layer byte for byte, checks that it did, and signs and attests the result. The binaries in the image report the GA version because the version package accepts that configuration only for the rc it was built as."
---

**Status:** Accepted
**Date:** 2026-11-25
**Relates:** ADR 0014 (supply chain signing), ADR 0033 (rc tags and E2E gates), ADR 0071 (stage, verify, promote), ADR 0108 (findings between rc and GA)
**Issues:** #878

## Context

A GA tag rebuilds every container image: GoReleaser builds the server, MCP and
CLI images again from the GA commit, and the Release workflow builds the
migrate image and the `dexaflow-runtime` images (each Python line, and `data`)
again. Since ADR 0071 and the rc-only E2E workflows, the GA no longer runs the
heavy k3d matrix, so the rebuild is no longer a flake risk the way #878 first
described it. It is still the weaker contract:

- The k3d E2E suites, the skip-version upgrade, the restore drill and the soak
  smoke run on the **rc** images. The GA images are other bytes: a different
  version string and build date, and every layer the Dockerfiles install again
  from the network (apt, PyPI, base image tags that moved in between). What
  shipped as GA is not what the rc checks validated.
- The code that changes between the last rc and its GA is release metadata.
  `v0.5.12-rc.1` to `v0.5.12` changed `CHANGELOG.md`, `helm/dexaflow/Chart.yaml`,
  `helm/dexaflow/README.md` and the rc's results file; `v0.5.11-rc.2` to
  `v0.5.11` also changed one test script. No image input changed in either.
- A rebuild can fail for reasons the code does not have: ADR 0108 exists
  because the data image scan of a rebuilt GA found what the rc's scan of the
  same code had not.

The obstacle #878 named is the version: every binary in those images carries
the tag it was built for (`-X internal/version.version`), so retagging the rc
image as the GA leaves a server that reports `v0.6.0-rc.2` in `--version`,
`dexaflow_build_info`, the UI and the user agent of the CLI image.

## Decision

1. **When it applies.** A GA tag `vX.Y.Z` is promoted from the newest
   `vX.Y.Z-rc.N` tag whose Release run ended green, when `git diff` between the
   two commits touches only paths no image is built from: `CHANGELOG.md`,
   `.changes/`, `helm/`, `test/`, `website/` and `*.md` files at the root.
   Anything else (a fix merged after the rc, a Dockerfile, a dependency) means
   the rc did not validate the GA's code, and the GA images are rebuilt as
   before. The workflow writes which path it took and why to the step summary
   and to the release body.

2. **What is promoted.** Every image the release publishes: `dexaflow-server`,
   `dexaflow-mcp`, `dexaflow-cli`, `dexaflow-migrate`, each
   `dexaflow-runtime:pyX.Y` line and the `data` image, and their pre-rename
   `leoflow-*` names, for every platform of each manifest list. The GitHub
   release archives are still built by GoReleaser from the GA commit: a binary
   in an archive has no image configuration to carry the version (point 4), and
   the install smoke runs on the GA archive itself.

3. **How.** For each image, a small tool (`internal/tools/imagepromote`, built
   on go-containerregistry, already a dependency) reads the rc manifest by
   digest, verifies the rc signature first, and writes a new image whose
   configuration differs only in the environment variable
   `DEXAFLOW_RELEASE_VERSION=vX.Y.Z`, the `org.opencontainers.image.version`
   label, and the annotation `org.opencontainers.image.base.digest` naming the
   rc digest it came from. The layers are referenced, not copied or rebuilt,
   and the tool fails unless every layer digest of the GA image equals the rc's,
   in order, for every platform. The GA digests are new (the configuration
   changed), so they are signed and attested as every release image is today.
   ADR 0108's GA scan of the data image still runs, on the promoted image.

4. **The version the binaries report.** `internal/version` reports
   `DEXAFLOW_RELEASE_VERSION` instead of its link-time version only when the
   link-time version is an rc (`vX.Y.Z-rc.N`) and the variable names exactly
   that rc's release, `vX.Y.Z`. Any other value is ignored, so the variable can
   promote an rc to its own GA and cannot make any other build claim a version.
   `--version` and the build info add the version the binary was built as
   (`built as v0.6.0-rc.2`), so the promotion stays visible.

5. **Fallback.** `cut-release.sh --rebuild-images` and the workflow input of
   the same name force the rebuild path for one GA, for the case the promotion
   itself is broken. It is recorded in the tag annotation.

## Alternatives

- **Rebuild with a lighter gate (#878 option 1).** What ADR 0071 and the
  rc-only E2E workflows already do. It keeps the version in the binary and
  ships bytes no rc check ran on.
- **Retag the rc digests as they are.** The same bytes, but every GA binary
  reports the rc version, and `dexaflow compile` in the CLI image resolves the
  runtime base of the rc tag (`py3.11-v0.6.0-rc.2`).
- **Reproducible builds with the version injected later.** Builds from the
  network are not reproducible here (apt, PyPI, base tags), so the GA bytes
  would still differ.
- **Read the version from a file added as one more layer.** Works, but adds a
  layer per platform for what a configuration field carries.

## Consequences

- A GA image's layers are the rc's. The k3d suites, the upgrade and restore
  checks and the soak smoke validated those bytes.
- A GA image digest differs from its rc digest by the configuration only;
  `org.opencontainers.image.base.digest` and the provenance attestation name
  the rc digest, and `crane manifest` shows the identical layer list.
- `internal/version` reads one environment variable. A program that is not an
  rc build, or sets the variable to anything but its own GA, sees no change.
- The GA path is shorter: no image build, only manifest writes.
- The first GA to take this path is v0.6.0. The workflow rehearses the
  promotion on every rc into a throwaway local registry (copy, rewrite, check
  the layers, run `--version` in the promoted image), so the GA path is
  exercised before a GA depends on it.
