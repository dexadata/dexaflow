---
aliases:
  - /cli/leoflow_validate.html
  - /reference/cli/leoflow_validate/
title: "dexaflow validate"
linkTitle: "validate"
weight: 72
---

Validate dexaflow.yaml and the DAG source against the schema.

### Synopsis

Validate dexaflow.yaml against the schema and check the DAG source.

By default the check is static: dexaflow.yaml is validated and dag.py is
checked for syntax errors, without being imported. A dag.py that is valid
Python but fails when it runs (an AttributeError, a missing import) still
passes, and the error appears at compile time.

Pass --parse to also import dag.py through the parser, as compile does, and
validate the DAG it declares. It needs the parser and the DAG's dependencies
on this host, takes a fraction of a second per DAG, and leaves no dag.json
behind.

```
dexaflow validate [path] [flags]
```

### Options

```
  -h, --help                help for validate
      --parse               also import dag.py through the parser, as compile does, and validate the DAG it declares
      --parser-cmd string   override the parser command used by --parse (default from config)
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

