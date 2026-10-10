---
aliases:
  - /cli/leoflow_lite_reset-password.html
  - /reference/cli/leoflow_lite_reset-password/
title: "dexaflow lite reset-password"
linkTitle: "lite reset-password"
weight: 42
---

Reset the Dexaflow Lite admin password.

### Synopsis

reset-password generates a new admin password, updates it in the Lite database, and shows it once. It also rotates the per-install session secret in ~/.dexaflow/config.yaml, so every browser session signed before the reset ends: at once when Lite is stopped, or when you restart `dexaflow lite` if it is running (the command says which). Run it as the same user as `dexaflow lite` (no sudo). The Lite Postgres must be reachable (start `dexaflow lite` if it is not).

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
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow lite](/reference/cli/dexaflow_lite/)	 - Run Dexaflow Lite locally with hot reload.

