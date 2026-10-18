---
title: "ADR 0080: registry credentials from the cloud CLI session, after the docker config"
linkTitle: "0080 · Registry credentials from cloud sessions"
weight: 800
description: "ADR 0080: the CLI's registry client reads a credential from the docker config and its credential helpers first, then, for an Artifact Registry, GCR, ECR or ACR host, from the token the gcloud, aws or az CLI prints. No cloud SDK is linked. registry.auth_method chooses between them: auto (the new default), docker_config and cloud; env and iam still load, deprecated."
---

**Status:** Accepted
**Date:** 2026-10-14
**Relates:** ADR 0073 (daemonless image builds, section 3: the registry client and its credentials), ADR 0075 (the registry is the source of the digest), ADR 0041 (deploy without a pipeline), ADR 0014 (supply chain)
**Issues:** #1704, #1705

## Context

ADR 0073 made the CLI read registries in process with `authn.DefaultKeychain`:
the docker config and its credential helpers. A person who is signed in to
their cloud (`gcloud auth login`, `aws sso login`, `az login`) still had to run
a second login before a deploy (`gcloud auth configure-docker`, `aws ecr
get-login-password | docker login`, `az acr login`), or install a credential
helper, and the preflight only said `docker login <registry>`. The
`registry.auth_method` key of `dexaflow.yaml` (`docker_config`, `env`, `iam`)
was parsed and never read.

The libraries that read cloud credentials in process are what ADR 0073 kept
out of the CLI: `pkg/v1/google` and `pkg/authn/k8schain` of go-containerregistry
pull cloud SDKs in, an ECR keychain needs the ECR service client of the AWS
SDK (or `amazon-ecr-credential-helper`, which pulls more), and ACR needs the
Azure identity SDK, which the module does not have at all.

## Decision

1. **The docker config comes first.** The registry client resolves a
   credential from `authn.DefaultKeychain` as before. A registry the docker
   config holds a credential for is spoken to with it and nothing else.
2. **Then the cloud CLI that owns the host, through its own command.** When the
   docker config holds nothing, and only for a host a cloud owns, the client
   runs that cloud's CLI and sends the token it prints:

   | Host | Command | User name |
   |---|---|---|
   | `gcr.io`, `*.gcr.io`, `<location>-docker.pkg.dev` | `gcloud auth print-access-token` | `oauth2accesstoken` |
   | `<account>.dkr.ecr[-fips].<region>.amazonaws.com[.cn]` | `aws ecr get-login-password --region <region>` | `AWS` |
   | `<name>.azurecr.io` (`.cn`, `.us`) | `az acr login --name <name> --expose-token --output tsv --query accessToken` | `00000000-0000-0000-0000-000000000000` |

   These are the commands each cloud documents for a docker login, run without
   a shell, with the region or name taken from a host the pattern matched. The
   CLI reads whatever session the cloud CLI has: a user sign-in, a profile, SSO,
   a workload identity. No cloud SDK is linked, so no module is added and
   ADR 0073's list of allowed packages stands.
3. **Nothing is stored.** A token lives in the CLI's memory for ten minutes at
   most, shorter than any of the three clouds issues, so one deploy runs the
   cloud CLI once; one token serves every Google host. A failure (not
   installed, signed out) is kept for thirty seconds, so a push does not run
   the CLI for every blob, and a sign-in during `deploy --watch` is picked up
   after that. Each run has thirty seconds and no input, and one run per host
   is in flight at a time; other hosts do not wait for it.
4. **`registry.auth_method` chooses the sources.** `auto` (the default): the
   docker config, then the cloud CLI. `docker_config`: the docker config only,
   as before. `cloud`: the cloud CLI only for a host a cloud owns, for a
   docker config whose entry for the registry has expired (an ECR login lasts
   twelve hours) and would be sent first; a host no cloud owns has no session
   and reads the docker config. `env` and `iam`, which were never read, still
   load, print a warning and read as `auto`, so the docker config they were
   used with keeps working. Any other value is refused when the project loads,
   as before. `auto`, `docker_config` and `cloud` hash as one value in the
   content label (where a credential comes from does not change the image);
   `env` and `iam` keep the literal they hashed before, so no label moves.
5. **Every refusal names the command for the detected registry.** The deploy
   preflight and `dexaflow doctor` name `gcloud auth configure-docker <host>`,
   `aws ecr get-login-password --region <region> | docker login --username AWS
   --password-stdin <host>`, `az acr login --name <name>` or
   `docker login <host>`; for another `--builder`, the cloud's token piped to
   `<tool> login`. When the cloud CLI could not give a token, the reason is
   printed with it (not on `PATH`, not signed in). When the registry refused
   the cloud session's own token, the refusal names the session to sign in to
   and the push role to grant instead. A push check that passed on a cloud
   session adds that a local-daemon build needs the tool's own login.

## Consequences

- A person signed in to their cloud deploys to that cloud's registry with no
  second login when a daemonless path builds (skip, code layer, dependency
  layer), and every registry read (digest, base image, reuse) works the same
  way.
- `docker`, `podman` and `nerdctl` push with their own credentials, so a build
  by the local daemon still needs the tool's login; a refused push names the
  command for the registry.
- A cloud CLI that is installed but slow adds its start-up time to the first
  registry request of a command, only for a cloud host and only when the docker
  config holds nothing for it.
- Public ECR (`public.ecr.aws`) and registries on other clouds are not detected;
  they keep the docker config and credential helpers.
- In CI, a runner that has only the cloud SDK's credentials (environment
  variables, a metadata server) and not the cloud CLI keeps using a credential
  helper or `docker login`, as before.
