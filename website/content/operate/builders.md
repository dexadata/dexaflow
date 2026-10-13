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
| 1 | `skip` | The image of your dependencies is already in the registry and the code did not change | Planned (#1706) |
| 2 | `code-layer` | Only your DAG code changed: it is added as one layer on the cached dependency image, through the registry | Planned (#1706) |
| 3 | `dependency-layer` | Every dependency has a wheel for the target platform: they are installed for that platform and added as a layer on the runtime base, with no daemon and no emulation | Planned (#1706) |
| 4 | `remote` | A real build is needed (a package without wheels, `system_packages`, your own `Dockerfile`): a build pod in your cluster, or a cloud builder | Planned (#1706) |
| 5 | `local-daemon` | None of the above applies: a local container tool builds and pushes the image | Available, the fallback |

Until the first four land, every build falls back to `local-daemon`, and the
output says so before the build starts:

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
registered: the CLI reads the pushed digest with `<tool> inspect` and runs
`<tool> manifest inspect <image>@<digest>`, and the digest must be a manifest
list holding an image for every requested platform. A tool can succeed at
build and push and still leave a partial image, so the build fails when the
build itself fails, when the list misses a platform, and when
`manifest inspect` cannot run or answers something that is not a list. The
error keeps the tool's message and says how to build one platform:

```text
Error: the multi-platform build failed: building for linux/amd64,linux/arm64 with local-daemon (docker) needs a tool that pushes one image per platform under one manifest list, such as docker with the containerd image store enabled. Otherwise build one platform: set build.platforms in dexaflow.yaml (or pass --platform where the command has it), for example linux/amd64: the pushed image ghcr.io/your-org/first-pro-dag@sha256:... is not a manifest list holding every platform; missing linux/arm64
```

`compile --build --push` runs the same check; a one-platform build is a plain
image and is not checked.

## The local daemon fallback

`local-daemon` runs a container tool on your machine: `docker` by default, or
`podman` or `nerdctl` with `--builder`. It runs `<tool> build --platform ...`,
then `<tool> push`, then reads the pushed image's digest with
`<tool> inspect`. Registry credentials are the tool's own: log in once with
`docker login ghcr.io` (or your cloud's helper, such as
`gcloud auth configure-docker` or `aws ecr get-login-password`). Dexaflow never
stores a registry credential.

On a machine whose architecture differs from the target platform (an Apple
Silicon laptop building `linux/amd64`), the tool builds under emulation, which
is slower; Docker Desktop includes it, and Colima, Lima or podman may need a
one-time `docker run --privileged tonistiigi/binfmt --install all`.

`deploy --skip-build` reuses an image the tool already pushed: it still reads
the digest with `<tool> inspect`, so it needs the same tool on the same machine.

A project that ships its own `Dockerfile` (or `build.dockerfile` in
`dexaflow.yaml`) is built from it as-is. Once the `remote` builder lands it can
build those too; until then they use the local daemon.
