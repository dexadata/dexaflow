---
aliases:
  - /cli/leoflow_lite_restore.html
  - /reference/cli/leoflow_lite_restore/
title: "dexaflow lite restore"
linkTitle: "lite restore"
weight: 50
---

Restore a Lite install from an archive produced by `dexaflow lite backup`.

### Synopsis

restore reads a tar.gz produced by `dexaflow lite backup`, validates the manifest against this binary (refuses an archive newer than what this binary knows about), provisions the managed Postgres the way `dexaflow lite` does (so it works right after `dexaflow setup` on a new machine) and stops it again if it started it, then replays the datastore SQL and, only once that succeeded, restores config and workspace. The config.yaml it replaces is kept as ~/.dexaflow/config.yaml.pre-restore until a scan of every datastore finds every stored secret under the restored keys: the next `dexaflow lite` when the install has one datastore, else `dexaflow lite migrate-key`. The archive's config is restored as it is: an archive from before this install had a key of its own restores onto the published key, and `dexaflow lite migrate-key` moves it.

By default refuses to overwrite an install that already has a datastore (the managed ~/.dexaflow/pgdata, or a Docker datastore a Lite run used); pass --force to confirm.

```
dexaflow lite restore [flags]
```

### Options

```
      --force          overwrite an existing ~/.dexaflow install that has a datastore
  -h, --help           help for restore
  -i, --input string   path to the archive (required)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.dexaflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
      --target name         name of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)
```

### SEE ALSO

* [dexaflow lite](/reference/cli/dexaflow_lite/)	 - Run Dexaflow Lite locally with hot reload.

