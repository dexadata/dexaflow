---
aliases:
  - /cli/leoflow_auth_create-token.html
  - /reference/cli/leoflow_auth_create-token/
title: "dexaflow auth create-token"
linkTitle: "auth create-token"
weight: 13
---

Obtain a JWT from the control plane.

```
dexaflow auth create-token [flags]
```

### Options

```
  -h, --help              help for create-token
      --password string   password
      --password-stdin    read the password from stdin instead of --password (avoids ps/shell-history exposure)
      --server string     control plane base URL (default: config server_url)
      --username string   username
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow auth](dexaflow_auth.md)	 - Manage authentication tokens.

