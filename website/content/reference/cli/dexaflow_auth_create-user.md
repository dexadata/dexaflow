---
aliases:
  - /cli/leoflow_auth_create-user.html
  - /reference/cli/leoflow_auth_create-user/
title: "dexaflow auth create-user"
linkTitle: "auth create-user"
weight: 14
---

Create a user on the control plane (admin only).

```
dexaflow auth create-user [flags]
```

### Options

```
      --email string       email of the user to create
  -h, --help               help for create-user
      --password string    password for the new user
      --password-stdin     read the password from stdin instead of --password (avoids ps/shell-history exposure)
      --role stringArray   existing role to grant (repeatable); empty grants none
      --server string      control plane base URL (default: config server_url)
      --token string       admin JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow auth](dexaflow_auth.md)	 - Manage authentication tokens.

