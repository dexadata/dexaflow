---
aliases:
  - /cli/leoflow_doctor.html
  - /reference/cli/leoflow_doctor/
title: "dexaflow doctor"
linkTitle: "doctor"
weight: 35
---

Report host platform, dependencies, and the achievable operating tier.

### Synopsis

doctor inspects the host (OS, architecture, libc), checks for Python 3.11, Docker, k3d, and kubectl, and reports which operating tier is achievable. It changes nothing; run `leoflow setup` to bootstrap.

```
dexaflow doctor [flags]
```

### Options

```
  -h, --help   help for doctor
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

