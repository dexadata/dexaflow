---
aliases:
  - /cli/leoflow_auth_create-user.html
  - /reference/cli/leoflow_auth_create-user/
title: "dexaflow auth create-user"
linkTitle: "auth create-user"
weight: 15
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
      --server string      control plane base URL, over --target (default: the target in use, then server_url from the config file)
      --token string       admin JWT bearer token (default: config token)
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

