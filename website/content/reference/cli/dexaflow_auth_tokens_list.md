---
aliases:
  - /cli/leoflow_auth_tokens_list.html
  - /reference/cli/leoflow_auth_tokens_list/
title: "dexaflow auth tokens list"
linkTitle: "auth tokens list"
weight: 18
---

List your API tokens (every token of the tenant for an admin).

```
dexaflow auth tokens list [flags]
```

### Options

```
  -h, --help                        help for list
      --server string               control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token dexaflow auth login   session JWT of a signed-in user; prefer leaving it out: the command reads DEXAFLOW_TOKEN, then the saved session from dexaflow auth login, and a value passed here shows in ps and shell history. An API token cannot manage tokens
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow auth tokens](/reference/cli/dexaflow_auth_tokens/)	 - List and revoke API tokens.

