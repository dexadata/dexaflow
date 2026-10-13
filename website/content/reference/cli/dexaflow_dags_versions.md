---
aliases:
  - /cli/leoflow_dags_versions.html
  - /reference/cli/leoflow_dags_versions/
title: "dexaflow dags versions"
linkTitle: "dags versions"
weight: 31
---

List a DAG's registered versions, marking the current one.

### Synopsis

Lists the versions registered for a DAG, newest first: the version number, the label (bundle_version), a * on the current version (the one new runs use), when and by whom it was registered, and the image digest it runs. Make another version current with `dexaflow rollback`.

```
dexaflow dags versions <dag_id> [flags]
```

### Options

```
  -h, --help            help for versions
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

* [dexaflow dags](/reference/cli/dexaflow_dags/)	 - Manage registered DAGs.

