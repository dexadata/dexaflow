---
aliases:
  - /cli/leoflow_variables_get.html
  - /reference/cli/leoflow_variables_get/
title: "dexaflow variables get"
linkTitle: "variables get"
weight: 55
---

Show a variable (value masked when the key looks sensitive).

```
dexaflow variables get <key> [flags]
```

### Options

```
  -h, --help            help for get
      --server string   control plane base URL (default: config server_url)
      --token string    JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow variables](/reference/cli/dexaflow_variables/)	 - Manage control-plane variables.

