---
aliases:
  - /cli/leoflow_auth_login.html
  - /reference/cli/leoflow_auth_login/
title: "dexaflow auth login"
linkTitle: "auth login"
weight: 15
---

Authenticate to a control plane (Pro) and store the token.

```
dexaflow auth login [flags]
```

### Options

```
  -h, --help              help for login
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

