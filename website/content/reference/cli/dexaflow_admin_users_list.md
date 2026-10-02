---
aliases:
  - /cli/leoflow_admin_users_list.html
  - /reference/cli/leoflow_admin_users_list/
title: "dexaflow admin users list"
linkTitle: "admin users list"
weight: 11
---

List accounts (email, roles, active, age), bounded by --limit/--offset.

```
dexaflow admin users list [flags]
```

### Options

```
  -h, --help            help for list
      --limit int       maximum number of accounts to return (default 100)
      --offset int      number of accounts to skip before returning results
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow admin users](/reference/cli/dexaflow_admin_users/)	 - Inspect accounts on the running control plane.

