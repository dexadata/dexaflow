---
aliases:
  - /cli/leoflow_auth_create-token.html
  - /reference/cli/leoflow_auth_create-token/
title: "dexaflow auth create-token"
linkTitle: "auth create-token"
weight: 14
---

Obtain a session JWT, or with --scope create a long-lived API token.

```
dexaflow auth create-token [flags]
```

### Options

```
  -h, --help                        help for create-token
      --name string                 name of the API token, unique among your live tokens (with --scope)
      --password string             password
      --password-stdin              read the password from stdin instead of --password (avoids ps/shell-history exposure)
      --scope string                create a long-lived API token with this scope instead of a session JWT: deploy (register versions, set the current version, read DAGs) or read
      --server string               control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token dexaflow auth login   session JWT of a signed-in user; prefer leaving it out: the command reads DEXAFLOW_TOKEN, then the saved session from dexaflow auth login, and a value passed here shows in ps and shell history. An API token cannot manage tokens
      --ttl string                  lifetime of the API token, in days (90d) or as a duration (12h), from 1h to 365d (with --scope; default 90d)
      --username string             username
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

