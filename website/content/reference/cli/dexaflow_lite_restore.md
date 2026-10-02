---
aliases:
  - /cli/leoflow_lite_restore.html
  - /reference/cli/leoflow_lite_restore/
title: "dexaflow lite restore"
linkTitle: "lite restore"
weight: 42
---

Restore a Lite install from an archive produced by `leoflow lite backup`.

### Synopsis

restore reads a tar.gz produced by `leoflow lite backup`, validates the manifest against this binary (refuses an archive newer than what this binary knows about), then replays the datastore SQL and restores config and workspace.

By default refuses to overwrite a non-empty ~/.leoflow; pass --force to confirm.

```
dexaflow lite restore [flags]
```

### Options

```
      --force          overwrite an existing ~/.leoflow install
  -h, --help           help for restore
  -i, --input string   path to the archive (required)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [dexaflow lite](dexaflow_lite.md)	 - Run Leoflow Lite locally with hot reload.

