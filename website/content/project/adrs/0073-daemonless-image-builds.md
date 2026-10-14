---
title: "ADR 0073: DAG images are built without a container daemon first; a local daemon is the fallback"
linkTitle: "0073 · Daemonless DAG image builds"
weight: 730
description: "ADR 0073: the CLI resolves a builder in a fixed order (skip, code layer, daemonless dependency layer, remote builder, local daemon), talks to registries through a pure Go client (go-containerregistry, never the Docker SDK), and addresses dependency images by the hash of what they contain."
---

**Status:** Accepted. Sections 1 and 5 are implemented by #1760; the rest is v0.6 work (section 6).
**Date:** 2026-10-13
**Relates:** ADR 0003 (a DAG is a `dag.json` and an image), ADR 0014 (supply chain gates), ADR 0015 (no Docker SDK), ADR 0041 (`deploy`, digest pinning and `--platform`, amended here), ADR 0048 (no user code in the control plane), ADR 0068 (patch content)
**Issues:** #1706

## Context

Every Pro deploy needs a local container daemon today, and every code change
rebuilds and pushes a full image.

- Build, push and digest are three calls to one executable with docker's argv
  (`internal/cli/compile.go`, `internal/cli/build_digest.go`). The digest is
  read from the local image store with `inspect`, so a build that ran anywhere
  else (Cloud Build, kaniko, a CI job that only pushes) cannot be deployed with
  `dexaflow deploy`.
- ADR 0041 promised `--platform` on `deploy` and multi-platform builds through
  buildx. Neither shipped (#1709 recorded it): a list of platforms reached a
  plain `docker build`, which fails on docker's default driver with an error
  that does not say what to change. On Apple Silicon the default
  `linux/amd64` build runs under emulation.
- A code-only change pays the whole build and push. Astronomer, Dagster and
  Flyte ship a fast code path; Dexaflow has only source mode, which is single
  file and operator opt-in.
- CI on Kubernetes runners needs privileged docker-in-docker, which Pod
  Security admission refuses.
- The showcase lab wraps the CLI in a fake `docker` for Cloud Build and falls
  back to `crane` in a container to push (section 4 of the build and deploy
  review).

The maintainers decided that Docker is a fallback, not the default and not the
recommended path, and that the CLI must be complete on its own.

## Decision

### 1. One builder seam, whose result carries the digest

`internal/build` defines `Builder`. A builder takes a request (image
reference, Dockerfile and context when it needs them, platforms, push, pin)
and returns a `Result` that names the path that ran and carries the pushed
image's digest-pinned reference. `deploy` registers that reference; it never
asks a local image store for it, except for `--skip-build`, which builds
nothing.

The inputs of a build are validated before anything is parsed, built or sent
to the control plane: every platform value, whatever its source (section 5).
What a builder can build is the builder's business. One that can tell in
advance (a daemonless path that finds a package without a wheel) declines,
names why, and resolution moves to the next path. The local daemon cannot tell
in advance, because the same tool builds a platform list or refuses it
depending on its configuration, so it runs the tool and, when the tool fails,
keeps its error and adds a hint that says what to change.

Every command that builds prints the path it took and why, for example
`builder: local-daemon (docker), fallback: no daemonless or remote builder is
available yet`, and, once dependency paths exist, the package that forced a
real build.

### 2. Resolution order

The CLI tries these paths in order and uses the first that applies:

| Order | Path | Applies when | Needs a daemon |
|---|---|---|---|
| 1 | `skip` | The dependency image for this content key (section 4) is already in the registry and the code layer is unchanged | No |
| 2 | `code-layer` | The dependency image exists and only the DAG code changed: a deterministic tar layer (sorted entries, fixed mtime, owner root, normalized modes) is appended through the registry API, per platform manifest | No |
| 3 | `dependency-layer` | Every resolved dependency has a wheel for the target platform: `uv` (or `pip`) installs them into a directory with the target platform tags, the directory is packed as a layer on the published runtime base pinned by digest, and pushed | No, and no emulation |
| 4 | `remote` | A real build is needed (an sdist-only package, `system_packages`, a project Dockerfile): a rootless BuildKit or kaniko Job the chart offers, off by default, in its own namespace with its own service account and limits; or a configured cloud builder, Cloud Build first | No |
| 5 | `local-daemon` | None of the above is available: docker (with buildx for multi-platform, `--push --metadata-file`), podman or nerdctl | Yes |

A project `Dockerfile` stays supported and is the only case where a Dockerfile
is the source of truth; it goes to path 4 or 5. dbt projects use the project
directory as the code layer.

The remote builder is a build pod, not the control plane: the build context is
user content, and ADR 0048 keeps it out of the control-plane process.

### 3. A pure Go registry client in the CLI

Paths 1 to 3 and the digest of path 4 need registry operations (HEAD a
manifest, read and write blobs and manifests, build an image index). The CLI
does them in process with **`github.com/google/go-containerregistry`**
(ggcr), under these rules:

- **CLI only.** It is imported by `internal/build` and reached only from
  `cmd/dexaflow`. The server (which also runs the scheduler role), the agent
  and the MCP server never link it, nor `internal/build`. A test enforces
  this from #1760 on, before the dependency exists:
  `internal/build/deps_test.go` runs `go list -deps` on `./cmd/dexaflow-server`,
  `./cmd/dexaflow-agent` and `./cmd/dexaflow-mcp` and fails if either import
  path appears.
- **Allowed packages:** `pkg/name`, `pkg/authn`, `pkg/v1`, `pkg/v1/remote`,
  `pkg/v1/mutate`, `pkg/v1/tarball`, `pkg/v1/empty`, `pkg/v1/types`. Never
  `pkg/v1/daemon`, which wraps the Docker SDK (ADR 0015), nor
  `pkg/authn/k8schain` or `pkg/v1/google`, which pull cloud SDKs into the CLI.
  The pull request that adds the dependency adds a `depguard` rule that
  enforces this list.
- **Credentials** come from `authn.DefaultKeychain`: the Docker config file and
  its credential helpers, the same ones `docker login`, `gcloud auth
  configure-docker`, `aws ecr get-login-password` and `az acr login` already
  write. Dexaflow never stores a registry credential. TLS is always verified;
  an insecure registry would need an explicit, separately reviewed option.
- **Pinned and scanned like every module** (ADR 0014): an exact version in
  `go.mod`, `make vuln` (govulncheck) clean at adoption and on every pull
  request, Dependabot updates through the existing `gomod` entry.

Candidates, measured on 2026-10-13 by importing the packages each would need
for paths 1 to 3 into an empty module (`go list -deps`):

| Library | License | Maintenance | Non-standard modules / packages pulled | Fit |
|---|---|---|---|---|
| go-containerregistry v0.22.1 | Apache-2.0 | Google org; the base of ko, crane, kaniko, cosign and Trivy's registry access | 9 / 47 (`docker/cli` for the config file, `docker-credential-helpers`, `klauspost/compress`, `opencontainers/*`, `logrus`, `x/sync`, `x/sys`) | Layer append (`mutate`), tarball layers, platform-aware indexes and the Docker config keychain are all there |
| regclient v0.11.6 | Apache-2.0 | Mostly one maintainer | 7 / 56 (`docker/libtrust`, `ulikunitz/xz`, `klauspost/compress`, `logrus`) | Comparable features; smaller user base and a bus factor of one |
| oras-go v2.6.3 | Apache-2.0 | CNCF project | 4 / 26 | Smallest tree, but artifact oriented: image config and manifest mutation, layer append and the per-platform index would be written by hand, which is the part most likely to be wrong |
| containers/image (go.podman.io/image) v5 | Apache-2.0 | Red Hat, podman and skopeo | Large; GPGME through cgo and storage drivers unless build tags exclude them | More than the CLI needs, and cgo breaks the static CLI build |
| Docker SDK (`docker/docker` client) | Apache-2.0 | Moby | The Moby tree | Refused by ADR 0015 |

ggcr is chosen because it already implements the operations that are hard to
get right (appending a layer to every manifest of an index, computing the
config and manifest digests) and is the library the tools Dexaflow users
already trust are built on. Its known exposure comes through `docker/cli`,
whose config-file code it imports for the keychain and which has had
advisories of its own (CVE-2021-41092, credentials sent to the default
registry with a misconfigured `credsStore`). That is why the version is
pinned, govulncheck gates every pull request, and the adoption pull request
records the module's entries in the Go vulnerability database next to the
`make vuln` output; an unfixed advisory reachable from the CLI blocks adoption.
If ggcr stops being maintained, oras-go is the fallback, at the cost of the
mutation code described above.

### 4. Dependency images are addressed by content

The dependency image's key is the SHA-256 of a canonical JSON document holding:

- the **resolved** dependency set: every package with its exact version and
  wheel hashes, sorted. `connectors:` are expanded to their provider packages
  before resolution, so they enter the key here;
- the **runtime base image digest**, never its tag;
- the **platform** (`os/arch[/variant]`), so each platform has its own key;
- the **wheel compatibility of the base image**: its libc family and version
  (the `manylinux_2_X` or `musllinux_1_X` tags it accepts), the Python
  implementation, version and ABI (`cp311`, `abi3`). They follow from the base
  digest, but the resolution depends on them, so they are recorded explicitly
  and a wrong reading changes the key instead of reusing a wrong image;
- the **installer** and its version (`uv` or `pip`), and every flag that
  changes the installed tree (`--only-binary`, `--no-binary`, bytecode
  compilation, index strategy);
- the package **index URLs** (never their credentials);
- **local and VCS dependencies**: a VCS dependency by the commit it resolved
  to, never a branch name; a local path or file by the hash of its content,
  read with the same file walk as the version label (section 4, "the code
  layer");
- the **image configuration** the dependency image sets: environment,
  `build.labels`, working directory, user and entrypoint;
- `system_packages`, sorted. A connector that needs system libraries adds them
  to `system_packages` when it is expanded, so they enter the key there. A
  system package takes the build to path 4 or 5, where the apt sources are
  fixed by the base image digest already in the key;
- a format version owned by the CLI, bumped when the layout of the layer
  changes.

**Lock reuse or re-resolution.** When the project ships a lock (`uv.lock`, or
a requirements file where every line pins a version and a hash), the CLI uses
it as is and resolves nothing: the key comes from the lock's content, and two
builds of the same lock always share one dependency image. Without a lock the
CLI re-resolves the declared specifiers on every build, for the target
platform (`uv pip compile --python-platform ...`), and the key covers the
result: a new upstream release that satisfies an unpinned specifier changes
the key and rebuilds the dependency image, which is what an unpinned
specifier asks for. Re-resolving costs a metadata request per package that
uv's local cache does not hold. Its budget is 5 s of the 30 s p50 target for a
redeploy with unchanged dependencies (#1706); the CLI prints how long the
resolution took, and the docs tell projects that need a faster or reproducible
redeploy to commit a lock. `--rebuild` forces a rebuild in both cases.

**The code layer** is built from the same file walk as the version label's
content hash (`contentHash`): the same `.dockerignore`, `exclude_paths` and
`include_paths`, and the same symlink rules: a root the build names
(`dag_source`, an `include_paths` entry, a dbt directory) is followed and must
resolve inside the project, and a symlink inside a walked directory is kept as
a link, never followed. The layer and the label
therefore always cover the same files, and nothing the generated Dockerfile
would have kept out (credentials, `.git`) can reach the registry through the
fast path.

The key is the dependency image's tag (`deps-<key prefix>`) in the DAG's own
repository; the skip path is a manifest HEAD for that tag. The DAG image is
the dependency image plus the code layer, and the digest registered in
`dag.json` is the digest of that final image, so ADR 0003 holds: one immutable
image per version. `--rebuild` ignores a cached dependency image. The DAG
version label stays the content hash of #1713; the dependency key is a build
cache key, not a version.

### 5. Platforms

The platform comes from `--platform` on `compile`, `build` and `deploy`, then
`build.platforms` in `dexaflow.yaml`, then the target, then the workspace
defaults, then `linux/amd64`, and the CLI prints the value with its source.
Every value is validated as `os/arch[/variant]`, whatever its source, and a
malformed one is refused, with the source named, before anything is parsed,
built or sent to the control plane; `dexaflow build` checks every project
before it builds any. Several platforms are built by a builder that pushes one
image per platform under one index (paths 1 to 4, and buildx in path 5). The
local daemon passes the list to the tool unchanged: docker with the containerd
image store builds and pushes it as a manifest list, and the digest of that
list is registered. Because a tool can succeed at build and push and still
leave a partial image (podman without `--manifest` keeps the last platform
under the tag; a push without all platforms sends part of a list), the pushed
digest is then checked with `<tool> manifest inspect` to be a list holding
every requested platform, and anything the check cannot confirm fails the
build before anything is registered. A failure, in the build or the check,
says how to build one platform, naming `build.platforms` first because not
every command that builds has `--platform`.

### 6. Scope of 0.5.4

0.5.4 ships the seam with the local daemon as its only backend (unchanged
behavior), the builder line in the output, and `--platform` on `compile`,
`build` and `deploy` with its source printed, the validation of every
platform value, and the hint on a failed multi-platform build.
It adds no dependency. Everything else in this record is v0.6 work, in this
order: the registry client with the digest read from the registry and the
skip path; the image split with the code layer; the daemonless dependency
layer; buildx, podman and nerdctl specifics in the fallback; the remote
builder in the chart; build args, build secrets and private package indexes
for paths 3 to 5. Each lands behind the same seam and changes no caller.

> **Note (2026-10-14, #1706).** This section is replaced by
> [ADR 0075](/project/adrs/0075-daemonless-builds-in-0-5-4/): the whole of
> #1706 ships in 0.5.4, not v0.6. Path 4 lands in 0.5.4 too, its Cloud Build
> backend first ([ADR 0083](/project/adrs/0083-remote-builder-on-cloud-build/));
> the in-cluster build pod follows behind the same seam.

## Alternatives considered

- **Keep the argv contract and document wrapper scripts.** This is how the
  showcase works today, with about 500 lines of shell; every environment
  without a daemon needs its own glue, and the digest still depends on a local
  image store.
- **Shell out to `crane`, `skopeo` or `regctl`.** Daemonless, but it adds a
  binary every user and CI image must install and keep in step, which is the
  same prerequisite problem Docker has, one level down.
- **The Docker SDK.** Refused by ADR 0015.
- **Buildpacks or ko.** ko builds Go programs only. Cloud Native Buildpacks
  need a daemon or the lifecycle binary in a builder image, and replace the
  generated Dockerfile with a second build model.
- **Source mode as the default fast path.** It keeps two artifact types,
  bypasses image policies, and is limited to one file (review, open question
  4). It stays an operator opt-in.
- **Content-address the whole image only.** Any code change would then miss
  the cache, which is the cost this record removes.

## Consequences

- A deploy can run where no daemon exists (a laptop without Docker, a CI pod
  without privileges) once paths 1 to 4 land, and the output says which path
  ran and, when it fell back, why.
- The CLI gains a registry client and its dependency tree, gated by govulncheck
  and depguard. The server, scheduler and agent are unchanged.
- The digest no longer comes from a local image store, so any builder (and a
  split CI job) can feed `deploy`.
- Dependency images accumulate per key in the DAG's repository. Retention is
  part of the image and version retention work (review, D-31).
- Docs present Docker only on the builders page, as the fallback.
- ADR 0041's statements on `--platform` and buildx are replaced by section 5.
