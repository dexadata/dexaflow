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

A platform is written `os/arch` or `os/arch/variant`, in lowercase, such as
`linux/amd64` or `linux/arm64/v8`. Anything else is refused before the build.

Several platforms (`--platform linux/amd64,linux/arm64`) need a builder that
pushes one image per platform under one manifest list. The `local-daemon`
builder builds one platform per image, so it refuses a list before the DAG is
even parsed, and names the platforms and the way out:

```text
Error: this builder cannot build for several platforms: building for linux/amd64,linux/arm64 needs one image per platform under one manifest list, and the local-daemon (docker) builder builds one platform per image. Choose one platform with --platform (or build.platforms in dexaflow.yaml), for example --platform linux/amd64
```

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
