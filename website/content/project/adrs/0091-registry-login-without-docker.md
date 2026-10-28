---
title: "ADR 0091: Registry login without docker"
linkTitle: "0091 · Registry login without docker"
weight: 910
description: "ADR 0091: dexaflow registry login signs in to a registry no cloud owns without docker. For GHCR it exchanges the gh CLI session; otherwise it takes a token on stdin. The credential is checked with the registry and stored in the docker config or its credential helper, where the registry client and every container tool read it."
---

**Status:** Accepted
**Date:** 2026-10-17
**Relates:** ADR 0080 (registry credentials from cloud CLI sessions), ADR 0073 (daemonless image builds), ADR 0015 (no Docker SDK)
**Issues:** #1891, #1705

## Context

ADR 0080 removed `docker login` for Artifact Registry, GCR, ECR and ACR: the
CLI reads the cloud CLI's session. For every other registry (GHCR, Docker
Hub, Harbor) the preflight, `dexaflow doctor` and a refused push still said
`docker login <registry>`. On a machine with no container daemon, which the
daemonless paths of ADR 0073 now serve, that is a command the user does not
have, for a build that never needs docker.

GHCR and Docker Hub both accept a token as the password of a basic
credential, exchanged for a registry token by their token services. GitHub
offers a device flow, but only for an OAuth app registered by whoever ships
the client; Docker Hub's device flow belongs to Docker's own client.

## Decision

1. **`dexaflow registry login <registry>`.** It reads a token from stdin
   (`--password-stdin`, with `--username`), never from a flag. For `ghcr.io`
   with no token given, it exchanges the session of the GitHub CLI
   (`gh auth token`, and the user from `gh api user`), as ADR 0080 reads a
   cloud CLI's session.
2. **Checked before it is stored.** The CLI speaks the registry API's
   handshake with the credential (a Basic challenge, or the token exchange of
   a Bearer one) through the registry client's transport, so the loopback
   and TLS rules of ADR 0075 hold. A credential the registry refuses is not
   stored, and no error quotes it.
3. **Stored where everything reads it.** The credential goes to the docker
   config (`DOCKER_CONFIG`, else `~/.docker/config.json`), the file
   go-containerregistry's keychain and every container tool read. A
   `credHelpers` entry for the host, else a `credsStore`, keeps the secret in
   that helper through the credential helper protocol
   (`docker-credential-<name> store`, JSON on stdin), as `docker login` does,
   and the file records only the host; with no helper the secret goes in
   `auths`, base64 as docker writes it. Every other field of the file is
   kept, the file is replaced atomically and stays readable by its owner
   only, and Docker Hub is filed under `https://index.docker.io/v1/`
   whichever of its names was given. The CLI writes the file itself:
   `github.com/docker/cli` stays reachable only through the keychain
   (ADR 0073), and no dependency is added.
4. **The hints name it.** For a registry no cloud owns, the login command the
   preflight, `doctor`, `promote` and a refused push print is
   `dexaflow registry login <host>` when the tool is docker. Another tool
   (podman, nerdctl) keeps its own login, since it keeps its own credentials.

## Consequences

- A deploy to GHCR or Docker Hub needs no docker on the machine, from the
  first sign-in on.
- The `gh` session's token has the scopes `gh auth login` asked for, which do
  not include `write:packages` by default; the command says how to add it.
- A token stored in the file (no helper configured) is as readable as a
  `docker login` one: base64 in a file only its owner can read.
- A real device flow for GHCR needs an OAuth app the project registers and
  operates; it can be added behind the same command later.

## Alternatives considered

- **Read the `gh` session on every request, as ADR 0080 does for clouds.**
  It stores nothing, but docker, podman and nerdctl would not see it, and a
  `local-daemon` build would still need `docker login`.
- **Write `config.json` through `github.com/docker/cli`'s config package.**
  It handles the helpers too, but the lint keeps that module behind the
  keychain (ADR 0073), and the part needed here is one file format and one
  helper call.
