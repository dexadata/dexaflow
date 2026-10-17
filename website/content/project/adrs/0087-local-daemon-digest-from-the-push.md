---
title: "ADR 0087: The local daemon takes the digest from its own push"
linkTitle: "0087 · Local daemon digest from the push"
weight: 870
description: "ADR 0087: replaces point 2 of ADR 0075 for the local daemon. docker pushes with buildx and a metadata file, podman through a manifest list and a digest file, nerdctl with all its platforms; the digest a deploy registers is the one the tool reports for its push, and the registry is read at that digest only to check a multi-platform push."
---

**Status:** Accepted
**Date:** 2026-10-16
**Relates:** ADR 0075 (pushed images read back through the registry), ADR 0073 (daemonless image builds), ADR 0041 (`deploy` and digest pinning), ADR 0015 (no Docker SDK)
**Issues:** #1885

## Context

ADR 0075 made the registry the source of the digest for every builder: the
local daemon runs `<tool> build` and `<tool> push`, then the CLI reads what
the tag names from the registry. Its consequences name the race this leaves:
another push of the same tag between the push and the read (two jobs
building one version) makes the deploy register the other job's image. It
left the comparison with the digest the tool reports to the daemonless
builders, which push in process.

The local daemon also handed a list of platforms to every tool as
`<tool> build --platform a,b` and `<tool> push`. That builds one manifest
list only with docker and the containerd image store. podman without
`--manifest` keeps the last platform under the tag, and nerdctl pushes only
the host's platform without `--all-platforms`; the platform check caught
both, but the build could not succeed with them.

Each tool can report the digest of what it pushed:

- `docker buildx build --push --metadata-file <file>` writes
  `containerimage.digest`, the digest of the manifest or index it pushed;
- `docker push` ends with `<tag>: digest: sha256:<hex> size: <n>`;
- `podman push` and `podman manifest push` write it to `--digestfile`;
- nerdctl has no such option, and its progress output is not a format to
  rely on.

## Decision

1. **Each tool pushes several platforms its own way.** docker with buildx
   builds and pushes in one `docker buildx build --push --metadata-file`;
   docker without buildx keeps `docker build` and `docker push`. podman
   builds one platform with `-t` and several into a manifest list
   (`--manifest`), removed first so a list left by an earlier build adds no
   old images, and pushes it with `podman manifest push --all`. nerdctl
   pushes with `--all-platforms` when there are several. A build that is not
   pushed stays a plain `<tool> build`.
2. **The digest comes from the push.** The digest a deploy registers is the
   one the tool reported for its push, accepted only as `sha256:` and 64 hex
   characters. For one platform the registry is not read at all.
3. **The platform check reads the registry at that digest.** A push of
   several platforms is still checked to be a manifest list holding every
   platform (ADR 0075, point 3), reading the reported digest, which no later
   push can move, instead of the tag.
4. **The tag read stays as the fallback.** When the tool reports no digest
   (nerdctl, or a report that does not parse), the CLI prints a note and
   reads the tag from the registry right after the push, as ADR 0075 decided.

## Consequences

- With docker and podman, two pushes of the same tag at the same moment can
  no longer make one deploy register the other's image. nerdctl keeps that
  race until it reports a digest.
- A one-platform deploy through docker or podman needs no read access to the
  registry beyond what the push used.
- With buildx, the build runs on the active buildx builder, the one
  `docker buildx use` selected. Its default, the `docker` driver, builds one
  platform; several need the containerd image store or a builder with another
  driver, and the error says so.
- The CLI parses three outputs of tools it does not control. Each parse is
  strict (a `sha256:` digest of the right length), and anything else falls
  back to the registry read rather than failing the deploy.
