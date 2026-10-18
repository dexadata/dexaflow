---
aliases:
  - /cli/leoflow_registry_login.html
  - /reference/cli/leoflow_registry_login/
title: "dexaflow registry login"
linkTitle: "registry login"
weight: 55
---

Sign in to a registry without docker, storing the credential where builds and pushes read it

### Synopsis

Sign in to a container registry without docker login. The credential is checked with the registry, then stored in the docker config (DOCKER_CONFIG, else ~/.docker/config.json), or in its credsStore or credHelpers helper, where the CLI's registry client and docker, podman and nerdctl read it.

For ghcr.io with no token given, the session of the gh CLI is used (gh auth login). Otherwise pass --username and a token on stdin: a Docker Hub personal access token, a GitHub token with write:packages, or the password of any other registry.

```
dexaflow registry login <registry> [flags]
```

### Examples

```
  dexaflow registry login ghcr.io
  echo "$DOCKERHUB_TOKEN" | dexaflow registry login docker.io --username my-user --password-stdin
```

### Options

```
  -h, --help              help for login
      --password-stdin    read the token or password from stdin (one line)
      --username string   the user name to sign in as
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow registry](/reference/cli/dexaflow_registry/)	 - Sign in to the container registry DAG images are pushed to

