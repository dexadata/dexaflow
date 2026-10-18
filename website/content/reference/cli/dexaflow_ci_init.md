---
aliases:
  - /cli/leoflow_ci_init.html
  - /reference/cli/leoflow_ci_init/
title: "dexaflow ci init"
linkTitle: "ci init"
weight: 22
---

Write a pipeline that deploys the DAGs a push changed.

### Synopsis

Writes a deploy pipeline for a CI system at the top of the repository:
  github      .github/workflows/dexaflow-deploy.yaml
  gitlab      .gitlab-ci.yml
  cloudbuild  cloudbuild.yaml
  generic     only ci/dexaflow-deploy.sh
and ci/dexaflow-deploy.sh, which every pipeline runs. The pipeline installs this
version of the CLI (on GitLab it runs in the dexaflow-cli image of that
release, pinned by digest), logs in to the workspace's registry the way the
CI system does natively, and runs `dexaflow deploy --all --changed-since <before>` on
each push to --branch. It reads the control plane from DEXAFLOW_SERVER_URL,
or from the workspace target (--target, else default_target), and a deploy
token from the CI secret DEXAFLOW_TOKEN:
  dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d
When that control plane offers a CI OIDC issuer for the system (auth.ci_oidc),
the pipeline instead exchanges the job's OIDC token for a short-lived session
(`dexaflow auth ci-token`), and no token is stored; --auth picks the path.
An existing file is never overwritten without --force.

```
dexaflow ci init <github|gitlab|cloudbuild|generic> [flags]
```

### Options

```
      --auth string          how the pipeline signs in: auto (the job's OIDC token when the server offers an issuer for the system, else a deploy token), oidc or token (default "auto")
      --branch string        the branch whose pushes deploy (default "main")
      --cli-version string   the CLI release the pipeline installs, such as v0.6.0, v0.5.0 or later (default: this CLI's version)
      --environment string   github: the environment the deploy job runs in, whose secrets and protection rules apply (default "production")
      --force                overwrite the files that already exist
  -h, --help                 help for init
      --stdout               print the files instead of writing them
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow ci](/reference/cli/dexaflow_ci/)	 - Set up a CI pipeline that deploys the workspace.

