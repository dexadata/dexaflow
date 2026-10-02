---
aliases:
  - /cli/leoflow_lite_provision.html
  - /reference/cli/leoflow_lite_provision/
title: "dexaflow lite provision"
linkTitle: "lite provision"
weight: 40
---

Check and provision the local deps the from-source `leoflow lite` loop needs.

### Synopsis

provision readies this machine for the from-source `leoflow lite` loop: it checks Docker/k3d/kubectl/python3 (installing the brew-installable ones with --install), ensures the task base image, and provisions the isolated local database (leoflow_dev). For end users, `leoflow setup` (the installer) handles onboarding; this is the contributor's machine prep.

```
dexaflow lite provision [flags]
```

### Options

```
  -h, --help      help for provision
      --install   brew-install missing dependencies (where possible)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow lite](/reference/cli/dexaflow_lite/)	 - Run Leoflow Lite locally with hot reload.

