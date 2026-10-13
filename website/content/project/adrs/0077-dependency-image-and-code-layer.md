---
title: "ADR 0077: a pushed build splits into a dependency image and a code layer"
linkTitle: "0077 · Dependency image and code layer"
weight: 770
description: "ADR 0077: the code-layer path of ADR 0073. A pushed build of the generated Dockerfile becomes a dependency image, reused by its key, plus one deterministic layer with the DAG code appended through the registry. Until the daemonless dependency layer lands, the local daemon builds the dependency image, and only projects whose Python dependencies are pinned take the path."
---

**Status:** Accepted
**Date:** 2026-10-13
**Relates:** ADR 0073 (daemonless image builds, sections 2 and 4), ADR 0075 (the daemonless build ships in 0.5.4, the registry is the source of the digest), ADR 0076 (reuse by content label), ADR 0003 (a DAG version is a `dag.json` and an image)
**Issues:** #1706

## Context

ADR 0073 orders the builders as skip, code layer, dependency layer, remote
builder and local daemon, and keys dependency images by what they contain.
ADR 0076 shipped the first form of the skip path, which reuses the whole image
when nothing changed. Every other deploy still runs a full build on a local
daemon, even when only `dag.py` changed and the dependencies are the ones
already pushed.

The code-layer path needs a dependency image to append to. The daemonless
dependency layer (path 3), which builds that image without a daemon by
installing wheels for the target platform, is a later step of #1706. This
record decides how the code-layer path works before path 3 exists, and where
it departs from ADR 0073 section 4 until then.

## Decision

1. **A pushed build of the generated Dockerfile splits in two.** The
   dependency image is the generated Dockerfile without its `COPY` lines:
   the base, the system packages, the Python dependencies, `PYTHONPATH` and
   the non-root user. The code layer holds what those `COPY` lines take. The
   DAG image is the dependency image with the code layer appended, so it
   holds the files and the configuration the full build would. A project
   Dockerfile, a build that is not pushed, and a build with no platform keep
   the full build.
2. **The dependency image is found by its key.** The key is the SHA-256 of a
   format version, the dependency image's Dockerfile with the base image
   pinned by digest (`FROM <repo>@sha256:...`) and the sorted platforms. The
   Dockerfile carries every input of the dependency image (base digest,
   system packages, dependencies with the installer command, environment,
   user), so the key changes when any of them does. The image lives in the
   DAG's own repository under `deps-<first 32 hex of the key>`.
3. **When the dependency image is in the registry** for every platform, no
   daemon runs: the code layer is appended to each platform image through the
   registry API, attestation manifests are dropped, and the result is pushed
   under the DAG image tag. The builder line says `code-layer`.
4. **When it is not**, or with `--rebuild`, the local daemon builds and
   pushes the dependency image first, from its Dockerfile in an empty
   temporary directory (nothing of the project reaches that build), and the
   code layer is appended to what it pushed. The builder line says
   `local-daemon (docker) for the dependency image, then code-layer`.
5. **Only pinned Python dependencies take the path.** Every dependency must
   be `name==version` (extras allowed), or be pinned by another entry of the
   list, which is how a connector's provider package is pinned. Otherwise the
   build stays whole and the builder line names the loose entries. ADR 0073
   wants an unpinned specifier re-resolved on every build; that needs the
   resolver of path 3, and reusing a dependency image for a range would keep
   the version it resolved first.
6. **The code layer is deterministic.** It comes from the walk of the
   version label (ADR 0073 section 4): the same `.dockerignore`,
   `exclude_paths` and symlink rules, never `.git` or `.leoflow*` files. Two
   differences follow the `COPY`: the files `dbt parse` writes under
   `target/` ship, and `dbt.manifest` is a file of its project. Entries are
   sorted, owned by root, dated 1970-01-01, and their modes normalized:
   `0755` for directories and files with an execute bit, `0644` for other
   files. Directories above a nested root are created, as `COPY` creates
   them; `/home/leoflow` itself is not in the layer and keeps the base's
   owner.
7. **The digest registered is the registry's** (ADR 0075): after the push,
   the CLI reads the tag back and pins that digest.

## Where this departs from ADR 0073 section 4, until path 3

- **The key covers the declared pins, not the resolved set with wheel
  hashes.** Transitive dependencies are what pip resolved when the
  dependency image was first built, which is what the local layer cache of a
  full build did already, now shared through the registry.
- **One key covers the list of platforms**, because the daemon builds them
  together as one index. Path 3 builds per platform and keys per platform.
- **Locks are not read yet.** A project that pins every dependency gets the
  same reuse a lock would give.

Path 3 replaces the daemon in step 4 and lifts the limit of step 5, behind
the same seam (`build.Select`), with a new key format.

## Consequences

- A redeploy that changed only DAG code costs a few registry requests and
  one layer upload, with no daemon, wherever the dependency image is already
  pushed: another machine, a CI runner, a second target.
- The first build of a key costs what a full build did, plus one append.
- Files keep a mode the task user can read: a `0600` file, which `COPY`
  keeps unreadable to the non-root user, ships as `0644`.
- `deps-*` tags accumulate in the DAG repository, one per key. Registry
  retention rules can expire old ones; a deploy whose dependency image is
  gone builds it again.
- The trust model of ADR 0076 applies to the dependency tag too: whoever can
  push to the DAG's repository can place an image under a predictable
  `deps-*` tag. Push access stays limited to the deploy identity, and tag
  immutability keeps a pushed key from being replaced. With immutable tags,
  `--rebuild` fails at the dependency push of a key that exists.
- Every pushed build of a generated Dockerfile reads the digest of its base
  image from the registry. When that read fails (no network, no
  credentials), the build stays whole and says why.
