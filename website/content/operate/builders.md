---
title: How deploy builds the DAG image
linkTitle: Builders
weight: 13
description: The builders `dexaflow deploy`, `compile --build` and `build` choose from, in order, how the target platform is picked, and the local container daemon as the fallback.
---

A DAG version is a `dag.json` and a container image
([ADR 0003](/project/adrs/0003-dag-as-image/)). `dexaflow deploy`,
`dexaflow compile --build` and `dexaflow build` produce that image with a
**builder**, chosen in a fixed order, and say which one ran. The image is
pushed to your registry and `deploy` registers it by digest, so the cluster
pulls exactly the bytes that were built.

## The order builders are tried in

The CLI uses the first builder that applies
([ADR 0073](/project/adrs/0073-daemonless-image-builds/)). The first four need
no container daemon on your machine.

| Order | Builder | Used when | Status |
|---|---|---|---|
| 1 | `skip` | The whole DAG image for this content is already in the registry under its content label ([see below](#reusing-an-image-already-in-the-registry)) | Available for `deploy` |
| 2 | `code-layer` | The image of your dependencies is already in the registry: only your DAG code is pushed, as one layer on it, through the registry ([see below](#pushing-only-the-dag-code)) | Available for pushed builds with pinned dependencies |
| 3 | `dependency-layer` | Every dependency has a wheel for the target platform: they are installed for that platform and added as a layer on the runtime base, with no daemon and no emulation | Planned (#1706) |
| 4 | `remote` | A real build is needed (a package without wheels, `system_packages`, your own `Dockerfile`): a build pod in your cluster, or a cloud builder | Planned (#1706) |
| 5 | `local-daemon` | None of the above applies: a local container tool builds and pushes the image | Available, the fallback |

Until paths 3 and 4 land, a build that neither path 1 nor path 2 covers
falls back to `local-daemon`, and the output says so before the build starts:

```console
$ dexaflow deploy --server https://pro.example.com --yes
server:     https://pro.example.com (--server)
registry:   ghcr.io/your-org/first-pro-dag (from dexaflow.yaml)
platforms:  linux/amd64 (from the default)
...
builder:    local-daemon (docker), fallback: no daemonless or remote builder is available yet
...
Deployed first_pro_dag -> https://pro.example.com
  image ghcr.io/your-org/first-pro-dag@sha256:9f2c...
```

`compile --build` and `build` print the same two lines, `platforms:` with its
source and `builder:`. With `deploy --skip-build` nothing is built and no
builder line is printed.

## Reusing an image already in the registry

The default version label is a hash of everything the image is built from
(`dexaflow.yaml`, the files the image copies, the CLI version). With
`tag_strategy: version`, the default, the image tag is that label, so an
image the registry already holds under it was built from the same inputs.
`deploy` then pins that image and registers it without building or pushing:

```console
builder:    skip, the registry already holds the image for this content label (pass --rebuild to build it again)
```

This happens when the same content was pushed before but is not the current
version on this control plane: a deploy to a second target that shares the
registry, a CI job that already built the image, or a deploy whose register
step failed. When the content is already the current version, `deploy` stops
earlier and says so.

The image is reused only when it holds every requested platform. It is never
reused for an explicit `--dag-version`, for a tag from git
(`tag_strategy: git_sha` or `git_tag`, unless it fell back to the version
label outside git or with no tag on `HEAD`), or with `--rebuild` or
`--force`.
The checks that run before a build still run: a `build.dockerfile` that
resolves outside the project is refused, and secrets the image would carry
are reported. Any registry error (a missing tag, `401`, a timeout) means a
normal build, and the `builder:` line names the error.

`deploy --plan` reads the registry too and says when the image would be
reused:

```console
  build       no
  push        no
  why         content changed: ...; the registry already holds the image for this content label, so it is pinned without a build or push (--rebuild builds it again)
```

### What the label does not cover

Like the "already deployed" check, a reuse trusts the label. It does not
pick up:

- a new upstream release of a dependency without a pinned version
  (`pandas>=2`), or of a `base_image` given by tag;
- anything the label does not hash, such as an environment variable the
  image build reads.

Pass `--rebuild` to build the image again. For images that are reproducible,
pin dependencies to exact versions (or commit a lock) and give `base_image`
by digest.

### Who decides what a reused image contains

The tag of a reused image is predictable from the sources, so anyone who can
push to the DAG's image repository can place an image under a label before
the deploy of that label runs, and the deploy registers it. Treat push access
to that repository as deploy access:

- give push rights only to the identity that deploys (the CI job or the
  operator), and pull rights to the cluster;
- enable tag immutability where your registry offers it (ECR, Artifact
  Registry, Harbor, ACR), so a tag that exists cannot be replaced.

A pipeline that must build what it ships, every time, passes `--rebuild`
when tags are mutable. With tag immutability on, `--rebuild` of a label that
is already in the registry fails at the push; such a pipeline gives every
build its own label instead, for example `--dag-version "$CI_PIPELINE_ID"`,
which never reuses and never collides.

The decision and its trade-offs are recorded in
[ADR 0076](/project/adrs/0076-reuse-image-by-content-label/).

## Pushing only the DAG code

A pushed build of the Dockerfile Dexaflow generates is made of two images:

- the **dependency image**: the base image, `system_packages` and your
  Python dependencies, with `PYTHONPATH` and the non-root user. It is the
  generated Dockerfile without its `COPY` lines;
- the **DAG image**: the dependency image plus one layer holding your DAG
  code, the files those `COPY` lines take.

The dependency image is stored in the DAG's own repository under a tag
derived from what it contains, `deps-<key>`. The key covers the base image
digest, `system_packages`, `dependencies` and `connectors`, and the
platforms. When the registry already holds the dependency image for the
key, no container daemon runs: the code layer is added to it through the
registry API and pushed under the DAG image tag.

```console
builder:    code-layer, the registry holds the dependency image ghcr.io/your-org/etl:deps-4f1c... for this build, so only the DAG code is pushed, as a layer on it (pass --rebuild to build the dependency image again)
```

The first build of a key, or a build with `--rebuild` (on `deploy`,
`compile --push` and `build --push`), has the local daemon
build and push the dependency image first, then adds the code layer:

```console
builder:    local-daemon (docker) for the dependency image, then code-layer, no dependency image ghcr.io/your-org/etl:deps-4f1c... in the registry (...): the daemon builds and pushes it, then the DAG code is appended to it
```

The dependency image is built from an empty directory, so no project file
reaches that build. Every later deploy that changes only DAG code, from any
machine or CI runner that can reach the registry, skips the daemon.

### When the build stays whole

The whole image is built by the local daemon, as before, and the `builder:`
line says why, when:

- a Python dependency is not pinned to one version. Pin each one
  (`pandas==2.2.3`); a connector's provider package is pinned by listing it
  pinned in `dependencies` too (`apache-airflow-providers-amazon==9.1.0`);
- the project ships its own Dockerfile;
- `dag_source` is in a subdirectory;
- the digest of the base image cannot be read (no network or no credentials
  for its registry), or the registry of the DAG image answers the read of
  the dependency image with an error other than "unknown tag";
- an `include_paths` entry is a pattern (`lib/*.py`);
- the build is not pushed (`compile --build` without `--push`), or has no
  platform.

### What the code layer contains

The layer holds the files the generated Dockerfile copies: the DAG source,
`include_paths`, the dbt project and the directories of `dbt_groups`,
filtered by your `.dockerignore` and `exclude_paths` with Docker's rules, as
the full build filters them. `.git`, `.dockerignore` and the compiled
`dag.json` never ship, even from a project at `.`. Symlinks inside a copied
directory stay links. A file whose name starts with `.wh.` is refused, since
a layer reads it as a deletion. Every
entry is owned by root, with a fixed date, mode `0755` for directories and
executables and `0644` for other files, so the same files always make the
same layer and the non-root task user can read every file.

### Keeping dependency images current

A dependency image is reused for as long as its key holds. Its transitive
dependencies (the packages your pinned ones pull in) are the versions pip
resolved when it was first built, and its `system_packages` the versions
apt installed then, so their security updates arrive only with a rebuild.
Pass `--rebuild` to build the dependency image again, for example on a
schedule in CI. A base image given by tag is read by digest on every build, so
a new image under that tag gives a new key and a new dependency image.

`deps-*` tags accumulate in the DAG repository, one per set of dependencies.
A retention rule of your registry can expire old ones; a deploy whose
dependency image is gone builds it again. The access rules of
[Who decides what a reused image contains](#who-decides-what-a-reused-image-contains)
cover these tags too: with tag immutability on, `--rebuild` of dependencies
that are already in the registry fails at the dependency push.

The decision is recorded in
[ADR 0077](/project/adrs/0077-dependency-image-and-code-layer/).

## Choosing the platform

The image is built for the cluster's architecture, not your machine's. The
platform comes from the first of these that is set, and the output names which:

1. `--platform` on `deploy`, `compile` or `build`;
2. `build.platforms` in the project's `dexaflow.yaml`;
3. the target's `platforms`
   ([workspaces and targets](/operate/workspaces-and-targets/));
4. `defaults.platforms` in the workspace file;
5. `linux/amd64`.

```bash
dexaflow deploy --platform linux/arm64 --yes    # a Graviton cluster
```

A platform is written `os/arch` or `os/arch/variant`, such as `linux/amd64` or
`linux/arm64/v8`. A malformed value is refused before anything is parsed,
built or sent to the control plane, wherever it came from, and the error names
that source.

Several platforms (`--platform linux/amd64,linux/arm64`) need a tool that
pushes one image per platform under one manifest list. The `local-daemon`
builder hands the list to the tool as is (`<tool> build --platform
linux/amd64,linux/arm64`, then `<tool> push`); docker with the containerd
image store enabled builds and pushes it as one manifest list.

Whatever the tool, a multi-platform push is then checked before anything is
registered: the CLI reads what the tag names from the registry itself, and it
must be a manifest list holding an image for every requested platform. A tool
can succeed at build and push and still leave a partial image, so the build
fails when the build itself fails, when the list misses a platform, and when
the registry cannot be read or answers something that is not a list. The
error keeps the cause and says how to build one platform:

```text
Error: the multi-platform build failed: building for linux/amd64,linux/arm64 with local-daemon (docker) needs a tool that pushes one image per platform under one manifest list, such as docker with the containerd image store enabled. Otherwise build one platform: set build.platforms in dexaflow.yaml (or pass --platform where the command has it), for example linux/amd64: the pushed image ghcr.io/your-org/first-pro-dag@sha256:... is not a manifest list holding every platform; missing linux/arm64
```

`compile --build --push` runs the same check; a one-platform build is a plain
image and is not checked.

## The local daemon fallback

`local-daemon` runs a container tool on your machine: `docker` by default, or
`podman` or `nerdctl` with `--builder`. It runs `<tool> build --platform ...`
and `<tool> push`. The digest `deploy` registers is then read from the
registry, not from the tool
([ADR 0075](/project/adrs/0075-daemonless-builds-in-0-5-4/)).

## Registry credentials

The tool pushes with its own credentials, and the CLI reads the registry with
the same ones: those `docker login ghcr.io` or your cloud's helper writes to
`~/.docker/config.json` (`gcloud auth configure-docker`,
`aws ecr get-login-password | docker login ...`, `az acr login`), credential
helpers included. Dexaflow never stores a registry credential.

Only a registry on `localhost`, a `*.localhost` name (as k3d names its
registries) or a loopback address may answer over plain HTTP; every other
one, private addresses included, is read over verified TLS, and a registry
that answers only HTTP there is refused rather than sent the credentials in
clear. `*.localhost` names are dialed on `127.0.0.1` and never sent through a
proxy.

podman users: the CLI reads `~/.docker/config.json`, and podman's
`auth.json` only when that file does not exist. If you have both, run
`docker login` for the registry too, or the push succeeds and the read is
refused with a 401.

On a machine whose architecture differs from the target platform (an Apple
Silicon laptop building `linux/amd64`), the tool builds under emulation, which
is slower; Docker Desktop includes it, and Colima, Lima or podman may need a
one-time `docker run --privileged tonistiigi/binfmt --install all`.

`deploy --skip-build` registers an image that is already in the registry,
whoever pushed it: a CI job, a cloud builder or another machine. It reads the
digest from the registry and needs no container tool at all.

A project that ships its own `Dockerfile` (or `build.dockerfile` in
`dexaflow.yaml`) is built from it as-is. Once the `remote` builder lands it can
build those too; until then they use the local daemon.
