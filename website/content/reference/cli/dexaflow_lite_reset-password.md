---
aliases:
  - /cli/leoflow_lite_reset-password.html
  - /reference/cli/leoflow_lite_reset-password/
title: "dexaflow lite reset-password"
linkTitle: "lite reset-password"
weight: 41
---

Reset the Leoflow Lite admin password.

### Synopsis

reset-password generates a new admin password, updates it in the Lite database, and shows it once. Run it as the same user as `leoflow lite` (no sudo). The Lite Postgres must be reachable (start `leoflow lite` if it is not).

```
dexaflow lite reset-password [flags]
```

### Options

```
  -h, --help          help for reset-password
      --user string   admin email to reset (default: the admin from config)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow lite](dexaflow_lite.md)	 - Run Leoflow Lite locally with hot reload.

