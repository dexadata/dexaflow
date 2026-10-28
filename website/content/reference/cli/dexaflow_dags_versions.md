---
aliases:
  - /cli/leoflow_dags_versions.html
  - /reference/cli/leoflow_dags_versions/
title: "dexaflow dags versions"
linkTitle: "dags versions"
weight: 37
---

List a DAG's registered versions, marking the current one.

### Synopsis

Lists the versions registered for a DAG, newest first: the version number, the label (bundle_version), a * on the current version (the one new runs use), when it was registered and the id of the user who registered it (`dexaflow admin users list` maps ids to emails), the git commit it was built from (when the deploy recorded one) and the image digest it runs. Make another version current with `dexaflow rollback`.

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
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow dags](/reference/cli/dexaflow_dags/)	 - Manage registered DAGs.

