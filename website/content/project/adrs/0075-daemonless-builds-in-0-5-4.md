---
title: "ADR 0075: The daemonless build of ADR 0073 ships in 0.5.4, and pushed images are read back through the registry"
linkTitle: "0075 · Daemonless builds ship in 0.5.4"
weight: 750
description: "ADR 0075: replaces section 6 of ADR 0073. The registry client, the skip and code-layer paths, the daemonless dependency layer and the remote builder ship in 0.5.4, each behind the builder seam. From the registry client on, the digest a deploy registers and the platform check of a multi-platform push are read from the registry, not from a local image store."
---

**Status:** Accepted
**Date:** 2026-10-13
**Relates:** ADR 0073 (daemonless image builds), ADR 0068 (patch content gated by safety), ADR 0041 (`deploy` and digest pinning), ADR 0015 (no Docker SDK)
**Issues:** #1706

## Context

ADR 0073 decided how a DAG image is built without a container daemon and
scoped 0.5.4 to the builder seam, the builder line and `--platform` (its
section 6). It left the registry client, the image split, the daemonless
dependency layer and the remote builder to v0.6.

The maintainers then decided that the whole of #1706 ships in 0.5.4. A Pro
deploy that needs Docker is the main friction the build and deploy review
found, and waiting for a minor that has no date leaves it in place.

Section 5 of ADR 0073 also checks a multi-platform push with
`<tool> manifest inspect`, and the digest comes from `<tool> inspect`. Both
read state the local tool keeps, which a build that ran elsewhere does not
have.

## Decision

1. **Scope.** Section 6 of ADR 0073 is replaced: 0.5.4 ships, in this order,
   the registry client, the skip path, the image split with the code layer,
   the daemonless dependency layer, and the remote builder in the chart. Each
   lands behind the builder seam as its own pull request, with its own review,
   and changes no caller. Each must meet the safety bar of ADR 0068 on its
   own; one that does not moves to the next patch, and the paths before it
   still ship.
2. **The registry is the source of the digest.** Once the registry client
   exists, every builder, including the local daemon, reads the digest of the
   image it pushed from the registry, through the client and credentials of
   ADR 0073 section 3. `deploy --skip-build` does the same, so an image pushed
   by any tool (Cloud Build, kaniko, a CI job) can be deployed without a local
   copy. `<tool> inspect` is no longer called.

   > **Note (2026-10, #1885).** The local daemon now takes the digest from the
   > tool's report of its own push (the buildx metadata file, podman's
   > `--digestfile`, the last line of `docker push`) and reads the registry at
   > that digest only to check a multi-platform push; the tag read remains for a
   > tool that reports none. See
   > [ADR 0087](/project/adrs/0087-local-daemon-digest-from-the-push/).
3. **The platform check reads the registry too.** The multi-platform check of
   ADR 0073 section 5 reads the pushed manifest through the registry client
   instead of `<tool> manifest inspect`. What it checks is unchanged: the
   digest must name an index that holds every requested platform, and
   anything it cannot confirm fails the build before anything is registered.
4. **The pinned reference keeps the user's spelling.** The reference
   registered is the image as written in the project, with its tag replaced by
   the digest the registry returned (`reg.io/team/etl@sha256:...`), not a
   normalized form, so `docker.io` images read the same as they did through
   `inspect`.

## Consequences

- A deploy needs read access to the registry it pushes to, with the
  credentials `docker login` or a credential helper wrote to
  `~/.docker/config.json`. For docker that is the access the push used. podman
  keeps its own `auth.json`, which the client reads only when no docker
  config exists, so a podman user with both logs in with `docker login` too.
- Plain HTTP is allowed only to `localhost`, `*.localhost` and loopback
  addresses. go-containerregistry would also use it for private (RFC 1918)
  addresses, falling back from HTTPS to whichever scheme answers first, which
  would hand the credentials and the choice of digest to anyone answering HTTP
  on the network; the client refuses that.
- The digest is what the tag names when it is read, right after the push. Two
  pushes of the same tag at the same moment (two jobs building one version)
  can make one deploy register the other's image. The default tag is the
  content version or the git commit, so the two images usually come from the
  same sources; a tag reused across different sources (uncommitted changes,
  a version label that was not bumped) is the case to avoid. Comparing with the digest the tool reports on push is left to the
  daemonless builders, which push in process and know it.
- The local tool only builds and pushes. podman and nerdctl no longer need an
  `inspect` that matches docker's output format.
- The new dependency (go-containerregistry) arrives in a patch. It is linked
  by the CLI only, enforced by `internal/build/deps_test.go` and a `depguard`
  rule, and gated by `make vuln` like every module (ADR 0014).
- The rc of 0.5.4 waits for the paths that meet the bar.
