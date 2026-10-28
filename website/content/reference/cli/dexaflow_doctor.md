---
aliases:
  - /cli/leoflow_doctor.html
  - /reference/cli/leoflow_doctor/
title: "dexaflow doctor"
linkTitle: "doctor"
weight: 42
---

Report host platform, dependencies, and the achievable operating tier.

### Synopsis

doctor inspects the host (OS, architecture, libc), checks for Python 3.11+, Docker, k3d, and kubectl, and reports which operating tier is achievable. With a target set it also runs the checks of the deploy preflight: the control plane answers and the token may deploy, and, in a DAG project, the registry accepts a push. It changes nothing but a saved session it refreshes, as deploy does; run `dexaflow setup` to bootstrap.

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
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow](/reference/cli/dexaflow/)	 - Dexaflow is a GitOps-first, container-native workflow orchestrator.

