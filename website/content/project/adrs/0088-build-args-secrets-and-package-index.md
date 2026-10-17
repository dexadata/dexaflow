---
title: "ADR 0088: Build args, build secrets and a private package index"
linkTitle: "0088 · Build args, secrets and package index"
weight: 880
description: "ADR 0088: dexaflow.yaml declares build args, build secrets read from the environment by reference, and a package index that replaces PyPI. Secrets and the index URL reach the build as BuildKit secrets or uv's environment, never a command line, a layer or dag.json; a build that has them runs on the local machine, never on a remote builder."
---

**Status:** Accepted
**Date:** 2026-10-17
**Relates:** ADR 0073 (daemonless image builds), ADR 0078 (dependency image from wheels), ADR 0083 (remote builder on Cloud Build), ADR 0070 (secrets by environment reference), ADR 0003 (DAG as image)
**Issues:** #1884, #1706

## Context

A project that installs from a private Python index, or whose own
Dockerfile needs a token or a build-time value, had no way to say so in
`dexaflow.yaml`. Users wrote the token into the Dockerfile, an `ENV` or the
index URL, where it lands in a layer, the image history, the build log or
the dependency list that reaches `dag.json`. ADR 0073 left build args,
build secrets and private indexes to a follow-up for every path that builds.

A secret is only safe when it reaches a process by reference (ADR 0070).
BuildKit has the mechanism for a Dockerfile build: `--secret id=<id>,env=<var>`
makes the tool read the value from its own environment and a
`RUN --mount=type=secret` line see it for one step only. docker, buildx,
podman and nerdctl accept the same form.

## Decision

1. **Three fields under `build`.** `args` (name to plain value),
   `secrets` (a list of `{id, env}`) and `package_index`
   (`{url, username_env, password_env}`). Names, ids and variable names are
   validated when the project loads (a secret id starts with a letter, a
   digit or `_`, so it never names `/run/secrets` or its parent); an index URL must be `https` (plain
   `http` on the loopback) and must not carry credentials, a query or a
   fragment, and a refusal never quotes a credential back.
2. **Values by reference.** A secret is passed to the tool as
   `--secret id=<id>,env=<var>` and only checked to be set. The CLI reads the
   index credentials to compose the URL and hands it to the tool in one
   variable of the tool's environment (`DEXAFLOW_BUILD_INDEX_URL`, secret id
   `dexaflow_index_url`), and to uv as `UV_DEFAULT_INDEX` and `UV_INDEX_URL`
   (a uv release older than `UV_DEFAULT_INDEX` ignores it and would resolve
   from PyPI). Neither appears in
   an argv, a layer, the image history or `dag.json`; a uv message that
   quotes the URL has its credential removed.
3. **The generated Dockerfile mounts the index.** With a package index, its
   `pip install` runs under `RUN --mount=type=secret,id=dexaflow_index_url,required=true`
   with `PIP_INDEX_URL` read from the mount. It declares no `ARG` and mounts
   no user secret: those are for a project's own Dockerfile, and the build
   warns when they are set without one.
4. **The index replaces PyPI.** There is no extra index: with two indexes,
   pip takes the highest version of a name from either, and a public package
   can shadow a private one. A team that needs PyPI packages mirrors it
   through its index.
5. **Inputs are content.** Build args and the index URL are part of the
   content label and the index URL of the dependency image key, so the skip
   and code-layer paths never reuse an image built with other inputs. A
   secret's value is not; its id and variable are.
6. **Secrets stay on this machine.** A build with secrets or a package index
   never goes to a remote builder: Cloud Build would have to receive the
   values and store them in its build record. Selection passes the remote
   builder over with that reason, and Cloud Build refuses a request that
   carries a secret. Build args reach Cloud Build as `--build-arg`.
7. **Missing values fail the build.** A secret or credential variable that is
   not set fails a build before the tool runs, naming it. A deploy that
   reuses an image already in the registry builds nothing and needs none.

## Consequences

- Private dependencies work on the daemonless path, with the local daemon
  and in a project's own Dockerfile without writing a credential anywhere.
- A secret mount needs BuildKit; the legacy docker builder refuses it, as
  does a podman or nerdctl release without `env=` secrets. A failed build
  that has secrets says so next to the tool's own message.
- A project with secrets or a package index cannot build on Cloud Build
  until the remote builder can read them from a secret store of its own
  (Secret Manager), a follow-up.
- The CLI composes the index URL in memory and passes it to child processes
  through their environment, which the same user can read in
  `/proc/<pid>/environ`; that is the boundary ADR 0070 accepts.

## Alternatives considered

- **Credentials in the index URL.** Simplest, and how pip documents it, but
  the URL is logged, echoed in errors and stored in config files.
- **`--extra-index-url`.** Lets a public package with a private name win;
  refused for the dependency confusion it opens.
- **Secret files (`src=<path>`).** Needs a file on disk for the length of the
  build; an environment variable is what CI systems provide.
