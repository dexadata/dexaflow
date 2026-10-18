---
aliases:
  - /cli/leoflow_auth_ci-token.html
  - /reference/cli/leoflow_auth_ci-token/
title: "dexaflow auth ci-token"
linkTitle: "auth ci-token"
weight: 13
---

Exchange a CI job's OIDC token for a deploy session.

### Synopsis

Posts the OIDC token of a CI job, read from DEXAFLOW_OIDC_TOKEN, to the control plane's
POST /api/v2/auth/ciToken, and prints the session it answers, alone on stdout:
  DEXAFLOW_TOKEN="$(dexaflow auth ci-token)"
The server checks the token against its trusted CI issuers (auth.ci_oidc) and
maps its claims to a role of the issuer's tenant. Pipelines written by
`dexaflow ci init` for a server that offers it run this for you.

```
dexaflow auth ci-token [flags]
```

### Options

```
  -h, --help            help for ci-token
      --server string   control plane URL (default: the target, DEXAFLOW_SERVER_URL, then the config file)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow auth](/reference/cli/dexaflow_auth/)	 - Manage authentication tokens.

