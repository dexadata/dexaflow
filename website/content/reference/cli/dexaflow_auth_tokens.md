---
aliases:
  - /cli/leoflow_auth_tokens.html
  - /reference/cli/leoflow_auth_tokens/
title: "dexaflow auth tokens"
linkTitle: "auth tokens"
weight: 17
---

List and revoke API tokens.

### Options

```
  -h, --help   help for tokens
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
* [dexaflow auth tokens list](/reference/cli/dexaflow_auth_tokens_list/)	 - List your API tokens (every token of the tenant for an admin).
* [dexaflow auth tokens revoke](/reference/cli/dexaflow_auth_tokens_revoke/)	 - Revoke an API token; it stops working on its next request.

